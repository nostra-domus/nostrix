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
	OwnerEmail            string `json:"ownerEmail,omitempty"`
	CloudflareTeamDomain  string `json:"cloudflareTeamDomain,omitempty"`
	CloudflareAud         string `json:"cloudflareAud,omitempty"`
	CloudflareTunnelToken string `json:"cloudflareTunnelToken,omitempty"`
}

type app struct {
	Name string `json:"name"`
	URL  string `json:"url"`
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
