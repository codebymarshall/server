// Package updatestatus is bluefin-update-status: update health on the login
// banners, and bluefin-update-status-migrate, which enables it on nodes that
// updated from an image without it.
//
// bluefin-update-status runs at boot and after every systemd-sysupdate.service
// run (OnSuccess=/OnFailure=, 30-update-status.conf). It records the outcome of
// the newest run in /var/lib/bluefin-update-status/state, so the last
// successful check and the last error survive reboots, and writes the same
// lines to /run/issue.d/40-bluefin-update.issue (agetty) and /run/motd (login
// and sshd, through /etc/motd): the running version, whether automatic updates
// are on, the last successful check, a staged update, and the error of the
// newest check while no later check has succeeded. It only reads local state
// and always exits 0: a failed check stays visible as the failed unit.
package updatestatus

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/projectbluefin/server/files/homelab/cluster/internal/host/sys"
)

const unit = "systemd-sysupdate.service"

var (
	signatureRE   = regexp.MustCompile(`signature|gpg|keyring`)
	unreachableRE = regexp.MustCompile(`transfer failed|http|resolve|connect|refused|timed? ?out|unreachable|network`)
)

type state struct{ lastSuccess, lastError, error, recorded string }

func readState(file string) state {
	var s state
	if !sys.Readable(file) {
		return s
	}
	data, _ := os.ReadFile(file)
	for _, line := range sys.Lines(data, false) {
		key, value := sys.SplitEq(line)
		switch key {
		case "last_success":
			s.lastSuccess = value
		case "last_error":
			s.lastError = value
		case "error":
			s.error = value
		case "recorded":
			s.recorded = value
		}
	}
	return s
}

// failure is the reason a run failed, from the errors it logged:
// systemd-pull logs the cause first, systemd-sysupdate the consequence.
func failure(h *sys.Host, journalctl, invocation, result string) string {
	errors, _ := h.Output(true, journalctl, "-q", "-o", "cat", "--no-pager", "-p", "err", "_SYSTEMD_INVOCATION_ID="+invocation)
	first := ""
	for _, line := range strings.Split(errors, "\n") {
		if line != "" {
			first = sys.CutBytes(sys.StripControl(line), 160)
			break
		}
	}
	matches := func(re *regexp.Regexp) bool {
		for _, line := range strings.Split(sys.LowerASCII(errors), "\n") {
			if re.MatchString(line) {
				return true
			}
		}
		return false
	}
	detail := ""
	if first != "" {
		detail = " (" + first + ")"
	}
	switch {
	case matches(signatureRE):
		return "signature verification failed" + detail
	case matches(unreachableRE):
		return "update source unreachable" + detail
	case first != "":
		return first
	}
	return unit + " failed with result " + result
}

