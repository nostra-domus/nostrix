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
