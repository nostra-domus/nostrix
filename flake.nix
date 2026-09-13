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
        nixosModules = {
          default     = ./modules/default.nix;
          # Opt-in add-ons — not part of `default`, but exposed so a
          # generated flake.nix (an external consumer of this input, same
          # as demo/flake.nix) can reference them by name instead of a
          # bare path into this repo.
          web         = ./modules/web.nix;
          cloudflared = ./modules/cloudflared.nix;
          apPortal    = ./modules/ap-portal.nix;
        };

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
        # First boot: either open http://nostrix.local:8080 from a phone/laptop
        # on the same network (Basic Auth: root / nostrix) and fill in the
        # bootstrap setup form, or SSH in as root (password: nostrix) via
        # nostrix.local and run `nostrix-setup`. Both paths converge on the
        # same generated flake.nix. If no ethernet cable is connected, the
        # device instead broadcasts its own setup hotspot (SSID
        # `nostrix-setup-nostrix`, same Basic Auth credential) so the same
        # bootstrap form can be reached with no LAN at all.
        images.raspberryPi3 = self.lib.mkImage {
          hostname = "nostrix";
          modules  = [
            self.hardware.raspberryPi3
            self.nixosModules.web
            self.nixosModules.apPortal
            ({ lib, ... }: {
              # Temporary credentials for first boot only.
              # nostrix-setup will replace these with your SSH key.
              users.users.root.password = "nostrix";
              services.openssh.settings.PasswordAuthentication =
                lib.mkForce true;
              # base.nix sets PermitRootLogin = "prohibit-password", which
              # blocks root password auth even when PasswordAuthentication
              # is true above — override it too so the temporary password
              # actually works for first-boot SSH access.
              services.openssh.settings.PermitRootLogin =
                lib.mkForce "yes";

              services.nginx.enable = true;
              networking.firewall.allowedTCPPorts = [ 80 ];
              services.nostrix-web.hardware = "raspberryPi3";

              system.stateVersion = "25.11";
            })
          ];
        };

        images.raspberryPiZero2W = self.lib.mkImage {
          hostname = "nostrix";
          modules  = [
            self.hardware.raspberryPiZero2W
            self.nixosModules.web
            self.nixosModules.apPortal
            ({ lib, ... }: {
              # Temporary credentials for first boot only.
              # nostrix-setup will replace these with your SSH key.
              users.users.root.password = "nostrix";
              services.openssh.settings.PasswordAuthentication =
                lib.mkForce true;
              services.openssh.settings.PermitRootLogin =
                lib.mkForce "yes";

              services.nginx.enable = true;
              networking.firewall.allowedTCPPorts = [ 80 ];
              services.nostrix-web.hardware = "raspberryPiZero2W";

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

      # AP-portal integration test: with no ethernet link and mac80211_hwsim
      # simulating wlan0/wlan1, verifies the access point actually comes up
      # (hostapd + dnsmasq), that a WiFi client can associate and get a DHCP
      # lease, and that the bootstrap web UI is reachable over it, including
      # a captive-portal probe path redirect. Complements, but doesn't
      # replace, real-hardware verification — a VM can't confirm phone OSes
      # actually pop their captive-portal prompt for this AP.
      # Run with: nix build .#checks.x86_64-linux.apPortal
      apPortalTest = nixpkgs.legacyPackages.x86_64-linux.testers.nixosTest {
        name = "nostrix-ap-portal";

        nodes.machine = { lib, ... }: {
          imports = [
            ./modules/base.nix
            ./modules/web.nix
            ./modules/ap-portal.nix
          ];

          # modules/web.nix takes `self` as a module argument (it calls
          # self.lib.mkSetupPackage); testers.nixosTest's legacy interface
          # doesn't support specialArgs (only the newer runTest does), so
          # supply it the ordinary way instead.
          _module.args.self = self;

          # mac80211_hwsim gives this VM a pair of simulated radios
          # (wlan0/wlan1) that can see each other's transmissions — the same
          # pattern nixpkgs' own nixos/tests/wpa_supplicant.nix uses. wlan0 is
          # what ap-portal.nix configures as the AP; wlan1 here plays the
          # part of a phone connecting to it.
          boot.kernelModules = [ "mac80211_hwsim" ];

          # qemu-vm.nix always adds one NIC (named eth0) for the VM's own
          # internet access, independent of the test driver's own vlans —
          # remove it so this node genuinely has no ethernet device at all,
          # the same as a Pi with nothing plugged into its ethernet port,
          # so ap-portal.nix's carrier check sees "no ethernet" for real.
          virtualisation.qemu.networkingOptions = lib.mkForce [ ];

          networking.hostName = "nostrix-ap-test";

          networking.wireless = {
            enable         = lib.mkOverride 0 true; # qemu-vm.nix force-disables wifi by default
            userControlled = true;
            interfaces     = [ "wlan1" ];
            # ap-portal.nix uses authentication.mode = "wpa2-sha1" (plain
            # classic WPA2-PSK), which is the default authProtocols here —
            # no override needed.
            networks."nostrix-setup-nostrix-ap-test".psk = "nostrix-setup";
          };

          system.autoUpgrade.enable = lib.mkForce false;
          system.stateVersion = "24.05";
        };

        testScript = ''
          machine.start()
          machine.wait_for_unit("multi-user.target")

          machine.wait_for_unit("nostrix-ap-mode.service")
          machine.wait_for_unit("hostapd.service")
          machine.wait_for_unit("dnsmasq.service")

          addr = machine.succeed("ip -4 -o addr show wlan0")
          assert "10.42.0.1/24" in addr, f"wlan0 did not get the AP address: {addr}"

          machine.wait_for_unit("wpa_supplicant-wlan1.service")
          machine.wait_until_succeeds(
            "wpa_cli -i wlan1 status | grep -q wpa_state=COMPLETED"
          )

          # dhcpcd runs as a single long-lived daemon; "dhcpcd -1" from the
          # test just relays an async request to it over its control socket
          # rather than blocking for the lease itself, so poll instead.
          machine.succeed("dhcpcd -1 wlan1")
          machine.wait_until_succeeds("ip -4 -o addr show wlan1 | grep -q inet")
          lease = machine.succeed("ip -4 -o addr show wlan1")
          assert "10.42.0." in lease, f"wlan1 did not get a DHCP lease from dnsmasq: {lease}"

          machine.wait_for_open_port(8080)
          machine.succeed(
            "curl -sf -u root:nostrix http://10.42.0.1:8080/ | grep -q 'Set up this Nostrix device'"
          )

          code = machine.succeed(
            "curl -s -o /dev/null -w '%{http_code}' -u root:nostrix http://10.42.0.1/generate_204"
          )
          assert code.strip() == "302", f"captive-portal probe path did not redirect: {code}"
        '';
      };

      # Per-system outputs: the setup wizard package and app.
      perSystemOutputs = flake-utils.lib.eachDefaultSystem (system:
        let
          pkgs  = nixpkgs.legacyPackages.${system};
          setup = self.lib.mkSetupPackage { inherit pkgs; };
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
      checks.x86_64-linux.apPortal    = apPortalTest;
    };
}
