# Hardware profile for the Raspberry Pi 4 (B).
#
# Uses the BCM2711 SoC (aarch64), extlinux boot, and an SD card root.
# Label the SD card NIXOS_SD when flashing.
{ lib, ... }:
{
  nixpkgs.hostPlatform = lib.mkDefault "aarch64-linux";

  hardware.enableRedistributableFirmware = true;

  boot = {
    # Use extlinux (U-Boot) — no GRUB on the Pi.
    loader.grub.enable                        = false;
    loader.generic-extlinux-compatible.enable = true;

    initrd.availableKernelModules =
      [ "usbhid" "usb_storage" "vc4" "bcm2835_dma" "i2c_bcm2835" ];
  };

  fileSystems."/" = {
    device  = "/dev/disk/by-label/NIXOS_SD";
    fsType  = "ext4";
    options = [ "noatime" ];
  };
}
