// Package diskless holds what only diskless boots run: bluefin-diskless-update-check,
// which flags a node whose boot server offers a newer release, and the
// systemd-sysupdated.service drop-in's mask of the UKI transfer.
//
// A diskless node runs the OS DDI it pulled into RAM at boot and updates by
// rebooting into whatever its boot server serves. The check finds the
// directory the node booted from (bluefin-boot-origin), fetches SHA256SUMS
// and SHA256SUMS.gpg from it, verifies them with gpgv against the image
// keyring (the trust root of the initrd's `rd.systemd.pull ...
// verify=signature`), reads the release version from the manifest's
// bluefin-server_<ver>.raw entry and compares it with the running
// IMAGE_VERSION using `systemd-analyze compare-versions`. When the release is
// newer and the next boot would pick it up it touches /run/reboot-required.
//
// An origin that names a versioned file (bluefin-server_<ver>.raw or
// bluefin-server-netboot_<ver>.efi) is pinned: the node pulls the same
// version again after a reboot, so flagging it would make kured drain and
// reboot it on every check; a pinned node only logs the newer release.
//
// It never reboots. Network and signature errors are logged and leave the
// flag untouched, and it always exits 0.
package diskless

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/projectbluefin/server/files/homelab/cluster/internal/host/bootorigin"
	"github.com/projectbluefin/server/files/homelab/cluster/internal/host/fetch"
	"github.com/projectbluefin/server/files/homelab/cluster/internal/host/sys"
)

var (
	pinnedRE  = regexp.MustCompile(`^bluefin-server(-netboot)?_[0-9A-Za-z.]+\.(raw|efi)$`)
	releaseRE = regexp.MustCompile(`^bluefin-server_([0-9A-Za-z.]+)\.raw$`)
)

// Client is the HTTP client; tests replace its sleep.
var Client = &fetch.Client{
	ConnectTimeout: 10 * time.Second,
	MaxTime:        60 * time.Second,
	MaxSize:        1 << 20,
	Retries:        2,
	Fail:           true,
}

// Origin is $BLUEFIN_BOOT_ORIGIN's output, or bootorigin.Find.
func Origin(h *sys.Host) (string, bool) {
	if helper := h.Getenv("BLUEFIN_BOOT_ORIGIN"); helper != "" {
		out, code := h.Output(false, helper)
		return out, code == 0
	}
	return bootorigin.Find(h)
}

