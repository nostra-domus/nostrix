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
      # Fixed channel, not automatic channel selection (the default when
      # unset): the Pi's brcmfmac driver doesn't support the survey-dump
      # scan ACS needs, so hostapd crash-loops forever with "ACS: Unable to
      # collect survey data" on real hardware — confirmed on a real Pi 3.
      # mac80211_hwsim (the VM test's simulated radio) supports survey data
      # fine, which is why this only surfaced on hardware, not in CI.
      channel = 6;
      # brcmfmac43430 (Pi 3 / Zero 2W's onboard chip) rejects the default
      # 802.11n HT capability set — "Driver does not support configured HT
      # capability [SHORT-GI-40]", interface fails to come up at all —
      # confirmed on real hardware. Throughput doesn't matter for a
      # provisioning-only portal, so just run plain 802.11g instead of
      # chasing which HT capability subset each Pi WiFi chip variant
      # actually supports.
      wifi4.enable = false;
      # nixpkgs' hostapd module defaults wifi5 (802.11ac/VHT) to enabled
      # regardless of band, so even this 2.4GHz-only radio config ends up
      # with "ieee80211ac=1" in hostapd.conf — nonsensical for a chip with
      # no 5GHz/VHT hardware at all. Doesn't hurt to keep disabled even
      # though it turned out not to be the actual cause of the key-install
      # failure below.
      wifi5.enable = false;
      networks.${wifiIf} = {
        # Fixed SSID/passphrase — same trust level already accepted for the
        # temporary first-boot root SSH password and bootstrap Basic Auth
        # credential (see modules/web.nix). Not worth randomizing for a
        # single-user Pi with no display to surface a random value on.
        ssid = "nostrix-setup-${config.networking.hostName}";
        authentication = {
          # Plain classic WPA2-PSK. wpa2-sha256 was tried first and hit the
          # same "key setting validation failed" as this mode (see below) —
          # turned out to be a red herring, since both modes set
          # ieee80211w=1 identically; wpa2-sha1 is kept as the more widely
          # compatible baseline regardless.
          mode            = "wpa2-sha1";
          wpaPasswordFile = pkgs.writeText "nostrix-ap-psk" "nostrix-setup";
        };
        # brcmfmac43430's old firmware (Pi 3 / Zero 2W) can't install the
        # IGTK key that Management Frame Protection requires — hostapd's
        # wpa2-sha1/wpa2-sha256 modes both unconditionally set
        # ieee80211w=1 ("optional" MFP), which is enough to make the
        # driver reject key installation outright: "nl80211: kernel
        # reports: key setting validation failed" / "Could not connect to
        # kernel driver", confirmed on real hardware — persisted across
        # both AKM choices and with 802.11n/ac disabled, narrowing it down
        # to this. MFP doesn't matter for a provisioning-only portal.
        settings.ieee80211w = lib.mkForce 0;
      };
    };
  };

  services.dnsmasq = {
    enable = true;
    # Without this, NixOS's dnsmasq module makes dnsmasq the system's own
    # resolver (/etc/resolv.conf -> 127.0.0.1) regardless of the
    # interface/bind-interfaces restriction below, which only limits which
    # network interface accepts requests FROM OTHER hosts — it does nothing
    # to stop this box's own local queries from going through dnsmasq too.
    # That meant the captive-portal wildcard DNS override (address=/#/...)
    # hijacked this device's own internet access system-wide, breaking
    # nixos-rebuild switch's flake fetch (github.com resolved to 10.42.0.1)
    # for as long as the AP was up — confirmed on real hardware, and it's
    # exactly what silently broke the first attempt to apply a config from
    # the AP-only bootstrap flow.
    resolveLocalQueries = false;
    settings = {
      interface       = wifiIf;
      bind-interfaces = true;
      dhcp-range      = [ "10.42.0.10,10.42.0.100,24h" ];
      # Wildcard DNS: every hostname resolves to the gateway, including the
      # OS-specific captive-portal probe domains (connectivitycheck.gstatic.com,
      # captive.apple.com, ...) — the classic captive-portal DNS hijack.
      address         = [ "/#/${apIP}" ];
      # Since dnsmasq 2.86, a domain matched by --address= for one record
      # type (here, A — apIP is IPv4-only) has queries for any OTHER
      # record type forwarded upstream instead of answered locally — see
      # dnsmasq(8)'s --address= section. With no real upstream on this
      # AP-only device, every AAAA lookup for every hostname (since the
      # wildcard matches all of them) hung waiting on a nameserver that
      # never answers — confirmed on real hardware as "the config page
      # takes minutes to reply", and almost certainly also what made
      # Safari's captive-portal browser "take a really long time" earlier
      # this session. --local= restores the old, pre-2.86 behaviour: an
      # immediate NoData reply for any non-A query on a --address=-matched
      # domain, instead of a forward.
      local           = [ "/#/" ];
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
    # Retry: right after network-pre.target the NIC may not have finished
    # physical link negotiation yet even with a cable plugged in. 5 attempts
    # (5s total) wasn't enough on real hardware — the Pi 3's onboard
    # ethernet hangs off an internal USB hub (see raspberry-pi-3.nix), which
    # took longer than that to report carrier after a cold boot, wrongly
    # triggering AP mode with a cable actually connected. 20s is a
    # comfortable margin over that while still bounded.
    script = ''
      carrier=0
      for _ in $(seq 20); do
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
