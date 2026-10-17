package updatestatus

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/projectbluefin/server/files/homelab/cluster/internal/host/hosttest"
)

type sysupdateRun struct {
	invocation, active, exit, result, journal, timer string
}

type node struct {
	dir string
	env map[string]string
}

func newNode(t *testing.T, ukis ...string) *node {
	t.Helper()
	dir := t.TempDir()
	linux := filepath.Join(dir, "boot", "EFI", "Linux")
	if err := os.MkdirAll(linux, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, u := range append(ukis, "bluefin-server-1.0.efi") {
		if err := os.WriteFile(filepath.Join(linux, u), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "os-release"), []byte("IMAGE_VERSION=\"1.0\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	return &node{dir: dir, env: map[string]string{
		"BLUEFIN_OS_RELEASE":       filepath.Join(dir, "os-release"),
		"BLUEFIN_BOOT_PATH":        filepath.Join(dir, "boot"),
		"BLUEFIN_UPDATE_STATE_DIR": filepath.Join(dir, "state"),
		"BLUEFIN_ISSUE_FILE":       filepath.Join(dir, "run", "issue.d", "40-bluefin-update.issue"),
		"BLUEFIN_MOTD_FILE":        filepath.Join(dir, "run", "motd"),
	}}
}

func (n *node) run(t *testing.T, u sysupdateRun) *hosttest.Host {
	t.Helper()
	if u.active == "" {
		u.active = "inactive"
	}
	if u.timer == "" {
		u.timer = "enabled"
	}
	h := hosttest.New(n.env, func(argv []string) (string, int) {
		line := strings.Join(argv, " ")
		switch {
		case line == "systemctl show -P InvocationID systemd-sysupdate.service":
			return u.invocation + "\n", 0
		case line == "systemctl show -P ActiveState systemd-sysupdate.service":
			return u.active + "\n", 0
		case line == "systemctl show -P ExecMainExitTimestamp --timestamp=unix systemd-sysupdate.service":
			return u.exit + "\n", 0
		case line == "systemctl show -P Result systemd-sysupdate.service":
			return u.result + "\n", 0
		case line == "systemctl is-enabled systemd-sysupdate.timer":
			return u.timer + "\n", 1
		case strings.HasPrefix(line, "journalctl -q -o cat --no-pager -p err _SYSTEMD_INVOCATION_ID="):
			return u.journal + "\n", 0
		case argv[0] == "systemd-analyze":
			if argv[2] > argv[4] {
				return "", 0
			}
			return "", 1
		}
		return "", 0
	})
	if code := Main(h.Host, nil); code != 0 {
		t.Fatalf("exit %d", code)
	}
	return h
}

func (n *node) issue(t *testing.T) string {
	t.Helper()
	b, err := os.ReadFile(n.env["BLUEFIN_ISSUE_FILE"])
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

func TestBanner(t *testing.T) {
	time.Local = time.UTC
	for _, tc := range []struct {
		name string
		ukis []string
		runs []sysupdateRun
		want string
	}{
		{"never", nil, []sysupdateRun{{}}, "Bluefin Server 1.0, automatic updates on\nLast update check: never\n\n"},
		{"up to date", nil, []sysupdateRun{{invocation: "a", exit: "@1700000000", result: "success"}},
			"Bluefin Server 1.0, automatic updates on\nLast update check: 2023-11-14 22:13 UTC, up to date\n\n"},
		{"signature", nil, []sysupdateRun{{invocation: "b", exit: "@1700000600", result: "exit-code", journal: "Signature verification failed.\nFailed to acquire manifest"}},
			"Bluefin Server 1.0, automatic updates on\nLast update check: never\nLast update check FAILED 2023-11-14 22:23 UTC: signature verification failed (Signature verification failed.)\n  journalctl -u systemd-sysupdate.service\n\n"},
		{"unreachable", nil, []sysupdateRun{{invocation: "c", exit: "@1700000600", result: "exit-code", journal: "\nCould not RESOLVE host"}},
			"Last update check FAILED 2023-11-14 22:23 UTC: update source unreachable (Could not RESOLVE host)\n"},
		{"result only", nil, []sysupdateRun{{invocation: "d", exit: "@1700000600", result: "timeout"}},
			"FAILED 2023-11-14 22:23 UTC: systemd-sysupdate.service failed with result timeout\n"},
		{"recovered", nil, []sysupdateRun{{invocation: "a", exit: "@1700000600", result: "exit-code", journal: "x"}, {invocation: "b", exit: "@1700001200", result: "success"}},
			"Last update check: 2023-11-14 22:33 UTC, up to date\n\n"},
		{"recorded once", nil, []sysupdateRun{{invocation: "a", exit: "@1700000000", result: "success"}, {invocation: "a", exit: "@1700000000", result: "exit-code", journal: "x"}},
			"Last update check: 2023-11-14 22:13 UTC, up to date\n\n"},
		{"in progress", nil, []sysupdateRun{{invocation: "a", active: "activating", result: "success"}}, "Last update check: never\n\n"},
		{"staged", []string{"bluefin-server-1.1+3.efi"}, []sysupdateRun{{}}, "Last update check: never; 1.1 staged, it boots at the next reboot\n"},
		{"rejected", []string{"bluefin-server-1.1+0-3.efi"}, []sysupdateRun{{}}, "Last update check: never; 1.1 failed its boot tries, staying on 1.0\n"},
		{"timer off", nil, []sysupdateRun{{timer: "disabled"}}, "Bluefin Server 1.0, automatic updates off (systemd-sysupdate.timer is disabled)\n"},
		{"escapes", nil, []sysupdateRun{{invocation: "e", exit: "@1700000000", result: "x", journal: "bad C:\\x\x1b[2J"}}, "FAILED 2023-11-14 22:13 UTC: bad C:\\\\x[2J\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			n := newNode(t, tc.ukis...)
			var h *hosttest.Host
			for _, u := range tc.runs {
				h = n.run(t, u)
			}
			if got := n.issue(t); !strings.Contains(got, tc.want) {
				t.Errorf("issue %q lacks %q", got, tc.want)
			}
			motd, _ := os.ReadFile(n.env["BLUEFIN_MOTD_FILE"])
			if strings.ReplaceAll(string(motd), `\`, `\\`) != n.issue(t) {
				t.Errorf("motd %q", motd)
			}
			if calls := h.Fake.Calls(); calls[len(calls)-1] != "agetty --reload" {
				t.Errorf("last call %q", calls[len(calls)-1])
			}
		})
	}
}

func TestStateFile(t *testing.T) {
	n := newNode(t)
	n.run(t, sysupdateRun{invocation: "a", exit: "@1700000000", result: "success"})
	n.run(t, sysupdateRun{invocation: "b", exit: "@1700000600", result: "exit-code", journal: "Transfer failed: x"})
	got, _ := os.ReadFile(filepath.Join(n.env["BLUEFIN_UPDATE_STATE_DIR"], "state"))
	want := "last_success=1700000000\nlast_error=1700000600\nerror=update source unreachable (Transfer failed: x)\nrecorded=b\n"
	if string(got) != want {
		t.Errorf("state %q", got)
	}
}

func TestMigrate(t *testing.T) {
	for _, tc := range []struct {
		name          string
		enabledBefore bool
		presetEnables bool
		presetFails   bool
		calls         []string
		log           string
	}{
		{"enables", false, true, false, []string{"is-enabled --quiet", "preset", "is-enabled --quiet", "start --no-block"}, "enabled bluefin-update-status.service"},
		{"already", true, false, false, []string{"is-enabled --quiet"}, "bluefin-update-status.service already enabled"},
		{"preset disables", false, false, false, []string{"is-enabled --quiet", "preset", "is-enabled --quiet"}, "bluefin-update-status.service left disabled by preset"},
		{"preset fails", false, false, true, []string{"is-enabled --quiet", "preset", "is-enabled --quiet"}, "left disabled by preset"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			enabled := tc.enabledBefore
			stamp := filepath.Join(t.TempDir(), "var", "lib", "bluefin", "update-status-migrate.stamp")
			h := hosttest.New(map[string]string{"BLUEFIN_SYSTEMCTL": "sc", "BLUEFIN_UPDATE_STATUS_MIGRATE_STAMP": stamp}, func(argv []string) (string, int) {
				switch argv[1] {
				case "is-enabled":
					if enabled {
						return "", 0
					}
					return "", 1
				case "preset":
					if tc.presetFails {
						return "", 1
					}
					enabled = tc.presetEnables
				}
				return "", 0
			})
			if code := Migrate(h.Host, nil); code != 0 {
				t.Fatalf("exit %d", code)
			}
			var want []string
			for _, c := range tc.calls {
				want = append(want, "sc "+c+" bluefin-update-status.service")
			}
			if got := h.Fake.Calls(); strings.Join(got, "\n") != strings.Join(want, "\n") {
				t.Errorf("calls %q", got)
			}
			if h.Out.String() != "bluefin-update-status-migrate: "+tc.log+"\n" && !strings.Contains(h.Out.String(), tc.log) {
				t.Errorf("log %q", h.Out.String())
			}
			if _, err := os.Stat(stamp); err != nil {
				t.Errorf("stamp: %v", err)
			}
		})
	}
}
