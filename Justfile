# Run `just` to list recipes. Get `just` via `nix develop` or `nix run nixpkgs#just`.

default:
    @just --list

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

# Boot the VM on this terminal (Ctrl-A X to quit)
vm: vm-build
    # LD_LIBRARY_PATH is unset because a host value can break QEMU's glibc closure.
    env -u LD_LIBRARY_PATH ./.vm-result/bin/run-nostrix-vm

vm-ssh:
    sshpass -p nostrix ssh -p 2222 -o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null root@localhost

# Wipe VM disk state for a fresh first boot
vm-reset:
    rm -f nostrix.qcow2
