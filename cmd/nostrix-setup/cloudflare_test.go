package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
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

type fakeIngressRule struct {
	Hostname string `json:"hostname,omitempty"`
	Service  string `json:"service"`
}

type fakeTunnel struct {
	ID        string
	Name      string
	ConfigSrc string
	Ingress   []fakeIngressRule
}

type fakeDNSRecord struct{ ID, Name, Type, Content string }

type fakeApp struct{ ID, Name, Domain, Type, Aud string }

type fakePolicy struct {
	ID, Name string
	Email    string
}

// fakeAccount is a stateful in-memory stand-in for the slice of Cloudflare's
// API that provisionCloudflare uses, for account "acct1" / zone "zone1".
// State survives across calls so tests can run provisioning twice.
type fakeAccount struct {
	mu       sync.Mutex
	tunnels  []*fakeTunnel
	dns      []fakeDNSRecord
	apps     []fakeApp
	policies map[string][]*fakePolicy // by app ID
	creates  map[string]int           // "tunnel", "dns", "app", "policy"
	deletes  map[string]int           // "dns", "app"
	puts     int                      // ingress configurations written
	log      []string                 // "METHOD path" of every request, in order
	nextID   int
}

func (a *fakeAccount) handler() http.Handler {
	mux := http.NewServeMux()
	lock := func(f http.HandlerFunc) http.HandlerFunc {
		return func(w http.ResponseWriter, r *http.Request) {
			a.mu.Lock()
			defer a.mu.Unlock()
			a.log = append(a.log, r.Method+" "+r.URL.Path)
			f(w, r)
		}
	}
	decode := func(r *http.Request) map[string]any {
		var m map[string]any
		_ = json.NewDecoder(r.Body).Decode(&m)
		return m
	}

	mux.HandleFunc("GET /accounts/acct1/cfd_tunnel", lock(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		out := []map[string]any{}
		for _, t := range a.tunnels {
			if name == "" || t.Name == name {
				out = append(out, map[string]any{"id": t.ID, "name": t.Name, "config_src": t.ConfigSrc, "deleted_at": nil})
			}
		}
		writeCFSuccess(w, out)
	}))
	mux.HandleFunc("POST /accounts/acct1/cfd_tunnel", lock(func(w http.ResponseWriter, r *http.Request) {
		body := decode(r)
		a.creates["tunnel"]++
		t := &fakeTunnel{ID: fmt.Sprintf("tunnel%d", len(a.tunnels)+1), Name: body["name"].(string), ConfigSrc: "cloudflare"}
		a.tunnels = append(a.tunnels, t)
		writeCFSuccess(w, map[string]any{"id": t.ID})
	}))
	find := func(id string) *fakeTunnel {
		for _, t := range a.tunnels {
			if t.ID == id {
				return t
			}
		}
		return nil
	}
	mux.HandleFunc("GET /accounts/acct1/cfd_tunnel/{id}/token", lock(func(w http.ResponseWriter, r *http.Request) {
		writeCFSuccess(w, "token-for-"+r.PathValue("id"))
	}))
	mux.HandleFunc("GET /accounts/acct1/cfd_tunnel/{id}/configurations", lock(func(w http.ResponseWriter, r *http.Request) {
		t := find(r.PathValue("id"))
		writeCFSuccess(w, map[string]any{"config": map[string]any{"ingress": t.Ingress}})
	}))
	mux.HandleFunc("PUT /accounts/acct1/cfd_tunnel/{id}/configurations", lock(func(w http.ResponseWriter, r *http.Request) {
		body := decode(r)
		a.puts++
		t := find(r.PathValue("id"))
		t.Ingress = nil
		for _, rule := range body["config"].(map[string]any)["ingress"].([]any) {
			m := rule.(map[string]any)
			host, _ := m["hostname"].(string)
			t.Ingress = append(t.Ingress, fakeIngressRule{Hostname: host, Service: m["service"].(string)})
		}
		writeCFSuccess(w, nil)
	}))

	mux.HandleFunc("GET /zones/zone1/dns_records", lock(func(w http.ResponseWriter, r *http.Request) {
		name := r.URL.Query().Get("name")
		out := []map[string]any{}
		for _, d := range a.dns {
			if name == "" || d.Name == name {
				out = append(out, map[string]any{"id": d.ID, "name": d.Name, "type": d.Type, "content": d.Content})
			}
		}
		writeCFSuccess(w, out)
	}))
	mux.HandleFunc("POST /zones/zone1/dns_records", lock(func(w http.ResponseWriter, r *http.Request) {
		body := decode(r)
		a.creates["dns"]++
		a.nextID++
		a.dns = append(a.dns, fakeDNSRecord{ID: fmt.Sprintf("dns%d", a.nextID), Name: body["name"].(string), Type: body["type"].(string), Content: body["content"].(string)})
		writeCFSuccess(w, nil)
	}))

	mux.HandleFunc("GET /accounts/acct1/access/apps", lock(func(w http.ResponseWriter, r *http.Request) {
		out := []map[string]any{}
		for _, app := range a.apps {
			out = append(out, map[string]any{"id": app.ID, "name": app.Name, "domain": app.Domain, "type": app.Type, "aud": app.Aud})
		}
		writeCFSuccess(w, out)
	}))
	mux.HandleFunc("POST /accounts/acct1/access/apps", lock(func(w http.ResponseWriter, r *http.Request) {
		body := decode(r)
		a.creates["app"]++
		n := len(a.apps) + 1
		app := fakeApp{ID: fmt.Sprintf("app%d", n), Name: body["name"].(string), Domain: body["domain"].(string), Type: "self_hosted", Aud: fmt.Sprintf("aud%d", n)}
		a.apps = append(a.apps, app)
		writeCFSuccess(w, map[string]any{"id": app.ID, "aud": app.Aud})
	}))
	emailOf := func(body map[string]any) string {
		inc := body["include"].([]any)[0].(map[string]any)["email"].(map[string]any)
		return inc["email"].(string)
	}
	mux.HandleFunc("GET /accounts/acct1/access/apps/{app}/policies", lock(func(w http.ResponseWriter, r *http.Request) {
		out := []map[string]any{}
		for _, p := range a.policies[r.PathValue("app")] {
			out = append(out, map[string]any{"id": p.ID, "name": p.Name})
		}
		writeCFSuccess(w, out)
	}))
	mux.HandleFunc("POST /accounts/acct1/access/apps/{app}/policies", lock(func(w http.ResponseWriter, r *http.Request) {
		body := decode(r)
		a.creates["policy"]++
		app := r.PathValue("app")
		a.policies[app] = append(a.policies[app], &fakePolicy{ID: fmt.Sprintf("pol%d", len(a.policies[app])+1), Name: body["name"].(string), Email: emailOf(body)})
		writeCFSuccess(w, nil)
	}))
	mux.HandleFunc("PUT /accounts/acct1/access/apps/{app}/policies/{pid}", lock(func(w http.ResponseWriter, r *http.Request) {
		body := decode(r)
		for _, p := range a.policies[r.PathValue("app")] {
			if p.ID == r.PathValue("pid") {
				p.Email = emailOf(body)
			}
		}
		writeCFSuccess(w, nil)
	}))

	mux.HandleFunc("GET /zones", lock(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("name") != "example.com" {
			writeCFSuccess(w, []any{})
			return
		}
		writeCFSuccess(w, []any{map[string]any{"id": "zone1", "account": map[string]any{"id": "acct1", "name": "Acct"}}})
	}))
	mux.HandleFunc("DELETE /zones/zone1/dns_records/{id}", lock(func(w http.ResponseWriter, r *http.Request) {
		a.deletes["dns"]++
		kept := a.dns[:0]
		for _, d := range a.dns {
			if d.ID != r.PathValue("id") {
				kept = append(kept, d)
			}
		}
		a.dns = kept
		writeCFSuccess(w, nil)
	}))
	mux.HandleFunc("DELETE /accounts/acct1/access/apps/{id}", lock(func(w http.ResponseWriter, r *http.Request) {
		a.deletes["app"]++
		kept := a.apps[:0]
		for _, app := range a.apps {
			if app.ID != r.PathValue("id") {
				kept = append(kept, app)
			}
		}
		a.apps = kept
		writeCFSuccess(w, nil)
	}))
	return mux
}

