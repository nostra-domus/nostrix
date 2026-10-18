package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
	"time"
)

// prepResult holds everything the bootstrap form / wizard asks for besides
// the API token itself and the owner's email, plus the outcome of each
// read-only permission probe.
type prepResult struct {
	AccountID   string      `json:"accountID"`
	AccountName string      `json:"accountName"`
	ZoneID      string      `json:"zoneID"`
	BaseDomain  string      `json:"baseDomain"`
	TeamDomain  string      `json:"teamDomain,omitempty"`
	APIToken    string      `json:"apiToken,omitempty"`  // only when --create-token minted one
	TokenKind   string      `json:"tokenKind,omitempty"` // "account" or "user"
	Checks      []prepCheck `json:"checks"`
}

type prepCheck struct {
	Name string `json:"name"`
	OK   bool   `json:"ok"`
	Err  string `json:"error,omitempty"`
}

func (r prepResult) allOK() bool {
	for _, c := range r.Checks {
		if !c.OK {
			return false
		}
	}
	return r.TeamDomain != ""
}

// cfZone is the part of a zone object both lookups need.
type cfZone struct {
	ID      string `json:"id"`
	Account struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	} `json:"account"`
}

// prepareCloudflare looks up the IDs provisionCloudflare needs and probes
// the token's permissions with read-only calls (no resources are created).
// The zone lookup also yields the account ID, so the token needs no
// separate "Account Settings: Read" permission.
func prepareCloudflare(c *cloudflareClient, baseDomain string) (prepResult, error) {
	res := prepResult{BaseDomain: baseDomain}

	var zones []cfZone
	if err := c.do("GET", "/zones?name="+url.QueryEscape(baseDomain), nil, &zones); err != nil {
		return res, fmt.Errorf("looking up zone (token needs Zone:Read): %w", err)
	}
	if len(zones) == 0 {
		return res, fmt.Errorf("no zone named %q is visible to this token — check the domain, and that the token's zone resources include it", baseDomain)
	}
	res.ZoneID = zones[0].ID
	res.AccountID = zones[0].Account.ID
	res.AccountName = zones[0].Account.Name

	// Read-only probes: reading is implied by the Edit permissions
	// provisionCloudflare needs, so a failure here means the matching write
	// would fail too.
	probe := func(name, path string) {
		err := c.do("GET", path, nil, nil)
		pc := prepCheck{Name: name, OK: err == nil}
		if err != nil {
			pc.Err = err.Error()
		}
		res.Checks = append(res.Checks, pc)
	}
	probe("Tunnel (Cloudflare Tunnel: Edit)", fmt.Sprintf("/accounts/%s/cfd_tunnel?per_page=1", res.AccountID))
	probe("Access apps (Access: Apps and Policies: Edit)", fmt.Sprintf("/accounts/%s/access/apps?per_page=1", res.AccountID))
	probe("DNS (Zone: DNS: Edit)", fmt.Sprintf("/zones/%s/dns_records?per_page=1", res.ZoneID))

	var org struct {
		AuthDomain string `json:"auth_domain"`
	}
	if err := c.do("GET", fmt.Sprintf("/accounts/%s/access/organizations", res.AccountID), nil, &org); err == nil {
		res.TeamDomain = strings.TrimSuffix(org.AuthDomain, ".cloudflareaccess.com")
	}
	return res, nil
}

const prepTokenHelp = `A Cloudflare API token is needed. (Alternatively, install Cloudflare's cf CLI,
run "cf auth login", and re-run this with --create-token to have one made for you.)
Create one by hand at
https://dash.cloudflare.com/profile/api-tokens ("Create Custom Token") with:

  Account | Cloudflare Tunnel                     | Edit
  Account | Access: Apps and Policies             | Edit
  Account | Access: Organizations, Identity Providers, and Groups | Read
  Zone    | DNS                                   | Edit
  Zone    | Zone                                  | Read

Restrict Zone Resources to the one zone you'll use, then re-run with the
token in $CLOUDFLARE_API_TOKEN (or enter it at the prompt).`

