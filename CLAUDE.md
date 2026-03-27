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
| `nixosConfigurations.example` | Smoke-test configuration (x86_64, nginx enabled) |
| `checks.x86_64-linux.integration` | NixOS VM integration test |
| `packages.default` / `apps.default` | The `nostrix-setup` Go binary |

### Module composition

`modules/default.nix` is the single entry point for NixOS. It composes:
1. `modules/base.nix` — SSH (key-only), firewall (port 22 only by default), weekly auto-upgrades from `/etc/nixos`, Nix GC (30d retention)
2. `modules/mdns.nix` — Avahi, broadcasts `hostname.local` over UDP 5353

Application modules are not part of `nixosModules.default`. Callers pass them in the `modules` list to `lib.mkSystem`. Hardware profiles also come from the caller.

### Setup wizard flow

`cmd/nostrix-setup/main.go` prompts for hostname, SSH key, hardware profile, and addon choices (currently: nginx on/off), then generates a `flake.nix` calling `nostrix.lib.mkSystem`, writes it to `--output` (default `/etc/nixos/flake.nix`), and runs `nixos-rebuild switch --flake /etc/nixos`. Use `--dry-run` to preview without writing.

### Integration test

Defined inline in `flake.nix`. Boots a single NixOS VM with `base.nix` + `mdns.nix` + plain nixpkgs nginx, verifies:
- nginx serves the configured content on port 8080
- SSH password authentication is disabled
- hostname is correct
