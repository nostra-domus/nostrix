# Run `just` to list recipes. Get `just` via `nix develop` or `nix run nixpkgs#just`.

default:
    @just --list

# ssh to the test VM. ControlMaster shares one connection between commands (and recipes, for 2 minutes),
# so an ssh agent that confirms each use is asked once, not once per command.
vm_port := "2222"
vm_ssh_opts := "-p " + vm_port + " -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR -o ConnectTimeout=5 -o ControlMaster=auto -o ControlPath=.vm-cloudflare/ctl -o ControlPersist=120"

# --- Go ---

# Build the setup wizard (./result/bin/nostrix-setup)
build:
    nix build

test:
    go test ./...
    go vet ./...

# Sign in to Cloudflare with the cf CLI (browser approval; login lasts ~1h)
cf-login:
    env -u LD_LIBRARY_PATH npx --yes cf auth login

# Look up Cloudflare IDs for a domain (cf login or $CLOUDFLARE_API_TOKEN); --create-token mints a device token
cloudflare domain *args:
    env -u LD_LIBRARY_PATH NOSTRIX_CF="npx --yes cf" go run ./cmd/nostrix-setup cloudflare --domain {{ domain }} {{ args }}

# --- Checks ---

# Evaluate the example config (smoke test)
eval:
    nix eval .#nixosConfigurations.example.config.networking.hostName

# VM integration tests
check-integration:
    nix build .#checks.x86_64-linux.integration

check-ap-portal:
    nix build .#checks.x86_64-linux.apPortal

# --- Images ---

# Raspberry Pi SD card image: pi3 or zero2w
image target="zero2w":
    nix build .#images.{{ if target == "pi3" { "raspberryPi3" } else { "raspberryPiZero2W" } }}

# --- Manual-testing VM (x86_64, mirrors the Pi first-boot state) ---
# Web UI http://localhost:8080 (root/nostrix), nginx :8081, SSH port 2222.

vm-build:
    nix build .#nixosConfigurations.vm.config.system.build.vm -o .vm-result

# Boot the VM on this terminal (Ctrl-A X to quit). A Cloudflare setup saved by `just vm-reset` is restored once the VM is up.
vm: vm-build
    mkdir -p -m 700 .vm-cloudflare; (just vm-restore > .vm-cloudflare/restore.log 2>&1 &)
    # LD_LIBRARY_PATH is unset because a host value can break QEMU's glibc closure.
    env -u LD_LIBRARY_PATH ./.vm-result/bin/run-nostrix-vm

vm-ssh:
    mkdir -p -m 700 .vm-cloudflare
    sshpass -p nostrix ssh {{ vm_ssh_opts }} root@localhost

