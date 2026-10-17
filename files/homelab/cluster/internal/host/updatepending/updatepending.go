// Package updatepending is bluefin-update-pending: is there a staged update
// worth rebooting into?
//
// It exits 0 when `systemd-sysupdate pending` reports a newer installed
// version than the booted one and that version has not already failed its
// boot tries, 1 otherwise. A version that failed its tries keeps its UKI as
// bluefin-server-<ver>+0-<done>.efi: systemd-boot sorts it last and boots the
// previous one, but systemd-sysupdate still reports it as pending, so without
// this check a rolled-back node would flag kured after every update run, or
// reboot in every nightly window, and never leave the version it is on. The
// next newer release takes the failed version's slot and clears the block.
//
// Used by systemd-sysupdate.service.d/20-kured.conf (KuredFlag) and
// systemd-sysupdate-reboot.service.d/20-interlock.conf.
package updatepending

import (
	"path/filepath"
	"strings"

	"github.com/projectbluefin/server/files/homelab/cluster/internal/host/sys"
)

// Main is bluefin-update-pending. Test overrides: BLUEFIN_SYSUPDATE,
// BLUEFIN_BOOT_PATH, BLUEFIN_OS_RELEASE.
func Main(h *sys.Host, _ []string) int {
	sysupdate := h.Env("BLUEFIN_SYSUPDATE", "/usr/bin/systemd-sysupdate")
	osRelease := h.Env("BLUEFIN_OS_RELEASE", "/usr/lib/os-release")
	boot := h.BootPath()

	if h.Exec(sysupdate, "pending") != 0 {
		return 1
	}
	booted := sys.ImageVersion(osRelease)
	if booted == "" {
		return 0
	}
	linux := filepath.Join(boot, "EFI", "Linux")
	ukis := append(sys.Glob(linux, "bluefin-server-*+0-*.efi"), sys.Glob(linux, "bluefin-server-*+0.efi")...)
	for _, uki := range ukis {
		if !sys.Exists(uki) {
			continue
		}
		name := strings.TrimPrefix(filepath.Base(uki), "bluefin-server-")
		version, _, _ := strings.Cut(name, "+")
		if h.VersionGT(version, booted, false) {
			h.Printf("Bluefin Server %s is installed but failed its boot tries (%s); staying on %s until a newer release replaces it\n",
				version, filepath.Base(uki), booted)
			return 1
		}
	}
	return 0
}

// RebootRequired is the sentinel kured watches.
const RebootRequired = "/run/reboot-required"

// KuredFlag is systemd-sysupdate.service's ExecStopPost=: flag
// /run/reboot-required for kured when Main reports an update.
func KuredFlag(h *sys.Host, _ []string) int {
	if Main(h, nil) != 0 {
		return 0
	}
	if err := sys.Touch(h.Env("BLUEFIN_REBOOT_SENTINEL", RebootRequired)); err != nil {
		h.Eprintf("bluefin-host kured-flag: %v\n", err)
		return 1
	}
	return 0
}
