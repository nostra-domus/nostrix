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
| `hardware.qemuVM` | x86_64 test-VM profile: imports nixpkgs' `qemu-vm.nix`, bootloader off. Used by `nixosConfigurations.vm` (`just vm`) so a *generated* flake can be applied inside the VM — `genericX86_64` there fails at bootloader install, and dropping `qemu-vm.nix` drops the VM's filesystems |
| `lib.mkSystem` | Build a NixOS config with the Nostrix base stack |
| `lib.mkImage` | Build a compressed SD card image (calls mkSystem + sd-image-aarch64 module) |
| `images.raspberryPiZero2W` | Pre-built SD card image with temp credentials for first boot |
| `nixosModules.apPortal` | Opt-in add-on: first-boot setup access point (hostapd + dnsmasq) |
| `nixosModules.offlineSrc` | Opt-in add-on: bakes a local, updatable git clone of nostrix (+ a nixpkgs override) onto the image, for a zero-network first `nixos-rebuild switch` |
| `nixosConfigurations.example` | Smoke-test configuration (x86_64, nginx enabled) |
| `checks.x86_64-linux.integration` | NixOS VM integration test |
| `checks.x86_64-linux.apPortal` | AP-portal VM integration test (simulated WiFi) |
| `checks.x86_64-linux.appsContract` | Pure-eval test of the `nostrix.apps` contract (`modules/apps.nix`): only `expose = "lan"` opens a firewall port, `apps.json` content, rejects exposing without a port |
| `packages.default` / `apps.default` | The `nostrix-setup` Go binary |

### Module composition

`modules/default.nix` is the single entry point for NixOS. It composes:
1. `modules/base.nix` — SSH (key-only), firewall (port 22 only by default), weekly auto-upgrades from `/etc/nixos`, Nix GC (30d retention)
2. `modules/mdns.nix` — Avahi, broadcasts `hostname.local` over UDP 5353
3. `modules/apps.nix` — the app contract (below)
4. `modules/web.nix` — the `nostrix-web` service, and puts `nostrix-setup` on every system's PATH

Application modules are not part of `nixosModules.default`. Callers pass them in the `modules` list to `lib.mkSystem`. Hardware profiles also come from the caller.

### Apps: the `nostrix.apps` contract

`modules/apps.nix` declares `nostrix.apps.<name> = { port; expose = "none" | "lan" | "cloudflare"; }` (default `none`). An app never touches the firewall or Cloudflare itself — it declares, Nostrix adapts: `lan` opens the port; `cloudflare` opens nothing (the origin is `127.0.0.1:<port>`, reached only through the tunnel). The module also writes the declarations to `/etc/nostrix/apps.json` so `nostrix-setup` can read them from the *running* system (no Nix evaluation on the device). Anyone can set the options: the app's own `nixosModules.nostrix`, or the user's `/etc/nixos/local.nix`.

