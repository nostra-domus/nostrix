package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

func baseState() state { return state{Hostname: "box"} }

func TestCatalogEntriesAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, e := range loadCatalog() {
		if !validIdentifier(e.ID) || seen[e.ID] {
			t.Errorf("bad or duplicate catalog id %q", e.ID)
		}
		seen[e.ID] = true
		if len(e.Expose) == 0 {
			t.Errorf("%s allows no exposure", e.ID)
		}
		for _, f := range e.Fields {
			if f.Type != "text" && f.Type != "bool" {
				t.Errorf("%s.%s: unknown type %q", e.ID, f.ID, f.Type)
			}
			// A catalog default must pass its own validation.
			c := catalogApp{Expose: e.Expose[0], Settings: map[string]string{f.ID: f.Default}}
			if err := validateCatalogApp(e.ID, c); err != nil {
				t.Errorf("%s.%s default is invalid: %v", e.ID, f.ID, err)
			}
		}
	}
	for _, id := range []string{"jellyfin", "vaultwarden", "uptime-kuma"} {
		if !seen[id] {
			t.Errorf("catalog is missing %s", id)
		}
	}
}

func TestValidateCatalogApp(t *testing.T) {
	cases := []struct {
		name string
		id   string
		app  catalogApp
		ok   bool
	}{
		{"fine", "jellyfin", catalogApp{Expose: "lan", Settings: map[string]string{"mediaDir": "/srv/tv"}}, true},
		{"unknown app", "nope", catalogApp{Expose: "none"}, false},
		{"vaultwarden has no lan", "vaultwarden", catalogApp{Expose: "lan"}, false},
		{"unknown setting", "jellyfin", catalogApp{Expose: "none", Settings: map[string]string{"x": "y"}}, false},
		{"relative path", "jellyfin", catalogApp{Expose: "none", Settings: map[string]string{"mediaDir": "media"}}, false},
		{"nix injection", "jellyfin", catalogApp{Expose: "none", Settings: map[string]string{"mediaDir": `/a"; evil = "`}}, false},
		{"bad bool", "vaultwarden", catalogApp{Expose: "none", Settings: map[string]string{"signupsAllowed": "maybe"}}, false},
	}
	for _, c := range cases {
		if err := validateCatalogApp(c.id, c.app); (err == nil) != c.ok {
			t.Errorf("%s: err = %v, want ok=%v", c.name, err, c.ok)
		}
	}
}

func TestGenerateEmitsCatalogApps(t *testing.T) {
	s := baseState()
	s.Catalog = map[string]catalogApp{
		"jellyfin":    {Expose: "lan", Settings: map[string]string{"mediaDir": "/srv/tv"}},
		"vaultwarden": {Expose: "cloudflare", Settings: map[string]string{"domain": "", "signupsAllowed": "false"}},
	}
	out, err := generate(s)
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"nostrix.catalog.jellyfin = {", `expose = "lan";`, `mediaDir = "/srv/tv";`,
		"nostrix.catalog.vaultwarden = {", "signupsAllowed = false;",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("generated flake is missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "domain =") {
		t.Errorf("an empty text setting should be left to the module default:\n%s", out)
	}
}

func TestGenerateRejectsBadCatalogState(t *testing.T) {
	s := baseState()
	s.Catalog = map[string]catalogApp{"vaultwarden": {Expose: "lan"}}
	if _, err := generate(s); err == nil {
		t.Error("expected vaultwarden on the LAN to be rejected")
	}
}

func TestHandleCatalogInstallsAnApp(t *testing.T) {
	srv, rec := newTestServer(t)
	saveState(srv.stateFile, baseState())

	w := postForm(srv.handleCatalog, "/apps/catalog", url.Values{
		"id": {"jellyfin"}, "expose": {"lan"}, "f_mediaDir": {"/srv/tv"},
	})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status %d: %s", w.Code, w.Body.String())
	}
	s, _ := loadState(srv.stateFile)
	if got := s.Catalog["jellyfin"]; got.Expose != "lan" || got.Settings["mediaDir"] != "/srv/tv" {
		t.Errorf("saved state = %+v", got)
	}
	waitIdle(t, srv)
	calls := rec.recorded()
	if len(calls) != 1 || !strings.Contains(calls[0].content, "nostrix.catalog.jellyfin") {
		t.Errorf("expected one apply with the catalog app, got %+v", calls)
	}
}

