package main

import (
	"embed"
	"flag"
	"fmt"
	"html/template"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

//go:embed templates/*.html
var templateFS embed.FS

var pageTemplates = template.Must(template.ParseFS(templateFS, "templates/*.html"))

// server holds the mutable pieces shared across requests: where the
// generated flake and state file live, and the status of the most recent
// rebuild (nixos-rebuild switch runs in the background since it can take
// a long time — the web UI polls it via the index page).
type server struct {
	output    string
	stateFile string

	mu         sync.Mutex
	rebuilding bool
	lastError  string
}

type pageData struct {
	State      state
	Rebuilding bool
	LastError  string
}

func runServe(args []string) {
	fs := flag.NewFlagSet("serve", flag.ExitOnError)
	addr := fs.String("addr", "0.0.0.0:8080", "address to listen on")
	output := fs.String("output", "/etc/nixos/flake.nix", "path to write the generated flake.nix")
	stateFile := fs.String("state", "/etc/nostrix/state.json", "path to the state file")
	cfTeamDomain := fs.String("cf-team-domain", "", "Cloudflare Access team domain (empty: run in bootstrap mode)")
	cfAud := fs.String("cf-aud", "", "Cloudflare Access application audience tag (empty: run in bootstrap mode)")
	fs.Parse(args) //nolint:errcheck

	srv := &server{output: *output, stateFile: *stateFile}
	mux := http.NewServeMux()

	// Bootstrap mode: Cloudflare hasn't been configured yet (the state a
	// freshly flashed image boots into). Serve only the bootstrap setup
	// form, unauthenticated by Access (there's no Access to check yet) but
	// behind a fixed shared credential — see requireBasicAuth. Once the
	// form's submission succeeds, the generated flake sets real
	// team-domain/AUD values and nixos-rebuild switch restarts this same
	// service in the branch below.
	if *cfTeamDomain == "" && *cfAud == "" {
		mux.HandleFunc("/", srv.handleBootstrap)
		fmt.Printf("Nostrix web UI listening on http://%s (bootstrap mode — not yet configured)\n", *addr)
		if err := http.ListenAndServe(*addr, requireBasicAuth(bootstrapUser, bootstrapPassword, mux)); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
		return
	}

	// Fail closed: a partially-set pair means genuine misconfiguration
	// (not "not yet bootstrapped") — refuse to serve rather than run
	// unauthenticated.
	if *cfTeamDomain == "" || *cfAud == "" {
		fmt.Fprintln(os.Stderr, "error: --cf-team-domain and --cf-aud must both be set, or both left empty for bootstrap mode")
		os.Exit(1)
	}
	verifier := newAccessVerifier(*cfTeamDomain, *cfAud)

	mux.HandleFunc("/", srv.handleIndex)
	mux.HandleFunc("/setup", srv.handleSetup)
	mux.HandleFunc("/apps", srv.handleApps)
	mux.HandleFunc("/apps/remove", srv.handleAppRemove)
	mux.HandleFunc("/rebuild", srv.handleRebuild)

	fmt.Printf("Nostrix web UI listening on http://%s\n", *addr)
	if err := http.ListenAndServe(*addr, requireAccess(verifier, mux)); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// handleBootstrap serves the first-boot setup form (LAN-reachable, behind
// Basic Auth only) that collects everything needed to both finish
// configuring this device and provision its Cloudflare Tunnel + Access app,
// in one step, from a phone browser.
func (srv *server) handleBootstrap(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}

	s, _ := loadState(srv.stateFile)

	if r.Method != http.MethodPost {
		srv.render(w, "bootstrap", pageData{State: s})
		return
	}

	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s.Hostname = strings.TrimSpace(r.FormValue("hostname"))
	s.SSHKey = strings.TrimSpace(r.FormValue("sshKey"))
	s.Hardware = r.FormValue("hardware")
	s.OwnerEmail = strings.TrimSpace(r.FormValue("ownerEmail"))

	client := newCloudflareClient(strings.TrimSpace(r.FormValue("cfAPIToken")))
	cfCfg := cloudflareConfig{
		AccountID:  strings.TrimSpace(r.FormValue("cfAccountID")),
		ZoneID:     strings.TrimSpace(r.FormValue("cfZoneID")),
		TeamDomain: strings.TrimSpace(r.FormValue("cfTeamDomain")),
		BaseDomain: strings.TrimSpace(r.FormValue("cfBaseDomain")),
		DeviceName: s.Hostname,
		OwnerEmail: s.OwnerEmail,
	}

	result, err := provisionCloudflare(client, cfCfg)
	if err != nil {
		srv.render(w, "bootstrap", pageData{State: s, LastError: "Cloudflare setup failed: " + err.Error()})
		return
	}
	s.CloudflareTeamDomain = result.TeamDomain
	s.CloudflareAud = result.Aud
	s.CloudflareTunnelToken = result.TunnelToken

	if err := srv.applyState(s); err != nil {
		srv.render(w, "bootstrap", pageData{State: s, LastError: err.Error()})
		return
	}

	// From here, nixos-rebuild switch (running in the background) will
	// bring up the tunnel and restart this very service bound to
	// localhost only, behind Access — this response may be the last one
	// this bootstrap listener ever serves.
	srv.render(w, "bootstrap", pageData{State: s, Rebuilding: true})
}

func (srv *server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	s, _ := loadState(srv.stateFile)

	srv.mu.Lock()
	data := pageData{State: s, Rebuilding: srv.rebuilding, LastError: srv.lastError}
	srv.mu.Unlock()

	srv.render(w, "index", data)
}

func (srv *server) handleSetup(w http.ResponseWriter, r *http.Request) {
	s, _ := loadState(srv.stateFile)

	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		s.Hostname = strings.TrimSpace(r.FormValue("hostname"))
		s.SSHKey = strings.TrimSpace(r.FormValue("sshKey"))
		s.Hardware = r.FormValue("hardware")
		s.NginxEnable = r.FormValue("nginx") == "on"
		s.WifiSSID = strings.TrimSpace(r.FormValue("wifiSSID"))
		s.WifiPSK = strings.TrimSpace(r.FormValue("wifiPSK"))

		if err := srv.applyState(s); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/", http.StatusSeeOther)
		return
	}

	srv.render(w, "setup", pageData{State: s})
}

