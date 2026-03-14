{
  description = "Nostrix — opinionated NixOS base for Nostradomus";

  inputs = {
    nixpkgs.url    = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";

    domus = {
      url = "github:nostra-domus/domus";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = { self, nixpkgs, flake-utils, domus }:
    let
      # NixOS-only outputs — not per-system.
      nixosOutputs = {
        # The NixOS module. Import this directly for fine-grained control.
        # Most users should use lib.mkSystem instead.
        nixosModules.default = ./modules/default.nix;

        # Hardware profiles — pass one in mkSystem's modules list.
        hardware = {
          raspberryPiZero2W = ./modules/hardware/raspberry-pi-zero-2w.nix;
          genericX86_64     = ./modules/hardware/generic-x86_64.nix;
        };

        # lib.mkSystem is the primary user-facing API.
        # The setup wizard generates a flake.nix that calls this.
        lib = import ./lib.nix { inherit self nixpkgs domus; };

        # Example configuration — verifies the module evaluates cleanly.
        # nix eval .#nixosConfigurations.example.config.networking.hostName
        nixosConfigurations.example = self.lib.mkSystem {
          hostname = "nostradomus";
          system   = "x86_64-linux";
          modules  = [
            self.hardware.genericX86_64
            { services.nostradomus.webserver.nginx.enable = true; }
          ];
        };
      };

      # Per-system outputs: the setup wizard package and app.
      perSystemOutputs = flake-utils.lib.eachDefaultSystem (system:
        let
          pkgs = nixpkgs.legacyPackages.${system};
          setup = pkgs.buildGoModule {
            pname      = "nostrix-setup";
            version    = "0.1.0";
            src        = ./.;
            subPackages = [ "cmd/nostrix-setup" ];
            # No external Go dependencies — stdlib only.
            vendorHash = null;
          };
        in {
          # nix build → ./result/bin/nostrix-setup
          packages.default = setup;

          # nix run github:nostra-domus/nostrix
          apps.default = flake-utils.lib.mkApp {
            drv  = setup;
            name = "nostrix-setup";
          };
        });
    in
    nixosOutputs // perSystemOutputs;
}
