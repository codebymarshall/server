package updatepending

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/projectbluefin/server/files/homelab/cluster/internal/host/hosttest"
)

// newer is systemd-analyze compare-versions for the dotted numbers used here.
func newer(a, b string) bool {
	pa, pb := strings.Split(a, "."), strings.Split(b, ".")
	for i := 0; i < len(pa) && i < len(pb); i++ {
		if len(pa[i]) != len(pb[i]) {
			return len(pa[i]) > len(pb[i])
		}
		if pa[i] != pb[i] {
			return pa[i] > pb[i]
		}
	}
	return len(pa) > len(pb)
}

func host(t *testing.T, pending bool, ukis ...string) (*hosttest.Host, string) {
	t.Helper()
	dir := t.TempDir()
	linux := filepath.Join(dir, "boot", "EFI", "Linux")
	if err := os.MkdirAll(linux, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, u := range ukis {
		if err := os.WriteFile(filepath.Join(linux, u), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	osRelease := filepath.Join(dir, "os-release")
	if err := os.WriteFile(osRelease, []byte("IMAGE_VERSION=\"26.09.3\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{
		"BLUEFIN_SYSUPDATE":       "sysupdate",
		"BLUEFIN_BOOT_PATH":       filepath.Join(dir, "boot"),
		"BLUEFIN_OS_RELEASE":      osRelease,
		"BLUEFIN_REBOOT_SENTINEL": filepath.Join(dir, "reboot-required"),
	}
	return hosttest.New(env, func(argv []string) (string, int) {
		switch {
		case argv[0] == "sysupdate" && argv[1] == "pending":
			if pending {
				return "", 0
			}
			return "", 1
		case argv[0] == "systemd-analyze" && argv[1] == "compare-versions" && argv[3] == "gt":
			if newer(argv[2], argv[4]) {
				return "", 0
			}
			return "", 1
		}
		return "", 127
	}), env["BLUEFIN_REBOOT_SENTINEL"]
}

func TestPending(t *testing.T) {
	for _, tc := range []struct {
		name    string
		pending bool
		ukis    []string
		want    int
		says    string
	}{
		{"update with tries left", true, []string{"bluefin-server-26.09.4+2-1.efi", "bluefin-server-26.09.3.efi"}, 0, ""},
		{"nothing pending", false, []string{"bluefin-server-26.09.3.efi"}, 1, ""},
		{"failed tries, done counter", true, []string{"bluefin-server-26.09.4+0-3.efi", "bluefin-server-26.09.3.efi"}, 1,
			"Bluefin Server 26.09.4 is installed but failed its boot tries (bluefin-server-26.09.4+0-3.efi); staying on 26.09.3 until a newer release replaces it\n"},
		{"failed tries, plain counter", true, []string{"bluefin-server-26.09.10+0.efi"}, 1, "(bluefin-server-26.09.10+0.efi)"},
		{"older exhausted UKI", true, []string{"bluefin-server-26.09.2+0-3.efi", "bluefin-server-26.09.5+3-0.efi"}, 0, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, _ := host(t, tc.pending, tc.ukis...)
			if got := Main(h.Host, nil); got != tc.want {
				t.Errorf("exit %d; want %d", got, tc.want)
			}
			if !strings.Contains(h.Out.String(), tc.says) || (tc.says == "" && h.Out.Len() != 0) {
				t.Errorf("stdout %q", h.Out.String())
			}
		})
	}
}

func TestKuredFlag(t *testing.T) {
	for _, tc := range []struct {
		pending bool
		uki     string
		flagged bool
	}{
		{true, "bluefin-server-26.09.4+2-1.efi", true},
		{false, "bluefin-server-26.09.4+2-1.efi", false},
		{true, "bluefin-server-26.09.4+0-3.efi", false},
	} {
		h, sentinel := host(t, tc.pending, tc.uki)
		if got := KuredFlag(h.Host, nil); got != 0 {
			t.Errorf("exit %d", got)
		}
		if _, err := os.Stat(sentinel); (err == nil) != tc.flagged {
			t.Errorf("%+v: flagged=%v", tc, err == nil)
		}
	}
}
