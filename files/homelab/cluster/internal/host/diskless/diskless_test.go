package diskless

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/projectbluefin/server/files/homelab/cluster/internal/host/hosttest"
)

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

func sums(versions ...string) string {
	var b strings.Builder
	for _, v := range versions {
		b.WriteString(strings.Repeat("0", 64) + " *bluefin-server_" + v + ".raw\n")
		b.WriteString(strings.Repeat("1", 64) + "  bluefin-server-netboot_" + v + ".efi\n")
	}
	return b.String()
}

func check(t *testing.T, files map[string]string, origin string, verifies bool) (h *hosttest.Host, flagged bool, url string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, ok := files[strings.TrimPrefix(r.URL.Path, "/rel/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	dir := t.TempDir()
	osRelease := filepath.Join(dir, "os-release")
	if err := os.WriteFile(osRelease, []byte("IMAGE_VERSION=\"26.09.2\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	sentinel := filepath.Join(dir, "run", "reboot-required")
	env := map[string]string{
		"BLUEFIN_OS_RELEASE":      osRelease,
		"BLUEFIN_REBOOT_SENTINEL": sentinel,
		"BLUEFIN_IMPORT_KEYRING":  "/keyring.pgp",
		"BLUEFIN_BOOT_ORIGIN":     "origin",
	}
	h = hosttest.New(env, func(argv []string) (string, int) {
		switch argv[0] {
		case "origin":
			if origin == "" {
				return "", 1
			}
			return strings.ReplaceAll(origin, "URL", srv.URL) + "\n", 0
		case "gpgv":
			if verifies {
				return "", 0
			}
			return "", 1
		case "systemd-analyze":
			if newer(argv[2], argv[4]) {
				return "", 0
			}
			return "", 1
		}
		return "", 127
	})
	if code := Check(h.Host, nil); code != 0 {
		t.Fatalf("exit %d", code)
	}
	_, err := os.Stat(sentinel)
	return h, err == nil, srv.URL
}

func TestCheck(t *testing.T) {
	signed := func(s string) map[string]string { return map[string]string{"SHA256SUMS": s, "SHA256SUMS.gpg": "sig"} }
	for _, tc := range []struct {
		name     string
		files    map[string]string
		origin   string
		verifies bool
		flag     bool
		log      string
	}{
		{"newer release", signed(sums("26.09.2", "26.09.10")), "URL/rel/bluefin-server-netboot.efi", true, true,
			"<5>Bluefin Server 26.09.10 is available from URL/rel/ (running 26.09.2); flagged SENTINEL, reboot to update\n"},
		{"pinned", signed(sums("26.09.10")), "URL/rel/bluefin-server_26.09.2.raw", true, false,
			"<5>Bluefin Server 26.09.10 is available from URL/rel/ (running 26.09.2), but this node is pinned to bluefin-server_26.09.2.raw; not flagging SENTINEL, point its boot origin at the new release to update\n"},
		{"up to date", signed(sums("26.09.1")), "URL/rel/x.efi", true, false, "<6>up to date: URL/rel/ offers 26.09.1, running 26.09.2\n"},
		{"does not verify", signed(sums("26.09.10")), "URL/rel/x.efi", false, false, "<4>SHA256SUMS from URL/rel/ does not verify against /keyring.pgp; not flagging an update\n"},
		{"unsigned", map[string]string{"SHA256SUMS": sums("26.09.10")}, "URL/rel/x.efi", true, false, "<4>cannot fetch SHA256SUMS and SHA256SUMS.gpg from URL/rel/; not flagging an update\n"},
		{"no release", signed("abc  zfs_1.raw\n"), "URL/rel/x.efi", true, false, "<4>SHA256SUMS from URL/rel/ lists no bluefin-server_<version>.raw; not flagging an update\n"},
		{"not network booted", nil, "", true, false, "<6>not network booted (no boot origin); nothing to check\n"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			h, flagged, url := check(t, tc.files, tc.origin, tc.verifies)
			for _, c := range h.Fake.Cmds() {
				if c.Argv[0] == "gpgv" && (len(c.Env) != 1 || !strings.HasPrefix(c.Env[0], "GNUPGHOME=")) {
					t.Errorf("gpgv env %v", c.Env)
				}
			}
			want := strings.NewReplacer("URL", url, "SENTINEL", h.Getenv("BLUEFIN_REBOOT_SENTINEL")).Replace(tc.log)
			if flagged != tc.flag || h.Out.String() != want {
				t.Errorf("flag %v\n got %q\nwant %q", flagged, h.Out.String(), want)
			}
		})
	}
}

func TestMaskUKITransfer(t *testing.T) {
	for _, diskless := range []bool{true, false} {
		root := t.TempDir()
		if diskless {
			if err := os.MkdirAll(filepath.Join(root, "run", "machines"), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(root, "run", "machines", "rootdisk.raw"), nil, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		for i := 0; i < 2; i++ {
			h := hosttest.New(nil, nil)
			if code := MaskUKITransfer(h.Host, []string{"--root=" + root + "/"}); code != 0 {
				t.Fatalf("exit %d: %s", code, h.Err)
			}
		}
		target, err := os.Readlink(filepath.Join(root, "run", "sysupdate.d", "20-uki.transfer"))
		if diskless != (err == nil && target == "/dev/null") {
			t.Errorf("diskless=%v: %q %v", diskless, target, err)
		}
	}
	if code := MaskUKITransfer(hosttest.New(nil, nil).Host, []string{"--bogus"}); code != 2 {
		t.Errorf("unknown argument: %d", code)
	}
}
