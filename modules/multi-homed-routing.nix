# Keeps reply traffic routed back out the same interface a connection
# came in on, even when two interfaces end up on the same subnet at once.
#
# Nostrix's AP-portal setup flow (ap-portal.nix) is designed around one
# active network path at a time, but nothing stops a real device from
# having both ethernet and WiFi connected simultaneously afterward —
# plugging ethernet back in for maintenance, say, once WiFi is already
# configured. Confirmed on real hardware: with both eth0 and wlan0 DHCP'd
# onto the same subnet, Linux's normal (destination-only) routing sends
# ALL reply traffic for that subnet out whichever interface has the lower
# route metric, regardless of which interface a connection actually
# arrived on — silently breaking SSH/ping to the other interface's
# address, since the client sees a reply from a different source address
# than it dialed and drops it as invalid.
#
# The general fix for a multi-homed host on one L2/L3 segment is policy
# routing by source address: give each interface its own routing table
# (its own directly-connected subnet + default route), and add an `ip
# rule` sending any packet whose *source* address is that interface's
# address through that table instead of falling through to the normal
# (destination-only) main table. A dhcpcd exit-hook is the natural place
# to set this up and tear it down, since it already runs on every lease
# change with exactly the addressing details needed — and, since dhcpcd's
# own PATH doesn't include iproute2, `ip` is referenced by its store path
# rather than relying on that PATH.
{ pkgs, ... }:
{
  networking.dhcpcd.runHook = ''
    ip=${pkgs.iproute2}/bin/ip

    case "$reason" in
      BOUND|RENEW|REBIND|REBOOT|STATIC)
        if [ -n "$new_ip_address" ]; then
          # Table IDs derived from the interface's ifindex keep them
          # unique without needing a registry; the offset stays well
          # clear of the reserved 253-255 range (default/main/local).
          table=$((20000 + $(cat "/sys/class/net/$interface/ifindex" 2>/dev/null || echo 0)))

          "$ip" route flush table "$table" 2>/dev/null
          if [ -n "$new_network_number" ] && [ -n "$new_subnet_cidr" ]; then
            "$ip" route add "$new_network_number/$new_subnet_cidr" dev "$interface" src "$new_ip_address" table "$table" 2>/dev/null
          fi
          for gw in $new_routers; do
            "$ip" route add default via "$gw" dev "$interface" table "$table" 2>/dev/null
            break
          done

          [ -n "$old_ip_address" ] && [ "$old_ip_address" != "$new_ip_address" ] && \
            "$ip" rule del from "$old_ip_address" priority 100 2>/dev/null
          "$ip" rule del from "$new_ip_address" priority 100 2>/dev/null
          "$ip" rule add from "$new_ip_address" table "$table" priority 100 2>/dev/null
        fi
        ;;
      EXPIRE|RELEASE|STOP|DEPARTED|NOCARRIER|FAIL)
        # Deliberately matched by (from, priority) rather than table here:
        # on DEPARTED the interface (and so /sys/class/net/$interface) is
        # already gone, so the table number used at add-time can't be
        # recomputed — but priority 100 is fixed for every rule this hook
        # ever adds, so it's still a reliable, sufficient key for removal.
        [ -n "$old_ip_address" ] && "$ip" rule del from "$old_ip_address" priority 100 2>/dev/null
        ;;
    esac
  '';
}
