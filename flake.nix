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

        # SD card image for the Raspberry Pi Zero 2W.
        #
        # Build:  nix build .#images.raspberryPiZero2W
        # Flash:  use Raspberry Pi Imager with "Custom image", or:
        #         zstd -d result/sd-image/*.img.zst --stdout \
        #           | sudo dd of=/dev/rdiskX bs=4m
        #
        # First boot: SSH in as root (password: nostradomus) via nostradomus.local
        # then run `nostrix-setup` to apply your real configuration.
        images.raspberryPiZero2W = self.lib.mkImage {
          hostname = "nostradomus";
          modules  = [
            self.hardware.raspberryPiZero2W
            ({ lib, ... }: {
              # Temporary credentials for first boot only.
              # nostrix-setup will replace these with your SSH key.
              users.users.root.password = "nostradomus";
              services.openssh.settings.PasswordAuthentication =
                lib.mkForce true;

              services.nostradomus.webserver.nginx = {
                enable  = true;
                content = "Hello from Nostradomus!";
              };
            })
          ];
        };

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

      # Integration test: boots the full nostrix stack and verifies
      # nginx serves content, SSH is hardened, and the hostname is correct.
      # Run with: nix build .#checks.x86_64-linux.integration
      integrationTest = nixpkgs.legacyPackages.x86_64-linux.testers.nixosTest {
        name = "nostrix-full-stack";

        nodes.machine = { lib, ... }: {
          imports = [
            domus.nixosModules.default
            ./modules/base.nix
            ./modules/mdns.nix
          ];

          services.nostradomus = {
            enable = true;
            webserver.nginx = {
              enable  = true;
              port    = 8080;
              content = "Hello from Nostrix!";
            };
          };

          networking.hostName = "nostradomus-test";

          # Auto-upgrade needs a real flake at /etc/nixos — disable in tests.
          system.autoUpgrade.enable = lib.mkForce false;

          system.stateVersion = "24.05";
        };

        testScript = ''
          machine.start()
          machine.wait_for_unit("multi-user.target")

          # Print service status early so failures are visible instead of hanging.
          machine.succeed("systemctl status nostradomus.service --no-pager || true")
          machine.succeed("journalctl -u nostradomus.service --no-pager -n 30 || true")

          machine.wait_for_unit("nostradomus.service")
          machine.wait_for_unit("nginx.service")
          machine.wait_for_open_port(8080)

          # nginx serves the configured content
          response = machine.succeed("curl -sf http://localhost:8080")
          assert "Hello from Nostrix!" in response, f"Unexpected response: {response}"

          # SSH password authentication is disabled
          machine.succeed("sshd -T | grep -i 'passwordauthentication no'")

          # hostname is correct
          hostname = machine.succeed("hostname").strip()
          assert hostname == "nostradomus-test", f"Unexpected hostname: {hostname}"
        '';
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
    nixosOutputs // perSystemOutputs // {
      checks.x86_64-linux.integration = integrationTest;
    };
}
