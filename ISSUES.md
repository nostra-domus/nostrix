# Issues / backlog

Lightweight local backlog for work that's been scoped but not yet implemented.

## Cloudflare provisioning for the web UI's Access auth — done via LAN bootstrap mode

**Status: implemented and verified end-to-end on real hardware (2026-09-08), merged to
main.** Originally scoped as a pre-flash, Raspberry-Pi-Imager-style provisioning tool run
on the installer's own computer before the SD card is ever in the Pi. That direction was
dropped: it would have required Nix plus aarch64 cross-build tooling on the installer's
machine, which doesn't work on Windows and is awkward on Mac — the opposite of the
phone-only goal.

What shipped instead: `modules/web.nix`'s `nostrix-web` service now has a **bootstrap
mode**, active whenever `cloudflareTeamDomain`/`cloudflareAud` are unset (the state a
freshly flashed image boots into). In that mode it binds to the LAN instead of
localhost and serves a setup form (`cmd/nostrix-setup/templates/bootstrap.html`,
behind a fixed shared credential — same trust level as the existing temporary
first-boot SSH password) collecting hostname, SSH key, owner email, and Cloudflare
account details. Submitting it calls the Cloudflare API directly
(`cmd/nostrix-setup/cloudflare.go`) to create the tunnel, DNS route, Access app, and
Access policy, then generates and applies a flake with the result baked in
(`modules/cloudflared.nix`'s `tunnelToken` option). `nixos-rebuild switch` then
restarts `nostrix-web` in its normal locked-down, Access-gated mode automatically.

Net effect: the entire setup — including Cloudflare provisioning — can be done from a
phone browser on the same network as the device, no SSH, no Nix, no laptop. The
existing SSH-based CLI wizard (`wizard.go`) gained the identical Cloudflare prompts for
parity, sharing the same `generate()`/`provisionCloudflare` code.

**Real-hardware verification (2026-09-08):** ran the full flow on an actual Pi 3 —
Cloudflare API calls confirmed against a live account (tunnel, DNS, Access app and
policy all created correctly), bootstrap form → `nixos-rebuild switch` → automatic
lockdown all confirmed on-device, and `https://<hostname>.<base-domain>` reachable
through Cloudflare Access from outside the LAN while the LAN port closed as expected.
Two unrelated latent bugs surfaced only by building from the real git history (Nix
flakes only see committed files, unlike local `go build`) and got fixed along the way:
`nostrix-setup add` never updated its call site after `generate()` gained an error
return (`cmd/nostrix-setup/add.go`), and the bare `nix` CLI had no experimental-features
enabled (only `nixos-rebuild`'s own internal invocations did) — now set globally in
`modules/base.nix`.

**Remaining scope, not yet done:** the device's ultimate *recipient*, if different from
whoever fills in the bootstrap form (e.g. an operator setting up a device for a
non-technical family member), still can't do so with only an email address — the form
needs a Cloudflare API token, which isn't something to hand to an end user. Making that
work would mean the device calling an operator-run backend (holding the operator's
Cloudflare credentials) instead of the Cloudflare API directly — a genuinely separate,
bigger piece of infrastructure, deliberately out of scope for this pass.

---

## Add WiFi configuration to the setup wizard

**Status: implemented (2026-09-08).**

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

**Follow-up (2026-09-08): brought to parity with the web UI, plus a Rebuild fix.** The CLI
wizard and the web configuration UI (`nostrix-web`'s configured-mode `/setup` page) both feed
the same `generate()`, so both now expose the same fields — WiFi SSID/password were added to
`templates/setup.html` and `handleSetup` in `serve.go`, with the current SSID also shown on the
status page (`templates/index.html`). Keep both entry points in sync going forward: any field
added to one belongs in the other too.

While wiring this up, fixed a separate bug found in the same code path: the web UI's "Rebuild
now" button (`handleRebuild`) called plain `nixos-rebuild switch`, which only re-applies the
already-resolved `flake.lock` — it could never pick up upstream nostrix/nixpkgs changes (e.g.
this very WiFi feature) the way the CLI wizard's fresh `nix run` invocation does. It now passes
`--upgrade-all` (`apply()` in `main.go` gained an `upgradeAll bool` parameter, threaded through
`rebuildAsync`); the wizard's and `add`'s own `apply()` calls stay non-upgrading, since those
already run against a freshly-fetched flake.

**Verification:**

- `go build ./cmd/nostrix-setup && go vet ./...` — done, passes.
- Run `./nostrix-setup --dry-run`, answer the WiFi prompt with a test SSID/password, confirm the
  printed flake.nix contains the `networking.wireless` block, and that skipping (blank SSID)
  omits it entirely — done, both confirmed (also covered by
  `TestGenerateIncludesWifiWhenSet`/`TestGenerateOmitsWifiWhenUnset` in `generate_test.go`).
- Manually eval the generated snippet's shape against a real hardware profile, e.g. drop the
  generated module into a scratch flake using `nostrix.hardware.raspberryPi3` and run `nix eval`
  on `config.networking.wireless.networks` to confirm it evaluates cleanly — done, evaluates
  cleanly.
- On real hardware: flash `images.raspberryPi3`, boot over ethernet, run `nostrix-setup` with
  real WiFi credentials, confirm `nixos-rebuild switch` succeeds and the Pi is reachable at
  `nostrix.local` after unplugging ethernet — **done (2026-09-08)**, see follow-ups below for
  the road to get there (two unrelated bugs surfaced and fixed along the way). Final
  confirmation: `wlan0` associated with a DHCP address, and with ethernet unplugged, `ping`/
  `ssh` to `nostrix-pi01.local` and the device's Cloudflare Tunnel URL all worked from another
  device on the WiFi network.

**Follow-up (2026-09-08): real-hardware WiFi test surfaced a separate, more general
livelock bug — fixed (`db049ac`).** Testing the WiFi flow on a live Pi 3 (via the web UI's
`/setup` page, device already configured from the earlier Cloudflare test) triggered a
`nixos-rebuild switch` that hung the entire box, including SSH — not the WiFi feature's fault,
but a pre-existing gap in the Pi 3's OOM handling that this rebuild happened to trip. Root
cause: `systemd-oomd` ships enabled by default but watches no cgroup slice out of the box
(`enableRootSlice`/`enableSystemSlice`/`enableUserSlices` all default false), so it never
intervenes; the kernel OOM killer doesn't fire either, since zram swap (added in `42b319e`)
makes just enough memory "available" to avoid a hard OOM — so instead of getting killed, the
box livelocked under swap thrashing, taking SSH down with it. Fixed by:
- `modules/base.nix` — `systemd.oomd.enableRootSlice = true`, on every Nostrix system (not
  just low-RAM boards): if a rebuild spirals, oomd now kills the offending process before the
  whole box livelocks, instead of nothing happening.
- `raspberry-pi-3.nix` / `raspberry-pi-zero-2w.nix` — `zramSwap.memoryPercent` raised from the
  50% default to 150%, plus `nix.settings.cores = 1` alongside the existing `max-jobs = 1`, to
  give the evaluator more real headroom before oomd needs to step in at all.

Deliberately kept on-device building as the model (per project design) rather than moving to
a `--target-host` remote-build workflow, which would have sidestepped the memory pressure
entirely but was explicitly ruled out — on-device building is a core Nostrix feature. Slower
rebuilds under memory pressure are an accepted tradeoff; a hung, unreachable box is not.

Still needed to close out the WiFi item's real-hardware verification: re-run the WiFi `/setup`
submission on the Pi 3 with this fix in place and confirm it completes (however slowly) and
the Pi is reachable at `nostrix.local` over WiFi with ethernet unplugged.

**Follow-up (2026-09-08): a second, unrelated blocker hit on the same retest — `nixos-rebuild
switch` refusing to switch after a `--upgrade-all` moved the nixpkgs pin forward and changed
the default D-Bus implementation (`dbus` → `dbus-broker`), which NixOS's pre-switch checks
correctly refuse to hot-swap into a live system ("Pre-switch checks failed" /
`switchInhibitors`). Fixed in `apply()` (`cmd/nostrix-setup/main.go`): it now tries
`nixos-rebuild switch` first as before, and only when that specific failure occurs falls back
to `nixos-rebuild boot` (which skips the inhibitor check entirely) followed by a
`shutdown -r +1` — mirroring how `system.autoUpgrade`'s `allowReboot` already resolves this
same situation for the weekly auto-upgrade. Without this, the wizard/web UI (and the CLI
wizard/`add`) would dead-end on an error a phone-only or remote user has no way to act on.
`go build`/`go vet`/`go test ./...` and `nix build .#default` all pass; CLAUDE.md's setup
wizard flow description updated to match. **Not yet confirmed on real hardware**: this fix
was pushed after the dbus-broker switch had already been resolved manually (`nixos-rebuild
boot` + reboot, run by hand before the fix existed), so the device's own switch→boot fallback
path has never actually fired. The immediately following "exit status 1" the web UI showed
turned out to be an unrelated, non-reproducing flake — the identical `nixos-rebuild switch
--upgrade-all` succeeded cleanly (`Checking switch inhibitors... done`) when run by hand
moments later, most likely Nix store/daemon lock contention from two rebuild requests landing
within ~8s of each other (`/setup` save, then "Rebuild now") rather than anything wrong with
the generated config. Remains open: trigger a genuine switch-inhibited change on this device
(or any Nostrix system) through the wizard/web UI and confirm the fallback to `boot` + reboot
actually engages end-to-end.

**Follow-up (2026-09-08): WiFi real-hardware verification now fully closed.** With both fixes
above in place, WiFi was re-verified on the Pi 3 end-to-end: `wlan0` came up via
`networking.wireless`/`wpa_supplicant.service` with a DHCP-assigned address, and with the
ethernet cable unplugged, `ping`/`ssh` to `nostrix-pi01.local` and the device's Cloudflare
Tunnel URL all worked from another device on the WiFi network. The "Add WiFi configuration to
the setup wizard" item above is now fully verified, not just implemented.

One minor, non-blocking rough edge noticed along the way: `nostrix-web`'s "Last rebuild
failed: ..." banner (`srv.lastError` in `serve.go`) is in-memory and sticky — it only clears
on the *next* rebuild the web UI itself runs (or a process restart/reboot), so a transient
failure like the one above leaves a stale "failed" banner up indefinitely even once the system
is demonstrably healthy, until someone happens to trigger another rebuild. Not scoped/fixed
here; low priority.

---

## Ethernet-free first boot via WiFi AP config portal

**Update (2026-09-08): the sibling WiFi item is now done, unblocking the full pitch.** "Add
WiFi configuration to the setup wizard" (above) shipped — `state.WifiSSID`/`WifiPSK`,
`networking.wireless` emission in `generate()`, and prompts in both the CLI wizard and the web
UI's `/setup` page. The "Deliberately independent" note below still holds (this item is its own
piece of work — hostapd/dnsmasq, the mode-switch logic, binding the bootstrap form to the AP
interface, all still to build), but the mode-switch step's dependency on WiFi client support is
no longer hypothetical: the device now has a real way to join a WiFi network once the AP-served
form submits credentials, via the same `WifiSSID`/`WifiPSK` fields and `networking.wireless`
module this item already assumed. That's what makes "phone-only setup with no ethernet ever"
achievable end-to-end, not just the transient-AP-then-ethernet fallback described below.

**Context:** Today, initial setup always requires an ethernet cable — both the SSH wizard and
the phone-browser Cloudflare bootstrap flow (`modules/web.nix`'s `nostrix-web` bootstrap mode,
see the first item in this file) assume the device is already reachable on the LAN. Many
consumer devices (Chromecast, smart plugs, mesh routers, etc.) avoid this by booting into their
own WiFi access point when unconfigured; you connect a phone to that hotspot directly, fill in a
setup form served from the device itself, and the device then joins the real network.

**Approach:** When unconfigured (same trigger state as the existing bootstrap mode — no
Cloudflare/hostname config present yet, or more precisely no completed `nostrix-setup` run), the
Pi would run `hostapd` to broadcast its own AP (SSID e.g. `nostrix-setup-<hostname>`, fixed
passphrase — same trust level as the existing temporary root SSH password and bootstrap shared
credential) plus a DHCP server (`dnsmasq` or similar) on the WiFi interface. The existing
bootstrap web UI (`nostrix-web` bootstrap mode, `bootstrap.html`) would bind to the AP interface
instead of (or in addition to) the LAN, so a phone connecting to the hotspot can reach the setup
form directly at a fixed AP-gateway address. Submitting the form applies the config as today
(`generate()` + `nixos-rebuild switch`), then the device needs to tear down `hostapd` and bring
up its real networking (ethernet, or WiFi client mode if the sibling WiFi item below is also in
place) — a genuine mode-switch since the Pi typically has a single WiFi radio that can't be AP
and client simultaneously.

**Deliberately independent of "Add WiFi configuration to the setup wizard" (above):** that item
is about the day-2 wizard prompting for WiFi *client* credentials to join a network. This item is
about the *delivery mechanism* for reaching the device at all during first boot, and doesn't
strictly require WiFi client support to exist — the AP could be used purely as a transient config
channel while the device keeps using ethernet for steady-state operation, which still solves the
"must already be on the same LAN as the device" problem for phone-only setup. The two items
combine for the full pitch (phone-only setup with no ethernet ever, before or after
configuration), but each should be implementable and useful on its own.

**Open questions, not yet resolved:**
- Trigger condition for entering AP mode: always when unconfigured, or only when no ethernet
  link is detected (so a plugged-in cable still gets today's LAN-bootstrap behavior unchanged)?
- Captive-portal UX: rely on the phone's OS auto-detecting the captive portal and prompting the
  user (needs a detection endpoint mimicking what iOS/Android probe for), or keep it simple for a
  first pass and just tell the user to open a fixed IP in their browser?
- Whether `hostapd` config (SSID/passphrase) should be static per-image or randomized/printed on
  first boot somehow — static is simpler but means anyone in WiFi range of an unconfigured device
  can see the setup form until it's locked down.

**Changes (rough, needs a real design pass before implementation):**
- New hardware/base module for `hostapd` + DHCP server, gated on the "unconfigured" trigger
  condition (mirrors how `modules/web.nix` already gates bootstrap mode).
- `modules/web.nix` — bind bootstrap mode to the AP interface as well as/instead of LAN.
- Mode-switch logic (systemd service/script) to stop the AP and start real networking after
  `nixos-rebuild switch` completes.
- README/CLAUDE.md updates describing the new no-ethernet setup path.

**Verification:** Flash a device, boot it with no ethernet connected, confirm a hotspot appears,
connect a phone to it, complete the setup form, confirm the device applies config and becomes
reachable on the real network afterward (ethernet or WiFi per whatever was configured).

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

## Add `images.raspberryPi4` SD card image

**Context:** `modules/hardware/raspberry-pi-4.nix` exists and is exposed as
`nostrix.hardware.raspberryPi4` (`flake.nix`'s `hardware` attrset), but unlike Pi 3 and Pi Zero
2W, nothing ever turns it into a flashable image — `flake.nix`'s `nixosOutputs` only defines
`images.raspberryPi3` and `images.raspberryPiZero2W`. The hardware profile itself looks
complete (BCM2711, extlinux, `enableRedistributableFirmware`, matches the Pi 3/Zero 2W shape
closely enough that it hasn't needed the zram/max-jobs tuning those two needed — Pi 4 boards
commonly ship with 2GB+ RAM). Docs are behind too: README's "Hardware profiles" table
(`README.md:152-158`) doesn't list `raspberryPi4` at all (not just the image — the hardware
attribute itself is undocumented), and CLAUDE.md's flake-outputs table is missing
`hardware.raspberryPi3`, `hardware.raspberryPi4`, and `images.raspberryPi3` as well — it only
lists `hardware.raspberryPiZero2W`/`hardware.genericX86_64` and `images.raspberryPiZero2W`.

**Approach:** Add an `images.raspberryPi4` block to `flake.nix`'s `nixosOutputs`, mirroring
`images.raspberryPi3` exactly (temporary root password + forced `PasswordAuthentication`/
`PermitRootLogin`, `nostrix-web` bootstrap module, nginx hardcoded on port 80, pinned
`system.stateVersion`) but built on `self.hardware.raspberryPi4`. Update README's hardware
table to add the `raspberryPi4` row, and fix CLAUDE.md's flake-outputs table to include all
three hardware profiles and both Pi images (not just add Pi 4 — the Pi 3 entries were already
missing before this item).

**Changes:**
- `flake.nix` — new `images.raspberryPi4` output.
- `README.md` — add `nostrix.hardware.raspberryPi4` to the hardware profiles table.
- `CLAUDE.md` — flake-outputs table: add `hardware.raspberryPi3`, `hardware.raspberryPi4`,
  `images.raspberryPi3`, `images.raspberryPi4`.

**Verification:** `nix build .#images.raspberryPi4` succeeds; `nix eval
.#nixosConfigurations` smoke-checks the same way the Pi 3/Zero 2W images already do. On real
hardware (if a Pi 4 is available): flash, boot over ethernet, confirm `nostrix.local` is
reachable and `nostrix-setup` completes successfully, same as the Pi 3 verification already
done for Cloudflare/WiFi.

---

## Implement or retire the `config.yaml` app convention

**Context:** `nostrix-setup add <git-url>` (`cmd/nostrix-setup/add.go`) creates
`/etc/nostrix/<name>/` and prints "Place your config.yaml there before the service will
start" — but nothing in the codebase ever reads that file: no Nix module references
`/etc/nostrix/<name>/config.yaml`, and neither README nor CLAUDE.md document what it's
supposed to contain or how an app module would consume it. The directory gets created; the
promise printed alongside it is currently fiction.

Separately, the CLI and web UI have drifted out of parity on app management itself: the web
UI's `/apps` page already supports add, list, and remove (`handleApps`/`handleAppRemove`/
`removeApp` in `serve.go`), but `nostrix-setup`'s CLI only has an `add` subcommand — no `list`
or `remove`. This is the same kind of drift the WiFi item's follow-up flagged and fixed for
`generate()`'s fields ("keep both entry points in sync"), just for app registration instead.

**Open questions, not yet resolved — needs a design pass before implementation:**
- What's actually meant to read `config.yaml`? Two very different shapes: (a) Nostrix itself
  provides some generic mechanism (e.g. a NixOS module that reads YAML from
  `/etc/nostrix/<name>/config.yaml` and exposes it to the app somehow), or (b) it's purely a
  per-app-module responsibility — Nostrix's job is only to guarantee the directory exists, and
  each app's own `nixosModules.default` is expected to read its own config file itself. (b)
  fits the project's "application-agnostic, bring your own module" design much better than
  (a), which would require Nostrix to standardize a config schema/injection mechanism it
  currently has no opinion on.
- If it's (b): is there anything left to build at all, or is the actual bug just that this
  contract (directory exists, you read your own file from it) was never written down anywhere
  an app author would find it?
- Is `config.yaml` even the right convention to keep, or was it aspirational scaffolding from
  before the app-registration flow (`d45c4e8`) solidified, worth just removing the misleading
  printed message instead?

**Changes (rough, pending the above):**
- At minimum: add `nostrix-setup list` and `nostrix-setup remove <name>` CLI subcommands,
  reusing the same `removeApp`/state-listing logic `serve.go` already has, for CLI/web parity.
- Depending on which reading of "config.yaml" above is correct: either document the
  directory-exists-you-read-it-yourself contract in README (for app authors), or design and
  implement an actual reading mechanism — and either way, fix or remove `add.go`'s current
  printed message so it stops promising behavior nothing delivers.

**Verification:** TBD — depends on which direction the design pass above lands on.

---

## Other gaps found during repo review (2026-08-31), not yet scoped

- **Go test coverage is partial** — `auth_test.go`, `cloudflare_test.go`, and `generate_test.go`
  now cover Access JWT verification, Cloudflare API provisioning, and flake generation. Still
  untested: `appNameFromURL` (git URL parsing, in `add.go`) has the most edge cases of what's left.
- **No CI** — no `.github/workflows/`; `go test`/`go vet` and the Nix integration check are
  documented but nothing runs them automatically on push/PR.
