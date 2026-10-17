package kmods

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/projectbluefin/server/files/homelab/cluster/internal/host/hosttest"
)

const kver = "7.2.2"

func setup(t *testing.T) (usr, basedir string, env map[string]string) {
	t.Helper()
	dir := t.TempDir()
	usr = filepath.Join(dir, "usr")
	src := filepath.Join(usr, "lib", "modules", kver)
	for _, d := range []string{filepath.Join(src, "kernel"), filepath.Join(src, "extra", "zfs"), filepath.Join(usr, "lib", "extension-release.d")} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for _, f := range []string{"kernel/drm.ko.zst", "extra/zfs/zfs.ko.zst", "modules.order"} {
		if err := os.WriteFile(filepath.Join(src, f), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	basedir = filepath.Join(dir, "run", "bluefin", "kmods")
	return usr, basedir, map[string]string{"BLUEFIN_KMOD_USR": usr, "BLUEFIN_KMOD_BASEDIR": basedir, "BLUEFIN_KVER": kver}
}

// depmod writes a two-module index into the -b tree.
func depmod(code int) hosttest.Handler {
	return func(argv []string) (string, int) {
		if argv[0] == "depmod" {
			if code == 0 {
				os.WriteFile(filepath.Join(argv[2], "lib", "modules", argv[3], "modules.dep"), []byte("a:\nb:\n"), 0o644)
			}
			return "", code
		}
		return "", 0
	}
}

func TestIndexOnceThenModprobe(t *testing.T) {
	usr, basedir, env := setup(t)
	for i := 0; i < 2; i++ {
		h := hosttest.New(env, depmod(0))
		if code := Main(h.Host, []string{"zfs", "spl"}); code != 0 {
			t.Fatalf("exit %d: %s", code, h.Err)
		}
		calls := h.Fake.Calls()
		last := calls[len(calls)-1]
		if last != "modprobe -d "+basedir+" -S "+kver+" -a -- zfs spl" {
			t.Errorf("run %d: %q", i, calls)
		}
		if (i == 0) != (len(calls) == 2) {
			t.Errorf("run %d indexes: %q", i, calls)
		}
		if i == 0 && h.Err.String() != "bluefin-sysext-modules: indexed 2 modules for "+kver+" in "+basedir+"\n" {
			t.Errorf("log %q", h.Err.String())
		}
	}
	if got, _ := os.Readlink(filepath.Join(basedir, "lib")); got != "usr/lib" {
		t.Errorf("lib -> %q", got)
	}
	tree := filepath.Join(basedir, "usr", "lib", "modules", kver)
	for _, e := range []string{"kernel", "extra", "modules.order"} {
		if got, _ := os.Readlink(filepath.Join(tree, e)); got != filepath.Join(usr, "lib", "modules", kver, e) {
			t.Errorf("%s -> %q", e, got)
		}
	}
	if _, err := os.Lstat(filepath.Join(tree, "updates")); !os.IsNotExist(err) {
		t.Errorf("updates linked without a source: %v", err)
	}

	// A newly merged module rebuilds the index.
	if err := os.WriteFile(filepath.Join(usr, "lib", "modules", kver, "extra", "zfs", "spl.ko.zst"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h := hosttest.New(env, depmod(0))
	if code := Main(h.Host, []string{"--basedir"}); code != 0 || h.Out.String() != basedir+"\n" {
		t.Errorf("--basedir: %d %q", code, h.Out)
	}
	if calls := h.Fake.Calls(); len(calls) != 1 || !strings.HasPrefix(calls[0], "depmod -b "+basedir+".") {
		t.Errorf("rebuild: %q", calls)
	}
}

func TestFailedDepmodLeavesNothing(t *testing.T) {
	_, basedir, env := setup(t)
	h := hosttest.New(env, depmod(3))
	if code := Main(h.Host, []string{"zfs"}); code != 3 {
		t.Errorf("exit %d", code)
	}
	entries, _ := os.ReadDir(filepath.Dir(basedir))
	for _, e := range entries {
		if e.Name() != "kmods.lock" {
			t.Errorf("left behind %s", e.Name())
		}
	}
}

func TestUsage(t *testing.T) {
	_, _, env := setup(t)
	for _, args := range [][]string{nil, {"-r", "zfs"}, {"--basedir", "zfs"}} {
		h := hosttest.New(env, depmod(0))
		if code := Main(h.Host, args); code != 2 || h.Err.String() != "usage: bluefin-sysext-modules MODULE... | --basedir\n" || len(h.Fake.Calls()) != 0 {
			t.Errorf("%q: exit %d %q", args, code, h.Err)
		}
	}
	env["BLUEFIN_KVER"] = "0.0"
	h := hosttest.New(env, depmod(0))
	if code := Main(h.Host, []string{"zfs"}); code != 1 || !strings.Contains(h.Err.String(), "no modules for kernel 0.0 in ") {
		t.Errorf("missing tree: %d %q", code, h.Err)
	}
}
