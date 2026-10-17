package bootorigin

import (
	"os"
	"path/filepath"
	"testing"
	"unicode/utf16"

	"github.com/projectbluefin/server/files/homelab/cluster/internal/host/hosttest"
)

const pull = "raw,machine,verify=signature,blockdev:rootdisk:"

func writeStubVar(t *testing.T, dir, url string) string {
	t.Helper()
	data := []byte{6, 0, 0, 0}
	for _, u := range utf16.Encode([]rune(url + "\x00")) {
		data = append(data, byte(u), byte(u>>8))
	}
	p := filepath.Join(dir, "StubDeviceURL")
	if err := os.WriteFile(p, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestFind(t *testing.T) {
	for _, tc := range []struct {
		name, stub, cmdline, cred, want string
		ok                              bool
	}{
		{name: "UEFI HTTP boot URL first", stub: "http://booty/n/bluefin-server-netboot.efi", cmdline: "rd.systemd.pull=" + pull + "http://other/x.raw", want: "http://booty/n/bluefin-server-netboot.efi", ok: true},
		{name: "explicit pull URL", cmdline: "root=tmpfs rd.systemd.pull=" + pull + "https://srv/r/bluefin-server_1.raw quiet", want: "https://srv/r/bluefin-server_1.raw", ok: true},
		{name: "import.pull credential", cmdline: "rd.systemd.pull=raw,bootorigin:rootdisk:x.raw", cred: "  " + pull + "http://10.0.2.2:8765/bluefin-server_1.raw\nignored\n", want: "http://10.0.2.2:8765/bluefin-server_1.raw", ok: true},
		{name: "first :http wins", cmdline: "rd.systemd.pull=a:httpx:http://u", want: "httpx:http://u", ok: true},
		{name: "not network booted", cmdline: "rd.systemd.pull=raw,machine,blockdev,bootorigin:rootdisk:x.raw"},
		{name: "empty stub falls through", stub: "", cmdline: "rd.systemd.pull=" + pull + "http://c/x.raw", want: "http://c/x.raw", ok: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			env := map[string]string{
				"BLUEFIN_STUB_URL_VAR": filepath.Join(dir, "none"),
				"BLUEFIN_CMDLINE":      filepath.Join(dir, "cmdline"),
			}
			if tc.stub != "" || tc.name == "empty stub falls through" {
				env["BLUEFIN_STUB_URL_VAR"] = writeStubVar(t, dir, tc.stub)
			}
			if err := os.WriteFile(env["BLUEFIN_CMDLINE"], []byte(tc.cmdline+"\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if tc.cred != "" {
				if err := os.WriteFile(filepath.Join(dir, "import.pull"), []byte(tc.cred), 0o644); err != nil {
					t.Fatal(err)
				}
				env["CREDENTIALS_DIRECTORY"] = dir
			}
			h := hosttest.New(env, nil)
			got, ok := Find(h.Host)
			if got != tc.want || ok != tc.ok {
				t.Errorf("Find = %q, %v; want %q, %v", got, ok, tc.want, tc.ok)
			}
			code := Main(h.Host, nil)
			if (code == 0) != tc.ok || (tc.ok && h.Out.String() != tc.want+"\n") || (!tc.ok && h.Out.Len() != 0) {
				t.Errorf("Main = %d, %q", code, h.Out.String())
			}
		})
	}
}
