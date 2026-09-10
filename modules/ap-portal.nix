# Nostrix setup access point.
#
# Only ever imported by the temporary SD-image shape (flake.nix's
# images.raspberryPi3 / images.raspberryPiZero2W), the same way the temporary
# root password / forced PasswordAuthentication blocks in those images work —
# never by nostrix-setup's generated flake. That means no explicit teardown
# logic is needed: once nixos-rebuild switch activates the real generated
# config (which doesn't import this module), NixOS's normal activation stops
# hostapd/dnsmasq on its own, the same way the bootstrap -> configured
# transition in modules/web.nix works.
#
# Hardcodes wlan0/eth0: both Pi 3 and Pi Zero 2W expose their onboard
# radio/NIC under these names, and only those two images import this module.
#
# Trigger: nostrix-ap-mode.service starts hostapd/dnsmasq only if eth0 has no
# carrier at boot — a plugged-in cable keeps today's LAN bootstrap mode
# (modules/web.nix binding 0.0.0.0:8080) unchanged. hostapd/dnsmasq's own
# default autostart is disabled (wantedBy = mkForce []) so this service can
# gate them.
{ config, lib, pkgs, ... }:
let
  wifiIf = "wlan0";
  ethIf  = "eth0";
  apIP   = "10.42.0.1";
in
{
  networking.interfaces.${wifiIf}.ipv4.addresses =
    [ { address = apIP; prefixLength = 24; } ];

  services.hostapd = {
    enable = true;
    radios.${wifiIf} = {
      band = "2g";
      networks.${wifiIf} = {
        # Fixed SSID/passphrase — same trust level already accepted for the
        # temporary first-boot root SSH password and bootstrap Basic Auth
        # credential (see modules/web.nix). Not worth randomizing for a
        # single-user Pi with no display to surface a random value on.
        ssid = "nostrix-setup-${config.networking.hostName}";
        authentication = {
          mode            = "wpa2-sha256";
          wpaPasswordFile = pkgs.writeText "nostrix-ap-psk" "nostrix-setup";
        };
      };
    };
  };

  services.dnsmasq = {
    enable = true;
    settings = {
      interface       = wifiIf;
      bind-interfaces = true;
      dhcp-range      = [ "10.42.0.10,10.42.0.100,24h" ];
      # Wildcard DNS: every hostname resolves to the gateway, including the
      # OS-specific captive-portal probe domains (connectivitycheck.gstatic.com,
      # captive.apple.com, ...) — the classic captive-portal DNS hijack.
      address         = [ "/#/${apIP}" ];
      # RFC 8910 captive-portal URI: modern Windows/Android/ChromeOS clients
      # read this straight from the DHCP lease instead of needing to probe.
      dhcp-option     = [ "114,http://${apIP}/" ];
    };
  };

  # Both modules enable+autostart themselves by default; only start them
  # conditionally, via nostrix-ap-mode below.
  systemd.services.hostapd.wantedBy = lib.mkForce [ ];
  systemd.services.dnsmasq.wantedBy = lib.mkForce [ ];

  # Scoped to wlan0 only — plain ethernet LAN bootstrap is unaffected, since
  # nothing opens these ports on eth0.
  networking.firewall.interfaces.${wifiIf} = {
    allowedUDPPorts = [ 53 67 ];
    allowedTCPPorts = [ 53 80 ];
  };

  systemd.services.nostrix-ap-mode = {
    description = "Start the Nostrix setup access point if no ethernet link is present";
    wantedBy    = [ "multi-user.target" ];
    after       = [ "network-pre.target" "sys-subsystem-net-devices-${ethIf}.device" ];
    serviceConfig = {
      Type            = "oneshot";
      RemainAfterExit = true;
    };
    # Retry briefly: right after network-pre.target the NIC may not have
    # finished physical link negotiation yet even with a cable plugged in.
    script = ''
      carrier=0
      for _ in 1 2 3 4 5; do
        if [ "$(cat /sys/class/net/${ethIf}/carrier 2>/dev/null || echo 0)" = "1" ]; then
          carrier=1
          break
        fi
        sleep 1
      done
      if [ "$carrier" = "1" ]; then
        echo "Ethernet link present — staying in LAN bootstrap mode."
      else
        echo "No ethernet link — starting the setup access point."
        systemctl start hostapd.service dnsmasq.service
      fi
    '';
  };
}
