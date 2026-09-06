# Cloudflare Tunnel — the only path to modules/web.nix's UI, which binds to
# localhost only once configured. Opt-in: not imported by modules/default.nix,
# since not every install wants public exposure and this module is inert
# without a tunnel token. Add it explicitly via mkSystem's `modules` list.
#
# services.nostrix-cloudflared.tunnelToken is a Cloudflare Tunnel connector
# token for a remotely-managed tunnel (created via the Cloudflare API —
# see cmd/nostrix-setup/cloudflare.go), rather than a locally-managed
# tunnel's config.yml + credentials JSON. The wizard/web UI's bootstrap flow
# (modules/web.nix) provisions the tunnel and writes this value into the
# generated flake automatically; same trust level as the SSH-key/nginx
# values already handled the same way.
{ config, pkgs, lib, ... }:
let
  cfg = config.services.nostrix-cloudflared;
in
{
  options.services.nostrix-cloudflared = {
    tunnelToken = lib.mkOption {
      type        = lib.types.str;
      description = ''
        Cloudflare Tunnel connector token for this device's tunnel
        (from `cloudflared tunnel token`, or the Cloudflare API's
        cfd_tunnel/token endpoint).
      '';
    };
  };

  config = {
    systemd.services.nostrix-cloudflared = {
      description = "Cloudflare Tunnel for the Nostrix web UI";
      wantedBy    = [ "multi-user.target" ];
      after       = [ "network.target" ];
      serviceConfig = {
        ExecStart = "${pkgs.cloudflared}/bin/cloudflared tunnel run --token ${lib.escapeShellArg cfg.tunnelToken}";
        Restart   = "on-failure";
      };
    };
  };
}
