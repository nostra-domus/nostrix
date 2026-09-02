# lib.nix — helper functions for building Nostrix NixOS systems.
{ self, nixpkgs }:
{
  # mkSystem builds a NixOS configuration with the Nostrix base stack.
  #
  # Arguments:
  #   hostname  — the machine's hostname (also reachable as hostname.local via mDNS)
  #   sshKeys   — list of SSH public key strings for root login (optional)
  #   system    — Nix system string, default "aarch64-linux" for Raspberry Pi
  #   modules   — additional NixOS modules (hardware profile, app config, etc.)
  #
  # Example:
  #   nostrix.lib.mkSystem {
  #     hostname = "myserver";
  #     sshKeys  = [ "ssh-ed25519 AAAA..." ];
  #     modules  = [ nostrix.hardware.raspberryPiZero2W
  #                  myapp.nixosModules.default
  #                  { services.nginx.enable = true; } ];
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
      # Modules (e.g. web.nix) build the nostrix-setup package themselves
      # via self.lib.mkSetupPackage, so they need self in scope.
      specialArgs = { inherit self; };
      modules = [
        self.nixosModules.default
        {
          networking.hostName = hostname;
          users.users.root.openssh.authorizedKeys.keys = sshKeys;
        }
      ] ++ modules;
    };

  # mkSetupPackage builds the nostrix-setup Go binary. Shared by the
  # per-system `packages.default` output and modules/web.nix's systemd
  # service, so both always reference the exact same derivation.
  mkSetupPackage = { pkgs }: pkgs.buildGoModule {
    pname       = "nostrix-setup";
    version     = "0.1.0";
    src         = self;
    subPackages = [ "cmd/nostrix-setup" ];
    # No external Go dependencies — stdlib only.
    vendorHash  = null;
  };
}
