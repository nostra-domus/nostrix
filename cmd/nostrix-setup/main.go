// Command nostrix-setup is the interactive setup wizard for Nostrix.
//
// Typical usage on a fresh NixOS installation:
//
//	nix run github:nostra-domus/nostrix
//
// Subcommands:
//
//	nostrix-setup                — interactive setup wizard
//	nostrix-setup add <git-url>  — register an app
//	nostrix-setup serve          — run the web configuration UI
package main

import (
	"bufio"
	"bytes"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func main() {
	if len(os.Args) > 1 && !strings.HasPrefix(os.Args[1], "-") {
		switch os.Args[1] {
		case "add":
			runAdd(os.Args[2:])
			return
		case "serve":
			runServe(os.Args[2:])
			return
		case "help", "--help", "-h":
			printUsage()
			return
		}
	}
	runWizard()
}

func printUsage() {
	fmt.Println("Usage:")
	fmt.Println("  nostrix-setup              — interactive setup wizard")
	fmt.Println("  nostrix-setup add <url>    — register an app")
	fmt.Println("  nostrix-setup serve        — run the web configuration UI")
	fmt.Println()
	fmt.Println("Flags:")
	fmt.Println("  --output   path to flake.nix  (default: /etc/nixos/flake.nix)")
	fmt.Println("  --state    path to state file  (default: /etc/nostrix/state.json)")
	fmt.Println("  --dry-run  print without writing or applying (wizard, add)")
	fmt.Println("  --addr     address to listen on  (serve; default: 0.0.0.0:8080)")
	fmt.Println("  --cf-team-domain  Cloudflare Access team domain  (serve; required)")
	fmt.Println("  --cf-aud          Cloudflare Access application audience  (serve; required)")
}

// switchInhibitedMarker is the message NixOS's pre-switch checks print
// (nixos/modules/system/activation/pre-switch-check.nix) when `switch`
// refuses to hot-apply a change to a critical component (e.g. the D-Bus
// implementation) that it considers unsafe to swap into a live system.
// `nixos-rebuild boot` skips this check entirely, so a plain boot+reboot
// always gets past it.
const switchInhibitedMarker = "Pre-switch checks failed"

// apply writes content to path and runs nixos-rebuild switch. With
// upgradeAll, it also passes --upgrade-all so the switch first re-resolves
// every flake input (nostrix, nixpkgs, ...) to its latest revision instead
// of reusing whatever's pinned in the local flake.lock.
//
// If switch is blocked by a pre-switch check — a critical-component change
// (D-Bus implementation, systemd, ...) NixOS refuses to hot-swap live —
// this falls back to `nixos-rebuild boot` plus a scheduled reboot, the same
// resolution system.autoUpgrade's allowReboot already uses for exactly this
// case (base.nix). Without this, the wizard/web UI would dead-end on an
// error that a phone-only or remote user has no way to act on (the fix is
// literally "run a different nixos-rebuild subcommand and reboot").
func apply(path, content string, upgradeAll bool) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return err
	}
	fmt.Printf("Written %s\n\n", path)

	switchErr, output := runRebuild(path, "switch", upgradeAll)
	if switchErr == nil {
		return nil
	}
	if !strings.Contains(output, switchInhibitedMarker) {
		return switchErr
	}

	fmt.Println()
	fmt.Println("Switch blocked by a critical-component change (see above) — falling back")
	fmt.Println("to `nixos-rebuild boot` plus a reboot, same as the weekly auto-upgrade does")
	fmt.Println("for this case.")
	fmt.Println()
	if bootErr, _ := runRebuild(path, "boot", upgradeAll); bootErr != nil {
		return bootErr
	}

	fmt.Println("Rebooting in 1 minute to activate the new generation...")
	return exec.Command("shutdown", "-r", "+1", "Nostrix: rebooting to apply pending configuration").Run()
}

