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
