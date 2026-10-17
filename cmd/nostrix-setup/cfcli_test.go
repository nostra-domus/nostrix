package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"
)

// fakeCF answers cf commands with output shaped like the real CLI's
// (bare "result" JSON), recording every invocation.
type fakeCF struct {
	calls  []string
	envs   [][]string
	groups string
	fail   map[string]bool // command prefix -> fail
}

func (f *fakeCF) run(env []string, args ...string) ([]byte, error) {
	cmd := strings.Join(args, " ")
	f.calls = append(f.calls, cmd)
	f.envs = append(f.envs, env)
	for p := range f.fail {
		if strings.HasPrefix(cmd, p) {
			return nil, fmt.Errorf("forbidden")
		}
	}
	switch {
	case strings.HasPrefix(cmd, "zones list"):
		return []byte(`[{"id":"zone1","name":"example.com","account":{"id":"acct1","name":"My Account"}}]`), nil
	case strings.HasPrefix(cmd, "zero-trust organization get"):
		return []byte(`{"auth_domain":"myteam.cloudflareaccess.com"}`), nil
	case strings.HasPrefix(cmd, "user tokens permission-groups list"):
		return []byte(f.groups), nil
	case strings.HasPrefix(cmd, "user tokens create"):
		return []byte(`{"id":"tok1","value":"secret-token"}`), nil
	}
	return nil, fmt.Errorf("unexpected cf command: %s", cmd)
}

// Duplicated names with different scopes, like the real list.
const testGroups = `[
 {"id":"tunnel-w","name":"Cloudflare Tunnel Write","scopes":["com.cloudflare.api.account"]},
 {"id":"apps-w-zone","name":"Access: Apps and Policies Write","scopes":["com.cloudflare.api.account.zone"]},
 {"id":"apps-w-acct","name":"Access: Apps and Policies Write","scopes":["com.cloudflare.api.account"]},
 {"id":"org-r","name":"Access: Organizations, Identity Providers, and Groups Read","scopes":["com.cloudflare.api.account"]},
 {"id":"dns-w","name":"DNS Write","scopes":["com.cloudflare.api.account.zone"]},
 {"id":"zone-r","name":"Zone Read","scopes":["com.cloudflare.api.account.zone"]}
]`

func TestPrepareViaCF(t *testing.T) {
	f := &fakeCF{}
	res, err := prepareViaCF(f.run, "example.com")
	if err != nil {
		t.Fatalf("prepareViaCF: %v", err)
	}
	if res.AccountID != "acct1" || res.ZoneID != "zone1" || res.TeamDomain != "myteam" {
		t.Errorf("result = %+v", res)
	}
	// The org lookup must be pinned to the zone's account.
	if got := f.envs[1]; len(got) != 1 || got[0] != "CLOUDFLARE_ACCOUNT_ID=acct1" {
		t.Errorf("organization get env = %v, want CLOUDFLARE_ACCOUNT_ID=acct1", got)
	}
}

func TestPrepareViaCFMissingOrgIsNotFatal(t *testing.T) {
	f := &fakeCF{fail: map[string]bool{"zero-trust": true}}
	res, err := prepareViaCF(f.run, "example.com")
	if err != nil {
		t.Fatalf("a missing Access org must not be a hard error: %v", err)
	}
	if res.TeamDomain != "" || res.ZoneID != "zone1" {
		t.Errorf("result = %+v", res)
	}
}

func TestMintScopedTokenBuildsScopedPolicies(t *testing.T) {
	f := &fakeCF{groups: testGroups}
	res := prepResult{AccountID: "acct1", ZoneID: "zone1"}
	tok, err := mintScopedToken(f.run, res, "nostrix-test", time.Date(2026, 10, 24, 0, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("mintScopedToken: %v", err)
	}
	if tok != "secret-token" {
		t.Errorf("token = %q", tok)
	}

	var create string
	for _, c := range f.calls {
		if strings.HasPrefix(c, "user tokens create") {
			create = c
		}
	}
	if !strings.Contains(create, "--expires-on 2026-10-24T00:00:00Z") {
		t.Errorf("missing expiry: %s", create)
	}

	policies, err := buildTokenPolicies(mustGroups(t, testGroups), "acct1", "zone1")
	if err != nil {
		t.Fatal(err)
	}
	var got []struct {
		Resources map[string]string `json:"resources"`
		Groups    []struct {
			ID string `json:"id"`
		} `json:"permission_groups"`
	}
	if err := json.Unmarshal(policies, &got); err != nil || len(got) != 2 {
		t.Fatalf("policies = %s (%v)", policies, err)
	}
	ids := func(i int) string {
		var s []string
		for _, g := range got[i].Groups {
			s = append(s, g.ID)
		}
		return strings.Join(s, ",")
	}
	if _, ok := got[0].Resources["com.cloudflare.api.account.acct1"]; !ok {
		t.Errorf("policy 0 resources = %v", got[0].Resources)
	}
	// Account policy must pick the account-scoped "Apps and Policies Write",
	// not the same-named zone-scoped group.
	if want := "tunnel-w,apps-w-acct,org-r"; ids(0) != want {
		t.Errorf("account groups = %s, want %s", ids(0), want)
	}
	if _, ok := got[1].Resources["com.cloudflare.api.account.zone.zone1"]; !ok {
		t.Errorf("policy 1 resources = %v", got[1].Resources)
	}
	if want := "dns-w,zone-r"; ids(1) != want {
		t.Errorf("zone groups = %s, want %s", ids(1), want)
	}
}

func TestMintScopedTokenMissingGroupFailsBeforeCreating(t *testing.T) {
	f := &fakeCF{groups: `[{"id":"x","name":"Zone Read","scopes":["com.cloudflare.api.account.zone"]}]`}
	if _, err := mintScopedToken(f.run, prepResult{AccountID: "a", ZoneID: "z"}, "n", time.Now()); err == nil {
		t.Fatal("expected an error when required permission groups are missing")
	}
	for _, c := range f.calls {
		if strings.HasPrefix(c, "user tokens create") {
			t.Error("must not create a token when the permission set can't be resolved")
		}
	}
}

func mustGroups(t *testing.T, s string) []permissionGroup {
	t.Helper()
	var g []permissionGroup
	if err := json.Unmarshal([]byte(s), &g); err != nil {
		t.Fatal(err)
	}
	return g
}
