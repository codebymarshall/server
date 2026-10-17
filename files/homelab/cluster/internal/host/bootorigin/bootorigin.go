// Package bootorigin is bluefin-boot-origin: the URL a network-booted node
// was booted from. Sources, first match wins:
//  1. the UEFI HTTP boot URL systemd-stub records in the StubDeviceURL EFI
//     variable;
//  2. the explicit http(s) source of rd.systemd.pull= on the kernel command
//     line (a node chainloaded by iPXE);
//  3. the http(s) source of the import.pull system credential, when the
//     calling unit imports it.
//
// The initrd keeps its own shell copy (files/boot-origin) for
// bluefin-ignition-credentials; this is the booted OS's.
package bootorigin

import (
	"bytes"
	"os"
	"strings"

	"github.com/projectbluefin/server/files/homelab/cluster/internal/host/sys"
)

const stubVar = "/sys/firmware/efi/efivars/StubDeviceURL-4a67b082-0a4c-41cf-b6c7-440b29bb8c4f"

// Find returns the boot origin; ok is false when the node was not network
// booted. BLUEFIN_STUB_URL_VAR and BLUEFIN_CMDLINE override the inputs.
func Find(h *sys.Host) (string, bool) {
	stub := h.Env("BLUEFIN_STUB_URL_VAR", stubVar)
	if sys.Readable(stub) {
		// efivarfs: 4 attribute bytes, then a NUL-terminated UTF-16LE string.
		if data, err := os.ReadFile(stub); err == nil && len(data) > 4 {
			if url := sys.Subst(bytes.ReplaceAll(data[4:], []byte{0}, nil)); url != "" {
				return url, true
			}
		}
	}
	cmdline := h.Env("BLUEFIN_CMDLINE", "/proc/cmdline")
	if sys.Readable(cmdline) {
		data, _ := os.ReadFile(cmdline)
		for _, arg := range sys.Fields(string(data)) {
			if v, ok := strings.CutPrefix(arg, "rd.systemd.pull="); ok {
				if url, ok := pullURL(v); ok {
					return url, true
				}
			}
		}
	}
	cred := h.Env("CREDENTIALS_DIRECTORY", "/nonexistent") + "/import.pull"
	if sys.Readable(cred) {
		data, _ := os.ReadFile(cred)
		first, _, _ := strings.Cut(string(data), "\n")
		if url, ok := pullURL(sys.TrimIFS(first)); ok {
			return url, true
		}
	}
	return "", false
}

// pullURL is the http(s) source of <options>:<name>:<url>, the syntax
// rd.systemd.pull= and import.pull share.
func pullURL(s string) (string, bool) {
	if !strings.Contains(s, ":http://") && !strings.Contains(s, ":https://") {
		return "", false
	}
	return s[strings.Index(s, ":http")+1:], true
}

// Main is bluefin-boot-origin: prints the origin, or exits 1 silently.
func Main(h *sys.Host, _ []string) int {
	url, ok := Find(h)
	if !ok {
		return 1
	}
	h.Printf("%s\n", url)
	return 0
}
