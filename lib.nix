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
