# Curated apps: `nostrix.catalog.<app>`.
#
# Each entry wraps a nixpkgs service in Nostrix's app contract
# (modules/apps.nix), so a user can switch an app on from the web UI and pick
# who can reach it without writing Nix. The modules are always imported; they
# only declare options and do nothing until `enable = true`.
#
# The web UI draws its forms from cmd/nostrix-setup/catalog.json. Every field
# named there must exist as an option here — the `catalogContract` check in
# flake.nix enforces it.
{ ... }:
{
  imports = [
    ./jellyfin.nix
    ./vaultwarden.nix
    ./uptime-kuma.nix
  ];
}
