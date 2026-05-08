package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strings"
)

func runAdd(args []string) {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	output := fs.String("output", "/etc/nixos/flake.nix", "path to the flake.nix")
	stateFile := fs.String("state", "/etc/nostrix/state.json", "path to the state file")
	dryRun := fs.Bool("dry-run", false, "print changes without applying")
	fs.Parse(args) //nolint:errcheck

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: nostrix-setup add <git-url>")
		os.Exit(1)
	}
	rawURL := fs.Arg(0)

	name, err := appNameFromURL(rawURL)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: invalid URL %q: %v\n", rawURL, err)
		os.Exit(1)
	}

	s, err := loadState(*stateFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: could not load state from %s: %v\n", *stateFile, err)
		fmt.Fprintln(os.Stderr, "Run nostrix-setup first to initialise the configuration.")
		os.Exit(1)
	}

	for _, a := range s.Apps {
		if a.Name == name {
			fmt.Fprintf(os.Stderr, "error: app %q is already registered\n", name)
			os.Exit(1)
		}
	}

	s.Apps = append(s.Apps, app{Name: name, URL: rawURL})
	flake := generate(s)

	fmt.Printf("Adding app %q from %s\n\n", name, rawURL)
	fmt.Println(strings.Repeat("─", 60))
	fmt.Print(flake)
	fmt.Println(strings.Repeat("─", 60))
	fmt.Println()

	if *dryRun {
		fmt.Println("Dry run — nothing written.")
		return
	}

	configDir := filepath.Join("/etc/nostrix", name)
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "warning: could not create %s: %v\n", configDir, err)
	} else {
		fmt.Printf("Created %s\n", configDir)
		fmt.Printf("Place your config.yaml there before the service will start.\n\n")
	}

	if err := saveState(*stateFile, s); err != nil {
		fmt.Fprintf(os.Stderr, "error: could not save state: %v\n", err)
		os.Exit(1)
	}
	if err := apply(*output, flake); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// appNameFromURL derives an app name from a flake URL.
// Supports "git+ssh://host/org/repo", "git+https://...", and "github:org/repo".
func appNameFromURL(raw string) (string, error) {
	// Normalise to a URL we can parse: strip flake protocol prefixes.
	normalised := raw
	for _, prefix := range []string{"git+ssh://", "git+https://", "git+http://"} {
		if strings.HasPrefix(normalised, prefix) {
			normalised = "https://" + strings.TrimPrefix(normalised, prefix)
			break
		}
	}
	// Handle "github:org/repo" shorthand → treat as path.
	if !strings.Contains(normalised, "://") {
		if i := strings.Index(normalised, ":"); i >= 0 {
			normalised = "https://dummy/" + normalised[i+1:]
		}
	}

	u, err := url.Parse(normalised)
	if err != nil || u.Path == "" {
		return "", fmt.Errorf("cannot parse URL")
	}

	seg := u.Path
	if i := strings.LastIndex(seg, "/"); i >= 0 {
		seg = seg[i+1:]
	}
	seg = strings.TrimSuffix(seg, ".git")

	// Strip any query/fragment that might have been attached.
	if i := strings.IndexAny(seg, "?#"); i >= 0 {
		seg = seg[:i]
	}

	if seg == "" || seg == "." {
		return "", fmt.Errorf("cannot derive app name from %q", raw)
	}
	return seg, nil
}
