# Nostrix web configuration UI.
#
# Runs `nostrix-setup serve` as a systemd service, exposing the same
# setup and app-management functionality as the CLI wizard through a
# small HTTP server. There is no login — access control is entirely the
# firewall rule below, which restricts the port to RFC1918 private
# network sources instead of using allowedTCPPorts (which would accept
# connections from anywhere). Treat this the same as SSH: anyone on the
# LAN is trusted.
{ config, pkgs, lib, self, ... }:
let
  nostrixSetup = self.lib.mkSetupPackage { inherit pkgs; };
  port = 8080;
in
{
  systemd.services.nostrix-web = {
    description = "Nostrix web configuration UI";
    wantedBy    = [ "multi-user.target" ];
    after       = [ "network.target" ];
    serviceConfig = {
      ExecStart = "${nostrixSetup}/bin/nostrix-setup serve --addr 0.0.0.0:${toString port}";
      Restart   = "on-failure";
    };
  };

  networking.firewall.extraCommands = ''
    iptables -A nixos-fw -p tcp --dport ${toString port} -s 10.0.0.0/8     -j nixos-fw-accept
    iptables -A nixos-fw -p tcp --dport ${toString port} -s 172.16.0.0/12  -j nixos-fw-accept
    iptables -A nixos-fw -p tcp --dport ${toString port} -s 192.168.0.0/16 -j nixos-fw-accept
  '';
}
