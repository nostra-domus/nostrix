package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// applyRecorder stands in for apply() so server tests never run
// nixos-rebuild. Every test that builds a server must go through
// newTestServer, which wires it in.
type applyRecorder struct {
	mu    sync.Mutex
	calls []applyCall
	err   error
	block chan struct{} // if set, apply waits for it to be closed
}

type applyCall struct {
	path, content string
	upgradeAll    bool
}

func (r *applyRecorder) apply(path, content string, upgradeAll bool) error {
	r.mu.Lock()
	r.calls = append(r.calls, applyCall{path, content, upgradeAll})
	block, err := r.block, r.err
	r.mu.Unlock()
	if block != nil {
		<-block
	}
	return err
}

func (r *applyRecorder) recorded() []applyCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]applyCall(nil), r.calls...)
}

func newTestServer(t *testing.T) (*server, *applyRecorder) {
	t.Helper()
	dir := t.TempDir()
	rec := &applyRecorder{}
	srv := &server{
		output:    filepath.Join(dir, "flake.nix"),
		stateFile: filepath.Join(dir, "state.json"),
		applyFn:   rec.apply,
	}
	// Let any background rebuild finish before t.TempDir()'s cleanup runs.
	t.Cleanup(func() { waitIdle(t, srv) })
	return srv, rec
}

func waitIdle(t *testing.T, srv *server) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		srv.mu.Lock()
		done := !srv.rebuilding
		srv.mu.Unlock()
		if done {
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Error("background rebuild did not finish")
}

func postForm(h http.HandlerFunc, path string, form url.Values) *httptest.ResponseRecorder {
	req := httptest.NewRequest(http.MethodPost, path, strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()
	h(w, req)
	return w
}

func TestHandleSetupPersistsWifi(t *testing.T) {
	srv, rec := newTestServer(t)

	w := postForm(srv.handleSetup, "/setup", url.Values{
		"hostname": {"testhost"},
		"wifiSSID": {"myssid"},
		"wifiPSK":  {"mypassword"},
	})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect, got status %d: %s", w.Code, w.Body.String())
	}

	s, err := loadState(srv.stateFile)
	if err != nil {
		t.Fatalf("loadState failed: %v", err)
	}
	if s.WifiSSID != "myssid" || s.WifiPSK != "mypassword" {
		t.Errorf("expected WiFi credentials to be persisted, got SSID=%q PSK=%q", s.WifiSSID, s.WifiPSK)
	}

	waitIdle(t, srv)
	calls := rec.recorded()
	if len(calls) != 1 {
		t.Fatalf("expected exactly one apply, got %d", len(calls))
	}
	if calls[0].path != srv.output || calls[0].upgradeAll {
		t.Errorf("apply(%q, upgradeAll=%v), want (%q, false)", calls[0].path, calls[0].upgradeAll, srv.output)
	}
	if !strings.Contains(calls[0].content, "myssid") || !strings.Contains(calls[0].content, "nixosConfigurations.testhost") {
		t.Errorf("applied flake is missing the saved settings:\n%s", calls[0].content)
	}
}

// "Rebuild now" is the one rebuild that pulls in upstream changes.
func TestHandleRebuildUpgradesAll(t *testing.T) {
	srv, rec := newTestServer(t)
	if err := saveState(srv.stateFile, state{Hostname: "testhost"}); err != nil {
		t.Fatal(err)
	}

	w := postForm(srv.handleRebuild, "/rebuild", nil)
	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect, got status %d", w.Code)
	}

	waitIdle(t, srv)
	calls := rec.recorded()
	if len(calls) != 1 || !calls[0].upgradeAll {
		t.Errorf("calls = %+v, want one apply with upgradeAll=true", calls)
	}
}

