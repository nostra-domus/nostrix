{
  description = "Nostrix — opinionated NixOS base for Nostradomus";

  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";

    domus = {
      url = "github:nostra-domus/domus";
      inputs.nixpkgs.follows = "nixpkgs";
    };
  };

  outputs = { self, nixpkgs, domus }: {
    # The NixOS module. Import this directly if you need fine-grained control.
    # Most users should use lib.mkSystem instead.
    nixosModules.default = ./modules/default.nix;

    # Hardware profiles — pass one as an element of mkSystem's modules list.
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
}
