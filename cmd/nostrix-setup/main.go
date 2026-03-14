// Command nostrix-setup is the interactive setup wizard for Nostradomus.
//
// It prompts for hostname, SSH key, hardware profile and addon choices,
// generates /etc/nixos/flake.nix, and applies the configuration via
// nixos-rebuild. Run with --dry-run to preview without writing anything.
//
// Typical usage on a fresh NixOS installation:
//
//	nix run github:nostra-domus/nostrix
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	output := flag.String("output", "/etc/nixos/flake.nix", "path to write the generated flake.nix")
	dryRun := flag.Bool("dry-run", false, "print the generated config without writing or applying")
	flag.Parse()

	fmt.Println("Nostradomus Setup Wizard")
	fmt.Println("========================")
	fmt.Println()

	r := bufio.NewReader(os.Stdin)

	hostname := prompt(r, "Hostname", "nostradomus")

	fmt.Println()
	fmt.Println("SSH public key — paste your key, or press Enter to skip.")
	fmt.Println("(You can add keys later by editing /etc/nixos/flake.nix.)")
	fmt.Print("  > ")
	sshKey, _ := r.ReadString('\n')
	sshKey = strings.TrimSpace(sshKey)

	fmt.Println()
	fmt.Println("Hardware profile:")
	fmt.Println("  1  Raspberry Pi Zero 2W")
	fmt.Println("  2  Generic x86_64  (PC / VM / VPS)")
	fmt.Println("  3  None — I will add my own hardware module")
	hw := prompt(r, "Choice", "1")

	fmt.Println()
	fmt.Println("Addons:")
	nginxEnable := promptBool(r, "  nginx webserver (static HTML page)", true)

	cfg := config{
		hostname:    hostname,
		sshKey:      sshKey,
		hardware:    hwToNix(hw),
		nginxEnable: nginxEnable,
	}
	flake := generate(cfg)

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

	if err := apply(*output, flake); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// ── config ────────────────────────────────────────────────────────────────────

type config struct {
	hostname    string
	sshKey      string
	hardware    string // Nix expression, e.g. "nostrix.hardware.raspberryPiZero2W"
	nginxEnable bool
}

func hwToNix(choice string) string {
	switch choice {
	case "1":
		return "nostrix.hardware.raspberryPiZero2W"
	case "2":
		return "nostrix.hardware.genericX86_64"
	default:
		return ""
	}
}

// ── generation ────────────────────────────────────────────────────────────────

func generate(cfg config) string {
	var b strings.Builder
	w := b.WriteString
	f := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	w("{\n")
	w("  inputs.nostrix.url = \"github:nostra-domus/nostrix\";\n")
	w("\n")
	w("  outputs = { nostrix, ... }: {\n")
	f("    nixosConfigurations.%s = nostrix.lib.mkSystem {\n", cfg.hostname)
	f("      hostname = \"%s\";\n", cfg.hostname)

	if cfg.sshKey != "" {
		f("      sshKeys  = [ \"%s\" ];\n", cfg.sshKey)
	}

	w("      modules  = [\n")

	if cfg.hardware != "" {
		f("        %s\n", cfg.hardware)
	}

	// Addon config block — only emitted when at least one addon is enabled.
	if cfg.nginxEnable {
		w("        {\n")
		w("          services.nostradomus.webserver.nginx.enable = true;\n")
		w("        }\n")
	}

	w("      ];\n")
	w("    };\n")
	w("  };\n")
	w("}\n")

	return b.String()
}

// ── apply ─────────────────────────────────────────────────────────────────────

func apply(path, content string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err
	}
	fmt.Printf("Written %s\n\n", path)

	flakeDir := filepath.Dir(path)
	fmt.Printf("Running: nixos-rebuild switch --flake %s\n\n", flakeDir)
	cmd := exec.Command("nixos-rebuild", "switch", "--flake", flakeDir)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ── prompts ───────────────────────────────────────────────────────────────────

func prompt(r *bufio.Reader, label, def string) string {
	fmt.Printf("%s [%s]: ", label, def)
	s, _ := r.ReadString('\n')
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	return s
}

func promptBool(r *bufio.Reader, label string, def bool) bool {
	hint := "Y/n"
	if !def {
		hint = "y/N"
	}
	fmt.Printf("%s [%s]: ", label, hint)
	s, _ := r.ReadString('\n')
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return def
	}
	return s == "y" || s == "yes"
}
