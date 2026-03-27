# Nostrix

Opinionated NixOS base for self-hosted servers. Provides SSH hardening, a
firewall, weekly auto-upgrades, mDNS (`hostname.local`), and hardware profiles
for the Raspberry Pi 3, Pi Zero 2W, and generic x86_64.

Nostrix is application-agnostic — you bring your own app and plug it in as a
NixOS module.

## Building a custom image from your own repo

The typical workflow is:

1. Your app repo defines a NixOS module that installs and runs your app.
2. Your repo's `flake.nix` takes `nostrix` as an input and calls
   `nostrix.lib.mkImage` with your hardware profile and module.
3. You run `nix build` in your repo to produce a flashable SD card image.

### Example: a Go app

Suppose your repo has a Go web server at `cmd/myapp/main.go`. A minimal
`flake.nix` that builds it into a Raspberry Pi 3 image:

```nix
{
  inputs = {
    nixpkgs.url  = "github:NixOS/nixpkgs/nixos-unstable";
    nostrix.url  = "github:nostra-domus/nostrix";
    nostrix.inputs.nixpkgs.follows = "nixpkgs";
  };

  outputs = { self, nixpkgs, nostrix }:
  let
    # Build the Go binary.
    myapp = nixpkgs.legacyPackages.aarch64-linux.buildGoModule {
      pname   = "myapp";
      version = "0.1.0";
      src     = ./.;
      subPackages = [ "cmd/myapp" ];
      vendorHash = null;  # replace with the real hash if you have dependencies
    };

    # NixOS module that installs and runs the app as a systemd service.
    myappModule = { ... }: {
      systemd.services.myapp = {
        description = "My Go app";
        wantedBy    = [ "multi-user.target" ];
        after       = [ "network.target" ];
        serviceConfig = {
          ExecStart      = "${myapp}/bin/myapp";
          Restart        = "on-failure";
          DynamicUser    = true;
        };
      };

      # Open whichever port your app listens on.
      networking.firewall.allowedTCPPorts = [ 8080 ];
    };
  in
  {
    # nix build → ./result/sd-image/*.img.zst
    packages.aarch64-linux.image = nostrix.lib.mkImage {
      hostname = "myserver";
      sshKeys  = [ "ssh-ed25519 AAAA..." ];  # your public key
      modules  = [
        nostrix.hardware.raspberryPi3
        myappModule
        { system.stateVersion = "25.11"; }
      ];
    };
  };
}
```

Build and flash:

```bash
nix build .#packages.aarch64-linux.image
zstd -d result/sd-image/*.img.zst --stdout | sudo dd of=/dev/rdiskX bs=4m
```

The Pi boots with your app running, SSH open on port 22 (key-only), and the
system set to upgrade itself weekly.

### Splitting the NixOS module into its own file

For larger apps it's cleaner to keep the module separate and export it from
the flake so other people can import it:

```
your-repo/
  cmd/myapp/main.go
  nix/module.nix        ← NixOS service definition
  flake.nix
```

`nix/module.nix`:
```nix
{ config, lib, pkgs, ... }:
let
  myapp = pkgs.buildGoModule {
    pname   = "myapp";
    version = "0.1.0";
    src     = ./..;
    subPackages = [ "cmd/myapp" ];
    vendorHash = null;
  };
in
{
  systemd.services.myapp = {
    description = "My Go app";
    wantedBy    = [ "multi-user.target" ];
    after       = [ "network.target" ];
    serviceConfig = {
      ExecStart   = "${myapp}/bin/myapp";
      Restart     = "on-failure";
      DynamicUser = true;
    };
  };

  networking.firewall.allowedTCPPorts = [ 8080 ];
}
```

`flake.nix`:
```nix
{
  inputs = {
    nixpkgs.url = "github:NixOS/nixpkgs/nixos-unstable";
    nostrix.url = "github:nostra-domus/nostrix";
    nostrix.inputs.nixpkgs.follows = "nixpkgs";
  };

  outputs = { self, nixpkgs, nostrix }: {
    # Export the module so others can import it.
    nixosModules.default = ./nix/module.nix;

    # SD card image for the Raspberry Pi 3.
    packages.aarch64-linux.image = nostrix.lib.mkImage {
      hostname = "myserver";
      sshKeys  = [ "ssh-ed25519 AAAA..." ];
      modules  = [
        nostrix.hardware.raspberryPi3
        self.nixosModules.default
        { system.stateVersion = "25.11"; }
      ];
    };
  };
}
```

### Hardware profiles

| Attribute | Board |
|---|---|
| `nostrix.hardware.raspberryPi3` | Raspberry Pi 3 (A+, B, B+) |
| `nostrix.hardware.raspberryPiZero2W` | Raspberry Pi Zero 2W |
| `nostrix.hardware.genericX86_64` | PC, VM, or VPS (systemd-boot + EFI) |

### `lib.mkImage` and `lib.mkSystem`

`lib.mkImage` builds a compressed SD card image (`.img.zst`). It accepts the
same arguments as `lib.mkSystem` and defaults to `aarch64-linux`.

`lib.mkSystem` builds a NixOS configuration (useful for VMs, VPS, or
`nixos-rebuild`).

Both accept:

| Argument | Required | Description |
|---|---|---|
| `hostname` | yes | Machine hostname; reachable as `hostname.local` via mDNS |
| `modules` | yes | List of NixOS modules (hardware profile, app, config) |
| `sshKeys` | no | List of SSH public key strings for root login |
| `system` | no | Nix system string; defaults to `"aarch64-linux"` |
