// Package kmods is bluefin-sysext-modules: load kernel modules shipped by
// system extensions.
//
//	bluefin-sysext-modules MODULE...   load MODULE... and their dependencies
//	bluefin-sysext-modules --basedir   print the module base directory, for
//	                                   modprobe -d / modinfo -b
//
// A sysext ships its modules under /usr/lib/modules/<kver>/extra/<name>/ and
// no modules.* index: two merged extensions shipping one would hide each
// other's. The base image's index only knows the base image's modules, so
// this indexes the merged tree under /run: <basedir>/lib/modules/<kver> links
// kernel/, extra/, updates/ and the depmod inputs back to
// /usr/lib/modules/<kver>, and depmod writes a fresh index next to them.
// `modprobe -d <basedir>` then resolves in-tree and cross-extension
// dependencies; module paths stay relative, so the kernel reads the same
// signed .ko files from /usr.
//
// The index is rebuilt once per change of the merged module set (the
// extension-release files plus a listing of extra/ and updates/), under a
// lock so concurrent callers build it once.
package kmods

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"

	"github.com/projectbluefin/server/files/homelab/cluster/internal/host/sys"
)

const prog = "bluefin-sysext-modules"

// linked are the entries of the module tree the index links to.
var linked = []string{
	"kernel", "extra", "updates",
	"modules.order", "modules.builtin", "modules.builtin.modinfo", "modules.builtin.ranges",
}

func kernelRelease() string {
	var u syscall.Utsname
	if syscall.Uname(&u) != nil {
		return ""
	}
	var b strings.Builder
	for _, c := range u.Release {
		if c == 0 {
			break
		}
		b.WriteByte(byte(c))
	}
	return b.String()
}

// stamp identifies the merged module set: the kernel, the merged
// extensions and every file under extra/ and updates/.
func stamp(usr, src, kver string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "kver %s\n", kver)
	if entries, err := os.ReadDir(usr + "/lib/extension-release.d"); err == nil {
		for _, e := range entries {
			fmt.Fprintf(&b, "%s\n", e.Name())
		}
	}
	for _, d := range []string{"extra", "updates"} {
		dir, err := filepath.EvalSymlinks(src + "/" + d)
		if err != nil || !sys.IsDir(dir) {
			continue
		}
		_ = filepath.WalkDir(dir, func(p string, e fs.DirEntry, err error) error {
			if err != nil {
				fmt.Fprintf(&b, "%s: %v\n", p, err)
				return nil
			}
			var st syscall.Stat_t
			if syscall.Lstat(p, &st) != nil {
				return nil
			}
			rel, _ := filepath.Rel(dir, p)
			target, _ := os.Readlink(p)
			fmt.Fprintf(&b, "%s/%s %o %d:%d %d %d.%09d %s\n", d, rel, st.Mode, st.Uid, st.Gid, st.Size,
				st.Mtim.Sec, st.Mtim.Nsec, target)
			return nil
		})
	}
	return strings.TrimRight(b.String(), "\n")
}

// Main is bluefin-sysext-modules. Test overrides: BLUEFIN_KMOD_USR (/usr),
// BLUEFIN_KMOD_BASEDIR (/run/bluefin/kmods), BLUEFIN_KVER (uname -r).
func Main(h *sys.Host, args []string) int {
	usr := h.Env("BLUEFIN_KMOD_USR", "/usr")
	basedir := h.Env("BLUEFIN_KMOD_BASEDIR", "/run/bluefin/kmods")
	kver := h.Getenv("BLUEFIN_KVER")
	if kver == "" {
		kver = kernelRelease()
	}
	src := usr + "/lib/modules/" + kver

	usage := func() int {
		h.Eprintf("usage: %s MODULE... | --basedir\n", prog)
		return 2
	}
	if len(args) == 0 {
		return usage()
	}
	if args[0] == "--basedir" {
		if len(args) != 1 {
			return usage()
		}
	} else if strings.HasPrefix(args[0], "-") {
		return usage()
	}
	if !sys.IsDir(src) {
		h.Eprintf("%s: no modules for kernel %s in %s\n", prog, kver, src)
		return 1
	}
	fail := func(err error) int {
		h.Eprintf("%s: %v\n", prog, err)
		return 1
	}

	if err := sys.MkdirP(sys.Dir(basedir)); err != nil {
		return fail(err)
	}
	lock, err := os.OpenFile(basedir+".lock", os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return fail(err)
	}
	defer lock.Close()
	if err := syscall.Flock(int(lock.Fd()), syscall.LOCK_EX); err != nil {
		return fail(err)
	}

	current := stamp(usr, src, kver)
	old, err := os.ReadFile(basedir + "/stamp")
	if err != nil || !sys.IsFile(basedir+"/stamp") || strings.TrimRight(string(old), "\n") != current {
		if rc := index(h, usr, src, kver, basedir, current); rc != 0 {
			return rc
		}
	}

	if args[0] == "--basedir" {
		h.Printf("%s\n", basedir)
		return 0
	}
	return h.Exec(append([]string{"modprobe", "-d", basedir, "-S", kver, "-a", "--"}, args...)...)
}

func index(h *sys.Host, usr, src, kver, basedir, current string) int {
	fail := func(err error) int {
		h.Eprintf("%s: %v\n", prog, err)
		return 1
	}
	tmp, err := os.MkdirTemp(sys.Dir(basedir), sys.Base(basedir)+".")
	if err != nil {
		return fail(err)
	}
	done := false
	defer func() {
		if !done {
			os.RemoveAll(tmp)
		}
	}()
	mod := tmp + "/usr/lib/modules/" + kver
	if err := sys.MkdirP(mod); err != nil {
		return fail(err)
	}
	// kmod looks in <basedir>/lib/modules or <basedir>/usr/lib/modules,
	// depending on how it was built.
	if err := os.Symlink("usr/lib", tmp+"/lib"); err != nil {
		return fail(err)
	}
	for _, entry := range linked {
		if sys.Exists(src + "/" + entry) {
			if err := os.Symlink(src+"/"+entry, mod+"/"+entry); err != nil {
				return fail(err)
			}
		}
	}
	if rc := h.Exec("depmod", "-b", tmp, kver); rc != 0 {
		return rc
	}
	dep, err := os.ReadFile(mod + "/modules.dep")
	if err != nil {
		return fail(err)
	}
	if err := sys.WriteFile(tmp+"/stamp", []byte(current+"\n")); err != nil {
		return fail(err)
	}
	if err := os.RemoveAll(basedir); err != nil {
		return fail(err)
	}
	if err := os.Rename(tmp, basedir); err != nil {
		return fail(err)
	}
	done = true
	h.Eprintf("%s: indexed %d modules for %s in %s\n", prog, bytes.Count(dep, []byte("\n")), kver, basedir)
	return 0
}
