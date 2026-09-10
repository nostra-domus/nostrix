# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## What this is

Nostrix is an opinionated NixOS base for self-hosted servers. It provides:
- NixOS modules and hardware profiles
- `lib.mkSystem` / `lib.mkImage` helpers (the primary user-facing API)
- `nostrix-setup`: an interactive Go setup wizard that generates `/etc/nixos/flake.nix` and applies it

Nostrix is application-agnostic. Users bring their own application modules and pass them via `mkSystem`'s `modules` argument. `demo/flake.nix` shows a concrete example using plain nixpkgs nginx.

## Commands

### Nix

```bash
# Build the setup wizard binary
nix build                                          # → ./result/bin/nostrix-setup

# Run the setup wizard directly from GitHub
nix run github:nostra-domus/nostrix

# Build the Raspberry Pi Zero 2W SD card image
nix build .#images.raspberryPiZero2W

# Run the integration test (boots a VM, checks nginx + SSH + hostname)
nix build .#checks.x86_64-linux.integration

# Run the AP-portal integration test (simulated WiFi via mac80211_hwsim)
nix build .#checks.x86_64-linux.apPortal

# Evaluate the example NixOS configuration (smoke test)
nix eval .#nixosConfigurations.example.config.networking.hostName
```

### Go

The Go module (`cmd/nostrix-setup`) uses stdlib only — no external dependencies.

```bash
go build ./cmd/nostrix-setup
go test ./...
go vet ./...
```

## Architecture

### Flake outputs

| Output | Purpose |
|---|---|
| `nixosModules.default` | Top-level NixOS module — imports base + mDNS |
| `hardware.raspberryPiZero2W` | aarch64 hardware profile (BCM2837B0, extlinux, zram) |
| `hardware.genericX86_64` | x86_64 hardware profile (systemd-boot, EFI) |
| `lib.mkSystem` | Build a NixOS config with the Nostrix base stack |
| `lib.mkImage` | Build a compressed SD card image (calls mkSystem + sd-image-aarch64 module) |
| `images.raspberryPiZero2W` | Pre-built SD card image with temp credentials for first boot |
| `nixosModules.apPortal` | Opt-in add-on: first-boot setup access point (hostapd + dnsmasq) |
| `nixosConfigurations.example` | Smoke-test configuration (x86_64, nginx enabled) |
| `checks.x86_64-linux.integration` | NixOS VM integration test |
| `checks.x86_64-linux.apPortal` | AP-portal VM integration test (simulated WiFi) |
| `packages.default` / `apps.default` | The `nostrix-setup` Go binary |

### Module composition

`modules/default.nix` is the single entry point for NixOS. It composes:
1. `modules/base.nix` — SSH (key-only), firewall (port 22 only by default), weekly auto-upgrades from `/etc/nixos`, Nix GC (30d retention)
2. `modules/mdns.nix` — Avahi, broadcasts `hostname.local` over UDP 5353

Application modules are not part of `nixosModules.default`. Callers pass them in the `modules` list to `lib.mkSystem`. Hardware profiles also come from the caller.

`modules/ap-portal.nix` (opt-in, `nixosModules.apPortal`) is a setup-only add-on wired into `images.raspberryPi3`/`images.raspberryPiZero2W` alongside `nixosModules.web`, not part of `default`. It runs hostapd + dnsmasq on `wlan0` (fixed SSID `nostrix-setup-<hostname>`/passphrase, gateway `10.42.0.1`), started by `nostrix-ap-mode.service` only when `eth0` has no carrier at boot — a plugged-in cable keeps the existing LAN bootstrap mode. It's never emitted by `nostrix-setup`'s `generate()`, so no teardown logic is needed: once the real generated flake (which doesn't import it) is switched to, NixOS's normal activation stops hostapd/dnsmasq on its own, the same way the web.nix bootstrap → configured transition works. `cmd/nostrix-setup/serve.go`'s bootstrap-mode branch additionally listens on `:80` and answers OS captive-portal probe paths (`cmd/nostrix-setup/captive.go`) with a redirect to `/`, so connecting to the AP pops the phone's captive-portal sign-in prompt.

### Setup wizard flow

`cmd/nostrix-setup/main.go` prompts for hostname, SSH key, hardware profile, addon choices (currently: nginx on/off), and optional WiFi SSID/password (`networking.wireless`, ethernet still required for the initial run itself), then generates a `flake.nix` calling `nostrix.lib.mkSystem`, writes it to `--output` (default `/etc/nixos/flake.nix`), and runs `nixos-rebuild switch --flake /etc/nixos` (`apply()` in `main.go`). If `switch` is blocked by a pre-switch check — a critical-component change (e.g. the D-Bus implementation) NixOS refuses to hot-swap live — `apply()` falls back to `nixos-rebuild boot` plus a scheduled reboot, mirroring how `system.autoUpgrade`'s `allowReboot` already handles this case. Use `--dry-run` to preview without writing.

### Integration test

Defined inline in `flake.nix`. Boots a single NixOS VM with `base.nix` + `mdns.nix` + plain nixpkgs nginx, verifies:
- nginx serves the configured content on port 8080
- SSH password authentication is disabled
- hostname is correct

### AP-portal integration test

Also defined inline in `flake.nix` (`checks.x86_64-linux.apPortal`). Boots a single NixOS VM with `mac80211_hwsim` simulating a `wlan0`/`wlan1` radio pair (the same pattern nixpkgs' own `nixos/tests/wpa_supplicant.nix` uses) and no ethernet NIC at all, so `ap-portal.nix`'s carrier check naturally sees "no ethernet". Verifies:
- `hostapd`/`dnsmasq` come up and `wlan0` gets the AP address
- a WiFi client (`wlan1`) can associate and get a DHCP lease
- the bootstrap web UI is reachable over the AP, including a captive-portal probe path redirect

This can't verify that a real phone OS actually pops its captive-portal prompt — that still needs real-hardware verification.