func newFakeAccount(t *testing.T) (*fakeAccount, *cloudflareClient) {
	t.Helper()
	acct := &fakeAccount{policies: map[string][]*fakePolicy{}, creates: map[string]int{}, deletes: map[string]int{}}
	srv := httptest.NewServer(acct.handler())
	t.Cleanup(srv.Close)
	c := newCloudflareClient("test-api-token")
	c.baseURLOverride = srv.URL
	return acct, c
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
	acct, client := newFakeAccount(t)

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
	if result.Aud != "aud1" {
		t.Errorf("Aud = %q, want aud1", result.Aud)
	}
	if result.TunnelToken != "token-for-tunnel1" {
		t.Errorf("TunnelToken = %q, want token-for-tunnel1", result.TunnelToken)
	}
	if len(result.Reused) != 0 {
		t.Errorf("a first run reuses nothing, got %v", result.Reused)
	}
	for _, k := range []string{"tunnel", "dns", "app", "policy"} {
		if acct.creates[k] != 1 {
			t.Errorf("creates[%s] = %d, want 1", k, acct.creates[k])
		}
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

// The scenario that motivated this: provisioning succeeded, the on-device
// switch failed, and the user submits the form again.
func TestProvisionCloudflareRetryReusesEverything(t *testing.T) {
	acct, client := newFakeAccount(t)

	first, err := provisionCloudflare(client, testConfig())
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	second, err := provisionCloudflare(client, testConfig())
	if err != nil {
		t.Fatalf("retry must succeed, got: %v", err)
	}

	for _, k := range []string{"tunnel", "dns", "app", "policy"} {
		if acct.creates[k] != 1 {
			t.Errorf("creates[%s] = %d after a retry, want still 1", k, acct.creates[k])
		}
	}
	if second.Aud != first.Aud || second.TunnelToken != first.TunnelToken {
		t.Errorf("retry returned different values: %+v vs %+v", second, first)
	}
	if got := strings.Join(second.Reused, ","); got != "tunnel,DNS record,Access application,Access policy" {
		t.Errorf("Reused = %q", got)
	}
}

func TestProvisionCloudflareRetryUpdatesCorrectedEmail(t *testing.T) {
	acct, client := newFakeAccount(t)
	if _, err := provisionCloudflare(client, testConfig()); err != nil {
		t.Fatal(err)
	}
	cfg := testConfig()
	cfg.OwnerEmail = "fixed@example.com"
	if _, err := provisionCloudflare(client, cfg); err != nil {
		t.Fatalf("retry: %v", err)
	}
	if got := acct.policies["app1"][0].Email; got != "fixed@example.com" {
		t.Errorf("policy email = %q, want the corrected one", got)
	}
	if acct.creates["policy"] != 1 {
		t.Errorf("policy was created again instead of updated")
	}
}

// A previous run that died right after creating the tunnel left it
// unconfigured; the retry should pick it up and finish the job.
func TestProvisionCloudflareResumesAfterPartialFailure(t *testing.T) {
	acct, client := newFakeAccount(t)
	acct.tunnels = append(acct.tunnels, &fakeTunnel{ID: "tunnel1", Name: "pi-test", ConfigSrc: "cloudflare"})

	result, err := provisionCloudflare(client, testConfig())
	if err != nil {
		t.Fatalf("provisionCloudflare: %v", err)
	}
	if acct.creates["tunnel"] != 0 {
		t.Errorf("created a second tunnel instead of reusing the leftover")
	}
	if result.TunnelToken != "token-for-tunnel1" || len(acct.dns) != 1 || len(acct.apps) != 1 {
		t.Errorf("did not finish provisioning: %+v dns=%v apps=%v", result, acct.dns, acct.apps)
	}
}

// Safety: a same-named resource that isn't ours must never be modified.
func TestProvisionCloudflareRefusesForeignTunnel(t *testing.T) {
	acct, client := newFakeAccount(t)
	acct.tunnels = append(acct.tunnels, &fakeTunnel{
		ID: "other", Name: "pi-test", ConfigSrc: "cloudflare",
		Ingress: []fakeIngressRule{{Hostname: "something.example.com", Service: "http://10.0.0.5:3000"}, {Service: "http_status:404"}},
	})

	if _, err := provisionCloudflare(client, testConfig()); err == nil {
		t.Fatal("expected an error for a tunnel that routes other traffic")
	}
	if acct.puts != 0 {
		t.Error("overwrote the routing of a tunnel that isn't ours")
	}
	if len(acct.dns)+len(acct.apps) != 0 {
		t.Error("created resources despite refusing")
	}
}

func TestProvisionCloudflareRefusesLocallyManagedTunnel(t *testing.T) {
	acct, client := newFakeAccount(t)
	acct.tunnels = append(acct.tunnels, &fakeTunnel{ID: "local", Name: "pi-test", ConfigSrc: "local"})
	if _, err := provisionCloudflare(client, testConfig()); err == nil {
		t.Fatal("expected an error for a locally-managed tunnel")
	}
	if acct.puts != 0 {
		t.Error("touched a locally-managed tunnel")
	}
}

func TestProvisionCloudflareRefusesForeignDNSRecord(t *testing.T) {
	acct, client := newFakeAccount(t)
	acct.dns = append(acct.dns, fakeDNSRecord{Name: "pi-test.example.com", Type: "CNAME", Content: "someone-elses.cfargotunnel.com"})

	if _, err := provisionCloudflare(client, testConfig()); err == nil {
		t.Fatal("expected an error when the hostname already points elsewhere")
	}
	if len(acct.dns) != 1 || acct.dns[0].Content != "someone-elses.cfargotunnel.com" {
		t.Errorf("modified an existing DNS record: %+v", acct.dns)
	}
	if len(acct.apps) != 0 {
		t.Error("went on to create an Access app after refusing")
	}
}

func TestProvisionCloudflareRefusesForeignAccessApp(t *testing.T) {
	acct, client := newFakeAccount(t)
	acct.apps = append(acct.apps, fakeApp{ID: "x", Name: "someone else's app", Domain: "pi-test.example.com", Type: "self_hosted", Aud: "a"})

	if _, err := provisionCloudflare(client, testConfig()); err == nil {
		t.Fatal("expected an error when another Access app protects the hostname")
	}
	if acct.creates["app"] != 0 || acct.creates["policy"] != 0 {
		t.Error("created an app/policy despite the conflict")
	}
}
