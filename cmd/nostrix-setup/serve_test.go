package main

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
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