`nostrix-setup add <flake-url> [--module m]` registers an app: `generate()` adds `inputs.<name>.url` and imports `inputs.<name>.nixosModules.<module>` (default **`nostrix`**, not `default` — `default` is whatever the author means by it and isn't adapted to Nostrix). `checkAppFlake` (`appcheck.go`) verifies the flake exports it first via `nix eval <url>#nixosModules --apply builtins.attrNames`. `list`/`remove` mirror the web UI. There is deliberately no per-app config file convention: configuration is ordinary NixOS options, and hand-written config goes in `/etc/nixos/local.nix`, which the generated flake imports when it exists (`generate()` rewrites `flake.nix` on every change, so it must never be edited by hand).

**Cloudflare exposure** (`expose.go`, `expose_cmd.go`, web `/apps/expose`): `reconcileExposure` makes Cloudflare match `apps.json` — per `cloudflare` app a single-label hostname `<device>-<app>.<base>` (Cloudflare's free certificate covers one subdomain level), an owner-only Access app + policy, a DNS CNAME to the tunnel, and a tunnel route to `127.0.0.1:<port>`. Order is a safety property: **Access app and policy first, DNS and route last** (on removal the reverse), so an app is never routable unprotected. Cleanup only considers hostnames the device's own tunnel used to route, and deletes a DNS record/Access app only if it still looks like ours. The API token is requested per run (form field / `$CLOUDFLARE_API_TOKEN` / prompt) and never stored. `ingressIsOurs` accepts the device rule plus any app rules, and `provisionCloudflare` preserves existing app routes when it rewrites the tunnel.

`modules/ap-portal.nix` (opt-in, `nixosModules.apPortal`) is a setup-only add-on wired into `images.raspberryPi3`/`images.raspberryPiZero2W` alongside `nixosModules.web`, not part of `default`. It runs hostapd + dnsmasq on `wlan0` (fixed SSID `nostrix-setup-<hostname>`/passphrase, gateway `10.42.0.1`), started by `nostrix-ap-mode.service` only when `eth0` has no carrier at boot — a plugged-in cable keeps the existing LAN bootstrap mode. It's never emitted by `nostrix-setup`'s `generate()`, so no teardown logic is needed: once the real generated flake (which doesn't import it) is switched to, NixOS's normal activation stops hostapd/dnsmasq on its own, the same way the web.nix bootstrap → configured transition works. `cmd/nostrix-setup/serve.go`'s bootstrap-mode branch additionally listens on `:80` and answers OS captive-portal probe paths (`cmd/nostrix-setup/captive.go`) with a redirect to `/`, so connecting to the AP pops the phone's captive-portal sign-in prompt.

`modules/offline-src.nix` (opt-in, `nixosModules.offlineSrc`) closes the gap that remains even with `apPortal` above: in the true zero-ethernet flow, the device has no internet uplink until *after* the very `nixos-rebuild switch` that joins real WiFi, but that switch needs to fetch `github:nostra-domus/nostrix` (and transitively nixpkgs) — a real chicken-and-egg problem. It bakes a real, writable git clone of this repo's own source onto the image at `/var/lib/nostrix/nostrix-src` (a single local commit on top of the exact build-time source, `origin` pointed at the real GitHub repo), with its `flake.lock` hand-rewritten (not produced by running `nix flake lock` inside the build — confirmed that doesn't work: a sandboxed build has no real Nix store access, so any store path it resolves only exists in its own private sandbox and is useless on the real system) to override `nixpkgs` to the exact source tree already part of the image's own closure. `generate()` (`cmd/nostrix-setup/generate.go`) references `git+file:///var/lib/nostrix/nostrix-src` instead of `github:nostra-domus/nostrix` whenever `services.nostrix-web.hardware` is set (i.e. only for devices built from one of our own images) — confirmed via `strace` that both building from an existing lock and `nix flake lock --update-input` against a `git+file://` source make zero network connections, unlike `github:`, which can still try to hit `api.github.com` even when the same content is already cached locally (a known, currently-unresolved Nix fetcher limitation — see NixOS/nix#12751). `modules/base.nix`'s weekly `system.autoUpgrade` refreshes the clone (`git fetch`/`reset --hard origin/main`, non-fatally) before each run, so it keeps converging back onto real upstream history once the device has real connectivity.

### Setup wizard flow

`cmd/nostrix-setup/main.go` prompts for hostname, SSH key, hardware profile, addon choices (currently: nginx on/off), and optional WiFi SSID/password (`networking.wireless`, ethernet still required for the initial run itself), then generates a `flake.nix` calling `nostrix.lib.mkSystem`, writes it to `--output` (default `/etc/nixos/flake.nix`), and runs `nixos-rebuild switch --flake /etc/nixos#<hostname>` (`apply()` in `main.go`; the configuration is named explicitly because `nixos-rebuild` otherwise looks it up by the *running* hostname, which is still the old one on the first apply after a rename). If `switch` is blocked by a pre-switch check — a critical-component change (e.g. the D-Bus implementation) NixOS refuses to hot-swap live — `apply()` falls back to `nixos-rebuild boot` plus a scheduled reboot, mirroring how `system.autoUpgrade`'s `allowReboot` already handles this case. Use `--dry-run` to preview without writing.

`nostrix-setup cloudflare --domain <d>` (`cmd/nostrix-setup/prep.go`) is a standalone pre-flight run on the user's own machine: given an API token it resolves the account ID + zone ID (one `/zones?name=` lookup — the zone object carries its account, so no extra token permission is needed), reads the team domain from the Access organization, and runs read-only probes of the tunnel/Access/DNS endpoints so missing token scopes surface before provisioning. With no `$CLOUDFLARE_API_TOKEN` it instead shells out to Cloudflare's `cf` CLI (`cfcli.go`; found as `cloudflare`/`cf` on PATH or `$NOSTRIX_CF`) and uses that login — `cf`'s credential isn't extractable, hence shelling out. `--create-token` mints a scoped API token — account-owned via `cf accounts tokens create` (preferred, per Cloudflare), falling back to a user-owned `cf user tokens create` if the login isn't allowed to make one (permission groups resolved by name **and scope**, since names like "Access: Apps and Policies Write" exist in both account and zone variants; the set lives in `tokenPermissions` and must be kept in step with what `provisionCloudflare` calls), then re-verifies it with the same read-only probes. `cf` prints the bare API `result`, not the `{success,result}` envelope. Without `--create-token` it creates nothing; `provisionCloudflare` remains the only code that writes provisioning resources to Cloudflare. It is find-or-create per step (the `ensure*` helpers in `cloudflare.go`), so a retry after a failed switch reuses its own earlier leftovers — but only resources provably ours (a tunnel with empty or exactly-our routing, a CNAME to this tunnel, an Access app of the same name on the same hostname); a same-named resource that is not ours must fail the run and never be modified.

### Testing VM builds of uncommitted work

Flakes only see tracked files. A new, untracked file is invisible to `nix build` on this repo, so a VM built from the working tree fails (or, worse, silently reuses a stale `vm-result` if a script ignores the build error). `git add -N <file>` (intent to add) makes it visible without staging content. Always check that the build succeeded before booting.

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
