package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"
)

const cloudflareAPIBase = "https://api.cloudflare.com/client/v4"

// cloudflareClient is a minimal stdlib client for the handful of Cloudflare
// API calls needed to provision a device's Tunnel + Access app. Kept
// deliberately small (no JOSE/JWK-style library, same as auth.go) to stay
// within this module's stdlib-only constraint.
type cloudflareClient struct {
	apiToken   string
	httpClient *http.Client

	// baseURLOverride replaces the real Cloudflare API base when set, so
	// tests can point at a local fake server.
	baseURLOverride string
}

func newCloudflareClient(apiToken string) *cloudflareClient {
	return &cloudflareClient{
		apiToken:   apiToken,
		httpClient: &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *cloudflareClient) baseURL() string {
	if c.baseURLOverride != "" {
		return c.baseURLOverride
	}
	return cloudflareAPIBase
}

type cfEnvelope struct {
	Success bool              `json:"success"`
	Errors  []json.RawMessage `json:"errors"`
	Result  json.RawMessage   `json:"result"`
}

// do sends a JSON API request to path and decodes its "result" field into
// out (if non-nil). Cloudflare's API always responds with the same
// {success, errors, result} envelope regardless of endpoint.
func (c *cloudflareClient) do(method, path string, body, out any) error {
	var reqBody io.Reader
	if body != nil {
		b, err := json.Marshal(body)
		if err != nil {
			return fmt.Errorf("encoding request body: %w", err)
		}
		reqBody = bytes.NewReader(b)
	}

	req, err := http.NewRequest(method, c.baseURL()+path, reqBody)
	if err != nil {
		return fmt.Errorf("building request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+c.apiToken)
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return fmt.Errorf("calling %s %s: %w", method, path, err)
	}
	defer resp.Body.Close()

	var env cfEnvelope
	if err := json.NewDecoder(resp.Body).Decode(&env); err != nil {
		return fmt.Errorf("decoding response from %s %s: %w", method, path, err)
	}
	if !env.Success {
		return fmt.Errorf("%s %s failed: %s", method, path, env.Errors)
	}
	if out != nil {
		if err := json.Unmarshal(env.Result, out); err != nil {
			return fmt.Errorf("decoding result from %s %s: %w", method, path, err)
		}
	}
	return nil
}

func (c *cloudflareClient) createTunnel(accountID, name string) (string, error) {
	var result struct {
		ID string `json:"id"`
	}
	body := map[string]any{"name": name, "config_src": "cloudflare"}
	if err := c.do(http.MethodPost, fmt.Sprintf("/accounts/%s/cfd_tunnel", accountID), body, &result); err != nil {
		return "", fmt.Errorf("creating tunnel: %w", err)
	}
	return result.ID, nil
}

func (c *cloudflareClient) tunnelToken(accountID, tunnelID string) (string, error) {
	var token string
	path := fmt.Sprintf("/accounts/%s/cfd_tunnel/%s/token", accountID, tunnelID)
	if err := c.do(http.MethodGet, path, nil, &token); err != nil {
		return "", fmt.Errorf("fetching tunnel token: %w", err)
	}
	return token, nil
}

// setTunnelIngress configures a remotely-managed tunnel's routing entirely
// server-side, so the device needs nothing beyond `cloudflared tunnel run
// --token ...` — no local config.yml.
func (c *cloudflareClient) setTunnelIngress(accountID, tunnelID, hostname string) error {
	return c.setTunnelRules(accountID, tunnelID, []ingressRule{{hostname, tunnelOriginService}})
}

// ingressRule is one routing rule of a remotely-managed tunnel.
type ingressRule struct{ Hostname, Service string }

// setTunnelRules replaces the tunnel's whole routing with rules, followed by
// the mandatory catch-all. setTunnelIngress is the device-only form used at
// setup; this is the general one, used once apps are exposed (expose.go).
func (c *cloudflareClient) setTunnelRules(accountID, tunnelID string, rules []ingressRule) error {
	ingress := []map[string]any{}
	for _, r := range rules {
		ingress = append(ingress, map[string]any{"hostname": r.Hostname, "service": r.Service})
	}
	ingress = append(ingress, map[string]any{"service": tunnelCatchAll})
	body := map[string]any{"config": map[string]any{"ingress": ingress}}
	path := fmt.Sprintf("/accounts/%s/cfd_tunnel/%s/configurations", accountID, tunnelID)
	if err := c.do(http.MethodPut, path, body, nil); err != nil {
		return fmt.Errorf("setting tunnel ingress: %w", err)
	}
	return nil
}

func (c *cloudflareClient) createDNSRecord(zoneID, hostname, target string) error {
	body := map[string]any{"type": "CNAME", "name": hostname, "content": target, "proxied": true}
	if err := c.do(http.MethodPost, fmt.Sprintf("/zones/%s/dns_records", zoneID), body, nil); err != nil {
		return fmt.Errorf("creating DNS record: %w", err)
	}
	return nil
}

func (c *cloudflareClient) createAccessApp(accountID, name, hostname string) (appID, aud string, err error) {
	var result struct {
		ID  string `json:"id"`
		Aud string `json:"aud"`
	}
	body := map[string]any{
		"name":             name,
		"domain":           hostname,
		"type":             "self_hosted",
		"session_duration": "24h",
	}
	if err := c.do(http.MethodPost, fmt.Sprintf("/accounts/%s/access/apps", accountID), body, &result); err != nil {
		return "", "", fmt.Errorf("creating access app: %w", err)
	}
	return result.ID, result.Aud, nil
}

const (
	tunnelOriginService = "http://127.0.0.1:8080"
	tunnelCatchAll      = "http_status:404"
	accessPolicyName    = "device owner"
)

func accessPolicyBody(email string) map[string]any {
	return map[string]any{
		"name":     accessPolicyName,
		"decision": "allow",
		"include":  []map[string]any{{"email": map[string]string{"email": email}}},
	}
}

func (c *cloudflareClient) createAccessPolicy(accountID, appID, email string) error {
	path := fmt.Sprintf("/accounts/%s/access/apps/%s/policies", accountID, appID)
	if err := c.do(http.MethodPost, path, accessPolicyBody(email), nil); err != nil {
		return fmt.Errorf("creating access policy: %w", err)
	}
	return nil
}

// The ensure* helpers below make provisioning safe to run again. The form
// provisions Cloudflare *before* the on-device switch, so a failed switch
// (or a mistyped field) means the next attempt finds the previous attempt's
// leftovers. Each helper reuses a resource only when it is provably ours —
// i.e. exactly what an earlier run would have produced — and otherwise
// refuses, because the account may hold unrelated resources with the same
// name (a real device called "arne-pc" must never get its routing replaced
// just because someone typed that hostname).

// tunnelIngress returns the routing rules of a remotely-managed tunnel.
func (c *cloudflareClient) tunnelIngress(accountID, tunnelID string) ([]ingressRule, error) {
	var cfg struct {
		Config struct {
			Ingress []struct {
				Hostname string `json:"hostname"`
				Service  string `json:"service"`
			} `json:"ingress"`
		} `json:"config"`
	}
	path := fmt.Sprintf("/accounts/%s/cfd_tunnel/%s/configurations", accountID, tunnelID)
	if err := c.do(http.MethodGet, path, nil, &cfg); err != nil {
		return nil, fmt.Errorf("reading tunnel config: %w", err)
	}
	var rules []ingressRule
	for _, r := range cfg.Config.Ingress {
		rules = append(rules, ingressRule{r.Hostname, r.Service})
	}
	return rules, nil
}

// ingressIsOurs reports whether a tunnel's routing is empty (created by an
// earlier run that failed before configuring it) or a routing we wrote: it
// routes the device's own hostname to the web UI and ends in the catch-all.
// Further hostname rules in between are exposed apps (expose.go) and are
// ours too; anything else means the tunnel belongs to something else.
func ingressIsOurs(rules []ingressRule, hostname string) bool {
	if len(rules) == 0 {
		return true
	}
	last := rules[len(rules)-1]
	if last.Hostname != "" || last.Service != tunnelCatchAll {
		return false
	}
	for _, r := range rules[:len(rules)-1] {
		if r.Hostname == hostname && r.Service == tunnelOriginService {
			return true
		}
	}
	return false
}

// ensureTunnel returns the ID of this device's tunnel, creating it unless
// an earlier run already did.
func (c *cloudflareClient) ensureTunnel(accountID, name, hostname string) (id string, reused bool, err error) {
	t, err := c.lookupTunnel(accountID, name)
	if err != nil {
		return "", false, err
	}
	if t != nil {
		if t.ConfigSrc != "cloudflare" {
			return "", false, fmt.Errorf("a locally-managed tunnel named %q already exists; choose a different hostname or delete it", name)
		}
		rules, err := c.tunnelIngress(accountID, t.ID)
		if err != nil {
			return "", false, err
		}
		if !ingressIsOurs(rules, hostname) {
			return "", false, fmt.Errorf("a tunnel named %q already exists and routes other traffic; choose a different hostname or delete it", name)
		}
		return t.ID, true, nil
	}
	id, err = c.createTunnel(accountID, name)
	return id, false, err
}

// cfTunnel is the part of a tunnel object the lookups need.
type cfTunnel struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	ConfigSrc string `json:"config_src"`
}

// lookupTunnel finds the live tunnel called name, or nil if there is none.
func (c *cloudflareClient) lookupTunnel(accountID, name string) (*cfTunnel, error) {
	var tunnels []struct {
		cfTunnel
		DeletedAt *string `json:"deleted_at"`
	}
	path := fmt.Sprintf("/accounts/%s/cfd_tunnel?is_deleted=false&name=%s", accountID, url.QueryEscape(name))
	if err := c.do(http.MethodGet, path, nil, &tunnels); err != nil {
		return nil, fmt.Errorf("looking up tunnel: %w", err)
	}
	for _, t := range tunnels {
		if t.Name == name && t.DeletedAt == nil {
			found := t.cfTunnel
			return &found, nil
		}
	}
	return nil, nil
}

// zoneByName resolves a domain to its zone ID and owning account ID.
func (c *cloudflareClient) zoneByName(domain string) (accountID, zoneID string, err error) {
	var zones []cfZone
	if err := c.do(http.MethodGet, "/zones?name="+url.QueryEscape(domain), nil, &zones); err != nil {
		return "", "", fmt.Errorf("looking up zone %s (token needs Zone:Read): %w", domain, err)
	}
	if len(zones) == 0 {
		return "", "", fmt.Errorf("no zone named %q is visible to this token", domain)
	}
	return zones[0].Account.ID, zones[0].ID, nil
}

type cfDNSRecord struct {
	ID      string `json:"id"`
	Type    string `json:"type"`
	Content string `json:"content"`
}

func (c *cloudflareClient) listDNSRecords(zoneID, hostname string) ([]cfDNSRecord, error) {
	var records []cfDNSRecord
	path := fmt.Sprintf("/zones/%s/dns_records?name=%s", zoneID, url.QueryEscape(hostname))
	if err := c.do(http.MethodGet, path, nil, &records); err != nil {
		return nil, fmt.Errorf("looking up DNS record: %w", err)
	}
	return records, nil
}

func (c *cloudflareClient) deleteDNSRecord(zoneID, recordID string) error {
	if err := c.do(http.MethodDelete, fmt.Sprintf("/zones/%s/dns_records/%s", zoneID, recordID), nil, nil); err != nil {
		return fmt.Errorf("deleting DNS record: %w", err)
	}
	return nil
}

type cfAccessApp struct {
	ID     string `json:"id"`
	Name   string `json:"name"`
	Domain string `json:"domain"`
	Type   string `json:"type"`
	Aud    string `json:"aud"`
}

// listAccessApps returns every Access application in the account.
func (c *cloudflareClient) listAccessApps(accountID string) ([]cfAccessApp, error) {
	const perPage = 100
	var all []cfAccessApp
	for page := 1; page <= 50; page++ {
		var apps []cfAccessApp
		path := fmt.Sprintf("/accounts/%s/access/apps?page=%d&per_page=%d", accountID, page, perPage)
		if err := c.do(http.MethodGet, path, nil, &apps); err != nil {
			return nil, fmt.Errorf("looking up access app: %w", err)
		}
		all = append(all, apps...)
		if len(apps) < perPage {
			break
		}
	}
	return all, nil
}

func (c *cloudflareClient) deleteAccessApp(accountID, appID string) error {
	if err := c.do(http.MethodDelete, fmt.Sprintf("/accounts/%s/access/apps/%s", accountID, appID), nil, nil); err != nil {
		return fmt.Errorf("deleting access app: %w", err)
	}
	return nil
}

// ensureDNSRecord creates the CNAME to the tunnel unless it already exists.
// A record with this name that points anywhere else is never touched.
func (c *cloudflareClient) ensureDNSRecord(zoneID, hostname, target string) (reused bool, err error) {
	records, err := c.listDNSRecords(zoneID, hostname)
	if err != nil {
		return false, err
	}
	for _, r := range records {
		if r.Type == "CNAME" && r.Content == target {
			return true, nil
		}
		return false, fmt.Errorf("DNS record %s already exists (%s → %s); delete it or choose a different hostname", hostname, r.Type, r.Content)
	}
	return false, c.createDNSRecord(zoneID, hostname, target)
}

// ensureAccessApp returns the Access application for hostname, creating it
// unless an earlier run already did. An app on that hostname under a
// different name is someone else's and is left alone.
func (c *cloudflareClient) ensureAccessApp(accountID, name, hostname string) (appID, aud string, reused bool, err error) {
	apps, err := c.listAccessApps(accountID)
	if err != nil {
		return "", "", false, err
	}
	for _, a := range apps {
		if a.Domain != hostname {
			continue
		}
		if a.Name == name && a.Type == "self_hosted" {
			return a.ID, a.Aud, true, nil
		}
		return "", "", false, fmt.Errorf("an Access application %q already protects %s; delete it or choose a different hostname", a.Name, hostname)
	}
	appID, aud, err = c.createAccessApp(accountID, name, hostname)
	return appID, aud, false, err
}

// ensureAccessPolicy makes the app's "device owner" policy allow exactly
// email: it updates an existing one in place (so a corrected email on a
// retry takes effect) or creates it.
func (c *cloudflareClient) ensureAccessPolicy(accountID, appID, email string) (reused bool, err error) {
	var policies []struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	path := fmt.Sprintf("/accounts/%s/access/apps/%s/policies", accountID, appID)
	if err := c.do(http.MethodGet, path, nil, &policies); err != nil {
		return false, fmt.Errorf("looking up access policy: %w", err)
	}
	for _, p := range policies {
		if p.Name == accessPolicyName {
			if err := c.do(http.MethodPut, path+"/"+p.ID, accessPolicyBody(email), nil); err != nil {
				return false, fmt.Errorf("updating access policy: %w", err)
			}
			return true, nil
		}
	}
	return false, c.createAccessPolicy(accountID, appID, email)
}

// cloudflareConfig holds the inputs needed to provision one device's
// Cloudflare Tunnel + Access app. The API token itself lives on the
// cloudflareClient, not here — see provisionCloudflare.
type cloudflareConfig struct {
	AccountID  string
	ZoneID     string
	TeamDomain string
	BaseDomain string
	DeviceName string
	OwnerEmail string
}

type cloudflareResult struct {
	Hostname    string
	TeamDomain  string
	Aud         string
	TunnelToken string
	// Reused names the resources that an earlier attempt had already
	// created and this run picked up instead of creating again.
	Reused []string
}

// provisionCloudflare makes sure a tunnel for cfg.DeviceName exists, routes
// <DeviceName>.<BaseDomain> to it, and that an Access application + policy
// allow only cfg.OwnerEmail. The caller's API token (via c) is used only
// for these calls — nothing here persists it; only the narrower tunnel
// token in the returned cloudflareResult is meant to be kept.
//
// It is safe to run again after a partial failure or a failed on-device
// switch: resources from the earlier attempt are reused when they are
// provably ours (see the ensure* helpers) and the run fails loudly,
// touching nothing, when a same-named resource belongs to something else.
func provisionCloudflare(c *cloudflareClient, cfg cloudflareConfig) (cloudflareResult, error) {
	hostname := cfg.DeviceName + "." + cfg.BaseDomain
	var reused []string
	note := func(was bool, what string) {
		if was {
			reused = append(reused, what)
		}
	}

	tunnelID, was, err := c.ensureTunnel(cfg.AccountID, cfg.DeviceName, hostname)
	if err != nil {
		return cloudflareResult{}, err
	}
	note(was, "tunnel")
	token, err := c.tunnelToken(cfg.AccountID, tunnelID)
	if err != nil {
		return cloudflareResult{}, err
	}
	// Writing the routing replaces it wholesale, so on a tunnel that already
	// exists keep the routes of any apps published through it (expose.go) —
	// re-running setup must not silently take them offline.
	rules := []ingressRule{{hostname, tunnelOriginService}}
	if was {
		old, err := c.tunnelIngress(cfg.AccountID, tunnelID)
		if err != nil {
			return cloudflareResult{}, err
		}
		for _, r := range old {
			if r.Hostname != "" && r.Hostname != hostname {
				rules = append(rules, r)
			}
		}
	}
	if err := c.setTunnelRules(cfg.AccountID, tunnelID, rules); err != nil {
		return cloudflareResult{}, err
	}
	was, err = c.ensureDNSRecord(cfg.ZoneID, hostname, tunnelID+".cfargotunnel.com")
	if err != nil {
		return cloudflareResult{}, err
	}
	note(was, "DNS record")
	appID, aud, was, err := c.ensureAccessApp(cfg.AccountID, cfg.DeviceName, hostname)
	if err != nil {
		return cloudflareResult{}, err
	}
	note(was, "Access application")
	was, err = c.ensureAccessPolicy(cfg.AccountID, appID, cfg.OwnerEmail)
	if err != nil {
		return cloudflareResult{}, err
	}
	note(was, "Access policy")

	return cloudflareResult{
		Hostname:    hostname,
		TeamDomain:  cfg.TeamDomain,
		Aud:         aud,
		TunnelToken: token,
		Reused:      reused,
	}, nil
}
