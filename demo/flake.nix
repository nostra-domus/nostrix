# Demo flake — shows how to use nostrix with a simple application.
#
# This example serves a static HTML page over nginx on port 80.
# Replace the inline module with your own application's nixosModule.
#
# Evaluate:  nix eval .#nixosConfigurations.demo.config.networking.hostName
# Build VM:  nix build .#nixosConfigurations.demo.config.system.build.vm
{
  inputs = {
    nostrix.url = "github:nostra-domus/nostrix";
  };

  outputs = { nostrix, ... }: {
    nixosConfigurations.demo = nostrix.lib.mkSystem {
      hostname = "demo";
      sshKeys  = [
        # Replace with your own public key.
        # "ssh-ed25519 AAAA..."
      ];
      modules = [
        # Pick a hardware profile, or supply your own.
        nostrix.hardware.genericX86_64

        # Application module — inline here, but normally imported from a
        # separate flake:  myapp.nixosModules.default
        ({ pkgs, ... }: {
          services.nginx = {
            enable = true;
            virtualHosts.default = {
              root = pkgs.writeTextDir "index.html" ''
                <!DOCTYPE html>
                <html><body><h1>Hello from Nostrix!</h1></body></html>
              '';
            };
          };
          networking.firewall.allowedTCPPorts = [ 80 ];
        })
      ];
    };
  };
}