// Check is bluefin-diskless-update-check. Test overrides:
// BLUEFIN_BOOT_ORIGIN (the origin helper; in-process when unset),
// BLUEFIN_IMPORT_KEYRING, BLUEFIN_REBOOT_SENTINEL, BLUEFIN_OS_RELEASE, and
// BLUEFIN_CMDLINE / BLUEFIN_STUB_URL_VAR, which the origin reads.
func Check(h *sys.Host, _ []string) int {
	sentinel := h.Env("BLUEFIN_REBOOT_SENTINEL", "/run/reboot-required")
	osRelease := h.Env("BLUEFIN_OS_RELEASE", "/usr/lib/os-release")
	// systemd reads /etc/systemd/import-pubring.pgp before the vendor keyring.
	keyring := h.Getenv("BLUEFIN_IMPORT_KEYRING")
	if keyring == "" {
		keyring = "/usr/lib/systemd/import-pubring.pgp"
		if sys.Exists("/etc/systemd/import-pubring.pgp") {
			keyring = "/etc/systemd/import-pubring.pgp"
		}
	}

	running := sys.ImageVersion(osRelease)
	if running == "" {
		h.Log(sys.Warning, "no IMAGE_VERSION in "+osRelease+"; not checking for updates")
		return 0
	}
	origin, ok := Origin(h)
	if !ok {
		h.Log(sys.Info, "not network booted (no boot origin); nothing to check")
		return 0
	}
	base, file := sys.Dir(origin), sys.Base(origin)
	pinned := ""
	if pinnedRE.MatchString(file) {
		pinned = file
	}

	tmp, err := os.MkdirTemp("", "tmp.")
	if err != nil {
		h.Eprintf("bluefin-diskless-update-check: %v\n", err)
		return 1
	}
	defer os.RemoveAll(tmp)

	client := *Client
	client.Sleep = h.Wait
	for _, name := range []string{"SHA256SUMS", "SHA256SUMS.gpg"} {
		resp, err := client.Do(fetch.Request{URL: base + "/" + name})
		if err == nil {
			err = os.WriteFile(filepath.Join(tmp, name), resp.Body, 0o666)
		}
		if err != nil {
			h.Eprintf("bluefin-diskless-update-check: %s/%s: %v\n", base, name, err)
			h.Log(sys.Warning, "cannot fetch SHA256SUMS and SHA256SUMS.gpg from "+base+"/; not flagging an update")
			return 0
		}
	}

	gnupg := filepath.Join(tmp, "gnupg")
	if err := os.Mkdir(gnupg, 0o700); err != nil {
		h.Eprintf("bluefin-diskless-update-check: %v\n", err)
		return 1
	}
	_, code := h.Runner.Run(sys.Cmd{
		Argv: []string{"gpgv", "--keyring", keyring, filepath.Join(tmp, "SHA256SUMS.gpg"), filepath.Join(tmp, "SHA256SUMS")},
		Env:  []string{"GNUPGHOME=" + gnupg},
	})
	if code != 0 {
		h.Log(sys.Warning, "SHA256SUMS from "+base+"/ does not verify against "+keyring+"; not flagging an update")
		return 0
	}

	sums, _ := os.ReadFile(filepath.Join(tmp, "SHA256SUMS"))
	offered := ""
	for _, line := range sys.Lines(sums, false) {
		_, name := sys.ReadRest(line)
		m := releaseRE.FindStringSubmatch(strings.TrimPrefix(name, "*"))
		if m == nil {
			continue
		}
		if offered == "" || h.VersionGT(m[1], offered, false) {
			offered = m[1]
		}
	}
	if offered == "" {
		h.Log(sys.Warning, "SHA256SUMS from "+base+"/ lists no bluefin-server_<version>.raw; not flagging an update")
		return 0
	}

	switch {
	case !h.VersionGT(offered, running, false):
		h.Log(sys.Info, "up to date: "+base+"/ offers "+offered+", running "+running)
	case pinned != "":
		h.Log(sys.Notice, "Bluefin Server "+offered+" is available from "+base+"/ (running "+running+"), but this node is pinned to "+pinned+"; not flagging "+sentinel+", point its boot origin at the new release to update")
	default:
		if err := sys.MkdirP(sys.Dir(sentinel)); err == nil {
			err = sys.Touch(sentinel)
		}
		if err != nil {
			h.Eprintf("bluefin-diskless-update-check: %v\n", err)
			return 1
		}
		h.Log(sys.Notice, "Bluefin Server "+offered+" is available from "+base+"/ (running "+running+"); flagged "+sentinel+", reboot to update")
	}
	return 0
}

// MaskUKITransfer is systemd-sysupdated.service's ExecStartPre= on diskless
// boots: no ESP is mounted, so 20-uki.transfer (PathRelativeTo=boot) cannot
// resolve $BOOT and systemd-sysupdate would refuse every verb for the host
// (#375). Diskless nodes never update their UKI, so mask that one transfer
// for this boot. --root=DIR is for tests.
func MaskUKITransfer(h *sys.Host, args []string) int {
	root := ""
	for _, arg := range args {
		r, ok := strings.CutPrefix(arg, "--root=")
		if !ok {
			h.Eprintf("bluefin-host mask-uki-transfer: unknown argument: %s\n", arg)
			return 2
		}
		root = strings.TrimSuffix(r, "/")
	}
	if !sys.Exists(root + "/run/machines/rootdisk.raw") {
		return 0
	}
	dir := root + "/run/sysupdate.d"
	if err := sys.MkdirP(dir); err == nil {
		err = sys.SymlinkForce("/dev/null", dir+"/20-uki.transfer")
		if err == nil {
			return 0
		}
		h.Eprintf("bluefin-host mask-uki-transfer: %v\n", err)
	} else {
		h.Eprintf("bluefin-host mask-uki-transfer: %v\n", err)
	}
	return 1
}
