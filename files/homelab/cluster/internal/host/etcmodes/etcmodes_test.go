package etcmodes

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func mode(t *testing.T, p string) uint32 {
	t.Helper()
	var st syscall.Stat_t
	if err := syscall.Lstat(p, &st); err != nil {
		t.Fatal(err)
	}
	return st.Mode & 0o7777
}

func TestRepair(t *testing.T) {
	dir := t.TempDir()
	etc := filepath.Join(dir, "etc")
	for _, d := range []string{"sudoers.d", "tmp"} {
		if err := os.MkdirAll(filepath.Join(etc, d), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	files := map[string]uint32{"sudoers.d": 0o777, "sudoers.d/x": 0o666, "setgid": 0o2775, "tight": 0o600, "tmp": 0o1777}
	for name, m := range files {
		p := filepath.Join(etc, name)
		if _, err := os.Stat(p); os.IsNotExist(err) {
			if err := os.WriteFile(p, nil, 0o600); err != nil {
				t.Fatal(err)
			}
		}
		if err := syscall.Chmod(p, m); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Symlink("/outside", filepath.Join(etc, "link")); err != nil {
		t.Fatal(err)
	}
	list := filepath.Join(dir, "etc-mode-repair.list")
	if err := os.WriteFile(list, []byte("sudoers.d\nsudoers.d/x\nsetgid\ntight\ntmp\nlink\ngone\nunterminated"), 0o644); err != nil {
		t.Fatal(err)
	}
	stamp := filepath.Join(dir, "var", "lib", "bluefin", "etc-mode-repair.stamp")
	n, err := Repair(etc, list, stamp)
	if err != nil || n != 4 {
		t.Fatalf("Repair = %d, %v", n, err)
	}
	for name, want := range map[string]uint32{"sudoers.d": 0o755, "sudoers.d/x": 0o644, "setgid": 0o2755, "tight": 0o600, "tmp": 0o1777} {
		if got := mode(t, filepath.Join(etc, name)); got != want {
			t.Errorf("%s: %o; want %o", name, got, want)
		}
	}
	if _, err := os.Stat(stamp); err != nil {
		t.Errorf("stamp: %v", err)
	}
}
