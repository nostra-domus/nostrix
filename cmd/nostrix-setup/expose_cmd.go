package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
)

// runExpose implements `nostrix-setup expose`: make Cloudflare match the apps
// the running system declares with expose = "cloudflare" (see expose.go).
// The API token comes from $CLOUDFLARE_API_TOKEN or a prompt, is used for
// this run only, and is never stored.
func runExpose(args []string) {
	fs := flag.NewFlagSet("expose", flag.ExitOnError)
	appsFile := fs.String("apps", defaultAppsFile, "declared-apps file written by the running system")
	stateFile := fs.String("state", "/etc/nostrix/state.json", "path to the state file")
	dryRun := fs.Bool("dry-run", false, "show what would be published without contacting Cloudflare")
	fs.Parse(args) //nolint:errcheck

	s, err := loadState(*stateFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: could not load state from %s: %v\n", *stateFile, err)
		os.Exit(1)
	}
	if s.CloudflareTunnelToken == "" {
		fmt.Fprintln(os.Stderr, "error: Cloudflare remote access isn't set up on this device (run nostrix-setup first).")
		os.Exit(1)
	}
	apps, err := readDeclaredApps(*appsFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	r := bufio.NewReader(os.Stdin)
	base := s.CloudflareBaseDomain
	token := ""
	if !*dryRun {
		token = os.Getenv("CLOUDFLARE_API_TOKEN")
		if token == "" {
			token = prompt(r, "Cloudflare API token (used for this run only, not stored)", "")
		}
		if token == "" {
			fmt.Fprintln(os.Stderr, "error: an API token is required")
			os.Exit(2)
		}
	}
	if base == "" && token != "" {
		// The device's token covers one domain; use it rather than asking.
		if b, err := newCloudflareClient(token).onlyZone(); err == nil {
			base = b
		} else {
			fmt.Fprintf(os.Stderr, "note: %v\n", err)
		}
	}
	if base == "" {
		base = prompt(r, "Base domain (e.g. example.com)", "")
	}
	cfg := exposeConfig{BaseDomain: base, DeviceName: s.Hostname, OwnerEmail: s.OwnerEmail}
	plan, err := planExposure(cfg, apps)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	if len(plan) == 0 {
		fmt.Println("No app is set to expose = \"cloudflare\"; anything published before will be taken down.")
	}
	for _, p := range plan {
		fmt.Printf("  %-20s https://%s  →  127.0.0.1:%d\n", p.Name, p.Hostname, p.Port)
	}
	if *dryRun {
		fmt.Println("Dry run — nothing contacted.")
		return
	}

	res, err := reconcileExposure(newCloudflareClient(token), cfg, apps)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	fmt.Println(res.summary())

	if s.CloudflareBaseDomain != base {
		s.CloudflareBaseDomain = base
		if err := saveState(*stateFile, s); err != nil {
			fmt.Fprintf(os.Stderr, "warning: could not remember the base domain: %v\n", err)
		}
	}
}
