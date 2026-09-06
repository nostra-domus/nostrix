package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeEnvelope struct {
	Success bool  `json:"success"`
	Errors  []any `json:"errors"`
	Result  any   `json:"result"`
}

func writeCFSuccess(w http.ResponseWriter, result any) {
	_ = json.NewEncoder(w).Encode(fakeEnvelope{Success: true, Result: result})
}

func writeCFFailure(w http.ResponseWriter, message string) {
	_ = json.NewEncoder(w).Encode(fakeEnvelope{
		Success: false,
		Errors:  []any{map[string]any{"code": 1000, "message": message}},
	})
}

// newFakeCloudflareAPI spins up a local server standing in for Cloudflare's
// API, handling exactly the calls provisionCloudflare makes for account
// "acct1" / zone "zone1", and returns a client pointed at it.
func newFakeCloudflareAPI(t *testing.T) (*httptest.Server, *cloudflareClient) {
	t.Helper()

	mux := http.NewServeMux()
	mux.HandleFunc("/accounts/acct1/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("create tunnel: method = %s, want POST", r.Method)
		}
		writeCFSuccess(w, map[string]any{"id": "tunnel1"})
	})
	mux.HandleFunc("/accounts/acct1/cfd_tunnel/tunnel1/token", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("tunnel token: method = %s, want GET", r.Method)
		}
		writeCFSuccess(w, "test-tunnel-token")
	})
	mux.HandleFunc("/accounts/acct1/cfd_tunnel/tunnel1/configurations", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPut {
			t.Errorf("tunnel config: method = %s, want PUT", r.Method)
		}
		writeCFSuccess(w, nil)
	})
	mux.HandleFunc("/zones/zone1/dns_records", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("dns record: method = %s, want POST", r.Method)
		}
		writeCFSuccess(w, nil)
	})
	mux.HandleFunc("/accounts/acct1/access/apps", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("access app: method = %s, want POST", r.Method)
		}
		writeCFSuccess(w, map[string]any{"id": "app1", "aud": "test-aud"})
	})
	mux.HandleFunc("/accounts/acct1/access/apps/app1/policies", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("access policy: method = %s, want POST", r.Method)
		}
		writeCFSuccess(w, nil)
	})

	srv := httptest.NewServer(mux)
	c := newCloudflareClient("test-api-token")
	c.baseURLOverride = srv.URL
	return srv, c
}

func testConfig() cloudflareConfig {
	return cloudflareConfig{
		AccountID:  "acct1",
		ZoneID:     "zone1",
		TeamDomain: "myteam",
		BaseDomain: "example.com",
		DeviceName: "pi-test",
		OwnerEmail: "owner@example.com",
	}
}

func TestProvisionCloudflareHappyPath(t *testing.T) {
	srv, client := newFakeCloudflareAPI(t)
	defer srv.Close()

	result, err := provisionCloudflare(client, testConfig())
	if err != nil {
		t.Fatalf("provisionCloudflare failed: %v", err)
	}

	if result.Hostname != "pi-test.example.com" {
		t.Errorf("Hostname = %q, want pi-test.example.com", result.Hostname)
	}
	if result.TeamDomain != "myteam" {
		t.Errorf("TeamDomain = %q, want myteam", result.TeamDomain)
	}
	if result.Aud != "test-aud" {
		t.Errorf("Aud = %q, want test-aud", result.Aud)
	}
	if result.TunnelToken != "test-tunnel-token" {
		t.Errorf("TunnelToken = %q, want test-tunnel-token", result.TunnelToken)
	}
}

func TestProvisionCloudflarePropagatesAPIError(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/accounts/acct1/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
		writeCFFailure(w, "invalid account")
	})
	srv := httptest.NewServer(mux)
	defer srv.Close()

	client := newCloudflareClient("test-api-token")
	client.baseURLOverride = srv.URL

	if _, err := provisionCloudflare(client, testConfig()); err == nil {
		t.Fatal("expected an error when the Cloudflare API call fails, got nil")
	}
}
