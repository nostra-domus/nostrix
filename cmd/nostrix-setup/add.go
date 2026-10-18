package main

import (
	"flag"
	"fmt"
	"net/url"
	"os"
	"strings"
)

func runAdd(args []string) {
	fs := flag.NewFlagSet("add", flag.ExitOnError)
	output := fs.String("output", "/etc/nixos/flake.nix", "path to the flake.nix")
	stateFile := fs.String("state", "/etc/nostrix/state.json", "path to the state file")
	dryRun := fs.Bool("dry-run", false, "print changes without applying")
	module := fs.String("module", "", "nixosModules attribute to import (default: "+defaultAppModule+")")
	noCheck := fs.Bool("no-check", false, "skip checking that the flake exports the module (e.g. offline)")
	fs.Parse(args) //nolint:errcheck

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: nostrix-setup add [--module <name>] <flake-url>")
		os.Exit(1)
	}
	rawURL := fs.Arg(0)

	s, err := loadState(*stateFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: could not load state from %s: %v\n", *stateFile, err)
		fmt.Fprintln(os.Stderr, "Run nostrix-setup first to initialise the configuration.")
		os.Exit(1)
	}

	name, err := addApp(&s, rawURL, *module)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
	if !*noCheck {
		fmt.Printf("Checking that %s exports nixosModules.%s ...\n", rawURL, s.Apps[len(s.Apps)-1].moduleName())
		if err := checkAppFlake(realNixRunner, rawURL, *module); err != nil {
			fmt.Fprintf(os.Stderr, "error: %v\n", err)
			os.Exit(1)
		}
	}

	flake, err := generate(s)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Adding app %q from %s\n\n", name, rawURL)
	fmt.Println(strings.Repeat("─", 60))
	fmt.Print(flake)
	fmt.Println(strings.Repeat("─", 60))
	fmt.Println()

	if *dryRun {
		fmt.Println("Dry run — nothing written.")
		return
	}

	fmt.Printf("Configure %s with ordinary NixOS options in /etc/nixos/local.nix\n", name)
	fmt.Printf("(e.g. nostrix.apps.%s = { port = ...; expose = \"lan\"; };).\n\n", name)

	if err := saveState(*stateFile, s); err != nil {
		fmt.Fprintf(os.Stderr, "error: could not save state: %v\n", err)
		os.Exit(1)
	}
	if err := apply(*output, flake, false); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// runList prints the registered apps.
func runList(args []string) {
	fs := flag.NewFlagSet("list", flag.ExitOnError)
	stateFile := fs.String("state", "/etc/nostrix/state.json", "path to the state file")
	fs.Parse(args) //nolint:errcheck

	s, err := loadState(*stateFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: could not load state from %s: %v\n", *stateFile, err)
		os.Exit(1)
	}
	if len(s.Apps) == 0 {
		fmt.Println("No apps registered.")
		return
	}
	for _, a := range s.Apps {
		fmt.Printf("%-20s %s  (nixosModules.%s)\n", a.Name, a.URL, a.moduleName())
	}
}

// runRemove unregisters an app and applies the result.
func runRemove(args []string) {
	fs := flag.NewFlagSet("remove", flag.ExitOnError)
	output := fs.String("output", "/etc/nixos/flake.nix", "path to the flake.nix")
	stateFile := fs.String("state", "/etc/nostrix/state.json", "path to the state file")
	dryRun := fs.Bool("dry-run", false, "print changes without applying")
	fs.Parse(args) //nolint:errcheck

	if fs.NArg() < 1 {
		fmt.Fprintln(os.Stderr, "usage: nostrix-setup remove <name>")
		os.Exit(1)
	}
	name := fs.Arg(0)

	s, err := loadState(*stateFile)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: could not load state from %s: %v\n", *stateFile, err)
		os.Exit(1)
	}
	if !removeApp(&s, name) {
		fmt.Fprintf(os.Stderr, "error: no app named %q (see: nostrix-setup list)\n", name)
		os.Exit(1)
	}
	flake, err := generate(s)
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}

	fmt.Printf("Removing app %q\n\n", name)
	fmt.Println(strings.Repeat("─", 60))
	fmt.Print(flake)
	fmt.Println(strings.Repeat("─", 60))
	fmt.Println()

	if *dryRun {
		fmt.Println("Dry run — nothing written.")
		return
	}
	if err := saveState(*stateFile, s); err != nil {
		fmt.Fprintf(os.Stderr, "error: could not save state: %v\n", err)
		os.Exit(1)
	}
	if err := apply(*output, flake, false); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

// addApp registers a new app in s, deriving its name from rawURL. module is
// which nixosModules attribute to import ("" for the default, "nostrix").
// Fails if the URL can't be parsed or the derived name is already
// registered. Returns the derived name on success.
func addApp(s *state, rawURL, module string) (string, error) {
	name, err := appNameFromURL(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid URL %q: %w", rawURL, err)
	}
	if !validIdentifier(name) {
		return "", fmt.Errorf("app name %q derived from %q is not safe to use: must start with a letter and contain only letters, digits, hyphens, and underscores", name, rawURL)
	}
	if module != "" && !moduleNameRe.MatchString(module) {
		return "", fmt.Errorf("invalid module name %q: must start with a letter or underscore and contain only letters, digits, hyphens, and underscores", module)
	}
	for _, a := range s.Apps {
		if a.Name == name {
			return "", fmt.Errorf("app %q is already registered", name)
		}
	}
	s.Apps = append(s.Apps, app{Name: name, URL: rawURL, Module: module})
	return name, nil
}

// removeApp removes a registered app by name. Reports whether it existed.
func removeApp(s *state, name string) bool {
	for i, a := range s.Apps {
		if a.Name == name {
			s.Apps = append(s.Apps[:i], s.Apps[i+1:]...)
			return true
		}
	}
	return false
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
