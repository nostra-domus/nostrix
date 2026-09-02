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
	fs.Parse(args) //nolint:errcheck

	srv := &server{output: *output, stateFile: *stateFile}

	mux := http.NewServeMux()
	mux.HandleFunc("/", srv.handleIndex)
	mux.HandleFunc("/setup", srv.handleSetup)
	mux.HandleFunc("/apps", srv.handleApps)
	mux.HandleFunc("/apps/remove", srv.handleAppRemove)
	mux.HandleFunc("/rebuild", srv.handleRebuild)

	fmt.Printf("Nostrix web UI listening on http://%s\n", *addr)
	if err := http.ListenAndServe(*addr, mux); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
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

func (srv *server) handleRebuild(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	s, _ := loadState(srv.stateFile)
	srv.rebuildAsync(generate(s))
	http.Redirect(w, r, "/", http.StatusSeeOther)
}

// applyState saves s and triggers a rebuild in the background.
func (srv *server) applyState(s state) error {
	if err := saveState(srv.stateFile, s); err != nil {
		return err
	}
	srv.rebuildAsync(generate(s))
	return nil
}

// rebuildAsync writes the flake and runs nixos-rebuild switch in the
// background, since it can take minutes. A rebuild already in progress is
// left to finish rather than starting a second, overlapping one.
func (srv *server) rebuildAsync(flake string) {
	srv.mu.Lock()
	if srv.rebuilding {
		srv.mu.Unlock()
		return
	}
	srv.rebuilding = true
	srv.lastError = ""
	srv.mu.Unlock()

	go func() {
		err := apply(srv.output, flake)

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
