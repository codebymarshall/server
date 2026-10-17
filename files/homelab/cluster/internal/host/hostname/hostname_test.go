package hostname

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/projectbluefin/server/files/homelab/cluster/internal/host/hosttest"
)

func TestHostname(t *testing.T) {
	for _, tc := range []struct {
		name, static, firstboot, system, machineID string
		exit                                       int
		want, log                                  string
	}{
		{name: "machine ID", machineID: "0123abcd456789ef0123456789abcdef\n", want: "bluefin-0123abcd\n", log: "bluefin-hostname: set hostname bluefin-0123abcd (from machine ID)\n"},
		{name: "localhost.", static: "LocalHost.\n", machineID: "0123abcd456789ef0123456789abcdef", want: "bluefin-0123abcd\n"},
		{name: "x.localhost.localdomain", static: "x.localhost.localdomain\n", machineID: "0123abcd456789ef0123456789abcdef", want: "bluefin-0123abcd\n"},
		{name: "kept", static: "# c\n  blue-1  \n", system: "other", want: "# c\n  blue-1  \n", log: "bluefin-hostname: keeping static hostname blue-1\n"},
		{name: "invalid static", static: "bad_name\n", exit: 1, want: "bad_name\n", log: "bluefin-hostname: invalid static hostname in /etc/hostname: bad_name; set one with hostnamectl set-hostname\n"},
		{name: "firstboot wins", firstboot: "blue-0\n", system: "x", want: "blue-0\n", log: "(from credential firstboot.hostname)"},
		{name: "system", system: "rack1-node4.example.com", want: "rack1-node4.example.com\n"},
		{name: "invalid credential", system: "bad name", exit: 1, log: "invalid hostname in credential system.hostname: bad\\ name\n"},
		{name: "label too long", system: strings.Repeat("x", 64), exit: 1},
		{name: "name too long", system: strings.Repeat("a", 60) + ".example", exit: 1},
		{name: "longest label", system: strings.Repeat("a", 63), want: strings.Repeat("a", 63) + "\n"},
		{name: "no machine ID", machineID: "uninitialized\n", exit: 1, log: "no machine ID in /etc/machine-id to derive a hostname from"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			root, creds := filepath.Join(dir, "root"), filepath.Join(dir, "creds")
			for _, d := range []string{filepath.Join(root, "etc"), creds} {
				if err := os.MkdirAll(d, 0o755); err != nil {
					t.Fatal(err)
				}
			}
			write := func(p, s string) {
				if s != "" {
					if err := os.WriteFile(p, []byte(s), 0o644); err != nil {
						t.Fatal(err)
					}
				}
			}
			write(filepath.Join(root, "etc", "hostname"), tc.static)
			write(filepath.Join(root, "etc", "machine-id"), tc.machineID)
			write(filepath.Join(creds, "firstboot.hostname"), tc.firstboot)
			write(filepath.Join(creds, "system.hostname"), tc.system)
			h := hosttest.New(map[string]string{"CREDENTIALS_DIRECTORY": creds}, nil)
			if code := Main(h.Host, []string{"--root=" + root + "/"}); code != tc.exit {
				t.Errorf("exit %d; %s", code, h.Err)
			}
			got, _ := os.ReadFile(filepath.Join(root, "etc", "hostname"))
			if string(got) != tc.want {
				t.Errorf("/etc/hostname %q; want %q", got, tc.want)
			}
			if !strings.Contains(h.Err.String(), tc.log) {
				t.Errorf("log %q lacks %q", h.Err.String(), tc.log)
			}
		})
	}
}

func TestUnknownArgument(t *testing.T) {
	h := hosttest.New(nil, nil)
	if code := Main(h.Host, []string{"--bogus"}); code != 2 || h.Err.String() != "bluefin-hostname: unknown argument: --bogus\n" {
		t.Errorf("exit %d %q", code, h.Err)
	}
}
