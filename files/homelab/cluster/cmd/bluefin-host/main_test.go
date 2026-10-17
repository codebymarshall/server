package main

import (
	"os"
	"testing"
)

func TestEveryHelperDispatchesByNameAndSubcommand(t *testing.T) {
	for name := range helpers {
		if _, ok := lookup(name); !ok {
			t.Errorf("bluefin-host %s", name)
		}
		if _, ok := lookup("bluefin-" + name); !ok {
			t.Errorf("bluefin-host bluefin-%s", name)
		}
	}
	for name := range units {
		if _, ok := lookup(name); !ok {
			t.Errorf("bluefin-host %s", name)
		}
	}
	if _, ok := lookup("bluefin-boot-is-good"); ok {
		t.Error("unit commands have no bluefin- name")
	}
}

func TestUsage(t *testing.T) {
	stderr := os.Stderr
	devnull, _ := os.Open(os.DevNull)
	os.Stderr = devnull
	defer func() { os.Stderr = stderr }()
	for _, argv := range [][]string{{"bluefin-host"}, {"/usr/bin/bluefin-host", "bogus"}, {"bluefin-bogus"}} {
		if code := run(argv); code != 2 {
			t.Errorf("%q: exit %d", argv, code)
		}
	}
}

func TestSymlinkNameRunsTheHelper(t *testing.T) {
	stderr := os.Stderr
	devnull, _ := os.Open(os.DevNull)
	os.Stderr = devnull
	defer func() { os.Stderr = stderr }()
	// bluefin-reboot-lock's usage error is exit 2; bluefin-host's would be too,
	// so use the helper's own: an unknown verb.
	t.Setenv("LC_ALL", "")
	if code := run([]string{"/usr/libexec/bluefin-reboot-lock", "bogus"}); code != 2 {
		t.Errorf("exit %d", code)
	}
	if os.Getenv("LC_ALL") != "C" {
		t.Errorf("LC_ALL=%q; the shell helper exported C", os.Getenv("LC_ALL"))
	}
	t.Setenv("LC_ALL", "")
	if code := run([]string{"/usr/libexec/bluefin-boot-origin"}); code != 1 && code != 0 {
		t.Errorf("boot-origin exit %d", code)
	}
}
