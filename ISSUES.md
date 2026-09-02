# Issues / backlog

Lightweight local backlog for work that's been scoped but not yet implemented.

## Add WiFi configuration to the setup wizard

**Context:** Nostrix has no WiFi support anywhere — no `networking.wireless` config in
`base.nix`, any hardware profile, or the wizard. A Pi 3 (or Pi 4 / Zero 2W) currently
requires an ethernet cable, full stop. Reachability otherwise already works: `images.raspberryPi3`
boots over ethernet, broadcasts `nostrix.local` via Avahi (`modules/mdns.nix`), and offers a
temporary root/password SSH login for first boot (`flake.nix:38-55`) until `nostrix-setup`
replaces it with key-only auth.

Scope: ethernet is available for the initial boot/setup. This is a day-2 wizard feature
(configure WiFi after first ethernet-based `nostrix-setup` run), not a boot-time one — no
changes to the pre-built SD image or `lib.mkImage` are needed.

All three Pi hardware profiles (`raspberry-pi-3.nix`, `raspberry-pi-4.nix`,
`raspberry-pi-zero-2w.nix`) already set `hardware.enableRedistributableFirmware = true`, which
pulls in the `brcmfmac` firmware their wifi chips need — no hardware-profile changes required.

**Approach:** Follow the existing pattern used for the nginx addon: the wizard prompts for an
optional value and `generate.go` emits an inline anonymous module in the `modules` list, rather
than adding a new file under `modules/`. This keeps `nixosModules.default` hardware/addon-agnostic
per the project's existing convention.

Generated block (only emitted if an SSID was given):
```nix
{
  networking.wireless.enable = true;
  networking.wireless.networks."<ssid>" = { psk = "<password>"; };
}
```

Uses NixOS's built-in `wpa_supplicant`-based `networking.wireless` (not NetworkManager),
consistent with the project's minimal, no-extra-daemons approach and the Go module's
stdlib-only constraint. The PSK lands in plaintext in the generated `flake.nix` / Nix store —
same trust model as the existing temporary root password and pasted SSH key. Acceptable for a
single-user Pi; not worth adding secrets management for.

**Changes:**

- `cmd/nostrix-setup/state.go` — add `WifiSSID string` / `WifiPSK string` (both `omitempty`) to `state`.
- `cmd/nostrix-setup/wizard.go` — after the addons section, prompt for an optional SSID
  ("leave blank to skip and use ethernet only") and, if given, a password. Wire into the
  `state{}` literal.
- `cmd/nostrix-setup/generate.go` — after the nginx block, emit the `networking.wireless` module
  when `s.WifiSSID != ""`, using the same unescaped `%s` string-interpolation style already used
  for hostname/SSH key in this file (same pre-existing escaping caveat, not a new one).
- `README.md` — mention the WiFi prompt in the setup wizard walkthrough; note the
  ethernet-for-first-boot requirement and the plaintext-PSK caveat.
- `CLAUDE.md` — update the "Setup wizard flow" bullet to include WiFi SSID/password.

**Verification:**

- `go build ./cmd/nostrix-setup && go vet ./...`
- Run `./nostrix-setup --dry-run`, answer the WiFi prompt with a test SSID/password, confirm the
  printed flake.nix contains the `networking.wireless` block, and that skipping (blank SSID)
  omits it entirely.
- Manually eval the generated snippet's shape against a real hardware profile, e.g. drop the
  generated module into a scratch flake using `nostrix.hardware.raspberryPi3` and run `nix eval`
  on `config.networking.wireless.networks` to confirm it evaluates cleanly.
- On real hardware (if available before merging): flash `images.raspberryPi3`, boot over
  ethernet, run `nostrix-setup` with real WiFi credentials, confirm `nixos-rebuild switch`
  succeeds and the Pi is reachable at `nostrix.local` after unplugging ethernet.

---

## Bake `nostrix-setup` into the SD card image