func TestRebuildFailureIsSurfaced(t *testing.T) {
	srv, rec := newTestServer(t)
	rec.err = errors.New("boom")

	srv.rebuildAsync("flake", false)
	waitIdle(t, srv)

	srv.mu.Lock()
	got := srv.lastError
	srv.mu.Unlock()
	if got != "boom" {
		t.Errorf("lastError = %q, want %q", got, "boom")
	}

	// A later successful rebuild clears the stale banner.
	rec.mu.Lock()
	rec.err = nil
	rec.mu.Unlock()
	srv.rebuildAsync("flake", false)
	waitIdle(t, srv)
	srv.mu.Lock()
	got = srv.lastError
	srv.mu.Unlock()
	if got != "" {
		t.Errorf("lastError = %q after a successful rebuild, want empty", got)
	}
}

func TestOverlappingRebuildIsIgnored(t *testing.T) {
	srv, rec := newTestServer(t)
	rec.block = make(chan struct{})

	srv.rebuildAsync("first", false)
	srv.rebuildAsync("second", false) // arrives while the first is still running
	close(rec.block)
	waitIdle(t, srv)

	calls := rec.recorded()
	if len(calls) != 1 || calls[0].content != "first" {
		t.Errorf("calls = %+v, want only the first rebuild to run", calls)
	}
}

func TestHandleAppsAddsAnAppThatPassesTheCheck(t *testing.T) {
	srv, rec := newTestServer(t)
	if err := saveState(srv.stateFile, state{Hostname: "testhost"}); err != nil {
		t.Fatal(err)
	}
	var checked [2]string
	srv.checkAppFn = func(u, m string) error { checked = [2]string{u, m}; return nil }

	w := postForm(srv.handleApps, "/apps", url.Values{"url": {"github:org/jelly"}, "module": {"default"}})
	if w.Code != http.StatusSeeOther {
		t.Fatalf("expected redirect, got %d: %s", w.Code, w.Body.String())
	}
	if checked != [2]string{"github:org/jelly", "default"} {
		t.Errorf("check ran with %v", checked)
	}
	s, _ := loadState(srv.stateFile)
	if len(s.Apps) != 1 || s.Apps[0].Name != "jelly" || s.Apps[0].Module != "default" {
		t.Errorf("state apps = %+v", s.Apps)
	}
	waitIdle(t, srv)
	if calls := rec.recorded(); len(calls) != 1 || !strings.Contains(calls[0].content, "inputs.jelly.nixosModules.default") {
		t.Errorf("the applied flake should import the chosen module: %+v", calls)
	}
}

func TestHandleAppsRejectsAnAppThatFailsTheCheck(t *testing.T) {
	srv, rec := newTestServer(t)
	if err := saveState(srv.stateFile, state{Hostname: "testhost"}); err != nil {
		t.Fatal(err)
	}
	srv.checkAppFn = func(u, m string) error { return errors.New("no nixosModules.nostrix") }

	w := postForm(srv.handleApps, "/apps", url.Values{"url": {"github:org/jelly"}})
	if w.Code != http.StatusBadRequest || !strings.Contains(w.Body.String(), "no nixosModules.nostrix") {
		t.Fatalf("expected 400 with the reason, got %d: %s", w.Code, w.Body.String())
	}
	if s, _ := loadState(srv.stateFile); len(s.Apps) != 0 {
		t.Errorf("a rejected app must not be saved: %+v", s.Apps)
	}
	waitIdle(t, srv)
	if calls := rec.recorded(); len(calls) != 0 {
		t.Errorf("a rejected app must not trigger a rebuild: %+v", calls)
	}
}

