# Top-level Nostrix NixOS module.
# Composes the domus application module with the opinionated base config.
{ domus, ... }:
{
  imports = [
    domus.nixosModules.default
    ./base.nix
    ./mdns.nix
  ];
}