// runRebuild runs `nixos-rebuild <action> --flake <dir>`, streaming its
// output live (as before) while also capturing it, so apply() can inspect
// it for switchInhibitedMarker without changing what the user/journal sees.
func runRebuild(path, action string, upgradeAll bool) (error, string) {
	flakeDir := filepath.Dir(path)
	args := []string{action, "--flake", flakeDir}
	if upgradeAll {
		args = append(args, "--upgrade-all")
	}
	fmt.Printf("Running: nixos-rebuild %s\n\n", strings.Join(args, " "))

	// Use the stable, well-known location rather than a PATH search.
	// Confirmed on real hardware: nostrix-web's own systemd-assigned PATH
	// (NixOS's default for a plain service — just
	// coreutils/findutils/grep/sed/systemd) never includes
	// /run/current-system/sw/bin, so exec.LookPath("nixos-rebuild") here
	// failed silently *before* ever reaching the systemd-run branch below
	// — and since runRebuild's error return was never surfaced anywhere
	// (not printed, not shown on the bootstrap page — a separate gap),
	// every switch triggered from the web UI looked like it did nothing
	// at all, with zero indication why.
	nixosRebuild := "/run/current-system/sw/bin/nixos-rebuild"
	if _, statErr := os.Stat(nixosRebuild); statErr != nil {
		// Fall back to a PATH search for any other context (e.g. a
		// non-standard NixOS layout) rather than failing outright.
		p, lookErr := exec.LookPath("nixos-rebuild")
		if lookErr != nil {
			return statErr, ""
		}
		nixosRebuild = p
	}

	viaSystemdRun := os.Getenv("INVOCATION_ID") != ""

	var cmd *exec.Cmd
	if viaSystemdRun {
		// Running as a systemd service (nostrix-web): this switch can
		// change nostrix-web's own unit (e.g. the bootstrap -> configured
		// transition in modules/web.nix), which makes systemd restart the
		// very unit this process runs under partway through. Run
		// nixos-rebuild in its own transient scope, outside nostrix-web's
		// cgroup, so that restart doesn't tear down the switch in progress.
		//
		// --setenv=PATH gives that scope a real NixOS PATH instead of
		// systemd-run's own compiled-in default (filesystem tools like
		// zfs/xfsprogs — no coreutils at all). Confirmed on real hardware:
		// without this, nixos-rebuild-ng's own activation logic — running
		// inside this same scope, inheriting that same restricted PATH —
		// fails at the very last step trying to shell out to a bare `test`
		// command it can't find ("[Errno 2] No such file or directory:
		// 'test'"), after 20+ minutes of otherwise-successful build work.
		//
		// Deliberately NOT --pipe: that would connect the transient
		// scope's stdout/stderr to *this* process's own fds, which live in
		// nostrix-web.service's cgroup. Confirmed on real hardware: the
		// switch itself stops nostrix-web.service partway through
		// activation (it's in the list of units the new generation no
		// longer needs), which kills this process and closes those fds —
		// and the next write nixos-rebuild-ng makes to its (now-closed)
		// stdout gets SIGPIPE, silently killing the switch right after it
		// stops the old units and before it starts the new ones, leaving
		// /run/current-system never updated. Routing output to the journal
		// instead decouples it from this process's lifetime, so the switch
		// can run to completion whether or not we survive to see it.
		systemdRunArgs := append(
			[]string{
				"--collect", "--wait",
				"--unit=" + switchUnitName,
				"--property=StandardOutput=journal",
				"--property=StandardError=journal",
				"--setenv=PATH=/run/wrappers/bin:/run/current-system/sw/bin",
				nixosRebuild,
			},
			args...,
		)
		cmd = exec.Command("systemd-run", systemdRunArgs...)
	} else {
		cmd = exec.Command(nixosRebuild, args...)
	}

	var captured bytes.Buffer
	if !viaSystemdRun {
		cmd.Stdout = io.MultiWriter(os.Stdout, &captured)
		cmd.Stderr = io.MultiWriter(os.Stderr, &captured)
	}
	startedAt := time.Now()
	err := cmd.Run()
	if !viaSystemdRun {
		return err, captured.String()
	}

	// We only reach here if this process survived the switch — i.e. it
	// didn't stop nostrix-web.service, which is exactly the case (a
	// pre-switch check aborting the switch before touching any units)
	// where apply() needs switchInhibitedMarker out of the output. Pull
	// just this run's slice of the journal by unit name and start time
	// rather than relying on stdio we deliberately didn't wire up above.
	journalOut, jErr := exec.Command("journalctl",
		"--no-pager", "-o", "cat",
		"--unit="+switchUnitName,
		"--since=@"+strconv.FormatInt(startedAt.Unix(), 10),
	).Output()
	if jErr == nil {
		os.Stdout.Write(journalOut)
		captured.Write(journalOut)
	}
	return err, captured.String()
}

// switchUnitName is the fixed transient-unit name given to the systemd-run
// scope that runs the actual nixos-rebuild switch/boot, so its journal
// output can be looked up by name afterwards (see runRebuild).
const switchUnitName = "nostrix-switch"

func prompt(r *bufio.Reader, label, def string) string {
	fmt.Printf("%s [%s]: ", label, def)
	s, _ := r.ReadString('\n')
	s = strings.TrimSpace(s)
	if s == "" {
		return def
	}
	return s
}

func promptBool(r *bufio.Reader, label string, def bool) bool {
	hint := "Y/n"
	if !def {
		hint = "y/N"
	}
	fmt.Printf("%s [%s]: ", label, hint)
	s, _ := r.ReadString('\n')
	s = strings.ToLower(strings.TrimSpace(s))
	if s == "" {
		return def
	}
	return s == "y" || s == "yes"
}