// exposeServer is a configured device (tunnel set up) with one app that
// declares expose = "cloudflare", backed by the fake Cloudflare account.
func exposeServer(t *testing.T) (*server, *fakeAccount, *string) {
	t.Helper()
	srv, _ := newTestServer(t)
	acct, client := provisioned(t)

	appsFile := filepath.Join(t.TempDir(), "apps.json")
	if err := os.WriteFile(appsFile, []byte(`{"jelly":{"port":8096,"expose":"cloudflare"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	srv.appsFile = appsFile
	var tokenUsed string
	srv.cfClientFn = func(token string) *cloudflareClient { tokenUsed = token; return client }

	if err := saveState(srv.stateFile, state{
		Hostname: "pi-test", OwnerEmail: "owner@example.com",
		CloudflareBaseDomain: "example.com", CloudflareTunnelToken: "tunnel-token",
	}); err != nil {
		t.Fatal(err)
	}
	return srv, acct, &tokenUsed
}

func TestHandleAppsExposePublishesWithTheSuppliedToken(t *testing.T) {
	srv, acct, tokenUsed := exposeServer(t)

	w := postForm(srv.handleAppsExpose, "/apps/expose", url.Values{"apiToken": {"SECRET-TOKEN-123"}})
	body := w.Body.String()
	if w.Code != http.StatusOK || !strings.Contains(body, "published jelly at https://pi-test-jelly.example.com") {
		t.Fatalf("status %d, body:\n%s", w.Code, body)
	}
	if *tokenUsed != "SECRET-TOKEN-123" {
		t.Errorf("the Cloudflare client was built with %q", *tokenUsed)
	}
	if got := acct.tunnels[0].Ingress; len(got) != 3 || got[1].Service != "http://127.0.0.1:8096" {
		t.Errorf("tunnel ingress = %+v", got)
	}

	// The token is used once and goes nowhere else.
	if strings.Contains(body, "SECRET-TOKEN-123") {
		t.Error("the response page echoes the API token")
	}
	if raw, _ := os.ReadFile(srv.stateFile); strings.Contains(string(raw), "SECRET-TOKEN-123") {
		t.Error("the API token was written to the state file")
	}
}

func TestHandleAppsExposeNeedsAToken(t *testing.T) {
	srv, acct, tokenUsed := exposeServer(t)
	acct.log = nil

	w := postForm(srv.handleAppsExpose, "/apps/expose", url.Values{})
	if !strings.Contains(w.Body.String(), "API token") || *tokenUsed != "" || len(acct.log) != 0 {
		t.Errorf("without a token nothing may be contacted; body:\n%s\nlog: %v", w.Body.String(), acct.log)
	}
}

func TestHandleAppsExposeRequiresCloudflareToBeSetUp(t *testing.T) {
	srv, _, tokenUsed := exposeServer(t)
	if err := saveState(srv.stateFile, state{Hostname: "pi-test"}); err != nil {
		t.Fatal(err)
	}
	w := postForm(srv.handleAppsExpose, "/apps/expose", url.Values{"apiToken": {"x"}})
	if !strings.Contains(w.Body.String(), "set up on this device") || *tokenUsed != "" {
		t.Errorf("want a clear 'not set up' message and no API use; body:\n%s", w.Body.String())
	}
}

func TestHandleAppsExposeRemembersTheBaseDomain(t *testing.T) {
	srv, _, _ := exposeServer(t)
	if err := saveState(srv.stateFile, state{Hostname: "pi-test", OwnerEmail: "owner@example.com", CloudflareTunnelToken: "t"}); err != nil {
		t.Fatal(err)
	}
	w := postForm(srv.handleAppsExpose, "/apps/expose", url.Values{"apiToken": {"x"}, "baseDomain": {"example.com"}})
	if !strings.Contains(w.Body.String(), "published jelly") {
		t.Fatalf("body:\n%s", w.Body.String())
	}
	if s, _ := loadState(srv.stateFile); s.CloudflareBaseDomain != "example.com" {
		t.Errorf("base domain not remembered: %+v", s)
	}
}

func TestAppsPageListsWhatIsDeclared(t *testing.T) {
	srv, _, _ := exposeServer(t)
	w := httptest.NewRecorder()
	srv.handleApps(w, httptest.NewRequest(http.MethodGet, "/apps", nil))
	body := w.Body.String()
	for _, want := range []string{"jelly", "8096", "cloudflare", "https://pi-test-jelly.example.com", "Publish"} {
		if !strings.Contains(body, want) {
			t.Errorf("Apps page is missing %q", want)
		}
	}
	if strings.Contains(body, "tunnel-token") {
		t.Error("the Apps page leaks the tunnel token")
	}
}
