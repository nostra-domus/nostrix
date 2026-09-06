package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	body := map[string]any{
		"config": map[string]any{
			"ingress": []map[string]any{
				{"hostname": hostname, "service": "http://127.0.0.1:8080"},
				{"service": "http_status:404"},
			},
		},
	}
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

func (c *cloudflareClient) createAccessPolicy(accountID, appID, email string) error {
	body := map[string]any{
		"name":     "device owner",
		"decision": "allow",
		"include":  []map[string]any{{"email": map[string]string{"email": email}}},
	}
	path := fmt.Sprintf("/accounts/%s/access/apps/%s/policies", accountID, appID)
	if err := c.do(http.MethodPost, path, body, nil); err != nil {
		return fmt.Errorf("creating access policy: %w", err)
	}
	return nil
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
}

// provisionCloudflare creates a tunnel for cfg.DeviceName, routes
// <DeviceName>.<BaseDomain> to it, and creates an Access application +
// policy allowing only cfg.OwnerEmail. The caller's API token (via c) is
// used only for these calls — nothing here persists it; only the narrower
// tunnel token in the returned cloudflareResult is meant to be kept.
func provisionCloudflare(c *cloudflareClient, cfg cloudflareConfig) (cloudflareResult, error) {
	hostname := cfg.DeviceName + "." + cfg.BaseDomain

	tunnelID, err := c.createTunnel(cfg.AccountID, cfg.DeviceName)
	if err != nil {
		return cloudflareResult{}, err
	}
	token, err := c.tunnelToken(cfg.AccountID, tunnelID)
	if err != nil {
		return cloudflareResult{}, err
	}
	if err := c.setTunnelIngress(cfg.AccountID, tunnelID, hostname); err != nil {
		return cloudflareResult{}, err
	}
	if err := c.createDNSRecord(cfg.ZoneID, hostname, tunnelID+".cfargotunnel.com"); err != nil {
		return cloudflareResult{}, err
	}
	appID, aud, err := c.createAccessApp(cfg.AccountID, cfg.DeviceName, hostname)
	if err != nil {
		return cloudflareResult{}, err
	}
	if err := c.createAccessPolicy(cfg.AccountID, appID, cfg.OwnerEmail); err != nil {
		return cloudflareResult{}, err
	}

	return cloudflareResult{
		Hostname:    hostname,
		TeamDomain:  cfg.TeamDomain,
		Aud:         aud,
		TunnelToken: token,
	}, nil
}
