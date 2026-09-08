# Base system configuration applied to every Nostrix installation.
#
# Covers: SSH hardening, firewall, automatic upgrades, Nix store GC.
# Does not include hardware, mDNS, or addon config — those are separate.
{ ... }:
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
