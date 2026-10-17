// Package sysextfetch is bluefin-sysext-fetch: fetch the sysexts of the
// sysupdate features this node enabled, for the booted image version, and
// merge them.
//
// A provisioning config (the homelab Ignition templates) enables optional
// sysupdate features with /etc/sysupdate.d/<feature>.feature.d/*.conf
// (Enabled=true) and links bluefin-sysext-fetch.service. The version-locked
// sysexts then follow OS updates through systemd-sysupdate, but nothing
// fetches them for the version that is already running: this does, once per
// image version. The image file each enabled feature's transfer installs comes
// from the first source that has it:
//  1. seed: <ESP>/bluefin/extensions/, which the USB installer's Homelab
//     entries copy onto the installed disk; removed from the ESP once used;
//  2. diskless node: the directory it booted from (bluefin-boot-origin);
//  3. installed node: the release, through plain `systemd-sysupdate update`.
//     If the release is newer it installs that version instead, and the
//     node reboots into it.
//
// 1 and 2 run systemd-sysupdate against copies of the feature transfers in
// /run whose Source Path= is that directory and whose Features= line is
// dropped. The boot server's and the release's SHA256SUMS must be signed
// (Verify=yes); the seed's is not (Verify=no): it was copied from the
// installer onto this disk, so it is as trustworthy as the disk itself.
// Afterwards `systemd-sysext refresh` merges what arrived and
// bluefin-sysext-activate.service starts the units the merged sysexts enable.
package sysextfetch

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/projectbluefin/server/files/homelab/cluster/internal/host/diskless"
	"github.com/projectbluefin/server/files/homelab/cluster/internal/host/sys"
	"github.com/projectbluefin/server/files/homelab/cluster/internal/host/updatepending"
)

const prog = "bluefin-sysext-fetch"

var (
	enabledRE  = regexp.MustCompile(`^[ \t\n\v\f\r]*Enabled[ \t\n\v\f\r]*=[ \t\n\v\f\r]*`)
	featuresRE = regexp.MustCompile(`^[ \t\n\v\f\r]*Features[ \t\n\v\f\r]*=`)
	verifyRE   = regexp.MustCompile(`^[ \t\n\v\f\r]*Verify[ \t\n\v\f\r]*=`)
	pathRE     = regexp.MustCompile(`^[ \t\n\v\f\r]*Path[ \t\n\v\f\r]*=.*`)
)

type transfer struct{ file, target, source string }

// sedRange is a sed /^\[<section>\]/,/^\[/ address: it opens on a line
// starting with [<section>] and closes on the next line starting with "[".
type sedRange struct {
	start string
	open  bool
}

func (r *sedRange) match(line string) bool {
	if !r.open {
		r.open = strings.HasPrefix(line, r.start)
		return r.open
	}
	if strings.HasPrefix(line, "[") {
		r.open = false
	}
	return true
}

// ini is the last value of key in [section] of file, as
// sed -n "/^\[S\]/,/^\[/{s/^[[:space:]]*K[[:space:]]*=[[:space:]]*//p}" | tail -n 1.
func ini(section, key string, data []byte) string {
	re := regexp.MustCompile(`^[ \t\n\v\f\r]*` + regexp.QuoteMeta(key) + `[ \t\n\v\f\r]*=[ \t\n\v\f\r]*`)
	r := sedRange{start: "[" + section + "]"}
	value := ""
	for _, line := range sys.Lines(data, true) {
		if !r.match(line) {
			continue
		}
		if loc := re.FindStringIndex(line); loc != nil {
			value = line[loc[1]:]
		}
	}
	return value
}

// rewrite is the transfer with Features= and Verify= dropped, Verify=<verify>
// after [Transfer] and [Source] Path=<url>.
func rewrite(data []byte, url, verify string) []byte {
	terminated := len(data) > 0 && data[len(data)-1] == '\n'
	lines := sys.Lines(data, true)
	r := sedRange{start: "[Source]"}
	var b strings.Builder
	for i, line := range lines {
		if featuresRE.MatchString(line) || verifyRE.MatchString(line) {
			continue
		}
		if rest, ok := strings.CutPrefix(line, "[Transfer]"); ok {
			line = "[Transfer]\nVerify=" + verify + rest
		}
		if r.match(line) && pathRE.MatchString(line) {
			line = "Path=" + url
		}
		b.WriteString(line)
		if i < len(lines)-1 || terminated {
			b.WriteByte('\n')
		}
	}
	return []byte(b.String())
}

