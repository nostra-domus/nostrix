# Vaultwarden, a Bitwarden-compatible password manager.
{ config, lib, ... }:
let
  cfg = config.nostrix.catalog.vaultwarden;
  port = 8222;
in
{
  options.nostrix.catalog.vaultwarden = {
    enable = lib.mkEnableOption "Vaultwarden password manager";
    expose = lib.mkOption {
      # No "lan": Bitwarden clients and browsers need HTTPS, which only the
      # Cloudflare address provides. Over plain HTTP the web vault won't work.
      type        = lib.types.enum [ "none" "cloudflare" ];
      default     = "none";
      description = "Who can reach Vaultwarden: only this device, or the internet behind Cloudflare Access (HTTPS).";
    };
    domain = lib.mkOption {
      type        = lib.types.nullOr lib.types.str;
      default     = null;
      example     = "myserver-vaultwarden.example.com";
      description = "The public address Vaultwarden is reached at, without https://.";
    };
    signupsAllowed = lib.mkOption {
      type        = lib.types.bool;
      default     = true;
      description = "Let anyone who can reach the page create an account. Turn off once your own account exists.";
    };
  };

  config = lib.mkIf cfg.enable {
    services.vaultwarden = {
      enable = true;
      inherit (cfg) domain;
      config = {
        ROCKET_ADDRESS   = "127.0.0.1";
        ROCKET_PORT      = port;
        SIGNUPS_ALLOWED  = cfg.signupsAllowed;
      };
    };
    nostrix.apps.vaultwarden = { inherit port; inherit (cfg) expose; };
  };
}
