# Hardware profile for generic x86_64 machines (PC, VM, VPS).
#
# Uses systemd-boot + EFI. Expects two labelled partitions:
#   nixos — root ext4 filesystem
#   boot  — EFI system partition (vfat)
{ lib, ... }:
{
  nixpkgs.hostPlatform = lib.mkDefault "x86_64-linux";

  boot = {
    initrd.availableKernelModules = [
      "ahci" "xhci_pci" "virtio_pci" "virtio_blk" "sd_mod"
    ];
    loader.systemd-boot.enable      = true;
    loader.efi.canTouchEfiVariables = true;
  };

  fileSystems."/" = {
    device = "/dev/disk/by-label/nixos";
    fsType = "ext4";
  };

  fileSystems."/boot" = {
    device = "/dev/disk/by-label/boot";
    fsType = "vfat";
  };
}