// Main is bluefin-update-status. Test overrides: BLUEFIN_SYSTEMCTL,
// BLUEFIN_JOURNALCTL, BLUEFIN_OS_RELEASE, BLUEFIN_BOOT_PATH,
// BLUEFIN_UPDATE_STATE_DIR, BLUEFIN_ISSUE_FILE, BLUEFIN_MOTD_FILE,
// BLUEFIN_AGETTY.
func Main(h *sys.Host, _ []string) int {
	systemctl := h.Env("BLUEFIN_SYSTEMCTL", "systemctl")
	journalctl := h.Env("BLUEFIN_JOURNALCTL", "journalctl")
	osRelease := h.Env("BLUEFIN_OS_RELEASE", "/usr/lib/os-release")
	stateDir := h.Env("BLUEFIN_UPDATE_STATE_DIR", h.Env("STATE_DIRECTORY", "/var/lib/bluefin-update-status"))
	issue := h.Env("BLUEFIN_ISSUE_FILE", "/run/issue.d/40-bluefin-update.issue")
	motd := h.Env("BLUEFIN_MOTD_FILE", "/run/motd")
	agetty := h.Env("BLUEFIN_AGETTY", "agetty")

	s := readState(stateDir + "/state")

	show := func(prop string, extra ...string) string {
		argv := append([]string{systemctl, "show", "-P", prop}, extra...)
		out, _ := h.Output(true, append(argv, unit)...)
		return out
	}
	invocation := show("InvocationID")
	active := show("ActiveState")
	finished := strings.TrimPrefix(show("ExecMainExitTimestamp", "--timestamp=unix"), "@")
	if invocation != "" && invocation != s.recorded && finished != "" && (active == "inactive" || active == "failed") {
		if result := show("Result"); result == "success" {
			s.lastSuccess = finished
		} else {
			s.lastError = finished
			s.error = failure(h, journalctl, invocation, result)
		}
		s.recorded = invocation
		record := "last_success=" + s.lastSuccess + "\nlast_error=" + s.lastError + "\nerror=" + s.error + "\nrecorded=" + s.recorded + "\n"
		if sys.MkdirP(stateDir) != nil ||
			sys.WriteFile(stateDir+"/state.tmp", []byte(record)) != nil ||
			os.Rename(stateDir+"/state.tmp", stateDir+"/state") != nil {
			h.Eprintf("could not record the update check in %s\n", stateDir)
		}
	}

	booted := sys.ImageVersion(osRelease)

	// A UKI newer than the booted one is staged; one with no tries left
	// (bluefin-server-<ver>+0[-<done>].efi) already failed its boot tries.
	staged, rejected := "", ""
	for _, uki := range sys.Glob(filepath.Join(h.BootPath(), "EFI", "Linux"), "bluefin-server-*.efi") {
		if !sys.Exists(uki) || booted == "" {
			continue
		}
		name := strings.TrimPrefix(filepath.Base(uki), "bluefin-server-")
		version, _, _ := strings.Cut(strings.TrimSuffix(name, ".efi"), "+")
		if !h.VersionGT(version, booted, true) {
			continue
		}
		if noTriesLeft(name) {
			rejected = version
		} else {
			staged = version
		}
	}

	timer, _ := h.Output(true, systemctl, "is-enabled", "systemd-sysupdate.timer")
	updates := "automatic updates on"
	if timer != "enabled" {
		if timer == "" {
			timer = "missing"
		}
		updates = "automatic updates off (systemd-sysupdate.timer is " + timer + ")"
	}

	failing := false
	if s.lastError != "" {
		if s.lastSuccess == "" {
			failing = true
		} else if e, ok := sys.ParseInt(s.lastError); ok {
			if l, ok := sys.ParseInt(s.lastSuccess); ok && e > l {
				failing = true
			}
		}
	}

	check := "never"
	if s.lastSuccess != "" {
		check = when(s.lastSuccess)
	}
	switch {
	case staged != "":
		check += "; " + staged + " staged, it boots at the next reboot"
	case rejected != "":
		check += "; " + rejected + " failed its boot tries, staying on " + booted
	case s.lastSuccess != "" && !failing:
		check += ", up to date"
	}

	if booted == "" {
		booted = "unknown"
	}
	lines := []string{"Bluefin Server " + booted + ", " + updates, "Last update check: " + check}
	if failing {
		lines = append(lines, "Last update check FAILED "+when(s.lastError)+": "+s.error, "  journalctl -u "+unit)
	}
	text := strings.TrimRight(strings.Join(lines, "\n"), "\n")
	// agetty expands backslash escapes in issue files.
	write(h, issue, strings.ReplaceAll(text, `\`, `\\`)+"\n")
	write(h, motd, text+"\n")
	h.Test(true, agetty, "--reload")
	return 0
}

func noTriesLeft(name string) bool {
	plain, _ := filepath.Match("*+0.efi", name)
	done, _ := filepath.Match("*+0-*.efi", name)
	return plain || done
}

// when is `date -d @<ts> '+%Y-%m-%d %H:%M %Z'`, or @<ts> when ts is no time.
func when(ts string) string {
	n, ok := sys.ParseInt(ts)
	if !ok {
		return "@" + ts
	}
	return time.Unix(n, 0).Format("2006-01-02 15:04 MST")
}

// write replaces file atomically with content and a newline, mode 0644.
func write(h *sys.Host, file, content string) {
	tmp := file + ".tmp"
	if sys.MkdirP(sys.Dir(file)) != nil ||
		sys.WriteFile(tmp, []byte(content+"\n")) != nil ||
		os.Chmod(tmp, 0o644) != nil ||
		os.Rename(tmp, file) != nil {
		h.Eprintf("could not write %s\n", file)
	}
}

// Migrate is bluefin-update-status-migrate, run once per installed node.
// Presets apply on first boot only, so a node that updated from an image
// without bluefin-update-status.service never enables it (#367): enable it
// from its preset rule and start it. Nothing else in 80-bluefin-updates.preset
// is touched, so an operator's opt-out of the update timers stays.
// Test overrides: BLUEFIN_SYSTEMCTL, BLUEFIN_UPDATE_STATUS_MIGRATE_STAMP.
func Migrate(h *sys.Host, _ []string) int {
	systemctl := h.Env("BLUEFIN_SYSTEMCTL", "systemctl")
	stamp := h.Env("BLUEFIN_UPDATE_STATUS_MIGRATE_STAMP", "/var/lib/bluefin/update-status-migrate.stamp")
	const unit = "bluefin-update-status.service"

	if h.Exec(systemctl, "is-enabled", "--quiet", unit) == 0 {
		h.Printf("bluefin-update-status-migrate: %s already enabled\n", unit)
	} else {
		// Honours an operator preset (e.g. Ignition's) that disables it.
		h.Exec(systemctl, "preset", unit)
		if h.Exec(systemctl, "is-enabled", "--quiet", unit) == 0 {
			h.Exec(systemctl, "start", "--no-block", unit)
			h.Printf("bluefin-update-status-migrate: enabled %s\n", unit)
		} else {
			h.Printf("bluefin-update-status-migrate: %s left disabled by preset\n", unit)
		}
	}
	if err := sys.MkdirP(sys.Dir(stamp)); err != nil {
		h.Eprintf("bluefin-update-status-migrate: %v\n", err)
		return 1
	}
	if err := sys.Truncate(stamp); err != nil {
		h.Eprintf("bluefin-update-status-migrate: %v\n", err)
		return 1
	}
	return 0
}
