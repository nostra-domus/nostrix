// Command nostrix-setup is the interactive setup wizard for Nostrix.
//
// Typical usage on a fresh NixOS installation:
//
//	nix run github:nostra-domus/nostrix
//
// Subcommands:
//
//	nostrix-setup                — interactive setup wizard
//	nostrix-setup add <git-url>  — register an app
//	nostrix-setup serve          — run the web configuration UI
package main

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

func main() {
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		switch os.Args[1] {
		case "add":
			runAdd(os.Args[2:])
			return
		case "serve":
			runServe(os.Args[2:])
			return
		case "help", "--help", "-h":
			printUsage()
			return
		}
	}
	runWizard()
}

func printUsage() {
	fmt.Println("Usage:")
	fmt.Println("  nostrix-setup              — interactive setup wizard")
	fmt.Println("  nostrix-setup add <url>    — register an app")
	fmt.Println("  nostrix-setup serve        — run the web configuration UI")
	fmt.Println()
	fmt.Println("Flags:")
	fmt.Println("  --output   path to flake.nix  (default: /etc/nixos/flake.nix)")
	fmt.Println("  --state    path to state file  (default: /etc/nostrix/state.json)")
	fmt.Println("  --dry-run  print without writing or applying (wizard, add)")
	fmt.Println("  --addr     address to listen on  (serve; default: 0.0.0.0:8080)")
	fmt.Println("  --cf-team-domain  Cloudflare Access team domain  (serve; required)")
	fmt.Println("  --cf-aud          Cloudflare Access application audience  (serve; required)")
}

// apply writes content to path and runs nixos-rebuild switch. With
// upgradeAll, it also passes --upgrade-all so the switch first re-resolves
// every flake input (nostrix, nixpkgs, ...) to its latest revision instead
// of reusing whatever's pinned in the local flake.lock.
func apply(path, content string, upgradeAll bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err
	}
	fmt.Printf("Written %s\n\n", path)

	flakeDir := filepath.Dir(path)
	args := []string{"switch", "--flake", flakeDir}
	if upgradeAll {
		args = append(args, "--upgrade-all")
	}
	fmt.Printf("Running: nixos-rebuild %s\n\n", strings.Join(args, " "))

	var cmd *exec.Cmd
	if os.Getenv("INVOCATION_ID") != "" {
		// Running as a systemd service (nostrix-web): this switch can
		// change nostrix-web's own unit (e.g. the bootstrap -> configured
		// transition in modules/web.nix), which makes systemd restart the
		// very unit this process runs under partway through. Run
		// nixos-rebuild in its own transient scope, outside nostrix-web's
		// cgroup, so that restart doesn't tear down the switch in progress.
		systemdRunArgs := append([]string{"--collect", "--wait", "--pipe", "nixos-rebuild"}, args...)
		cmd = exec.Command("systemd-run", systemdRunArgs...)
	} else {
		cmd = exec.Command("nixos-rebuild", args...)
	}
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

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