func (srv *server) handleApps(w http.ResponseWriter, r *http.Request) {
	s, _ := loadState(srv.stateFile)

	if r.Method == http.MethodPost {
		if err := r.ParseForm(); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		rawURL := strings.TrimSpace(r.FormValue("url"))
		name, err := addApp(&s, rawURL)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}

		configDir := filepath.Join("/etc/nostrix", name)
		if err := os.MkdirAll(configDir, 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not create %s: %v\n", configDir, err)
		}

		if err := srv.applyState(s); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		http.Redirect(w, r, "/apps", http.StatusSeeOther)
		return
	}

	srv.render(w, "apps", pageData{State: s})
}

func (srv *server) handleAppRemove(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if err := r.ParseForm(); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	s, _ := loadState(srv.stateFile)
	removeApp(&s, r.FormValue("name"))

	if err := srv.applyState(s); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	http.Redirect(w, r, "/apps", http.StatusSeeOther)
}

// handleRebuild is the index page's explicit "Rebuild now" button. Unlike
// the rebuilds triggered by saving Setup/Apps/bootstrap, this one passes
// upgradeAll: it's the user's deliberate call to pull in whatever's changed
// upstream (nostrix, nixpkgs) since the flake was last resolved, not just
// re-apply the state already on disk.
func (srv *server) handleRebuild(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s, _ := loadState(srv.stateFile)
	flake, err := generate(s)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	srv.rebuildAsync(flake, true)
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// applyState validates and generates s's flake, saves s, and triggers a
// rebuild in the background. Generating (and thus validating) before
// saving keeps invalid state out of the state file.
func (srv *server) applyState(s state) error {
	flake, err := generate(s)
	if err != nil {
		return err
	}
	if err := saveState(srv.stateFile, s); err != nil {
		return err
	}
	srv.rebuildAsync(flake, false)
	return nil
}

// rebuildAsync writes the flake and runs nixos-rebuild switch in the
// background, since it can take minutes. A rebuild already in progress is
// left to finish rather than starting a second, overlapping one.
func (srv *server) rebuildAsync(flake string, upgradeAll bool) {
	srv.mu.Lock()
	if srv.rebuilding {
		srv.mu.Unlock()
		return
	}
	srv.rebuilding = true
	srv.lastError = ""
	srv.mu.Unlock()

	go func() {
		err := apply(srv.output, flake, upgradeAll)

		srv.mu.Lock()
		srv.rebuilding = false
		if err != nil {
			srv.lastError = err.Error()
		}
		srv.mu.Unlock()
	}()
}

func (srv *server) render(w http.ResponseWriter, name string, data pageData) {
	if err := pageTemplates.ExecuteTemplate(w, name, data); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}
