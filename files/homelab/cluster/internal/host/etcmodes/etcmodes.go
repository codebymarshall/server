// Package etcmodes is bluefin-repair-etc-modes: one-shot repair of /etc modes
// on nodes installed from an image built before the sysroot modes were fixed.
//
// It only clears group/other write on the paths
// elements/oci/bluefin-server-usr.bst listed in
// /usr/lib/bluefin/etc-mode-repair.list. It never widens a mode and never
// re-asserts a factory mode, so an operator who tightened a path keeps their
// mode. It runs once; a stamp in /var suppresses later boots.
package etcmodes

import (
	"os"
	"syscall"

	"github.com/projectbluefin/server/files/homelab/cluster/internal/host/sys"
)

const (
	List  = "/usr/lib/bluefin/etc-mode-repair.list"
	Stamp = "/var/lib/bluefin/etc-mode-repair.stamp"
)

// Repair clears group/other write from etc/<path> for every path in list
// and creates stamp; it returns how many paths it changed.
func Repair(etc, list, stamp string) (int, error) {
	data, err := os.ReadFile(list)
	if err != nil {
		return 0, err
	}
	repaired := 0
	for _, rel := range sys.Lines(data, false) {
		p := etc + "/" + rel
		// Skip what the node no longer has, symlinks (chmod would follow
		// them out of /etc) and sticky dirs, which are meant to be 1777.
		var st syscall.Stat_t
		if syscall.Stat(p, &st) != nil {
			continue
		}
		if fi, err := os.Lstat(p); err != nil || fi.Mode()&os.ModeSymlink != 0 || st.Mode&syscall.S_ISVTX != 0 {
			continue
		}
		if err := syscall.Chmod(p, st.Mode&0o7777&^0o022); err != nil {
			return repaired, &os.PathError{Op: "chmod", Path: p, Err: err}
		}
		repaired++
	}
	if err := sys.MkdirP(sys.Dir(stamp)); err != nil {
		return repaired, err
	}
	return repaired, sys.Truncate(stamp)
}

// Main is bluefin-repair-etc-modes.
func Main(h *sys.Host, _ []string) int {
	repaired, err := Repair("/etc", List, Stamp)
	if err != nil {
		h.Eprintf("bluefin-repair-etc-modes: %v\n", err)
		return 1
	}
	h.Printf("Cleared group/other write from %d path(s) under /etc\n", repaired)
	return 0
}
