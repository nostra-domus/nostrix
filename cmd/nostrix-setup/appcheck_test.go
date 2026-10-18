package main

import (
	"errors"
	"strings"
	"testing"
)

func fakeNix(out string, err error) nixRunner {
	return func(args ...string) ([]byte, error) {
		return []byte(out), err
	}
}

func TestCheckAppFlakeAcceptsNostrixModule(t *testing.T) {
	var got []string
	run := func(args ...string) ([]byte, error) {
		got = args
		return []byte(`["default","nostrix"]`), nil
	}
	if err := checkAppFlake(run, "github:org/app", ""); err != nil {
		t.Fatalf("checkAppFlake: %v", err)
	}
	if want := "eval --json github:org/app#nixosModules --apply builtins.attrNames"; strings.Join(got, " ") != want {
		t.Errorf("nix args = %q, want %q", strings.Join(got, " "), want)
	}
}

func TestCheckAppFlakeExplainsAMissingNostrixModule(t *testing.T) {
	err := checkAppFlake(fakeNix(`["default"]`, nil), "github:org/app", "")
	if err == nil {
		t.Fatal("expected an error: the flake only exports default")
	}
	for _, want := range []string{"nixosModules.nostrix", "default", "--module", "local.nix"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error should mention %q: %v", want, err)
		}
	}
}

func TestCheckAppFlakeHonoursAnExplicitModule(t *testing.T) {
	if err := checkAppFlake(fakeNix(`["default"]`, nil), "github:org/app", "default"); err != nil {
		t.Errorf("--module default should be accepted when the flake exports it: %v", err)
	}
	if err := checkAppFlake(fakeNix(`["default"]`, nil), "github:org/app", "server"); err == nil {
		t.Error("expected an error for a module the flake doesn't export")
	}
}

func TestCheckAppFlakeFlakeWithoutModules(t *testing.T) {
	err := checkAppFlake(fakeNix("", errors.New("error: flake 'github:org/app' does not provide attribute 'nixosModules'")), "github:org/app", "")
	if err == nil || !strings.Contains(err.Error(), "exports no nixosModules") {
		t.Errorf("want a plain 'no nixosModules' message, got: %v", err)
	}
}

func TestCheckAppFlakeFetchFailure(t *testing.T) {
	err := checkAppFlake(fakeNix("", errors.New("unable to download")), "github:org/typo", "")
	if err == nil || !strings.Contains(err.Error(), "could not read the flake") {
		t.Errorf("want a 'could not read' message, got: %v", err)
	}
}

func TestCheckAppFlakeRejectsUnsafeModuleNames(t *testing.T) {
	// The name is spliced into the generated flake unquoted.
	if err := checkAppFlake(fakeNix(`[]`, nil), "github:org/app", "x; builtins.abort \"p\""); err == nil {
		t.Error("expected an error for an unsafe module name")
	}
}
