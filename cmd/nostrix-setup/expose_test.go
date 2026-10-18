package main

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func intp(n int) *int { return &n }

func exposeCfg() exposeConfig {
	return exposeConfig{BaseDomain: "example.com", DeviceName: "pi-test", OwnerEmail: "owner@example.com"}
}

func TestReadDeclaredApps(t *testing.T) {
	path := filepath.Join(t.TempDir(), "apps.json")
	os.WriteFile(path, []byte(`{"zeta":{"port":9000,"expose":"lan"},"alpha":{"port":null,"expose":"none"}}`), 0o644)
	apps, err := readDeclaredApps(path)
	if err != nil {
		t.Fatal(err)
	}
	if len(apps) != 2 || apps[0].Name != "alpha" || apps[1].Name != "zeta" || apps[0].Port != nil || *apps[1].Port != 9000 {
		t.Errorf("apps = %+v, want sorted by name with null port preserved", apps)
	}
}

func TestReadDeclaredAppsMissingFileMeansNoApps(t *testing.T) {
	apps, err := readDeclaredApps(filepath.Join(t.TempDir(), "absent.json"))
	if err != nil || len(apps) != 0 {
		t.Errorf("apps = %v, err = %v; want none and no error", apps, err)
	}
}

func TestPlanExposureOnlyTakesCloudflareApps(t *testing.T) {
	plan, err := planExposure(exposeCfg(), []declaredApp{
		{Name: "a", Port: intp(1), Expose: "none"},
		{Name: "b", Port: intp(2), Expose: "lan"},
		{Name: "my_app", Port: intp(3), Expose: "cloudflare"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(plan) != 1 || plan[0].Hostname != "pi-test-my-app.example.com" || plan[0].Port != 3 {
		t.Errorf("plan = %+v; want only my_app, on a single-label hostname", plan)
	}
}

func TestPlanExposureRejectsBadInput(t *testing.T) {
	cases := map[string][]declaredApp{
		"no port":       {{Name: "a", Expose: "cloudflare"}},
		"invalid label": {{Name: "a.b", Port: intp(1), Expose: "cloudflare"}},
		"too long":      {{Name: strings.Repeat("x", 70), Port: intp(1), Expose: "cloudflare"}},
		"collision":     {{Name: "a_b", Port: intp(1), Expose: "cloudflare"}, {Name: "a-b", Port: intp(2), Expose: "cloudflare"}},
	}
	for name, apps := range cases {
		if _, err := planExposure(exposeCfg(), apps); err == nil {
			t.Errorf("%s: expected an error", name)
		}
	}
}

// provisioned returns a fake account where the device itself is already set
// up, the state a configured device is in when apps get published.
func provisioned(t *testing.T) (*fakeAccount, *cloudflareClient) {
	t.Helper()
	acct, client := newFakeAccount(t)
	if _, err := provisionCloudflare(client, testConfig()); err != nil {
		t.Fatalf("provisionCloudflare: %v", err)
	}
	return acct, client
}

var jelly = []declaredApp{{Name: "jelly", Port: intp(8096), Expose: "cloudflare"}}

func TestReconcileExposurePublishesAnApp(t *testing.T) {
	acct, client := provisioned(t)

	res, err := reconcileExposure(client, exposeCfg(), jelly)
	if err != nil {
		t.Fatalf("reconcileExposure: %v", err)
	}
	if len(res.Exposed) != 1 || res.Exposed[0].Hostname != "pi-test-jelly.example.com" {
		t.Errorf("Exposed = %+v", res.Exposed)
	}

	want := []fakeIngressRule{
		{Hostname: "pi-test.example.com", Service: "http://127.0.0.1:8080"},
		{Hostname: "pi-test-jelly.example.com", Service: "http://127.0.0.1:8096"},
		{Service: "http_status:404"},
	}
	if got := acct.tunnels[0].Ingress; !reflect.DeepEqual(got, want) {
		t.Errorf("ingress = %+v, want %+v", got, want)
	}
	if len(acct.dns) != 2 || acct.dns[1].Name != "pi-test-jelly.example.com" || acct.dns[1].Content != "tunnel1.cfargotunnel.com" {
		t.Errorf("dns = %+v", acct.dns)
	}
	if len(acct.apps) != 2 || acct.apps[1].Name != "pi-test-jelly" || acct.apps[1].Domain != "pi-test-jelly.example.com" {
		t.Errorf("access apps = %+v", acct.apps)
	}
	if p := acct.policies[acct.apps[1].ID]; len(p) != 1 || p[0].Email != "owner@example.com" {
		t.Errorf("the app must be restricted to the owner: %+v", p)
	}
}

// An app must never be routable before Access protects it.
func TestReconcileExposureProtectsBeforeRouting(t *testing.T) {
	acct, client := provisioned(t)
	acct.log = nil

	if _, err := reconcileExposure(client, exposeCfg(), jelly); err != nil {
		t.Fatal(err)
	}
	index := func(prefix string) int {
		for i, l := range acct.log {
			if l == prefix || (strings.HasSuffix(prefix, "/") && strings.HasPrefix(l, prefix)) {
				return i
			}
		}
		return -1
	}
	app, policy := index("POST /accounts/acct1/access/apps"), index("POST /accounts/acct1/access/apps/")
	dns, route := index("POST /zones/zone1/dns_records"), index("PUT /accounts/acct1/cfd_tunnel/tunnel1/configurations")
	if app < 0 || policy < 0 || dns < 0 || route < 0 {
		t.Fatalf("missing calls in log: %v", acct.log)
	}
	if !(app < policy && policy < dns && dns < route) {
		t.Errorf("order must be Access app → policy → DNS → route, got indexes app=%d policy=%d dns=%d route=%d\n%v", app, policy, dns, route, acct.log)
	}
}

func TestReconcileExposureIsIdempotent(t *testing.T) {
	acct, client := provisioned(t)
	if _, err := reconcileExposure(client, exposeCfg(), jelly); err != nil {
		t.Fatal(err)
	}
	before := map[string]int{"dns": acct.creates["dns"], "app": acct.creates["app"], "policy": acct.creates["policy"]}
	if _, err := reconcileExposure(client, exposeCfg(), jelly); err != nil {
		t.Fatalf("second run: %v", err)
	}
	for k, n := range before {
		if acct.creates[k] != n {
			t.Errorf("creates[%s] went from %d to %d on a repeat run", k, n, acct.creates[k])
		}
	}
	if acct.deletes["dns"]+acct.deletes["app"] != 0 {
		t.Error("a repeat run deleted something")
	}
}

func TestReconcileExposureTakesDownAnAppThatIsNoLongerPublished(t *testing.T) {
	acct, client := provisioned(t)
	if _, err := reconcileExposure(client, exposeCfg(), jelly); err != nil {
		t.Fatal(err)
	}

	res, err := reconcileExposure(client, exposeCfg(), []declaredApp{{Name: "jelly", Port: intp(8096), Expose: "lan"}})
	if err != nil {
		t.Fatalf("reconcile after un-publishing: %v", err)
	}
	if len(res.Removed) != 1 || res.Removed[0] != "pi-test-jelly.example.com" {
		t.Errorf("Removed = %v", res.Removed)
	}
	if got := len(acct.tunnels[0].Ingress); got != 2 { // device rule + catch-all
		t.Errorf("ingress should be back to the device alone, got %+v", acct.tunnels[0].Ingress)
	}
	if len(acct.dns) != 1 || len(acct.apps) != 1 {
		t.Errorf("the app's DNS record and Access app should be gone: dns=%+v apps=%+v", acct.dns, acct.apps)
	}
	// The device's own resources must survive.
	if acct.dns[0].Name != "pi-test.example.com" || acct.apps[0].Name != "pi-test" {
		t.Errorf("removed the wrong things: dns=%+v apps=%+v", acct.dns, acct.apps)
	}
}

// Cleanup only deletes what still looks like ours.
func TestReconcileExposureLeavesRepointedRecordsAlone(t *testing.T) {
	acct, client := provisioned(t)
	if _, err := reconcileExposure(client, exposeCfg(), jelly); err != nil {
		t.Fatal(err)
	}
	// Someone has since repointed the record and replaced the Access app.
	for i := range acct.dns {
		if acct.dns[i].Name == "pi-test-jelly.example.com" {
			acct.dns[i].Content = "somewhere-else.example.net"
		}
	}
	for i := range acct.apps {
		if acct.apps[i].Domain == "pi-test-jelly.example.com" {
			acct.apps[i].Name = "someone else's app"
		}
	}

	if _, err := reconcileExposure(client, exposeCfg(), nil); err != nil {
		t.Fatal(err)
	}
	if acct.deletes["dns"] != 0 || acct.deletes["app"] != 0 {
		t.Errorf("deleted resources that were no longer ours: %+v", acct.deletes)
	}
	if got := len(acct.tunnels[0].Ingress); got != 2 {
		t.Errorf("the route itself should still be removed, ingress = %+v", acct.tunnels[0].Ingress)
	}
}

func TestReconcileExposureRefusesAForeignTunnel(t *testing.T) {
	acct, client := newFakeAccount(t)
	acct.tunnels = append(acct.tunnels, &fakeTunnel{
		ID: "other", Name: "pi-test", ConfigSrc: "cloudflare",
		Ingress: []fakeIngressRule{{Hostname: "something.example.com", Service: "http://10.0.0.5:3000"}, {Service: "http_status:404"}},
	})
	if _, err := reconcileExposure(client, exposeCfg(), jelly); err == nil {
		t.Fatal("expected a refusal to edit a tunnel that isn't this device's")
	}
	if acct.puts != 0 {
		t.Error("rewrote a tunnel that isn't ours")
	}
}

func TestReconcileExposureNeedsAnExistingTunnel(t *testing.T) {
	_, client := newFakeAccount(t)
	if _, err := reconcileExposure(client, exposeCfg(), jelly); err == nil || !strings.Contains(err.Error(), "no tunnel") {
		t.Errorf("want a clear 'no tunnel' error, got: %v", err)
	}
}

// The bootstrap/CLI flow must still recognise a tunnel that has app routes.
func TestProvisionAfterAppsAreExposedStillRecognisesTheTunnel(t *testing.T) {
	acct, client := provisioned(t)
	if _, err := reconcileExposure(client, exposeCfg(), jelly); err != nil {
		t.Fatal(err)
	}
	if _, err := provisionCloudflare(client, testConfig()); err != nil {
		t.Fatalf("provisioning must accept a tunnel that routes exposed apps: %v", err)
	}
	if acct.creates["tunnel"] != 1 {
		t.Errorf("created a second tunnel")
	}
	// ...and re-running setup must not take the published app offline.
	got := acct.tunnels[0].Ingress
	if len(got) != 3 || got[1].Hostname != "pi-test-jelly.example.com" || got[1].Service != "http://127.0.0.1:8096" {
		t.Errorf("provisioning dropped the app route: %+v", got)
	}
}

// A device set up before the base domain was recorded must not ask for it:
// its token covers one domain, which is it.
func TestReconcileExposureDetectsTheBaseDomain(t *testing.T) {
	_, client := provisioned(t)
	cfg := exposeCfg()
	cfg.BaseDomain = ""

	res, err := reconcileExposure(client, cfg, jelly)
	if err != nil {
		t.Fatalf("reconcileExposure: %v", err)
	}
	if res.BaseDomain != "example.com" || len(res.Exposed) != 1 || res.Exposed[0].Hostname != "pi-test-jelly.example.com" {
		t.Errorf("res = %+v; want the base domain detected as example.com", res)
	}
}
