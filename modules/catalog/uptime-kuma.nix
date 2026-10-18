# Uptime Kuma, a self-hosted status monitor.
{ config, lib, ... }:
let
  cfg = config.nostrix.catalog.uptime-kuma;
  port = 3001;
in
{
  options.nostrix.catalog.uptime-kuma = {
    enable = lib.mkEnableOption "Uptime Kuma status monitor";
    expose = lib.mkOption {
      type        = lib.types.enum [ "none" "lan" "cloudflare" ];
      default     = "none";
      description = "Who can reach Uptime Kuma: only this device, the local network, or the internet behind Cloudflare Access.";
    };
  };

  config = lib.mkIf cfg.enable {
    services.uptime-kuma = {
      enable = true;
      settings = {
        PORT = toString port;
        # Only the local network needs it to listen beyond loopback; the
        # Cloudflare tunnel connects to 127.0.0.1.
        HOST = if cfg.expose == "lan" then "0.0.0.0" else "127.0.0.1";
      };
    };
    nostrix.apps.uptime-kuma = { inherit port; inherit (cfg) expose; };
  };
}
