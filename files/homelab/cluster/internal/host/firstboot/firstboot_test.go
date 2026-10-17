package firstboot

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/projectbluefin/server/files/homelab/cluster/internal/host/hosttest"
)

func applyCreds(t *testing.T, creds map[string]string) (root string, h *hosttest.Host, code int) {
	t.Helper()
	dir := t.TempDir()
	root = filepath.Join(dir, "root")
	credDir := filepath.Join(dir, "creds")
	for _, d := range []string{filepath.Join(root, "etc"), filepath.Join(root, "usr", "share", "zoneinfo", "Europe"), credDir} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "usr", "share", "zoneinfo", "Europe", "Berlin"), []byte("TZif2"), 0o644); err != nil {
		t.Fatal(err)
	}
	for k, v := range creds {
		if err := os.WriteFile(filepath.Join(credDir, k), []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	h = hosttest.New(map[string]string{"CREDENTIALS_DIRECTORY": credDir}, nil)
	return root, h, Main(h.Host, []string{"--root=" + root})
}

func TestAllCredentials(t *testing.T) {
	root, h, code := applyCreds(t, map[string]string{
		"firstboot.locale":          "de_DE.UTF-8\n",
		"firstboot.locale-messages": "en_US.UTF-8",
		"firstboot.keymap":          "de-latin1\n",
		"firstboot.timezone":        "Europe/Berlin\n",
		"firstboot.hostname":        "node-01.lab.example\n\n",
	})
	if code != 0 {
		t.Fatalf("exit %d: %s", code, h.Err)
	}
	for name, want := range map[string]string{
		"locale.conf":   "LANG=de_DE.UTF-8\nLC_MESSAGES=en_US.UTF-8\n",
		"vconsole.conf": "KEYMAP=de-latin1\n",
		"hostname":      "node-01.lab.example\n",
	} {
		if got, _ := os.ReadFile(filepath.Join(root, "etc", name)); string(got) != want {
			t.Errorf("%s = %q", name, got)
		}
	}
	if got, _ := os.Readlink(filepath.Join(root, "etc", "localtime")); got != "../usr/share/zoneinfo/Europe/Berlin" {
		t.Errorf("localtime -> %q", got)
	}
	want := "bluefin-firstboot-credentials: wrote /etc/locale.conf\n" +
		"bluefin-firstboot-credentials: wrote /etc/vconsole.conf\n" +
		"bluefin-firstboot-credentials: set /etc/localtime -> Europe/Berlin\n" +
		"bluefin-firstboot-credentials: set hostname node-01.lab.example\n"
	if h.Err.String() != want {
		t.Errorf("log %q", h.Err.String())
	}
}

func TestInvalidValuesWriteNothing(t *testing.T) {
	for creds, msg := range map[string]string{
		"firstboot.hostname=bad host":                  "invalid hostname in credential: bad\\ host\n",
		"firstboot.hostname=host\r":                    "invalid hostname in credential: $'host\\r'\n",
		"firstboot.hostname=foo..bar":                  "invalid hostname",
		"firstboot.timezone=../../etc/passwd":          "invalid or unknown timezone in credential: ../../etc/passwd\n",
		"firstboot.timezone=Mars/Olympus":              "invalid or unknown timezone",
		"firstboot.locale=en_US.UTF-8; rm -rf /":       "invalid locale in credential: en_US.UTF-8\\;\\ rm\\ -rf\\ /\n",
		"firstboot.keymap=h\u00f4te":                   "invalid keymap in credential: $'h\\303\\264te'\n",
		"firstboot.keymap=" + strings.Repeat("k", 128): "invalid keymap",
		"firstboot.hostname=host\x00name":              "NUL byte in credential firstboot.hostname\n",
	} {
		name, value, _ := strings.Cut(creds, "=")
		root, h, code := applyCreds(t, map[string]string{"firstboot.hostname": "good", name: value})
		if code != 1 || !strings.Contains(h.Err.String(), msg) {
			t.Errorf("%q: exit %d %q", creds, code, h.Err.String())
		}
		if entries, _ := os.ReadDir(filepath.Join(root, "etc")); len(entries) != 0 {
			t.Errorf("%q: wrote %v", creds, entries)
		}
	}
}

func TestCredentialsDirectory(t *testing.T) {
	for env, msg := range map[string]string{
		"":             "bluefin-firstboot-credentials: CREDENTIALS_DIRECTORY is not set; run from a unit with ImportCredential=firstboot.*\n",
		"/nonexistent": "bluefin-firstboot-credentials: CREDENTIALS_DIRECTORY /nonexistent is not a directory\n",
	} {
		h := hosttest.New(map[string]string{"CREDENTIALS_DIRECTORY": env}, nil)
		if code := Main(h.Host, []string{"--root=" + t.TempDir()}); code != 1 || h.Err.String() != msg {
			t.Errorf("%q: exit %d %q", env, code, h.Err.String())
		}
	}
}
