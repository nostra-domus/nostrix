# Top-level Nostrix NixOS module.
# Composes the opinionated base config with mDNS.
# Application modules are added by the caller via mkSystem's modules argument.
{ ... }:
{
  imports = [
    ./base.nix
    ./mdns.nix
  ];
}
