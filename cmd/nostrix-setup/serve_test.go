package main

import (
	"net/http"
	"net/http/httptest"
	"net/url"
	"path/filepath"
	"strings"
	"testing"
)

func TestHandleSetupPersistsWifi(t *testing.T) {
	dir := t.TempDir()
	srv := &server{
		output:    filepath.Join(dir, "flake.nix"),
		stateFile: filepath.Join(dir, "state.json"),
	}

	form := url.Values{
		"hostname": {"testhost"},
		"wifiSSID": {"myssid"},
		"wifiPSK":  {"mypassword"},
	}
	req := httptest.NewRequest(http.MethodPost, "/setup", strings.NewReader(form.Encode()))
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	w := httptest.NewRecorder()

	srv.handleSetup(w, req)

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
}
