# The contract between an app and Nostrix: `nostrix.apps.<name>`.
#
# An app (or a thin adapter for it) does not touch the firewall or
# Cloudflare itself; it only *declares* what it is, and Nostrix does the
# adapting:
#
#   nostrix.apps.jellyfin = {
#     port   = 8096;
#     expose = "lan";        # "none" (default) | "lan" | "cloudflare"
#   };
#
#   none        — nothing is exposed (the default: an app that says nothing
#                 about exposure is reachable from nowhere but the device).
#   lan         — the port is opened in the firewall.
#   cloudflare  — reachable only through the device's Cloudflare Tunnel,
#                 behind Cloudflare Access, on its own hostname. The origin
#                 is 127.0.0.1:<port>, so no firewall port is opened. The
#                 tunnel/DNS/Access side is not Nix: `nostrix-setup`
#                 reconciles it with this declaration.
#
# Anyone can set these: the app's own `nixosModules.nostrix`, an adapter
# shipped with Nostrix, or the user's /etc/nixos/local.nix. They are plain
# NixOS options, so all of them merge and override the normal way.
{ config, lib, ... }:
let
  cfg = config.nostrix.apps;
  openPorts = lib.concatMap
    (a: lib.optional (a.expose == "lan" && a.port != null) a.port)
    (lib.attrValues cfg);
in
{
  options.nostrix.apps = lib.mkOption {
    default     = { };
    description = "Apps running on this device, and how Nostrix should expose each.";
    type = lib.types.attrsOf (lib.types.submodule {
      options = {
        port = lib.mkOption {
          type        = lib.types.nullOr lib.types.port;
          default     = null;
          description = "TCP port the app listens on. Required unless `expose` is \"none\".";
        };
        expose = lib.mkOption {
          type        = lib.types.enum [ "none" "lan" "cloudflare" ];
          default     = "none";
          description = "Who can reach the app: nobody beyond the device itself, the local network, or the internet through Cloudflare Access.";
        };
      };
    });
  };

  config = {
    assertions = lib.mapAttrsToList (name: a: {
      assertion = a.expose == "none" || a.port != null;
      message   = "nostrix.apps.${name}: expose = \"${a.expose}\" needs a port.";
    }) cfg;

    networking.firewall.allowedTCPPorts = openPorts;

    # What this system declares, for `nostrix-setup expose` and the web UI to
    # read from the *running* system — no Nix evaluation on the device. It
    # is what the Cloudflare side is reconciled against.
    environment.etc."nostrix/apps.json".text =
      builtins.toJSON (lib.mapAttrs (_: a: { inherit (a) port expose; }) cfg);
  };
}
