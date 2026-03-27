# Hardware profile for the Raspberry Pi 3 (A+, B, B+).
#
# Uses the BCM2837 SoC (aarch64), extlinux boot, and an SD card root.
# Label the SD card NIXOS_SD when flashing.
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
}