func TestHandleCatalogUncheckedBoxMeansFalse(t *testing.T) {
	srv, _ := newTestServer(t)
	saveState(srv.stateFile, baseState())
	postForm(srv.handleCatalog, "/apps/catalog", url.Values{"id": {"vaultwarden"}, "expose": {"none"}})
	s, _ := loadState(srv.stateFile)
	if s.Catalog["vaultwarden"].Settings["signupsAllowed"] != "false" {
		t.Errorf("settings = %v", s.Catalog["vaultwarden"].Settings)
	}
}

func TestHandleCatalogCloudflareNeedsCloudflare(t *testing.T) {
	srv, rec := newTestServer(t)
	saveState(srv.stateFile, baseState())
	w := postForm(srv.handleCatalog, "/apps/catalog", url.Values{"id": {"uptime-kuma"}, "expose": {"cloudflare"}})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", w.Code)
	}
	if len(rec.recorded()) != 0 {
		t.Error("nothing should have been applied")
	}
}

func TestHandleCatalogRejectsUnknownApp(t *testing.T) {
	srv, _ := newTestServer(t)
	saveState(srv.stateFile, baseState())
	w := postForm(srv.handleCatalog, "/apps/catalog", url.Values{"id": {"evil"}, "expose": {"none"}})
	if w.Code != http.StatusBadRequest {
		t.Errorf("status %d, want 400", w.Code)
	}
}

func TestHandleCatalogRemove(t *testing.T) {
	srv, _ := newTestServer(t)
	s := baseState()
	s.Catalog = map[string]catalogApp{"uptime-kuma": {Expose: "none"}}
	saveState(srv.stateFile, s)
	w := postForm(srv.handleCatalogRemove, "/apps/catalog/remove", url.Values{"id": {"uptime-kuma"}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("status %d", w.Code)
	}
	got, _ := loadState(srv.stateFile)
	if len(got.Catalog) != 0 {
		t.Errorf("catalog = %v", got.Catalog)
	}
}

func TestAppsPageShowsTheCatalog(t *testing.T) {
	srv, _ := newTestServer(t)
	s := baseState()
	s.Catalog = map[string]catalogApp{"jellyfin": {Expose: "lan", Settings: map[string]string{"mediaDir": "/srv/tv"}}}
	saveState(srv.stateFile, s)
	w := httptest.NewRecorder()
	srv.handleApps(w, httptest.NewRequest(http.MethodGet, "/apps", nil))
	body := w.Body.String()
	for _, want := range []string{"Jellyfin", "Vaultwarden", "Uptime Kuma", `value="/srv/tv"`, "not running yet"} {
		if !strings.Contains(body, want) {
			t.Errorf("Apps page is missing %q", want)
		}
	}
	// Cloudflare isn't set up, so publishing is offered but disabled.
	if !strings.Contains(body, "disabled") {
		t.Error("the Cloudflare choice should be disabled without Cloudflare")
	}
}

func TestAppsPageShowsRebuildStatus(t *testing.T) {
	srv, _ := newTestServer(t)
	s := baseState()
	s.Catalog = map[string]catalogApp{"uptime-kuma": {Expose: "none"}}
	saveState(srv.stateFile, s)
	srv.mu.Lock()
	srv.rebuilding = true
	srv.mu.Unlock()
	w := httptest.NewRecorder()
	srv.handleApps(w, httptest.NewRequest(http.MethodGet, "/apps", nil))
	body := w.Body.String()
	for _, want := range []string{"Applying your changes", "http-equiv=\"refresh\"", "being installed"} {
		if !strings.Contains(body, want) {
			t.Errorf("Apps page is missing %q while rebuilding", want)
		}
	}
	srv.mu.Lock()
	srv.rebuilding, srv.lastError = false, "boom"
	srv.mu.Unlock()
	w = httptest.NewRecorder()
	srv.handleApps(w, httptest.NewRequest(http.MethodGet, "/apps", nil))
	body = w.Body.String()
	if !strings.Contains(body, "The last rebuild failed: boom") || !strings.Contains(body, "not running yet") {
		t.Errorf("Apps page should show the failure and that the app isn't running:\n%s", body)
	}
}

func TestStatusPageListsCatalogApps(t *testing.T) {
	srv, _ := newTestServer(t)
	s := baseState()
	s.Catalog = map[string]catalogApp{
		"jellyfin":    {Expose: "lan"},
		"uptime-kuma": {Expose: "cloudflare"},
	}
	saveState(srv.stateFile, s)
	w := httptest.NewRecorder()
	srv.handleIndex(w, httptest.NewRequest(http.MethodGet, "/", nil))
	body := w.Body.String()
	for _, want := range []string{"jellyfin (lan)", "uptime-kuma (cloudflare)"} {
		if !strings.Contains(body, want) {
			t.Errorf("Status page is missing %q:\n%s", want, body)
		}
	}
}
