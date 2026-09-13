# Bakes an offline-usable copy of nostrix's own source (plus nixpkgs) onto
# the image, so the very first nixos-rebuild switch can succeed with zero
# network — needed for the true zero-ethernet AP-portal flow, where the
# device has no internet uplink at all until after that very switch
# completes (joining real WiFi is part of what the switch applies).
#
# /var/lib/nostrix/nostrix-src is a real, writable git clone (not a
# read-only environment.etc symlink — git needs to update it in place
# later, and the Nix store is read-only): a single local commit on top of
# this image's exact source, with `origin` pointed at the real repo, so a
# later "check for updates" flow can do a plain `git fetch && git reset
# --hard origin/main` once the device has real connectivity — converging
# back onto normal upstream history and discarding the local-only relock
# commit below along with it, since at that point the device isn't
# offline anymore and doesn't need it.
#
# cmd/nostrix-setup/generate.go references this path via
# `inputs.nostrix.url = "git+file:///var/lib/nostrix/nostrix-src"` instead
# of `github:nostra-domus/nostrix`, but only for devices built from one of
# our own SD images (signalled by services.nostrix-web.hardware being
# set) — a plain `nix run github:nostra-domus/nostrix` on someone's
# existing system has no such path baked in and keeps using the real
# github: input.
{ config, lib, pkgs, self, nixpkgs, ... }:
let
  offlineSrcPath = "/var/lib/nostrix/nostrix-src";

  # Hand-built rather than produced by running `nix flake lock` as part of
  # the derivation below: confirmed by testing that doesn't work inside a
  # sandboxed build — with no /nix/var/nix access there, Nix falls back to
  # a private "chroot store", so any store path a sandboxed `nix flake
  # lock` resolves only exists inside that sandbox, not on the real
  # system it ends up on.
  #
  # narHash comes straight from the nixpkgs input's own locked value —
  # guaranteed to match nixpkgs.outPath's content exactly, since that's
  # what a locked flake input's narHash always is (confirmed: relocking
  # nixpkgs to a `path:` input with the real `nix flake lock
  # --override-input` during prototyping reused this exact same hash).
  offlineNixpkgsLock = pkgs.writeText "nostrix-offline-flake.lock" (builtins.toJSON {
    nodes = {
      nixpkgs = {
        locked = {
          lastModified = 0;
          narHash      = nixpkgs.narHash;
          path         = nixpkgs.outPath;
          type         = "path";
        };
        original = {
          type  = "github";
          owner = "NixOS";
          repo  = "nixpkgs";
          ref   = "nixos-unstable";
        };
      };
      root.inputs.nixpkgs = "nixpkgs";
    };
    root    = "root";
    version = 7;
  });

  offlineNostrixSrc = pkgs.runCommand "nostrix-offline-src"
    {
      nativeBuildInputs = [ pkgs.git ];
    }
    ''
      cp -r ${self} $out
      chmod -R u+w $out
      cp ${offlineNixpkgsLock} $out/flake.lock

      cd $out
      # Belt-and-braces: force git to ignore any ambient global/system
      # config from the build machine (e.g. a personal url.insteadOf
      # rewriting https://github.com/ to an SSH remote), on top of the
      # sandboxed build's own isolated HOME — a device flashed from this
      # image has no SSH key for such a rewritten remote, so the origin
      # URL baked in here must stay plain HTTPS no matter what the build
      # machine's own git is configured to prefer.
      export HOME="$TMPDIR"
      export GIT_CONFIG_GLOBAL=/dev/null
      export GIT_CONFIG_SYSTEM=/dev/null
      git init -q -b main
      git config user.email "image-build@nostrix.invalid"
      git config user.name  "nostrix-image-build"
      git add -A
      git commit -q -m "nostrix ${self.rev or "dirty"}: relock nixpkgs to this image's local copy"
      git remote add origin https://github.com/nostra-domus/nostrix.git
    '';
in
{
  # Runs on every activation (first boot included), but only actually
  # copies once: a device that has since git-pulled real history into
  # this path must never have it silently clobbered back to the image's
  # build-time snapshot on a later switch.
  system.activationScripts.nostrixOfflineSrc = lib.stringAfter [ "var" ] ''
    if [ ! -e "${offlineSrcPath}" ]; then
      ${pkgs.coreutils}/bin/mkdir -p "$(dirname "${offlineSrcPath}")"
      ${pkgs.coreutils}/bin/cp -r "${offlineNostrixSrc}" "${offlineSrcPath}"
      ${pkgs.coreutils}/bin/chmod -R u+w "${offlineSrcPath}"
    fi
  '';
}
