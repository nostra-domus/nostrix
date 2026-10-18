package main

import (
	"strings"
	"testing"
)

func TestGenerateOmitsCloudflareWhenUnset(t *testing.T) {
	out, err := generate(state{Hostname: "pi-test", Hardware: "raspberryPiZero2W"})
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if strings.Contains(out, "cloudflared") {
		t.Errorf("expected no cloudflared reference when CloudflareTunnelToken is unset, got:\n%s", out)
	}
}

func TestGenerateIncludesWebWhenCloudflareUnset(t *testing.T) {
	// The web UI must stay present even without Cloudflare configured —
	// otherwise a device set up with just hostname/SSH key/WiFi (e.g. via
	// the AP-portal bootstrap flow) has no way to be reached again to add
	// Cloudflare, nginx, or apps later, and if no SSH key was given either,
	// no way to be reached at all.
	out, err := generate(state{Hostname: "pi-test", Hardware: "raspberryPiZero2W"})
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if !strings.Contains(out, "nostrix.nixosModules.web") {
		t.Errorf("expected nostrix.nixosModules.web even without Cloudflare configured, got:\n%s", out)
	}
	if strings.Contains(out, "cloudflareTeamDomain") {
		t.Errorf("expected no cloudflareTeamDomain override when Cloudflare is unset, got:\n%s", out)
	}
}

func TestGenerateIncludesCloudflareWhenSet(t *testing.T) {
	s := state{
		Hostname:              "pi-test",
		Hardware:              "raspberryPiZero2W",
		CloudflareTeamDomain:  "myteam",
		CloudflareAud:         "aud-xyz",
		CloudflareTunnelToken: "tunnel-tok",
	}
	out, err := generate(s)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}

	for _, want := range []string{
		"nostrix.nixosModules.cloudflared",
		`services.nostrix-cloudflared.tunnelToken = "tunnel-tok";`,
		"nostrix.nixosModules.web",
		`services.nostrix-web.cloudflareTeamDomain = "myteam";`,
		`services.nostrix-web.cloudflareAud = "aud-xyz";`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("generated flake missing %q, got:\n%s", want, out)
		}
	}
}

func TestGenerateUsesLocalNostrixInputWhenHardwareSet(t *testing.T) {
	// Hardware is only ever set by our own SD images (see
	// services.nostrix-web.hardware), which bake a real, updatable git
	// clone of nostrix onto the device (modules/offline-src.nix) — the
	// generated flake must reference that instead of github:, so the very
	// first nixos-rebuild switch can succeed with zero network.
	out, err := generate(state{Hostname: "pi-test", Hardware: "raspberryPiZero2W"})
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if !strings.Contains(out, `inputs.nostrix.url = "git+file:///var/lib/nostrix/nostrix-src";`) {
		t.Errorf("expected local git+file nostrix input when Hardware is set, got:\n%s", out)
	}
	if strings.Contains(out, "github:nostra-domus/nostrix") {
		t.Errorf("expected no github: nostrix input when Hardware is set, got:\n%s", out)
	}
}

func TestGenerateUsesGithubNostrixInputWhenHardwareUnset(t *testing.T) {
	// A plain `nix run github:nostra-domus/nostrix` on an existing system
	// has no baked-in local clone (Hardware is empty) and must keep using
	// the real github: input.
	out, err := generate(state{Hostname: "pi-test"})
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if !strings.Contains(out, `inputs.nostrix.url = "github:nostra-domus/nostrix";`) {
		t.Errorf("expected github: nostrix input when Hardware is unset, got:\n%s", out)
	}
	if strings.Contains(out, "git+file://") {
		t.Errorf("expected no git+file nostrix input when Hardware is unset, got:\n%s", out)
	}
}

func TestGenerateOmitsWifiWhenUnset(t *testing.T) {
	out, err := generate(state{Hostname: "pi-test", Hardware: "raspberryPiZero2W"})
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if strings.Contains(out, "networking.wireless") {
		t.Errorf("expected no networking.wireless block when WifiSSID is unset, got:\n%s", out)
	}
}

func TestGenerateIncludesWifiWhenSet(t *testing.T) {
	s := state{
		Hostname: "pi-test",
		Hardware: "raspberryPiZero2W",
		WifiSSID: "myssid",
		WifiPSK:  "mypassword",
	}
	out, err := generate(s)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	for _, want := range []string{
		"networking.wireless.enable = true;",
		`networking.wireless.networks."myssid" = { psk = "mypassword"; };`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("generated flake missing %q, got:\n%s", want, out)
		}
	}
}

