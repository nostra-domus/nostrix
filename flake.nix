{
  description = "Nostrix — opinionated NixOS base for self-hosted servers";

  inputs = {
    nixpkgs.url     = "github:NixOS/nixpkgs/nixos-unstable";
    flake-utils.url = "github:numtide/flake-utils";
  };

  outputs = { self, nixpkgs, flake-utils }:
    let
      # NixOS-only outputs — not per-system.
      nixosOutputs = {
        # The NixOS module. Import this directly for fine-grained control.
        # Most users should use lib.mkSystem instead.
        nixosModules.default = ./modules/default.nix;

        # Hardware profiles — pass one in mkSystem's modules list.
        hardware = {
          raspberryPi3      = ./modules/hardware/raspberry-pi-3.nix;
          raspberryPi4      = ./modules/hardware/raspberry-pi-4.nix;
          raspberryPiZero2W = ./modules/hardware/raspberry-pi-zero-2w.nix;
          genericX86_64     = ./modules/hardware/generic-x86_64.nix;
        };

        # lib.mkSystem is the primary user-facing API.
        # The setup wizard generates a flake.nix that calls this.
        lib = import ./lib.nix { inherit self nixpkgs; };

        # SD card image for the Raspberry Pi Zero 2W.
        #
        # Build:  nix build .#images.raspberryPiZero2W
        # Flash:  use Raspberry Pi Imager with "Custom image", or:
        #         zstd -d result/sd-image/*.img.zst --stdout \
        #           | sudo dd of=/dev/rdiskX bs=4m
        #
        # First boot: SSH in as root (password: nostrix) via nostrix.local
        # then run `nostrix-setup` to apply your real configuration.
        images.raspberryPi3 = self.lib.mkImage {
          hostname = "nostrix";
          modules  = [
            self.hardware.raspberryPi3
            ({ lib, ... }: {
              # Temporary credentials for first boot only.
              # nostrix-setup will replace these with your SSH key.
              users.users.root.password = "nostrix";
              services.openssh.settings.PasswordAuthentication =
                lib.mkForce true;

              services.nginx.enable = true;
              networking.firewall.allowedTCPPorts = [ 80 ];

              system.stateVersion = "25.11";
            })
          ];
        };

        images.raspberryPiZero2W = self.lib.mkImage {
          hostname = "nostrix";
          modules  = [
            self.hardware.raspberryPiZero2W
            ({ lib, ... }: {
              # Temporary credentials for first boot only.
              # nostrix-setup will replace these with your SSH key.
              users.users.root.password = "nostrix";
              services.openssh.settings.PasswordAuthentication =
                lib.mkForce true;

              services.nginx.enable = true;
              networking.firewall.allowedTCPPorts = [ 80 ];

              system.stateVersion = "25.11";
            })
          ];
        };

        # Example configuration — verifies the module evaluates cleanly.
        # nix eval .#nixosConfigurations.example.config.networking.hostName
        nixosConfigurations.example = self.lib.mkSystem {
          hostname = "nostrix";
          system   = "x86_64-linux";
          modules  = [
            self.hardware.genericX86_64
            { services.nginx.enable = true;
              networking.firewall.allowedTCPPorts = [ 80 ]; }
          ];
        };
      };

      # Integration test: boots the base nostrix stack and verifies
      # nginx serves content, SSH is hardened, and the hostname is correct.
      # Run with: nix build .#checks.x86_64-linux.integration
      integrationTest = nixpkgs.legacyPackages.x86_64-linux.testers.nixosTest {
        name = "nostrix-base";

        nodes.machine = { lib, pkgs, ... }: {
          imports = [
            ./modules/base.nix
            ./modules/mdns.nix
          ];

          services.nginx = {
            enable = true;
            virtualHosts.default = {
              root = pkgs.writeTextDir "index.html" "Hello from Nostrix!";
              listen = [{ addr = "0.0.0.0"; port = 8080; }];
            };
          };
          networking.firewall.allowedTCPPorts = [ 8080 ];

          networking.hostName = "nostrix-test";

          # Auto-upgrade needs a real flake at /etc/nixos — disable in tests.
          system.autoUpgrade.enable = lib.mkForce false;

          system.stateVersion = "24.05";
        };

        testScript = ''
          machine.start()
          machine.wait_for_unit("multi-user.target")

          machine.wait_for_unit("nginx.service")
          machine.wait_for_open_port(8080)

          # nginx serves the configured content
          response = machine.succeed("curl -sf http://localhost:8080")
          assert "Hello from Nostrix!" in response, f"Unexpected response: {response}"

          # SSH password authentication is disabled
          machine.succeed("sshd -T | grep -i 'passwordauthentication no'")

          # hostname is correct
          hostname = machine.succeed("hostname").strip()
          assert hostname == "nostrix-test", f"Unexpected hostname: {hostname}"
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

          # nix run github:nostra-domus/nostrix -- (runs nostrix-setup)
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