// Main is bluefin-sysext-fetch. Test overrides: BLUEFIN_SYSUPDATE_DIRS
// (feature drop-in and transfer search path, highest priority first),
// BLUEFIN_OS_RELEASE, BLUEFIN_ESP, BLUEFIN_ROOTDISK, BLUEFIN_BOOT_ORIGIN,
// BLUEFIN_UPDATE_PENDING, BLUEFIN_SYSEXT_FETCH_RUN; commands
// (systemd-sysupdate, systemd-sysext, systemctl, bootctl) through PATH.
func Main(h *sys.Host, _ []string) int {
	log := func(msg string) { h.Printf("%s: %s\n", prog, msg) }
	die := func(msg string) int {
		h.Eprintf("%s: %s\n", prog, msg)
		return 1
	}
	dirs := sys.Fields(h.Env("BLUEFIN_SYSUPDATE_DIRS", "/etc/sysupdate.d /run/sysupdate.d /usr/local/lib/sysupdate.d /usr/lib/sysupdate.d"))
	osRelease := h.Env("BLUEFIN_OS_RELEASE", "/usr/lib/os-release")
	rootdisk := h.Env("BLUEFIN_ROOTDISK", "/run/machines/rootdisk.raw")
	run := h.Env("BLUEFIN_SYSEXT_FETCH_RUN", h.Env("RUNTIME_DIRECTORY", "/run/bluefin-sysext-fetch"))

	data, err := os.ReadFile(osRelease)
	if err != nil {
		return die(err.Error())
	}
	version := ""
	for _, line := range sys.Lines(data, false) {
		if key, value := sys.SplitEq(line); key == "IMAGE_VERSION" {
			version = sys.Unquote(value)
		}
	}
	if version == "" {
		return die("no IMAGE_VERSION in " + osRelease)
	}

	// Drop-ins in higher-priority directories, and later ones in a
	// directory, win.
	state := map[string]string{}
	for n := len(dirs) - 1; n >= 0; n-- {
		for _, d := range sys.Glob(dirs[n], "*.feature.d") {
			for _, conf := range sys.Glob(d, "*.conf") {
				if !sys.Exists(conf) {
					continue
				}
				name := strings.TrimSuffix(filepath.Base(d), ".feature.d")
				data, _ := os.ReadFile(conf)
				v := ""
				for _, line := range sys.Lines(data, true) {
					if loc := enabledRE.FindStringIndex(line); loc != nil {
						v = line[loc[1]:]
					}
				}
				if v != "" {
					state[name] = sys.LowerASCII(v)
				}
			}
		}
	}
	var enabled []string
	for name, v := range state {
		switch v {
		case "1", "yes", "y", "true", "t", "on":
			enabled = append(enabled, name)
		}
	}
	if len(enabled) == 0 {
		log("no optional sysupdate feature is enabled; nothing to fetch")
		return 0
	}
	sort.Strings(enabled)
	features := strings.Join(enabled, " ")

	var transfers []transfer
	seen := map[string]bool{}
	for _, d := range dirs {
		for _, t := range sys.Glob(d, "*.transfer") {
			if !sys.Exists(t) || seen[filepath.Base(t)] {
				continue
			}
			seen[filepath.Base(t)] = true
			data, _ := os.ReadFile(t)
			want := false
			for _, f := range sys.Fields(ini("Transfer", "Features", data)) {
				for _, e := range enabled {
					want = want || f == e
				}
			}
			if !want {
				continue
			}
			path, target, source := ini("Target", "Path", data), ini("Target", "MatchPattern", data), ini("Source", "MatchPattern", data)
			if path == "" || target == "" || source == "" {
				return die(t + ": needs [Target] Path=, MatchPattern= and [Source] MatchPattern=")
			}
			transfers = append(transfers, transfer{
				file:   t,
				target: strings.TrimSuffix(path, "/") + "/" + strings.ReplaceAll(target, "@v", version),
				source: strings.ReplaceAll(source, "@v", version),
			})
		}
	}
	if len(transfers) == 0 {
		log("enabled features " + features + " have no transfers; nothing to fetch")
		return 0
	}

	missing := func() []transfer {
		var need []transfer
		for _, t := range transfers {
			if !sys.Exists(t.target) {
				need = append(need, t)
			}
		}
		return need
	}
	list := func(need []transfer, field func(transfer) string) string {
		var b strings.Builder
		for _, t := range need {
			b.WriteString(field(t) + " ")
		}
		return b.String()
	}
	// fetchFrom runs systemd-sysupdate with copies of those transfers that
	// download from url regardless of their features.
	fetchFrom := func(dir, url, verify string, need []transfer) int {
		defs := run + "/" + dir
		if err := os.RemoveAll(defs); err != nil {
			return die(err.Error())
		}
		if err := sys.MkdirP(defs); err != nil {
			return die(err.Error())
		}
		for _, t := range need {
			data, err := os.ReadFile(t.file)
			if err == nil {
				err = sys.WriteFile(defs+"/"+filepath.Base(t.file), rewrite(data, url, verify))
			}
			if err != nil {
				return die(err.Error())
			}
		}
		return h.Exec("systemd-sysupdate", "--definitions="+defs, "update")
	}

	need := missing()
	if len(need) == 0 {
		log("the " + features + " sysext(s) for " + version + " are already installed")
		return 0
	}
	log("fetching for " + version + ": " + list(need, func(t transfer) string { return filepath.Base(t.target) }))
	mode := ""

	// 1. The installer's seed on the ESP: only what its manifest lists.
	esp := h.Getenv("BLUEFIN_ESP")
	if esp == "" {
		esp, _ = h.Output(true, "bootctl", "--print-esp-path")
	}
	seed := ""
	if esp != "" {
		seed = strings.TrimSuffix(esp, "/") + "/bluefin/extensions"
	}
	if seed != "" && sys.NonEmpty(seed+"/SHA256SUMS") {
		sums, _ := os.ReadFile(seed + "/SHA256SUMS")
		var fromSeed []transfer
		for _, t := range need {
			re, err := regexp.Compile(`^[0-9a-f]{64} [ *]` + strings.ReplaceAll(t.source, ".", `\.`) + `$`)
			if err != nil {
				continue
			}
			for _, line := range sys.Lines(sums, true) {
				if re.MatchString(line) {
					fromSeed = append(fromSeed, t)
					break
				}
			}
		}
		if len(fromSeed) > 0 {
			log("installing from the installer's copy in " + seed + "/")
			if rc := fetchFrom("seed", "file://"+seed+"/", "no", fromSeed); rc != 0 {
				return rc
			}
			mode = "seed"
		}
		// The ESP is small and holds the A/B UKIs; the copy is not needed again.
		if err := os.RemoveAll(seed); err != nil {
			return die(err.Error())
		}
		need = missing()
	}

	if len(need) > 0 {
		if sys.Exists(rootdisk) {
			// 2. A diskless node: the boot server's copy of this release.
			origin, ok := diskless.Origin(h)
			if !ok {
				return die("diskless, but no boot origin to fetch " + list(need, func(t transfer) string { return t.source }) + "from")
			}
			log("fetching from the boot server " + sys.Dir(origin) + "/")
			if rc := fetchFrom("origin", sys.Dir(origin)+"/", "yes", need); rc != 0 {
				return rc
			}
			mode = "origin"
		} else {
			// 3. An installed node: the release, as the update timer would.
			log("fetching through systemd-sysupdate (features: " + features + ")")
			if rc := h.Exec("systemd-sysupdate", "update"); rc != 0 {
				return rc
			}
			mode = "release"
		}
		need = missing()
	}

	if len(need) > 0 {
		if mode == "release" && pending(h) {
			log("the release is newer than " + version + "; its sysexts arrived with it, rebooting into it")
			if rc := h.Exec("systemctl", "reboot"); rc != 0 {
				return rc
			}
			return 0
		}
		return die("still missing: " + list(need, func(t transfer) string { return t.target }))
	}

	for _, argv := range [][]string{
		{"systemd-sysext", "refresh"},
		{"systemctl", "daemon-reload"},
		// Start what the merged sysexts enable: at boot this joins the start
		// job that waits for this unit (Before=); after a retry it starts it
		// again.
		{"systemctl", "start", "--no-block", "bluefin-sysext-activate.service"},
	} {
		if rc := h.Exec(argv...); rc != 0 {
			return rc
		}
	}
	log("merged the " + features + " sysext(s) for " + version + " (" + mode + ")")
	return 0
}

func pending(h *sys.Host) bool {
	if helper := h.Getenv("BLUEFIN_UPDATE_PENDING"); helper != "" {
		return h.Exec(helper) == 0
	}
	return updatepending.Main(h, nil) == 0
}
