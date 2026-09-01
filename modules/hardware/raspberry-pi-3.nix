# Hardware profile for the Raspberry Pi 3 (A+, B, B+).
#
# Uses the BCM2837 SoC (aarch64), extlinux boot, and an SD card root.
# Label the SD card NIXOS_SD when flashing.
#
# 1GB RAM isn't enough to evaluate/build a nixos-unstable closure without
# swap — nixos-rebuild gets OOM-killed (SIGKILL) otherwise.
{ lib, ... }:
{
  nixpkgs.hostPlatform = lib.mkDefault "aarch64-linux";

  hardware.enableRedistributableFirmware = true;

  boot = {
    # Use extlinux (U-Boot) — no GRUB on the Pi.
    loader.grub.enable                        = false;
    loader.generic-extlinux-compatible.enable = true;

    initrd.availableKernelModules = [ "usbhid" "usb_storage" ];
  };

  fileSystems."/" = {
    device  = "/dev/disk/by-label/NIXOS_SD";
    fsType  = "ext4";
    options = [ "noatime" ];
  };

  # zram swap gives nixos-rebuild enough headroom to evaluate/build without
  # being OOM-killed.
  zramSwap.enable = true;

  # Build locally with a single job to avoid OOM.
  # Remote builds (via nix.buildMachines) ignore this.
  nix.settings.max-jobs = 1;
}
