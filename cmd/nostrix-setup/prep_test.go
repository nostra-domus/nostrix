package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

func newPrepClient(t *testing.T, mux *http.ServeMux) *cloudflareClient {
	t.Helper()
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	c := newCloudflareClient("test-api-token")
	c.baseURLOverride = srv.URL
	return c
}

func prepMux(zones []any) *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("/zones", func(w http.ResponseWriter, r *http.Request) {
		if got := r.URL.Query().Get("name"); got != "example.com" {
			writeCFSuccess(w, []any{})
			return
		}
		writeCFSuccess(w, zones)
	})
	return mux
}

var oneZone = []any{map[string]any{
	"id":      "zone1",
	"account": map[string]any{"id": "acct1", "name": "My Account"},
}}

func TestPrepareCloudflareHappyPath(t *testing.T) {
	mux := prepMux(oneZone)
	for _, p := range []string{
		"/accounts/acct1/cfd_tunnel",
		"/accounts/acct1/access/apps",
		"/zones/zone1/dns_records",
	} {
		mux.HandleFunc(p, func(w http.ResponseWriter, r *http.Request) {
			if r.Method != http.MethodGet {
				t.Errorf("%s: probe must be read-only, got %s", r.URL.Path, r.Method)
			}
			writeCFSuccess(w, []any{})
		})
	}
	mux.HandleFunc("/accounts/acct1/access/organizations", func(w http.ResponseWriter, r *http.Request) {
		writeCFSuccess(w, map[string]any{"auth_domain": "myteam.cloudflareaccess.com"})
	})

	res, err := prepareCloudflare(newPrepClient(t, mux), "example.com")
	if err != nil {
		t.Fatalf("prepareCloudflare failed: %v", err)
	}
	if res.AccountID != "acct1" || res.ZoneID != "zone1" || res.AccountName != "My Account" {
		t.Errorf("ids = %+v", res)
	}
	if res.TeamDomain != "myteam" {
		t.Errorf("TeamDomain = %q, want myteam", res.TeamDomain)
	}
	if len(res.Checks) != 3 || !res.allOK() {
		t.Errorf("expected 3 passing checks, got %+v", res.Checks)
	}
}

func TestPrepareCloudflareReportsMissingPermissions(t *testing.T) {
	mux := prepMux(oneZone)
	mux.HandleFunc("/accounts/acct1/cfd_tunnel", func(w http.ResponseWriter, r *http.Request) {
		writeCFFailure(w, "forbidden")
	})
	mux.HandleFunc("/accounts/acct1/access/apps", func(w http.ResponseWriter, r *http.Request) {
		writeCFSuccess(w, []any{})
	})
	mux.HandleFunc("/zones/zone1/dns_records", func(w http.ResponseWriter, r *http.Request) {
		writeCFSuccess(w, []any{})
	})
	mux.HandleFunc("/accounts/acct1/access/organizations", func(w http.ResponseWriter, r *http.Request) {
		writeCFFailure(w, "forbidden")
	})

	res, err := prepareCloudflare(newPrepClient(t, mux), "example.com")
	if err != nil {
		t.Fatalf("a failed probe must not be a hard error: %v", err)
	}
	if res.AccountID != "acct1" || res.ZoneID != "zone1" {
		t.Errorf("IDs should still be returned: %+v", res)
	}
	if res.Checks[0].OK || res.Checks[0].Err == "" {
		t.Errorf("tunnel probe should have failed: %+v", res.Checks[0])
	}
	if res.TeamDomain != "" || res.allOK() {
		t.Errorf("missing org + failed probe must not be allOK: %+v", res)
	}
}

func TestPrepareCloudflareUnknownZone(t *testing.T) {
	_, err := prepareCloudflare(newPrepClient(t, prepMux(oneZone)), "other.org")
	if err == nil {
		t.Fatal("expected an error for a domain with no matching zone")
	}
}
