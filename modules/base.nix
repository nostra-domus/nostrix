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
}
