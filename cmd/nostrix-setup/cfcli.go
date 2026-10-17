package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"
)

// Cloudflare's own `cf` CLI (https://developers.cloudflare.com/cf/, beta) is
// an optional alternative to hand-making an API token: after one
// `cf auth login` (browser OAuth) it can look up the account/zone/team
// values and mint a narrowly scoped API token for provisioning. Its login
// credential itself can't be reused as a bearer token by us (it's not
// exposed), so we shell out to it rather than call the API directly.
//
// Output shapes below were verified against a real login: `cf` prints the
// API response's bare "result" (not the {success, result} envelope).

// cfRunner runs one cf command and returns its stdout. env is extra
// KEY=VALUE pairs on top of the process environment.
type cfRunner func(env []string, args ...string) ([]byte, error)

// newCFRunner finds the cf CLI: $NOSTRIX_CF (a command line such as
// "npx --yes cf"), else `cloudflare` (installed alongside cf, and not
// ambiguous with Cloud Foundry's unrelated `cf`), else `cf`.
func newCFRunner() (cfRunner, bool) {
	base := strings.Fields(os.Getenv("NOSTRIX_CF"))
	if len(base) == 0 {
		for _, name := range []string{"cloudflare", "cf"} {
			if p, err := exec.LookPath(name); err == nil {
				base = []string{p}
				break
			}
		}
	}
	if len(base) == 0 {
		return nil, false
	}
	return func(env []string, args ...string) ([]byte, error) {
		cmd := exec.Command(base[0], append(append([]string{}, base[1:]...), args...)...)
		cmd.Env = append(os.Environ(), env...)
		var stderr bytes.Buffer
		cmd.Stderr = &stderr
		out, err := cmd.Output()
		if err != nil {
			return nil, fmt.Errorf("cf %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(stderr.String()))
		}
		return out, nil
	}, true
}

// prepareViaCF is prepareCloudflare's counterpart using the cf CLI's
// existing login instead of an API token. It runs no permission probes —
// the login's scopes are broad; see mintScopedToken for the token that the
// device actually receives.
func prepareViaCF(run cfRunner, baseDomain string) (prepResult, error) {
	res := prepResult{BaseDomain: baseDomain}

	out, err := run(nil, "zones", "list", "--name", baseDomain)
	if err != nil {
		return res, fmt.Errorf("looking up zone (run `cf auth login` first): %w", err)
	}
	var zones []cfZone
	if err := json.Unmarshal(out, &zones); err != nil {
		return res, fmt.Errorf("decoding `cf zones list` output: %w", err)
	}
	if len(zones) == 0 {
		return res, fmt.Errorf("no zone named %q is visible to this login", baseDomain)
	}
	res.ZoneID = zones[0].ID
	res.AccountID = zones[0].Account.ID
	res.AccountName = zones[0].Account.Name

	// Pin the account so a login with several accounts doesn't prompt.
	env := []string{"CLOUDFLARE_ACCOUNT_ID=" + res.AccountID}
	if out, err := run(env, "zero-trust", "organization", "get"); err == nil {
		var org struct {
			AuthDomain string `json:"auth_domain"`
		}
		if json.Unmarshal(out, &org) == nil {
			res.TeamDomain = strings.TrimSuffix(org.AuthDomain, ".cloudflareaccess.com")
		}
	}
	return res, nil
}

const (
	scopeAccount = "com.cloudflare.api.account"
	scopeZone    = "com.cloudflare.api.account.zone"
)

// tokenPermissions are exactly what provisionCloudflare needs, plus the
// organization read prepareCloudflare uses for the team domain. Names are
// as listed by `cf user tokens permission-groups list`; some exist in both
// an account-scoped and a zone-scoped variant, so the scope is part of the
// match.
var tokenPermissions = []struct{ name, scope string }{
	{"Cloudflare Tunnel Write", scopeAccount},
	{"Access: Apps and Policies Write", scopeAccount},
	{"Access: Organizations, Identity Providers, and Groups Read", scopeAccount},
	{"DNS Write", scopeZone},
	{"Zone Read", scopeZone},
}

type permissionGroup struct {
	ID     string   `json:"id"`
	Name   string   `json:"name"`
	Scopes []string `json:"scopes"`
}

func hasScope(g permissionGroup, scope string) bool {
	for _, s := range g.Scopes {
		if s == scope {
			return true
		}
	}
	return false
}

// buildTokenPolicies resolves tokenPermissions against the account's
// permission groups and returns the token's `policies` JSON: one policy
// scoped to the account, one to the single zone.
func buildTokenPolicies(groups []permissionGroup, accountID, zoneID string) ([]byte, error) {
	var accountIDs, zoneIDs []map[string]string
	for _, want := range tokenPermissions {
		found := ""
		for _, g := range groups {
			if g.Name == want.name && hasScope(g, want.scope) {
				found = g.ID
				break
			}
		}
		if found == "" {
			return nil, fmt.Errorf("permission group %q (scope %s) not found — cf's permission list may have changed", want.name, want.scope)
		}
		id := map[string]string{"id": found}
		if want.scope == scopeZone {
			zoneIDs = append(zoneIDs, id)
		} else {
			accountIDs = append(accountIDs, id)
		}
	}
	return json.Marshal([]any{
		map[string]any{
			"effect":            "allow",
			"resources":         map[string]string{scopeAccount + "." + accountID: "*"},
			"permission_groups": accountIDs,
		},
		map[string]any{
			"effect":            "allow",
			"resources":         map[string]string{scopeZone + "." + zoneID: "*"},
			"permission_groups": zoneIDs,
		},
	})
}

// mintScopedToken creates, through the cf CLI's login, an API token limited
// to exactly what provisioning needs (one account, one zone), expiring at
// expires. The secret is returned once by Cloudflare and never again.
func mintScopedToken(run cfRunner, res prepResult, name string, expires time.Time) (string, error) {
	out, err := run(nil, "user", "tokens", "permission-groups", "list")
	if err != nil {
		return "", err
	}
	var groups []permissionGroup
	if err := json.Unmarshal(out, &groups); err != nil {
		return "", fmt.Errorf("decoding permission groups: %w", err)
	}
	policies, err := buildTokenPolicies(groups, res.AccountID, res.ZoneID)
	if err != nil {
		return "", err
	}
	out, err = run(nil, "user", "tokens", "create",
		"--name", name,
		"--policies", string(policies),
		"--expires-on", expires.UTC().Format(time.RFC3339))
	if err != nil {
		return "", fmt.Errorf("creating token (the login may lack API Tokens: Edit): %w", err)
	}
	var created struct {
		Value string `json:"value"`
	}
	if err := json.Unmarshal(out, &created); err != nil || created.Value == "" {
		return "", fmt.Errorf("`cf user tokens create` returned no token value")
	}
	return created.Value, nil
}
