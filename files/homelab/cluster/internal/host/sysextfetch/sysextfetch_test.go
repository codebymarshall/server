package sysextfetch

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/projectbluefin/server/files/homelab/cluster/internal/host/hosttest"
)

func TestIniFollowsSedRanges(t *testing.T) {
	data := []byte(`[Transfer]
Features=kubeadm homelab
[Source]
Path=https://x/
MatchPattern=a_@v.raw.zst
[Target]
  Path = /var/lib/extensions/
MatchPattern=a_@v.raw
PathRelativeTo=root
[Transfer]
Features=later-section
[Target]
MatchPattern=b_@v.raw`)
	for _, tc := range []struct{ section, key, want string }{
		{"Transfer", "Features", "later-section"},
		{"Source", "MatchPattern", "a_@v.raw.zst"},
		{"Target", "Path", "/var/lib/extensions/"},
		{"Target", "MatchPattern", "b_@v.raw"},
		{"Source", "Missing", ""},
	} {
		if got := ini(tc.section, tc.key, data); got != tc.want {
			t.Errorf("ini(%s, %s) = %q; want %q", tc.section, tc.key, got, tc.want)
		}
	}
	// sed's range ends on the next "[": a [Transfer] right after [Transfer]
	// closes the first range and does not open another.
	if got := ini("Transfer", "Features", []byte("[Transfer]\n[Transfer]\nFeatures=x\n")); got != "" {
		t.Errorf("back-to-back sections: %q", got)
	}
}

func TestRewrite(t *testing.T) {
	in := "[Transfer]\nFeatures=kubeadm\nVerify=yes\nProtectVersion=%A\n\n[Source]\nType=url-file\n Path = https://github.com/x\nMatchPattern=k_@v.raw.zst\n\n[Target]\nPath=/var/lib/extensions\n"
	want := "[Transfer]\nVerify=no\nProtectVersion=%A\n\n[Source]\nType=url-file\nPath=file:///esp/bluefin/extensions/\nMatchPattern=k_@v.raw.zst\n\n[Target]\nPath=/var/lib/extensions\n"
	if got := string(rewrite([]byte(in), "file:///esp/bluefin/extensions/", "no")); got != want {
		t.Errorf("rewrite:\n%s\nwant:\n%s", got, want)
	}
	if got := string(rewrite([]byte("[Transfer] # c\nVerify=no"), "u", "yes")); got != "[Transfer]\nVerify=yes # c\n" {
		t.Errorf("deleted unterminated last line: %q", got)
	}
	if got := string(rewrite([]byte("[Source]\nPath=x"), "u", "yes")); got != "[Source]\nPath=u" {
		t.Errorf("unterminated last line stays unterminated: %q", got)
	}
}

const transferTemplate = `[Transfer]
Features=%f
Verify=yes

[Source]
Type=url-file
Path=https://example.invalid/
MatchPattern=%f_@v.raw.zst

[Target]
Type=regular-file
Path=%ext
MatchPattern=%f_@v.raw
`

type node struct {
	dir, etc, usr, ext, esp, run string
	env                          map[string]string
}

