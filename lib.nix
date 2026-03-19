# lib.nix — helper functions for building Nostradomus NixOS systems.
{ self, nixpkgs, domus }:
{
  # mkSystem builds a NixOS configuration with the full Nostradomus stack.
  #
  # Arguments:
  #   hostname  — the machine's hostname (also reachable as hostname.local via mDNS)
  #   sshKeys   — list of SSH public key strings for root login (optional)
  #   system    — Nix system string, default "aarch64-linux" for Raspberry Pi
  #   modules   — additional NixOS modules (hardware profile, addon config, etc.)
  #
  # Example:
  #   nostrix.lib.mkSystem {
  #     hostname = "myserver";
  #     sshKeys  = [ "ssh-ed25519 AAAA..." ];
  #     modules  = [ nostrix.hardware.raspberryPiZero2W
  #                  { services.nostradomus.webserver.nginx.enable = true; } ];
  #   }
  # mkImage builds a compressed SD card image (.img.zst) for flashing to an
  # SD card. Accepts the same arguments as mkSystem; system defaults to
  # "aarch64-linux" (Raspberry Pi). The image is built by adding NixOS's
  # sd-image-aarch64 module on top of the regular system configuration.
  #
  # Example:
  #   nix build .#images.raspberryPiZero2W
  #   # Flash with Raspberry Pi Imager or:
  #   # zstd -d result/sd-image/*.img.zst --stdout | sudo dd of=/dev/rdiskX bs=4m
  mkImage = args:
    (self.lib.mkSystem (args // {
      system  = args.system or "aarch64-linux";
      modules = [
        "${nixpkgs}/nixos/modules/installer/sd-card/sd-image-aarch64.nix"
      ] ++ (args.modules or []);
    })).config.system.build.sdImage;

  mkSystem =
    { hostname
    , sshKeys  ? []
    , system   ? "aarch64-linux"
    , modules  ? []
    }:
    nixpkgs.lib.nixosSystem {
      inherit system;
      # domus is passed as a module argument so modules/default.nix can
      # import domus.nixosModules.default without hardcoding a store path.
      specialArgs = { inherit domus; };
      modules = [
        self.nixosModules.default
        {
          networking.hostName = hostname;
          users.users.root.openssh.authorizedKeys.keys = sshKeys;
        }
      ] ++ modules;
    };
}
