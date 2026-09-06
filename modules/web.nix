# Nostrix web configuration UI.
#
# Runs `nostrix-setup serve` as a systemd service, exposing the same
# setup and app-management functionality as the CLI wizard through a
# small HTTP server.
#
# Trust model has two phases:
#
#   - Unconfigured (cloudflareTeamDomain/cloudflareAud unset, the state a
#     freshly flashed image boots into): the service binds to the LAN
#     (0.0.0.0) and serves only a bootstrap setup form, protected by HTTP
#     Basic Auth with the same fixed credential as the temporary first-boot
#     SSH root password. This is the same trust level already accepted for
#     that password — a short-lived shared secret, not real authentication
#     — and exists only until the bootstrap form is submitted once. The
#     point of this mode is that first-time setup, including provisioning
#     the Cloudflare Tunnel + Access app itself, can be done from a phone
#     browser on the same network, no SSH required.
#
#   - Configured (once the bootstrap flow — or the CLI wizard — has run):
#     binds to localhost only, reachable exclusively via a Cloudflare
#     Tunnel (modules/cloudflared.nix), gated by Cloudflare Access — every
#     request must carry a JWT that this service verifies itself
#     (cmd/nostrix-setup/auth.go), so a missing or misconfigured Access
#     policy fails closed instead of silently exposing the app. SSH remains
#     the separate, always-available local admin path; a technical user can
#     also reach this UI locally via `ssh -L 8080:localhost:8080 <host>`,
#     which is gated by SSH alone.
#
# The transition between the two happens automatically: submitting the
# bootstrap form (or running the CLI wizard) generates a flake.nix with
# cloudflareTeamDomain/cloudflareAud set and runs `nixos-rebuild switch`,
# which restarts this service under the new, locked-down configuration.
{ config, pkgs, lib, self, ... }:
let
  cfg          = config.services.nostrix-web;
  nostrixSetup = self.lib.mkSetupPackage { inherit pkgs; };
  port         = 8080;
in
{
  options.services.nostrix-web = {
    # No default value would be safe here, but these can't be made
    # Nix-level `mkOption` requireds either — that would break evaluating
    # any system (including the prebuilt SD images in flake.nix) that
    # imports modules/default.nix without setting them, since Nix
    # evaluates the whole config even when only e.g. hostName is queried.
    # Instead these default to empty, and nostrix-setup itself refuses to
    # start without both set (see cmd/nostrix-setup/serve.go) — the
    # service just crash-loops under systemd until configured, which is
    # still fail-closed, just enforced at runtime instead of eval time.
    cloudflareTeamDomain = lib.mkOption {
      type        = lib.types.str;
      default     = "";
      description = ''
        Cloudflare Access team domain (the `<team>` in
        `https://<team>.cloudflareaccess.com`), used to fetch the JWKS
        that verifies Access-issued JWTs. Required at runtime —
        nostrix-web refuses to start without it.
      '';
    };

    cloudflareAud = lib.mkOption {
      type        = lib.types.str;
      default     = "";
      description = ''
        Audience (AUD) tag of the Cloudflare Access application that
        fronts this service. Required at runtime — nostrix-web refuses
        to start without it.
      '';
    };
  };

  config = {
    # Only reachable from the LAN while unconfigured — see the bootstrap-mode
    # note above. Closes automatically once cloudflareTeamDomain/Aud are set,
    # since the service then binds to localhost only regardless.
    networking.firewall.allowedTCPPorts =
      lib.optional (cfg.cloudflareTeamDomain == "") port;

    systemd.services.nostrix-web = {
      description = "Nostrix web configuration UI";
      wantedBy    = [ "multi-user.target" ];
      after       = [ "network.target" ];
      serviceConfig = {
        ExecStart = ''
          ${nostrixSetup}/bin/nostrix-setup serve \
            --addr ${if cfg.cloudflareTeamDomain == "" then "0.0.0.0" else "127.0.0.1"}:${toString port} \
            --cf-team-domain ${lib.escapeShellArg cfg.cloudflareTeamDomain} \
            --cf-aud ${lib.escapeShellArg cfg.cloudflareAud}
        '';
        Restart = "on-failure";
      };
    };
  };
}