func newNode(t *testing.T, features ...string) *node {
	t.Helper()
	dir := t.TempDir()
	n := &node{dir: dir, etc: filepath.Join(dir, "etc"), usr: filepath.Join(dir, "usr"), ext: filepath.Join(dir, "ext"), esp: filepath.Join(dir, "esp"), run: filepath.Join(dir, "run")}
	for _, d := range []string{n.etc, n.usr, n.ext, n.esp} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	for i, f := range append([]string{"zfs"}, features...) {
		text := strings.NewReplacer("%f", f, "%ext", n.ext).Replace(transferTemplate)
		if err := os.WriteFile(filepath.Join(n.usr, string(rune('a'+i))+"-"+f+".transfer"), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	osRelease := filepath.Join(dir, "os-release")
	if err := os.WriteFile(osRelease, []byte("IMAGE_VERSION=\"26.10.3\"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	n.env = map[string]string{
		"BLUEFIN_SYSUPDATE_DIRS":   n.etc + " " + n.usr,
		"BLUEFIN_OS_RELEASE":       osRelease,
		"BLUEFIN_ESP":              n.esp,
		"BLUEFIN_ROOTDISK":         filepath.Join(dir, "rootdisk.raw"),
		"BLUEFIN_BOOT_ORIGIN":      "origin",
		"BLUEFIN_UPDATE_PENDING":   "pending",
		"BLUEFIN_SYSEXT_FETCH_RUN": n.run,
	}
	return n
}

func (n *node) enable(t *testing.T, dir, feature, value string) {
	t.Helper()
	d := filepath.Join(dir, feature+".feature.d")
	if err := os.MkdirAll(d, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(d, "50-x.conf"), []byte("[Feature]\nEnabled="+value+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (n *node) fetch(t *testing.T, install bool, pending bool) (*hosttest.Host, int) {
	t.Helper()
	h := hosttest.New(n.env, func(argv []string) (string, int) {
		switch argv[0] {
		case "origin":
			return "http://192.0.2.10:8765/bluefin-server_26.10.3.raw\n", 0
		case "pending":
			if pending {
				return "", 0
			}
			return "", 1
		case "systemd-sysupdate":
			if install {
				for _, f := range []string{"kubeadm", "homelab"} {
					os.WriteFile(filepath.Join(n.ext, f+"_26.10.3.raw"), nil, 0o644)
				}
			}
			return "", 0
		}
		return "", 0
	})
	return h, Main(h.Host, nil)
}

func TestNothingEnabled(t *testing.T) {
	n := newNode(t, "kubeadm")
	n.enable(t, n.usr, "kubeadm", "true")
	n.enable(t, n.etc, "kubeadm", "no")
	h, code := n.fetch(t, true, false)
	if code != 0 || h.Out.String() != "bluefin-sysext-fetch: no optional sysupdate feature is enabled; nothing to fetch\n" || len(h.Fake.Calls()) != 0 {
		t.Errorf("exit %d %q %v", code, h.Out, h.Fake.Calls())
	}
}

func TestDisklessFetchesFromTheBootServer(t *testing.T) {
	n := newNode(t, "kubeadm", "homelab")
	n.enable(t, n.etc, "homelab", "On")
	n.enable(t, n.etc, "kubeadm", "1")
	if err := os.WriteFile(n.env["BLUEFIN_ROOTDISK"], nil, 0o644); err != nil {
		t.Fatal(err)
	}
	h, code := n.fetch(t, true, false)
	if code != 0 {
		t.Fatalf("exit %d: %s", code, h.Err)
	}
	want := []string{
		"origin",
		"systemd-sysupdate --definitions=" + n.run + "/origin update",
		"systemd-sysext refresh",
		"systemctl daemon-reload",
		"systemctl start --no-block bluefin-sysext-activate.service",
	}
	if got := h.Fake.Calls(); !reflect.DeepEqual(got, want) {
		t.Errorf("calls %q", got)
	}
	if got := strings.Split(strings.TrimSpace(h.Out.String()), "\n"); !reflect.DeepEqual(got, []string{
		"bluefin-sysext-fetch: fetching for 26.10.3: kubeadm_26.10.3.raw homelab_26.10.3.raw ",
		"bluefin-sysext-fetch: fetching from the boot server http://192.0.2.10:8765/",
		"bluefin-sysext-fetch: merged the homelab kubeadm sysext(s) for 26.10.3 (origin)",
	}) {
		t.Errorf("log %q", got)
	}
	copied, err := os.ReadFile(filepath.Join(n.run, "origin", "c-homelab.transfer"))
	if err != nil || !strings.Contains(string(copied), "[Transfer]\nVerify=yes\n") || !strings.Contains(string(copied), "Path=http://192.0.2.10:8765/\n") || strings.Contains(string(copied), "Features=") {
		t.Errorf("transfer copy %q %v", copied, err)
	}
}

func TestReleaseNewerReboots(t *testing.T) {
	n := newNode(t, "kubeadm")
	n.enable(t, n.etc, "kubeadm", "yes")
	h, code := n.fetch(t, false, true)
	if code != 0 || !reflect.DeepEqual(h.Fake.Calls(), []string{"systemd-sysupdate update", "pending", "systemctl reboot"}) {
		t.Errorf("exit %d calls %q", code, h.Fake.Calls())
	}
}

func TestStillMissingIsAnError(t *testing.T) {
	n := newNode(t, "kubeadm")
	n.enable(t, n.etc, "kubeadm", "t")
	h, code := n.fetch(t, false, false)
	if code != 1 || h.Err.String() != "bluefin-sysext-fetch: still missing: "+n.ext+"/kubeadm_26.10.3.raw \n" {
		t.Errorf("exit %d %q", code, h.Err)
	}
}

func TestBadTransferIsAnError(t *testing.T) {
	n := newNode(t)
	if err := os.WriteFile(filepath.Join(n.usr, "z.transfer"), []byte("[Transfer]\nFeatures=bad\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	n.enable(t, n.etc, "bad", "y")
	h, code := n.fetch(t, false, false)
	if code != 1 || !strings.HasSuffix(h.Err.String(), "z.transfer: needs [Target] Path=, MatchPattern= and [Source] MatchPattern=\n") {
		t.Errorf("exit %d %q", code, h.Err)
	}
}
