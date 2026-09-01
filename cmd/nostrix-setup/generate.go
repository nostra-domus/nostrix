package main

import (
	"fmt"
	"strings"
)

func generate(s state) string {
	var b strings.Builder
	w := b.WriteString
	f := func(format string, args ...any) { fmt.Fprintf(&b, format, args...) }

	w("{\n")
	w("  inputs.nostrix.url = \"github:nostra-domus/nostrix\";\n")
	for _, a := range s.Apps {
		f("  inputs.%s.url = \"%s\";\n", a.Name, a.URL)
	}
	w("\n")

	if len(s.Apps) > 0 {
		w("  outputs = { nostrix, ... }@inputs: {\n")
	} else {
		w("  outputs = { nostrix, ... }: {\n")
	}

	f("    nixosConfigurations.%s = nostrix.lib.mkSystem {\n", s.Hostname)
	f("      hostname = \"%s\";\n", s.Hostname)

	if s.SSHKey != "" {
		f("      sshKeys  = [ \"%s\" ];\n", s.SSHKey)
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

	w("      ];\n")
	w("    };\n")
	w("  };\n")
	w("}\n")

	return b.String()
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
