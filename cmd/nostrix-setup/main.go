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
}

// apply writes content to path and runs nixos-rebuild switch.
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
