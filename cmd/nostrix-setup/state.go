package main

import (
	"encoding/json"
	"os"
	"path/filepath"
)

type state struct {
	Hostname    string `json:"hostname"`
	SSHKey      string `json:"sshKey,omitempty"`
	Hardware    string `json:"hardware"`
	NginxEnable bool   `json:"nginxEnable"`
	Apps        []app  `json:"apps,omitempty"`

	// Catalog holds the curated apps the user switched on, by catalog id
	// (catalog.go).
	Catalog map[string]catalogApp `json:"catalog,omitempty"`

	// WiFi client credentials for networking.wireless. Ethernet is required
	// for the initial nostrix-setup run itself; these just let the device
	// join a WiFi network afterward. Plaintext in the generated flake and
	// state file — same trust model as SSHKey above.
	WifiSSID string `json:"wifiSSID,omitempty"`
	WifiPSK  string `json:"wifiPSK,omitempty"`

	// Cloudflare Tunnel + Access, set once the bootstrap web form or the
	// CLI wizard's equivalent prompts have run. CloudflareAPIToken is
	// deliberately not one of these fields — it's used transiently to call
	// the Cloudflare API (see cloudflare.go) and is never persisted here or
	// written into the generated flake, only its narrower-scoped result is.
	OwnerEmail string `json:"ownerEmail,omitempty"`
	// CloudflareBaseDomain is the domain the device (and every app it
	// publishes) lives under. Kept so exposing an app later needs only an API
	// token, not the setup answers again.
	CloudflareBaseDomain  string `json:"cloudflareBaseDomain,omitempty"`
	CloudflareTeamDomain  string `json:"cloudflareTeamDomain,omitempty"`
	CloudflareAud         string `json:"cloudflareAud,omitempty"`
	CloudflareTunnelToken string `json:"cloudflareTunnelToken,omitempty"`
}

type app struct {
	Name string `json:"name"`
	URL  string `json:"url"`

	// Module is which of the flake's nixosModules to import; empty means
	// the default, "nostrix" (see defaultAppModule). Set it to use an app
	// whose flake has no nostrix-adapted module: import its own module
	// (e.g. "default") and declare nostrix.apps.<name> in /etc/nixos/local.nix.
	Module string `json:"module,omitempty"`
}

// defaultAppModule is the nixosModules attribute apps are expected to export
// to run under Nostrix; it sets nostrix.apps.<name> (modules/apps.nix).
const defaultAppModule = "nostrix"

// moduleName is the nixosModules attribute to import for a.
func (a app) moduleName() string {
	if a.Module == "" {
		return defaultAppModule
	}
	return a.Module
}

func loadState(path string) (state, error) {
	var s state
	data, err := os.ReadFile(path)
	if err != nil {
		return s, err
	}
	return s, json.Unmarshal(data, &s)
}

func saveState(path string, s state) error {
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, append(data, '\n'), 0o644)
}
