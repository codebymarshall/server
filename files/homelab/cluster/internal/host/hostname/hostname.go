// Package hostname is bluefin-hostname: give a node without a static
// hostname a unique one.
//
// A node whose static hostname (/etc/hostname) is unset or "localhost" gets,
// in this order: the firstboot.hostname credential, the system.hostname
// credential (PID 1 only applies it as the transient hostname), or
// bluefin-<first 8 hex digits of /etc/machine-id>, so appliances on one LAN
// never collide over mDNS. It writes /etc/hostname and sets the kernel
// hostname, before networkd's first DHCP request and before resolved
// announces the name.
//
// A static hostname that is already set is kept. An invalid one, or an
// invalid hostname credential, fails the run: nothing is written and no other
// name is substituted, so the mistake shows in `systemctl --failed`.
//
// bluefin-hostname.service runs it on first boot; the homelab's
// bluefin-cluster runs /usr/libexec/bluefin-hostname when a node is still
// called localhost.
package hostname

import (
	"os"
	"regexp"
	"strings"
	"syscall"

	"github.com/projectbluefin/server/files/homelab/cluster/internal/host/sys"
)

const prog = "bluefin-hostname"

var (
	// At least as strict as systemd's hostname_is_valid(): dot-separated
	// labels of letters, digits and inner hyphens, at most 63 characters
	// each and 64 in total.
	hostnameRE  = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]{0,61}[A-Za-z0-9])?)*$`)
	machineIDRE = regexp.MustCompile(`^[0-9a-f]{32}$`)
)

func valid(s string) bool { return hostnameRE.MatchString(s) && len(s) <= 64 }

// unnamed is systemd's is_localhost(): "localhost" and its variants mean
// "not named yet".
func unnamed(s string) bool {
	h := strings.TrimSuffix(sys.LowerASCII(s), ".")
	return h == "" || h == "localhost" || h == "localhost.localdomain" ||
		strings.HasSuffix(h, ".localhost") || strings.HasSuffix(h, ".localhost.localdomain")
}

// Static is the first line of /etc/hostname that is not a comment, trimmed,
// as systemd reads it.
func Static(file string) string {
	if !sys.IsFile(file) {
		return ""
	}
	data, _ := os.ReadFile(file)
	for _, line := range sys.Lines(data, true) {
		line = sys.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		return line
	}
	return ""
}

// ParseRoot reads the helpers' only argument, --root=DIR (tests: read and
// write DIR/etc and leave the kernel alone).
func ParseRoot(h *sys.Host, prog string, args []string) (string, bool) {
	root := ""
	for _, arg := range args {
		r, ok := strings.CutPrefix(arg, "--root=")
		if !ok {
			h.Eprintf("%s: unknown argument: %s\n", prog, arg)
			return "", false
		}
		root = strings.TrimSuffix(r, "/")
	}
	return root, true
}

// SetKernel writes name to /proc/sys/kernel/hostname when it is writable.
func SetKernel(name string) error {
	const proc = "/proc/sys/kernel/hostname"
	if !sys.Writable(proc) {
		return nil
	}
	return sys.WriteFile(proc, []byte(name))
}

// Main is bluefin-hostname [--root=DIR].
func Main(h *sys.Host, args []string) int {
	root, ok := ParseRoot(h, prog, args)
	if !ok {
		return 2
	}
	log := func(msg string) { h.Eprintf("%s: %s\n", prog, msg) }
	die := func(msg string) int { log(msg); return 1 }

	current := Static(root + "/etc/hostname")
	if !unnamed(current) {
		if !valid(current) {
			return die("invalid static hostname in /etc/hostname: " + sys.Quote(current) + "; set one with hostnamectl set-hostname")
		}
		log("keeping static hostname " + current)
		return 0
	}

	name, source := "", ""
	if creds := h.Getenv("CREDENTIALS_DIRECTORY"); creds != "" {
		for _, cred := range []string{"firstboot.hostname", "system.hostname"} {
			file := creds + "/" + cred
			if !sys.NonEmpty(file) {
				continue
			}
			value, _ := sys.ReadSubst(file)
			if value == "" {
				continue
			}
			if !valid(value) {
				return die("invalid hostname in credential " + cred + ": " + sys.Quote(value))
			}
			name, source = value, "credential "+cred
			break
		}
	}
	if name == "" {
		id, _ := sys.ReadSubst(root + "/etc/machine-id")
		if !machineIDRE.MatchString(id) {
			return die("no machine ID in /etc/machine-id to derive a hostname from")
		}
		name, source = "bluefin-"+id[:8], "machine ID"
	}

	syscall.Umask(0o022)
	etc := root + "/etc"
	err := sys.MkdirP(etc)
	if err == nil {
		err = sys.WriteFile(etc+"/hostname.bluefin-new", []byte(name+"\n"))
	}
	if err == nil {
		err = os.Rename(etc+"/hostname.bluefin-new", etc+"/hostname")
	}
	if err == nil && root == "" {
		err = SetKernel(name)
	}
	if err != nil {
		return die(err.Error())
	}
	log("set hostname " + name + " (from " + source + ")")
	return 0
}