// runPrep implements `nostrix-setup cloudflare`: given an API token and a
// domain, it prints the account ID, zone ID and team domain the bootstrap
// form asks for, and verifies the token has the permissions provisioning
// will need — so mistakes surface here, not mid-provisioning on the device.
func runPrep(args []string) {
	fs := flag.NewFlagSet("cloudflare", flag.ExitOnError)
	domain := fs.String("domain", "", "base domain in your Cloudflare account (e.g. example.com)")
	asJSON := fs.Bool("json", false, "print machine-readable JSON instead of text")
	createToken := fs.Bool("create-token", false, "with the cf CLI: also create a scoped API token for the device")
	tokenDays := fs.Int("token-days", 7, "lifetime of the token made by --create-token, in days")
	_ = fs.Parse(args)

	r := bufio.NewReader(os.Stdin)
	if *domain == "" {
		*domain = prompt(r, "Base domain (e.g. example.com)", "")
	}
	if *domain == "" {
		fmt.Fprintln(os.Stderr, "error: a domain is required")
		os.Exit(2)
	}

	// An explicit API token wins; otherwise use the cf CLI's login if it's
	// installed; otherwise ask for a token.
	var res prepResult
	var err error
	token := os.Getenv("CLOUDFLARE_API_TOKEN")
	run, haveCF := newCFRunner()
	switch {
	case token == "" && haveCF:
		res, err = prepareViaCF(run, *domain)
		if err == nil && *createToken {
			name := fmt.Sprintf("nostrix-%s-%s", *domain, time.Now().Format("20060102"))
			expires := time.Now().Add(time.Duration(*tokenDays) * 24 * time.Hour)
			if res.APIToken, res.TokenKind, err = mintScopedToken(run, res, name, expires); err == nil {
				// Verify the fresh token really has what provisioning needs.
				if checked, cerr := prepareCloudflare(newCloudflareClient(res.APIToken), *domain); cerr == nil {
					res.Checks = checked.Checks
					if res.TeamDomain == "" {
						res.TeamDomain = checked.TeamDomain
					}
				}
			}
		}
	default:
		if *createToken {
			fmt.Fprintln(os.Stderr, "error: --create-token needs the cf CLI (and no $CLOUDFLARE_API_TOKEN set)")
			os.Exit(2)
		}
		if token == "" {
			fmt.Fprintln(os.Stderr, prepTokenHelp)
			fmt.Fprintln(os.Stderr)
			token = prompt(r, "Cloudflare API token", "")
		}
		if token == "" {
			fmt.Fprintln(os.Stderr, "error: an API token is required")
			os.Exit(2)
		}
		res, err = prepareCloudflare(newCloudflareClient(token), *domain)
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if *asJSON {
		_ = json.NewEncoder(os.Stdout).Encode(res)
	} else {
		printPrep(res)
	}
	if !res.allOK() {
		os.Exit(1)
	}
}

func printPrep(res prepResult) {
	fmt.Println("Values for the Nostrix setup form (the API token and your email are the other two):")
	fmt.Println()
	fmt.Printf("  Cloudflare account ID : %s  (%s)\n", res.AccountID, res.AccountName)
	fmt.Printf("  Cloudflare zone ID    : %s\n", res.ZoneID)
	fmt.Printf("  Base domain           : %s\n", res.BaseDomain)
	if res.TeamDomain != "" {
		fmt.Printf("  Cloudflare team domain: %s\n", res.TeamDomain)
	} else {
		fmt.Println("  Cloudflare team domain: (not found — see below)")
	}
	if res.APIToken != "" {
		fmt.Printf("  Cloudflare API token  : %s\n", res.APIToken)
		fmt.Printf("                          (%s-owned; shown once — it can't be retrieved later; scoped to\n", res.TokenKind)
		fmt.Println("                          this account + zone, with an expiry. Treat it like a password.)")
	}
	if len(res.Checks) > 0 {
		fmt.Println()
		fmt.Println("Token permission checks:")
	}
	for _, c := range res.Checks {
		if c.OK {
			fmt.Printf("  ok    %s\n", c.Name)
		} else {
			fmt.Printf("  FAIL  %s\n        %s\n", c.Name, c.Err)
		}
	}
	if res.TeamDomain == "" {
		fmt.Println("  FAIL  Access organization (Access: Organizations, Identity Providers, and Groups: Read)")
		fmt.Println("        Could not read the team domain. Either the token lacks that permission, or")
		fmt.Println("        Zero Trust isn't set up yet: open https://one.dash.cloudflare.com, pick your")
		fmt.Println("        account, and finish onboarding (the free plan works). The team domain is the")
		fmt.Println("        <team> in <team>.cloudflareaccess.com (Settings → Custom Pages).")
	}
	if res.allOK() {
		fmt.Println()
		fmt.Println("All good — paste these into the setup form, or answer the wizard prompts with them.")
	}
}