**Context:** Found while getting a live Pi 3 running (2026-09-01). Today, first boot requires
`nix run --extra-experimental-features "nix-command flakes" github:nostra-domus/nostrix` over
the network just to get the wizard binary onto the box — it isn't installed on the image at all
(no `environment.systemPackages` entry for it anywhere in `modules/` or the image blocks in
`flake.nix`).

Note this only removes *one* network dependency, not the whole one: `nostrix-setup` still
generates a `flake.nix` with `inputs.nostrix.url = "github:nostra-domus/nostrix";`, and the
subsequent `nixos-rebuild switch` (plus the weekly `system.autoUpgrade` in `base.nix`) will
always need network access to fetch the actual Nostrix modules regardless. This item is purely
about not needing network access for the wizard binary itself on first boot.

**Approach:** Add the `nostrix-setup` package (already built per-system in `flake.nix`'s
`perSystemOutputs`) to `environment.systemPackages` in the image-specific modules
(`images.raspberryPi3` / `images.raspberryPiZero2W` in `flake.nix`), so it's already on `$PATH`
over the temporary first-boot SSH session without needing `nix run`.

---

## Pre-populate the image's Nix store with the wizard's target closure

**Context:** Found alongside the item above (2026-09-01). Even with `nostrix-setup` baked into
the image, running the wizard and then `nixos-rebuild switch` still downloads a lot on first
real setup — `nixpkgs` itself (one-time, gets cached after), and, more significantly, whatever
packages the *real* generated config needs that aren't already present in the local Nix store.

The SD image only ever builds its *temporary* first-boot shape (forced root password, forced
`PasswordAuthentication`/`PermitRootLogin`, nginx hardcoded on) — not the shape `nostrix-setup`
actually generates (real SSH key, hardened SSH restored, whatever addons the user picked). Since
Nix is content-addressed, any package that's genuinely identical between the two configs is
already in the store and won't be re-fetched — but nothing today makes sure the *target* shape's
closure is present in advance.

**Approach:** In the image-specific modules in `flake.nix`, add a `system.extraDependencies`
entry that references the closure of a representative "typical wizard output" config — same
hardware profile, a placeholder hostname/SSH key, nginx enabled — built via
`nostrix.lib.mkSystem` (or the shared `nostrix.hardware.*` + module set the wizard would use).
`system.extraDependencies` pulls a derivation's full closure into the image's Nix store without
making it the active generation. As long as the wizard-generated config resolves to package
versions matching that same `flake.lock` pin, `nixos-rebuild switch` after running the wizard
should find (almost) everything already local and complete close to instantly, without hitting
the network.

Combined with baking in the `nostrix-setup` binary (previous item), this would make the whole
first-boot → real-config flow work almost entirely offline after the initial image flash.

**Verification:** Build an updated image with this change, flash it, and time
`nixos-rebuild switch` after running the wizard with typical answers (same hardware profile,
nginx enabled) — it should complete with little to no network fetch, versus the current
from-scratch download.

---

## Other gaps found during repo review (2026-08-31), not yet scoped

- **Pi 4 wiring incomplete** — `hardware.raspberryPi4` exists but there's no
  `images.raspberryPi4` SD-card build output (unlike Pi 3 and Pi Zero 2W), and the README's
  hardware table doesn't mention Pi 4.
- **`nostrix-setup add`'s `config.yaml` convention is unimplemented** — it creates
  `/etc/nostrix/<name>/` and tells the user to place a `config.yaml` there, but nothing (no Nix
  module, no docs) ever reads that file. No `remove`/`list` counterpart either.
- **Zero Go test coverage** — no `_test.go` files anywhere, despite `CLAUDE.md` documenting
  `go test ./...`. `appNameFromURL` (git URL parsing) has the most edge cases and would benefit
  most.
- **No CI** — no `.github/workflows/`; `go test`/`go vet` and the Nix integration check are
  documented but nothing runs them automatically on push/PR.
- **Minor doc drift** — `CLAUDE.md`'s flake-outputs table is missing `images.raspberryPi3`.
