// Package firstboot is bluefin-firstboot-credentials: apply the firstboot.*
// system credentials.
//
// It writes the same files systemd-firstboot(1) would, in the same format:
//
//	firstboot.locale, firstboot.locale-messages  -> /etc/locale.conf
//	firstboot.keymap                             -> /etc/vconsole.conf
//	firstboot.timezone                           -> /etc/localtime
//
// and additionally firstboot.hostname -> /etc/hostname plus the kernel
// hostname, from $CREDENTIALS_DIRECTORY (ImportCredential= in
// bluefin-firstboot-credentials.service). Bluefin needs firstboot.hostname
// applied unconditionally and the live kernel hostname set so the first DHCP
// request carries the name.
//
// Known differences from systemd-firstboot: trailing newlines in a credential
// are stripped rather than rejected; LC_MESSAGES= is written even when it
// equals LANG=; locales and keymaps are shape-checked, not checked against
// what is installed; timezones must exist under /usr/share/zoneinfo. The
// validators are at least as strict as systemd's and ASCII-only.
//
// Every value is validated before anything is written; an invalid credential
// fails the whole run and leaves /etc untouched.
package firstboot

import (
	"bytes"
	"errors"
	"os"
	"regexp"
	"syscall"

	"github.com/projectbluefin/server/files/homelab/cluster/internal/host/hostname"
	"github.com/projectbluefin/server/files/homelab/cluster/internal/host/sys"
)

const prog = "bluefin-firstboot-credentials"

// At least as strict as systemd's locale_is_valid(), keymap_is_valid(),
// timezone_is_valid() and hostname_is_valid(); length caps match theirs.
var (
	localeRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.@+-]*$`)
	keymapRE   = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9_.-]*$`)
	timezoneRE = regexp.MustCompile(`^[A-Za-z0-9_+-]+(/[A-Za-z0-9_+-]+)*$`)
	hostnameRE = regexp.MustCompile(`^[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?(\.[A-Za-z0-9]([A-Za-z0-9-]*[A-Za-z0-9])?)*$`)
)

// Main is bluefin-firstboot-credentials [--root=DIR]; --root is for tests.
func Main(h *sys.Host, args []string) int {
	root, ok := hostname.ParseRoot(h, prog, args)
	if !ok {
		return 2
	}
	log := func(msg string) { h.Eprintf("%s: %s\n", prog, msg) }
	if err := apply(h, root, log); err != nil {
		log(err.Error())
		return 1
	}
	return 0
}

// read is the credential with trailing newlines removed, or "" when it is
// absent or empty. systemd's read_credential() rejects NUL bytes.
func read(creds, name string) (string, error) {
	file := creds + "/" + name
	if !sys.NonEmpty(file) {
		return "", nil
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return "", err
	}
	if bytes.IndexByte(data, 0) >= 0 {
		return "", errors.New("NUL byte in credential " + name)
	}
	return sys.Subst(data), nil
}

func apply(h *sys.Host, root string, log func(string)) error {
	creds := h.Getenv("CREDENTIALS_DIRECTORY")
	if creds == "" {
		return errors.New("CREDENTIALS_DIRECTORY is not set; run from a unit with ImportCredential=firstboot.*")
	}
	if !sys.IsDir(creds) {
		return errors.New("CREDENTIALS_DIRECTORY " + creds + " is not a directory")
	}
	var values [5]string
	for i, name := range []string{"firstboot.locale", "firstboot.locale-messages", "firstboot.keymap", "firstboot.timezone", "firstboot.hostname"} {
		v, err := read(creds, name)
		if err != nil {
			return err
		}
		values[i] = v
	}
	locale, messages, keymap, timezone, host := values[0], values[1], values[2], values[3], values[4]

	invalid := func(what, value string) error {
		return errors.New("invalid " + what + " in credential: " + sys.Quote(value))
	}
	for _, v := range []string{locale, messages} {
		if v != "" && !(localeRE.MatchString(v) && len(v) < 128) {
			return invalid("locale", v)
		}
	}
	if keymap != "" && !(keymapRE.MatchString(keymap) && len(keymap) < 128) {
		return invalid("keymap", keymap)
	}
	if timezone != "" && !(timezoneRE.MatchString(timezone) && sys.IsFile(root+"/usr/share/zoneinfo/"+timezone)) {
		return invalid("or unknown timezone", timezone)
	}
	if host != "" && !(hostnameRE.MatchString(host) && len(host) <= 64) {
		return invalid("hostname", host)
	}

	syscall.Umask(0o022)
	etc := root + "/etc"
	if err := sys.MkdirP(etc); err != nil {
		return err
	}
	if locale != "" || messages != "" {
		var conf string
		if locale != "" {
			conf += "LANG=" + locale + "\n"
		}
		if messages != "" {
			conf += "LC_MESSAGES=" + messages + "\n"
		}
		if err := sys.WriteFile(etc+"/locale.conf", []byte(conf)); err != nil {
			return err
		}
		log("wrote /etc/locale.conf")
	}
	if keymap != "" {
		if err := sys.WriteFile(etc+"/vconsole.conf", []byte("KEYMAP="+keymap+"\n")); err != nil {
			return err
		}
		log("wrote /etc/vconsole.conf")
	}
	if timezone != "" {
		if err := sys.SymlinkForce("../usr/share/zoneinfo/"+timezone, etc+"/localtime"); err != nil {
			return err
		}
		log("set /etc/localtime -> " + timezone)
	}
	if host != "" {
		if err := sys.WriteFile(etc+"/hostname", []byte(host+"\n")); err != nil {
			return err
		}
		// PID 1 already applied the image's /etc/hostname before this unit
		// ran, so also set the live kernel hostname for the DHCP request
		// networkd is about to send.
		if root == "" {
			if err := hostname.SetKernel(host); err != nil {
				return err
			}
		}
		log("set hostname " + host)
	}

	// The unit's ExecStartPost stamp gates re-runs; flush the writes above
	// before it is created so a power loss cannot leave the stamp without
	// the files.
	syscall.Sync()
	return nil
}
