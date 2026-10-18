package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

// nixRunner runs `nix <args>` and returns its stdout. Stderr is folded into
// the returned error. Injectable so tests never run nix or touch the network.
type nixRunner func(args ...string) ([]byte, error)

func realNixRunner(args ...string) ([]byte, error) {
	// Same reasoning as runRebuild: nostrix-web's systemd PATH doesn't
	// include /run/current-system/sw/bin, so look there first.
	nix := "/run/current-system/sw/bin/nix"
	if _, err := os.Stat(nix); err != nil {
		p, lookErr := exec.LookPath("nix")
		if lookErr != nil {
			return nil, fmt.Errorf("nix not found")
		}
		nix = p
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	cmd := exec.CommandContext(ctx, nix, append([]string{"--extra-experimental-features", "nix-command flakes"}, args...)...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return out, nil
}

// moduleNameRe is what a nixosModules attribute may be called for us to
// splice it into the generated flake unquoted.
var moduleNameRe = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// checkAppFlake verifies the flake at rawURL exports nixosModules.<module>
// ("" meaning the default, "nostrix"), so a bad URL or a flake that isn't
// adapted for Nostrix is reported when the app is added, in plain words,
// and not as an attribute error from nixos-rebuild minutes later.
func checkAppFlake(run nixRunner, rawURL, module string) error {
	if module == "" {
		module = defaultAppModule
	}
	if !moduleNameRe.MatchString(module) {
		return fmt.Errorf("invalid module name %q", module)
	}
	out, err := run("eval", "--json", rawURL+"#nixosModules", "--apply", "builtins.attrNames")
	if err != nil {
		if strings.Contains(err.Error(), "does not provide attribute") {
			return fmt.Errorf("%s exports no nixosModules, so there is nothing to import", rawURL)
		}
		return fmt.Errorf("could not read the flake at %s: %w", rawURL, err)
	}
	var names []string
	if err := json.Unmarshal(out, &names); err != nil {
		return fmt.Errorf("unexpected output from nix for %s: %w", rawURL, err)
	}
	for _, n := range names {
		if n == module {
			return nil
		}
	}
	if module != defaultAppModule {
		return fmt.Errorf("%s has no nixosModules.%s (it exports: %s)", rawURL, module, strings.Join(names, ", "))
	}
	return fmt.Errorf("%s has no nixosModules.nostrix (it exports: %s). "+
		"An app runs under Nostrix through a nixosModules.nostrix that sets nostrix.apps.<name>. "+
		"If its own module is fine as it is, add it with --module <name> and declare nostrix.apps.<name> in /etc/nixos/local.nix",
		rawURL, strings.Join(names, ", "))
}
