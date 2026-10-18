package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"regexp"
	"sort"
	"strings"
)

// Publishing apps through Cloudflare.
//
// An app declares `nostrix.apps.<name>.expose = "cloudflare"` (modules/apps.nix),
// which makes the running system list it in /etc/nostrix/apps.json. Nix can't
// create tunnel routes, DNS records or Access applications, so
// reconcileExposure makes Cloudflare match that file, using an API token the
// user supplies for that one run (it is never stored).

const defaultAppsFile = "/etc/nostrix/apps.json"

// declaredApp is one entry of apps.json.
type declaredApp struct {
	Name   string `json:"-"`
	Port   *int   `json:"port"`
	Expose string `json:"expose"`
}

// readDeclaredApps reads apps.json, sorted by name. A missing file means the
// running system declares no apps (it predates modules/apps.nix, or has none).
func readDeclaredApps(path string) ([]declaredApp, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var byName map[string]declaredApp
	if err := json.Unmarshal(data, &byName); err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	apps := make([]declaredApp, 0, len(byName))
	for name, a := range byName {
		a.Name = name
		apps = append(apps, a)
	}
	sort.Slice(apps, func(i, j int) bool { return apps[i].Name < apps[j].Name })
	return apps, nil
}

// dnsLabelRe is one DNS label: lowercase letters, digits and inner hyphens.
var dnsLabelRe = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]*[a-z0-9])?$`)

// appHostLabel is the single DNS label an exposed app gets: <device>-<app>.
// One label, not <app>.<device>, because Cloudflare's free certificate
// covers only one level of subdomain.
func appHostLabel(device, appName string) (string, error) {
	label := strings.ToLower(strings.ReplaceAll(device+"-"+appName, "_", "-"))
	if len(label) > 63 || !dnsLabelRe.MatchString(label) {
		return "", fmt.Errorf("cannot make a hostname for app %q on device %q (%q is not a valid DNS label)", appName, device, label)
	}
	return label, nil
}

type exposeConfig struct {
	BaseDomain string
	DeviceName string
	OwnerEmail string
}

// plannedApp is one app that should be reachable through Cloudflare.
type plannedApp struct {
	Name     string
	Label    string // also the Access application's name
	Hostname string
	Port     int
}

// planExposure works out, without touching anything, which apps should be
// published and under which hostnames.
func planExposure(cfg exposeConfig, apps []declaredApp) ([]plannedApp, error) {
	var plan []plannedApp
	seen := map[string]string{}
	for _, a := range apps {
		if a.Expose != "cloudflare" {
			continue
		}
		if a.Port == nil {
			return nil, fmt.Errorf("app %q is set to expose = \"cloudflare\" but has no port", a.Name)
		}
		label, err := appHostLabel(cfg.DeviceName, a.Name)
		if err != nil {
			return nil, err
		}
		if other, dup := seen[label]; dup {
			return nil, fmt.Errorf("apps %q and %q would both be published as %s.%s", other, a.Name, label, cfg.BaseDomain)
		}
		seen[label] = a.Name
		plan = append(plan, plannedApp{Name: a.Name, Label: label, Hostname: label + "." + cfg.BaseDomain, Port: *a.Port})
	}
	return plan, nil
}

type exposeResult struct {
	BaseDomain string // the domain used (detected from the token when none was given)
	Exposed    []plannedApp
	Removed    []string // hostnames whose routing, DNS record and Access app were taken down
}

// reconcileExposure makes Cloudflare match the declared apps: every app with
// expose = "cloudflare" gets an Access application (owner-only), a DNS
// record and a tunnel route, and an app that was published before but no
// longer is gets all three removed.
//
// Order matters for safety: an app's Access application and policy exist
// before its DNS record and route, so it is never reachable unprotected, and
// on removal the route and DNS go first and the Access application last.
//
// Only this device's own resources are touched: the tunnel must be ours
// (ingressIsOurs), and cleanup only considers hostnames that were routed by
// that very tunnel.
func reconcileExposure(c *cloudflareClient, cfg exposeConfig, apps []declaredApp) (exposeResult, error) {
	var res exposeResult
	if cfg.BaseDomain == "" {
		base, err := c.onlyZone()
		if err != nil {
			return res, err
		}
		cfg.BaseDomain = base
	}
	res.BaseDomain = cfg.BaseDomain
	plan, err := planExposure(cfg, apps)
	if err != nil {
		return res, err
	}
	accountID, zoneID, err := c.zoneByName(cfg.BaseDomain)
	if err != nil {
		return res, err
	}
	tunnel, err := c.lookupTunnel(accountID, cfg.DeviceName)
	if err != nil {
		return res, err
	}
	if tunnel == nil {
		return res, fmt.Errorf("no tunnel named %q — has Cloudflare remote access been set up on this device?", cfg.DeviceName)
	}
	if tunnel.ConfigSrc != "cloudflare" {
		return res, fmt.Errorf("tunnel %q is locally managed; its routing can't be changed from here", cfg.DeviceName)
	}
	deviceHost := cfg.DeviceName + "." + cfg.BaseDomain
	old, err := c.tunnelIngress(accountID, tunnel.ID)
	if err != nil {
		return res, err
	}
	if !ingressIsOurs(old, deviceHost) {
		return res, fmt.Errorf("tunnel %q routes traffic that isn't this device's; not changing it", cfg.DeviceName)
	}
	target := tunnel.ID + ".cfargotunnel.com"

	rules := []ingressRule{{deviceHost, tunnelOriginService}}
	for _, p := range plan {
		appID, _, _, err := c.ensureAccessApp(accountID, p.Label, p.Hostname)
		if err != nil {
			return res, fmt.Errorf("%s: %w", p.Name, err)
		}
		if _, err := c.ensureAccessPolicy(accountID, appID, cfg.OwnerEmail); err != nil {
			return res, fmt.Errorf("%s: %w", p.Name, err)
		}
		if _, err := c.ensureDNSRecord(zoneID, p.Hostname, target); err != nil {
			return res, fmt.Errorf("%s: %w", p.Name, err)
		}
		rules = append(rules, ingressRule{p.Hostname, fmt.Sprintf("http://127.0.0.1:%d", p.Port)})
	}
	if err := c.setTunnelRules(accountID, tunnel.ID, rules); err != nil {
		return res, err
	}
	res.Exposed = plan

	wanted := map[string]bool{deviceHost: true}
	for _, p := range plan {
		wanted[p.Hostname] = true
	}
	for _, r := range old {
		if r.Hostname == "" || wanted[r.Hostname] {
			continue
		}
		if err := c.removeExposedHostname(accountID, zoneID, target, cfg.BaseDomain, r.Hostname); err != nil {
			return res, err
		}
		res.Removed = append(res.Removed, r.Hostname)
	}
	return res, nil
}

// removeExposedHostname takes down the DNS record and Access application of
// a hostname that this device's tunnel used to route. Each is deleted only if
// it still looks like what we created (a CNAME to this tunnel; an Access app
// named after the hostname's label), so something that was since repointed
// or replaced by hand is left alone.
func (c *cloudflareClient) removeExposedHostname(accountID, zoneID, target, baseDomain, hostname string) error {
	records, err := c.listDNSRecords(zoneID, hostname)
	if err != nil {
		return err
	}
	for _, r := range records {
		if r.Type == "CNAME" && r.Content == target {
			if err := c.deleteDNSRecord(zoneID, r.ID); err != nil {
				return err
			}
		}
	}
	label := strings.TrimSuffix(hostname, "."+baseDomain)
	apps, err := c.listAccessApps(accountID)
	if err != nil {
		return err
	}
	for _, a := range apps {
		if a.Domain == hostname && a.Name == label && a.Type == "self_hosted" {
			if err := c.deleteAccessApp(accountID, a.ID); err != nil {
				return err
			}
		}
	}
	return nil
}

// ---- web UI -------------------------------------------------------------

func (srv *server) appsPath() string {
	if srv.appsFile != "" {
		return srv.appsFile
	}
	return defaultAppsFile
}

func (srv *server) newCFClient(token string) *cloudflareClient {
	if srv.cfClientFn != nil {
		return srv.cfClientFn(token)
	}
	return newCloudflareClient(token)
}

// appsPage is the data for the Apps page: the registered apps plus what the
// running system declares about each app's exposure.
func (srv *server) appsPage(s state) pageData {
	apps, err := readDeclaredApps(srv.appsPath())
	live := map[string]bool{}
	for _, a := range apps {
		live[a.Name] = true
	}
	d := pageData{State: s, Catalog: catalogViews(s, live)}
	srv.mu.Lock()
	d.Rebuilding, d.LastError = srv.rebuilding, srv.lastError
	srv.mu.Unlock()
	if err != nil {
		d.Problem = err.Error()
	}
	for _, a := range apps {
		v := declaredView{Name: a.Name, Expose: a.Expose, Port: "—"}
		if a.Port != nil {
			v.Port = fmt.Sprint(*a.Port)
		}
		if a.Expose == "cloudflare" && s.CloudflareBaseDomain != "" {
			if label, err := appHostLabel(s.Hostname, a.Name); err == nil {
				v.URL = "https://" + label + "." + s.CloudflareBaseDomain
			}
		}
		d.Declared = append(d.Declared, v)
	}
	return d
}

// handleAppsExpose publishes the declared apps through Cloudflare. The API
// token comes from the form, is used for this request only, and is never
// written anywhere.
func (srv *server) handleAppsExpose(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	s, _ := loadState(srv.stateFile)
	page := func(notice, problem string) {
		d := srv.appsPage(s)
		d.Notice, d.Problem = notice, problem
		srv.render(w, "apps", d)
	}

	if s.CloudflareTunnelToken == "" {
		page("", "Cloudflare remote access isn't set up on this device.")
		return
	}
	token := strings.TrimSpace(r.FormValue("apiToken"))
	base := strings.TrimSpace(r.FormValue("baseDomain"))
	if base == "" {
		base = s.CloudflareBaseDomain
	}
	if token == "" {
		page("", "An API token is needed.")
		return
	}
	apps, err := readDeclaredApps(srv.appsPath())
	if err != nil {
		page("", err.Error())
		return
	}

	res, err := reconcileExposure(srv.newCFClient(token), exposeConfig{
		BaseDomain: base, DeviceName: s.Hostname, OwnerEmail: s.OwnerEmail,
	}, apps)
	if err != nil {
		page("", err.Error())
		return
	}
	if res.BaseDomain != "" && s.CloudflareBaseDomain != res.BaseDomain {
		s.CloudflareBaseDomain = res.BaseDomain
		_ = saveState(srv.stateFile, s)
	}
	page(res.summary(), "")
}

func (r exposeResult) summary() string {
	var parts []string
	for _, p := range r.Exposed {
		parts = append(parts, "published "+p.Name+" at https://"+p.Hostname)
	}
	for _, h := range r.Removed {
		parts = append(parts, "removed "+h)
	}
	if len(parts) == 0 {
		return "Nothing to publish; Cloudflare already matches."
	}
	return strings.Join(parts, "; ") + "."
}
