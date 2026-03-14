# Hardware profile for the Raspberry Pi Zero 2W.
#
# The Pi Zero 2W uses the BCM2837B0 SoC (identical to Pi 3) in the Zero
# form factor. It has 512MB RAM, which requires conservative Nix settings.
#
# To flash NixOS: use nixos-generators or the official sd-image generator:
#   nix build .#nixosConfigurations.<name>.config.system.build.sdImage
{ lib, ... }:
{
  nixpkgs.hostPlatform = lib.mkDefault "aarch64-linux";

  hardware.enableRedistributableFirmware = true;

  boot = {
    # Use extlinux (U-Boot) — no GRUB on the Pi.
    loader.grub.enable                         = false;
    loader.generic-extlinux-compatible.enable  = true;

    initrd.availableKernelModules = [ "usbhid" "usb_storage" ];
  };

  # SD card filesystem — label the card NIXOS_SD when flashing.
  fileSystems."/" = {
    device  = "/dev/disk/by-label/NIXOS_SD";
    fsType  = "ext4";
    options = [ "noatime" ];
  };

  # zram swap compensates for the 512MB RAM limit.
  zramSwap.enable = true;

  # Build locally with a single job to avoid OOM.
  # Remote builds (via nix.buildMachines) ignore this.
  nix.settings.max-jobs = 1;
}
