# Hardware profile for the x86_64 QEMU test VM (flake.nix's
# nixosConfigurations.vm, run with `just vm`).
#
# Imports nixpkgs' qemu-vm.nix, which supplies the VM's disk/store mounts and
# direct-kernel boot, and leaves the bootloader alone. That makes it the
# profile a *generated* flake.nix needs in order to be applied inside the VM:
# genericX86_64 would demand an EFI /boot partition the VM doesn't have, and
# dropping qemu-vm.nix would drop the VM's filesystems altogether.
#
# Only for the test VM. Real PCs/VPSes want genericX86_64.
{ lib, modulesPath, ... }:
{
  imports = [ "${modulesPath}/virtualisation/qemu-vm.nix" ];

  nixpkgs.hostPlatform = lib.mkDefault "x86_64-linux";

  # The VM boots its kernel directly (no bootloader on the disk). Without
  # this NixOS defaults to GRUB, and `switch` then fails at the very end
  # trying to install it onto the raw ext4 disk ("will not proceed with
  # blocklists").
  boot.loader.grub.enable = lib.mkDefault false;

  # Keep the serial console (console=ttyS0) so the VM stays usable in a terminal.
  virtualisation.graphics = lib.mkDefault false;
}