# Copy the running VM's Cloudflare setup to .vm-cloudflare/ (git-ignored; holds the tunnel token)
vm-save:
    #!/usr/bin/env bash
    set -u
    mkdir -p -m 700 .vm-cloudflare
    ssh=(sshpass -p nostrix ssh {{ vm_ssh_opts }} root@localhost)
    if ! "${ssh[@]}" true; then
        echo "vm-save: can't reach the VM over ssh (is it running? is your ssh agent unlocked?); its Cloudflare setup was NOT saved." >&2
        exit 1
    fi
    "${ssh[@]}" 'grep -q cloudflareTunnelToken /etc/nostrix/state.json' 2>/dev/null
    case $? in
        0) ;;
        1) echo "vm-save: the VM has no Cloudflare setup; keeping any earlier backup."; exit 0 ;;
        *) echo "vm-save: couldn't read the VM's state (ssh failed); keeping any earlier backup."; exit 1 ;;
    esac
    mkdir -p -m 700 .vm-cloudflare
    "${ssh[@]}" 'cat /etc/nostrix/state.json' > .vm-cloudflare/state.json.new &&
    "${ssh[@]}" 'cat /etc/nixos/flake.nix'    > .vm-cloudflare/flake.nix.new &&
    chmod 600 .vm-cloudflare/*.new &&
    mv .vm-cloudflare/state.json.new .vm-cloudflare/state.json &&
    mv .vm-cloudflare/flake.nix.new  .vm-cloudflare/flake.nix &&
    echo "vm-save: saved the VM's Cloudflare setup to .vm-cloudflare/" || { echo "vm-save: failed"; exit 1; }

# Put the saved Cloudflare setup into a freshly booted VM and switch to it (run by `just vm`; log: .vm-cloudflare/restore.log)
vm-restore:
    #!/usr/bin/env bash
    set -u
    [ -f .vm-cloudflare/state.json ] && [ -f .vm-cloudflare/flake.nix ] || exit 0
    mkdir -p -m 700 .vm-cloudflare
    ssh=(sshpass -p nostrix ssh {{ vm_ssh_opts }} root@localhost)
    for _ in $(seq 1 90); do "${ssh[@]}" true 2>/dev/null && break; sleep 3; done
    "${ssh[@]}" true 2>/dev/null || { echo "vm-restore: the VM never became reachable"; exit 1; }
    if "${ssh[@]}" 'test -e /etc/nostrix/state.json'; then
        echo "vm-restore: the VM is already set up; leaving it alone."
        exit 0
    fi
    name=$(sed -n 's/.*nixosConfigurations\.\([A-Za-z0-9_-]*\) *=.*/\1/p' .vm-cloudflare/flake.nix | head -1)
    [ -n "$name" ] || { echo "vm-restore: no configuration name in the saved flake.nix"; exit 1; }
    echo "vm-restore: restoring the saved Cloudflare setup as #$name"
    # No flake.lock on purpose: the old one pins the previous build's source.
    "${ssh[@]}" 'mkdir -p /etc/nostrix /etc/nixos && cat > /etc/nostrix/state.json' < .vm-cloudflare/state.json &&
    "${ssh[@]}" 'cat > /etc/nixos/flake.nix' < .vm-cloudflare/flake.nix &&
    "${ssh[@]}" "systemd-run --collect --wait --unit=nostrix-restore --setenv=PATH=/run/wrappers/bin:/run/current-system/sw/bin nixos-rebuild switch --flake /etc/nixos#$name" &&
    echo "vm-restore: done" || { echo "vm-restore: failed (journalctl -u nostrix-restore in the VM)"; exit 1; }

# Wipe VM disk state for a fresh first boot. A running VM has its Cloudflare setup saved (restored by the next
# `just vm`), is shut down, and only then loses its disk. If the VM can't be reached the disk is kept and this fails;
# `just vm-reset --force` skips the save when no VM is running. A running VM never has its disk deleted under it.
vm-reset *args:
    #!/usr/bin/env bash
    set -u
    force=0
    for a in {{ args }}; do
        case "$a" in
            --force) force=1 ;;
            *) echo "vm-reset: unknown argument $a"; exit 2 ;;
        esac
    done
    vm_up() { (exec 3<>/dev/tcp/127.0.0.1/{{ vm_port }}) 2>/dev/null; }
    stuck="a VM is still running, so its disk was NOT deleted. Stop it (Ctrl-A X in its terminal) and retry."
    if vm_up; then
        if ! just vm-save; then
            echo "vm-reset: $stuck" >&2
            exit 1
        fi
        echo "vm-reset: shutting the VM down..."
        mkdir -p -m 700 .vm-cloudflare
        sshpass -p nostrix ssh {{ vm_ssh_opts }} root@localhost poweroff 2>/dev/null || true
        for _ in $(seq 1 40); do vm_up || break; sleep 1.5; done
        if vm_up; then
            echo "vm-reset: $stuck" >&2
            exit 1
        fi
    elif [ -e nostrix.qcow2 ] && [ "$force" != 1 ]; then
        echo "vm-reset: no VM is running, so its Cloudflare setup can't be saved; the disk was kept. Start the VM and retry, or use 'just vm-reset --force'." >&2
        exit 1
    fi
    rm -f nostrix.qcow2
