package main

import (
	"fmt"
	"regexp"
	"strings"
)

// identifierRe matches strings safe to splice into the flake as a bare Nix
// attribute name (nixosConfigurations.<name>, inputs.<name>). Nix bare
// identifiers must start with a letter and contain only letters, digits,
// hyphens, and underscores — anything else at that position would inject
// arbitrary Nix syntax rather than just a name.
var identifierRe = regexp.MustCompile(`^[a-zA-Z][a-zA-Z0-9_-]{0,62}$`)

func validIdentifier(s string) bool {
	return identifierRe.MatchString(s)
}

// escapeNixString escapes s for safe embedding inside a double-quoted Nix
// string literal: backslash and quote to prevent quote-breakout, and $ to
// prevent ${...} interpolation.
func escapeNixString(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, `$`, `\$`).Replace(s)
}

func generate(s state) (string, error) {
	if !validIdentifier(s.Hostname) {
		return "", fmt.Errorf("invalid hostname %q: must start with a letter and contain only letters, digits, hyphens, and underscores", s.Hostname)
	}
	for _, a := range s.Apps {
		if !validIdentifier(a.Name) {
			return "", fmt.Errorf("invalid app name %q: must start with a letter and contain only letters, digits, hyphens, and underscores", a.Name)
		}
	}

	var b strings.Builder
	w := b.WriteString
	f := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	w("{\n")
	w("  inputs.nostrix.url = \"github:nostra-domus/nostrix\";\n")
	for _, a := range s.Apps {
		f("  inputs.%s.url = \"%s\";\n", a.Name, escapeNixString(a.URL))
	}
	w("\n")

	if len(s.Apps) > 0 {
		w("  outputs = { nostrix, ... }@inputs: {\n")
	} else {
		w("  outputs = { nostrix, ... }: {\n")
	}

	f("    nixosConfigurations.%s = nostrix.lib.mkSystem {\n", s.Hostname)
	f("      hostname = \"%s\";\n", escapeNixString(s.Hostname))

	if s.SSHKey != "" {
		f("      sshKeys  = [ \"%s\" ];\n", escapeNixString(s.SSHKey))
	}

	w("      modules  = [\n")

	if hw := hwToNix(s.Hardware); hw != "" {
		f("        %s\n", hw)
	}

	// Pinned once at generation time and never touched again — stateVersion
	// must not silently track nixpkgs as it advances via auto-upgrade.
	w("        { system.stateVersion = \"25.11\"; }\n")

	for _, a := range s.Apps {
		f("        inputs.%s.nixosModules.default\n", a.Name)
	}

	if s.NginxEnable {
		w("        {\n")
		w("          services.nginx.enable = true;\n")
		w("          networking.firewall.allowedTCPPorts = [ 80 ];\n")
		w("        }\n")
	}

	if s.WifiSSID != "" {
		w("        {\n")
		w("          networking.wireless.enable = true;\n")
		f("          networking.wireless.networks.\"%s\" = { psk = \"%s\"; };\n",
			escapeNixString(s.WifiSSID), escapeNixString(s.WifiPSK))
		w("        }\n")
	}

	if s.CloudflareTunnelToken != "" {
		w("        nostrix.nixosModules.cloudflared\n")
		f("        { services.nostrix-cloudflared.tunnelToken = \"%s\"; }\n", escapeNixString(s.CloudflareTunnelToken))
		w("        nostrix.nixosModules.web\n")
		f("        { services.nostrix-web.cloudflareTeamDomain = \"%s\"; services.nostrix-web.cloudflareAud = \"%s\"; }\n",
			escapeNixString(s.CloudflareTeamDomain), escapeNixString(s.CloudflareAud))
	}

	w("      ];\n")
	w("    };\n")
	w("  };\n")
	w("}\n")

	return b.String(), nil
}

// hwToNix maps a bare hardware name to its Nix expression.
func hwToNix(name string) string {
	switch name {
	case "raspberryPi3":
		return "nostrix.hardware.raspberryPi3"
	case "raspberryPi4":
		return "nostrix.hardware.raspberryPi4"
	case "raspberryPiZero2W":
		return "nostrix.hardware.raspberryPiZero2W"
	case "genericX86_64":
		return "nostrix.hardware.genericX86_64"
	default:
		return ""
	}
}