func TestGenerateEscapesWifiValues(t *testing.T) {
	s := state{
		Hostname: "pi-test",
		Hardware: "raspberryPiZero2W",
		WifiSSID: `ssid"; malicious = true; x = "`,
		WifiPSK:  "pass",
	}
	out, err := generate(s)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if strings.Contains(out, `ssid"; malicious = true`) {
		t.Errorf("WiFi SSID was not escaped, got:\n%s", out)
	}
}

func TestGenerateEscapesCloudflareValues(t *testing.T) {
	s := state{
		Hostname:              "pi-test",
		CloudflareTeamDomain:  `team"; malicious = true; x = "`,
		CloudflareAud:         "aud",
		CloudflareTunnelToken: "tok",
	}
	out, err := generate(s)
	if err != nil {
		t.Fatalf("generate failed: %v", err)
	}
	if strings.Contains(out, `team"; malicious = true`) {
		t.Errorf("Cloudflare team domain was not escaped, got:\n%s", out)
	}
}

// Regression: a first-time rename (running hostname "nostrix", generated
// config "nostrix-vm") must still name the config explicitly, or
// nixos-rebuild looks up nixosConfigurations.nostrix and fails.
func TestConfigNameMatchesGeneratedHostname(t *testing.T) {
	flake, err := generate(state{Hostname: "nostrix-vm", Hardware: "genericX86_64"})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if got := configName(flake); got != "nostrix-vm" {
		t.Errorf("configName = %q, want nostrix-vm", got)
	}
	if got := configName("{ }"); got != "" {
		t.Errorf("configName of a flake without one = %q, want empty", got)
	}
}

func TestRebuildArgsNamesTheConfigForARenamedDevice(t *testing.T) {
	flake, err := generate(state{Hostname: "nostrix-vm", Hardware: "genericX86_64"})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	got := strings.Join(rebuildArgs("/etc/nixos/flake.nix", configName(flake), "switch", false), " ")
	if want := "switch --flake /etc/nixos#nostrix-vm"; got != want {
		t.Errorf("args = %q, want %q", got, want)
	}
	got = strings.Join(rebuildArgs("/etc/nixos/flake.nix", "", "boot", true), " ")
	if want := "boot --flake /etc/nixos --upgrade-all"; got != want {
		t.Errorf("args without a name = %q, want %q", got, want)
	}
}

func TestGenerateUsesQemuVMHardware(t *testing.T) {
	flake, err := generate(state{Hostname: "nostrix-vm", Hardware: "qemuVM"})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.Contains(flake, "nostrix.hardware.qemuVM") {
		t.Errorf("generated flake does not use the qemuVM profile:\n%s", flake)
	}
}

func TestGenerateImportsTheNostrixModuleByDefault(t *testing.T) {
	flake, err := generate(state{Hostname: "h", Apps: []app{{Name: "jelly", URL: "github:org/jelly"}}})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.Contains(flake, "inputs.jelly.nixosModules.nostrix") || strings.Contains(flake, "nixosModules.default") {
		t.Errorf("an app should be imported through nixosModules.nostrix:\n%s", flake)
	}
}

func TestGenerateImportsAnExplicitModule(t *testing.T) {
	flake, err := generate(state{Hostname: "h", Apps: []app{{Name: "jelly", URL: "github:org/jelly", Module: "default"}}})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.Contains(flake, "inputs.jelly.nixosModules.default") {
		t.Errorf("--module default should be honoured:\n%s", flake)
	}
}

func TestGenerateRejectsAnUnsafeModuleName(t *testing.T) {
	_, err := generate(state{Hostname: "h", Apps: []app{{Name: "x", URL: "github:o/x", Module: "a b"}}})
	if err == nil {
		t.Error("expected generate to refuse a module name that can't be spliced safely")
	}
}

// local.nix is the user's hand-written module; the generated flake must pick
// it up when present and never require it.
func TestGenerateIncludesLocalNixWhenPresent(t *testing.T) {
	flake, err := generate(state{Hostname: "h"})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if !strings.Contains(flake, "builtins.pathExists ./local.nix") {
		t.Errorf("generated flake does not pick up local.nix:\n%s", flake)
	}
}
