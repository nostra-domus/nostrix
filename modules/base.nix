# Base system configuration applied to every Nostrix installation.
#
# Covers: SSH hardening, firewall, automatic upgrades, Nix store GC.
# Does not include hardware, mDNS, or addon config — those are separate.
{ pkgs, ... }:
{
  # SSH: public-key authentication only, no passwords.
  services.openssh = {
    enable = true;
    settings = {
      PasswordAuthentication         = false;
      KbdInteractiveAuthentication   = false;
      PermitRootLogin                = "prohibit-password";
    };
  };

  # Firewall: deny all inbound except SSH.
  # Addons that need inbound ports (nginx, etc.) open them via their own
  # NixOS module by setting networking.firewall.allowedTCPPorts.
  networking.firewall = {
    enable          = true;
    allowedTCPPorts = [ 22 ];
  };

  # Automatic upgrades: rebuild from the local /etc/nixos flake weekly,
  # updating all flake inputs (nostrix, nixpkgs, and any app inputs) in one step.
  # The server reboots automatically when needed (e.g. kernel update).
  system.autoUpgrade = {
    enable            = true;
    flake             = "/etc/nixos";
    flags             = [ "--upgrade-all" ];
    allowReboot       = true;
    dates             = "weekly";
    randomizedDelaySec = "45min";  # stagger updates across a fleet
  };

  # A device built from one of our own SD images (modules/offline-src.nix)
  # references nostrix via a local git clone
  # (/var/lib/nostrix/nostrix-src), not github:nostra-domus/nostrix —
  # --upgrade-all above only re-resolves that input to whatever the clone's
  # current HEAD already is, so without this it would never actually pick
  # up new nostrix commits. A leading "-" makes failure here non-fatal
  # (e.g. genuinely offline this week): the upgrade still proceeds, it
  # just re-resolves to the same commit as before. No-op (directory
  # missing) on any system that isn't one of our images.
  #
  # `timeout 30` bounds the whole thing: this timer is `persistent = true`
  # (systemd's default), so it fires within randomizedDelaySec of the
  # very first boot on a brand-new device — which may still be offline
  # or only AP-connected at that point. Without a hard timeout, `git
  # fetch` over HTTPS with a broken/absent resolver can hang for minutes
  # (DNS + TCP connect retries), starving a single-core Pi 3 of CPU and
  # making everything else on the box — including nostrix-web — feel
  # unresponsive in the meantime.
  systemd.services.nixos-upgrade.serviceConfig.ExecStartPre =
    let
      refresh = pkgs.writeShellScript "nostrix-refresh-offline-src" ''
        dir=/var/lib/nostrix/nostrix-src
        if [ -d "$dir" ]; then
          ${pkgs.coreutils}/bin/timeout 30 ${pkgs.git}/bin/git -C "$dir" fetch origin
          ${pkgs.git}/bin/git -C "$dir" reset --hard origin/main
        fi
      '';
    in
    [ "-${refresh}" ];

  # Garbage collect old generations weekly; keep 30 days of history
  # so a bad upgrade can be rolled back.
  nix.gc = {
    automatic = true;
    dates     = "weekly";
    options   = "--delete-older-than 30d";
  };
  nix.settings.auto-optimise-store = true;

  # Nostrix is flake-only by design (mkSystem, mkImage, nostrix-setup's
  # generated flake.nix all assume it). nixos-rebuild already passes
  # --extra-experimental-features itself on every invocation, but that
  # doesn't cover a bare `nix ...` call (e.g. `nix flake update`) — enable
  # both globally so the plain CLI works too, on every Nostrix system.
  nix.settings.experimental-features = [ "nix-command" "flakes" ];

  # systemd-oomd ships enabled by default but doesn't watch any slice by
  # default (enableRootSlice/enableSystemSlice/enableUserSlices all false) —
  # so it never actually intervenes. On memory-constrained boards (Pi 3,
  # Pi Zero 2W) a `nixos-rebuild switch` that outgrows RAM+swap doesn't get
  # killed by the kernel either, since zram swap makes just enough memory
  # "available" to avoid a hard OOM — instead the whole box livelocks under
  # swap thrashing, taking SSH down with it. Watching the root slice lets
  # oomd kill the offending process under sustained memory/swap pressure
  # before that happens, on every Nostrix system (not just low-RAM boards).
  systemd.oomd.enableRootSlice = true;
}
