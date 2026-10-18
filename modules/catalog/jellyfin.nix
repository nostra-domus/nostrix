# Jellyfin media server.
{ config, lib, ... }:
let
  cfg = config.nostrix.catalog.jellyfin;
  port = 8096;
in
{
  options.nostrix.catalog.jellyfin = {
    enable = lib.mkEnableOption "Jellyfin media server";
    expose = lib.mkOption {
      type        = lib.types.enum [ "none" "lan" "cloudflare" ];
      default     = "none";
      description = "Who can reach Jellyfin: only this device, the local network, or the internet behind Cloudflare Access.";
    };
    mediaDir = lib.mkOption {
      type        = lib.types.path;
      default     = "/srv/media";
      description = "Folder holding your media. Created if missing, writable by Jellyfin.";
    };
  };

  config = lib.mkIf cfg.enable {
    services.jellyfin.enable = true;
    nostrix.apps.jellyfin = { inherit port; inherit (cfg) expose; };
    systemd.tmpfiles.rules = [ "d ${cfg.mediaDir} 0775 jellyfin jellyfin -" ];
  };
}
