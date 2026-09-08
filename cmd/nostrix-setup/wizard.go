package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"
)

func runWizard() {
	output := flag.String("output", "/etc/nixos/flake.nix", "path to write the generated flake.nix")
	stateFile := flag.String("state", "/etc/nostrix/state.json", "path to write state file")
	dryRun := flag.Bool("dry-run", false, "print the generated config without writing or applying")
	flag.Parse()

	fmt.Println("Nostrix Setup Wizard")
	fmt.Println("========================")
	fmt.Println()

	r := bufio.NewReader(os.Stdin)

	hostname := prompt(r, "Hostname", "nostrix")

	fmt.Println()
	fmt.Println("SSH public key — paste your key, or press Enter to skip.")
	fmt.Println("(You can add keys later by editing /etc/nixos/flake.nix.)")
	fmt.Print("  > ")
	sshKey, _ := r.ReadString('\n')
	sshKey = strings.TrimSpace(sshKey)

	fmt.Println()
	fmt.Println("Hardware profile:")
	fmt.Println("  1  Raspberry Pi 3  (A+, B, B+)")
	fmt.Println("  2  Raspberry Pi 4  (B)")
	fmt.Println("  3  Raspberry Pi Zero 2W")
	fmt.Println("  4  Generic x86_64  (PC / VM / VPS)")
	fmt.Println("  5  None — I will add my own hardware module")
	hw := prompt(r, "Choice", "1")

	fmt.Println()
	fmt.Println("Addons:")
	nginxEnable := promptBool(r, "  nginx webserver (port 80)", true)

	fmt.Println()
	fmt.Println("WiFi — leave blank to skip and use ethernet only.")
	wifiSSID := prompt(r, "  SSID", "")
	var wifiPSK string
	if wifiSSID != "" {
		wifiPSK = prompt(r, "  Password", "")
	}

	s := state{
		Hostname:    hostname,
		SSHKey:      sshKey,
		Hardware:    hwChoice(hw),
		NginxEnable: nginxEnable,
		WifiSSID:    wifiSSID,
		WifiPSK:     wifiPSK,
	}

	fmt.Println()
	if promptBool(r, "Set up remote access via Cloudflare Tunnel?", false) {
		if err := promptCloudflare(r, &s); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	}

	flake, err := generate(s)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Println()
	fmt.Println(strings.Repeat("─", 60))
	fmt.Print(flake)
	fmt.Println(strings.Repeat("─", 60))
	fmt.Println()

	if *dryRun {
		fmt.Println("Dry run — nothing written.")
		return
	}

	if !promptBool(r, fmt.Sprintf("Write to %s and apply?", *output), true) {
		fmt.Println("Aborted.")
		return
	}

	if err := saveState(*stateFile, s); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not save state: %v\n", err)
	}
	if err := apply(*output, flake, false); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// promptCloudflare asks for the same inputs as the web UI's bootstrap form
// (cmd/nostrix-setup/templates/bootstrap.html) and provisions the device's
// Cloudflare Tunnel + Access app, filling in s's Cloudflare fields on
// success. Kept as a CLI equivalent of that form — both call the same
// provisionCloudflare — rather than a second implementation.
func promptCloudflare(r *bufio.Reader, s *state) error {
	s.OwnerEmail = prompt(r, "  Your email (for the Access allowlist)", "")

	apiToken := prompt(r, "  Cloudflare API token", "")
	client := newCloudflareClient(apiToken)

	cfg := cloudflareConfig{
		AccountID:  prompt(r, "  Cloudflare account ID", ""),
		ZoneID:     prompt(r, "  Cloudflare zone ID", ""),
		BaseDomain: prompt(r, "  Base domain (e.g. example.com)", ""),
		TeamDomain: prompt(r, "  Cloudflare team domain", ""),
		DeviceName: s.Hostname,
		OwnerEmail: s.OwnerEmail,
	}

	fmt.Println()
	fmt.Println("Provisioning Cloudflare Tunnel + Access app...")
	result, err := provisionCloudflare(client, cfg)
	if err != nil {
		return fmt.Errorf("Cloudflare setup failed: %w", err)
	}

	s.CloudflareTeamDomain = result.TeamDomain
	s.CloudflareAud = result.Aud
	s.CloudflareTunnelToken = result.TunnelToken
	fmt.Printf("Done — device will be reachable at https://%s\n", result.Hostname)
	return nil
}

// hwChoice maps wizard input (1–5) to bare hardware names stored in state.
func hwChoice(choice string) string {
	switch choice {
	case "1":
		return "raspberryPi3"
	case "2":
		return "raspberryPi4"
	case "3":
		return "raspberryPiZero2W"
	case "4":
		return "genericX86_64"
	default:
		return ""
	}
}
