package fleetlock

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/projectbluefin/server/files/homelab/cluster/internal/host/hosttest"
)

const id = "0123456789abcdef0123456789abcdef"

type fake struct {
	mu     sync.Mutex
	bodies []string
	paths  []string
	header []string
}

func server(t *testing.T, code int, reply string) (*httptest.Server, *fake) {
	t.Helper()
	f := &fake{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		f.mu.Lock()
		f.bodies = append(f.bodies, string(body))
		f.paths = append(f.paths, r.URL.Path)
		f.header = append(f.header, r.Header.Get("fleet-lock-protocol"))
		f.mu.Unlock()
		w.WriteHeader(code)
		io.WriteString(w, reply)
	}))
	t.Cleanup(srv.Close)
	return srv, f
}

func lockHost(t *testing.T, conf string, creds map[string]string) *hosttest.Host {
	t.Helper()
	dir := t.TempDir()
	env := map[string]string{"BLUEFIN_REBOOT_LOCK_CONF": filepath.Join(dir, "reboot-lock.conf"), "CREDENTIALS_DIRECTORY": dir}
	if conf != "" {
		if err := os.WriteFile(env["BLUEFIN_REBOOT_LOCK_CONF"], []byte(conf), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for k, v := range creds {
		if err := os.WriteFile(filepath.Join(dir, "bluefin.reboot-lock."+k), []byte(v), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return hosttest.New(env, func(argv []string) (string, int) {
		if argv[0] == "systemd-id128" && argv[1] == "machine-id" && argv[2] == "--app-specific="+appID {
			return id + "\n", 0
		}
		return "", 1
	})
}

func TestAcquireAndRelease(t *testing.T) {
	for _, tc := range []struct {
		verb, conf string
		code       int
		reply      string
		exit       int
		path, log  string
	}{
		{"acquire", "URL=%s/base/\n", 200, "", 0, "/base/v1/pre-reboot", "<6>holding a reboot slot on %s/base (id " + id + ", group default)\n"},
		{"release", "URL=\"%s\"\nGROUP = db-1 \r\n", 200, "", 0, "/v1/steady-state", "<6>released the reboot slot on %s (id " + id + ", group db-1)\n"},
		{"acquire", " URL = %s\n", 409, "semaphore\x1b full\n", 1, "/v1/pre-reboot", "<4>no reboot slot on %s (id " + id + ", group default): HTTP 409 semaphore full; not rebooting\n"},
		{"release", "URL=%s\n", 404, "", 1, "/v1/steady-state", "<3>the reboot slot on %s (id " + id + ", group default) is not released: HTTP 404\n"},
	} {
		srv, f := server(t, tc.code, tc.reply)
		h := lockHost(t, strings.ReplaceAll(tc.conf, "%s", srv.URL), nil)
		if got := Main(h.Host, []string{tc.verb}); got != tc.exit {
			t.Errorf("%s %d: exit %d; %s", tc.verb, tc.code, got, h.Out)
		}
		if want := strings.ReplaceAll(tc.log, "%s", srv.URL); h.Out.String() != want {
			t.Errorf("log %q; want %q", h.Out.String(), want)
		}
		f.mu.Lock()
		if len(f.paths) != 1 || f.paths[0] != tc.path || f.header[0] != "true" {
			t.Errorf("requests %v %v", f.paths, f.header)
		}
		var body map[string]map[string]string
		if err := json.Unmarshal([]byte(f.bodies[0]), &body); err != nil || body["client_params"]["id"] != id {
			t.Errorf("body %q", f.bodies[0])
		}
		f.mu.Unlock()
	}
}

func TestRefusedBeforeAnyRequest(t *testing.T) {
	for _, tc := range []struct {
		conf  string
		creds map[string]string
		exit  int
		log   string
	}{
		{"", nil, 0, "<6>no FleetLock server configured (%conf, credential bluefin.reboot-lock.url); nothing to acquire\n"},
		{"# URL=http://x\nGROUP=web\n", nil, 0, "nothing to acquire"},
		{"URL=http://x\n", map[string]string{"url": "\n"}, 0, "nothing to acquire"},
		{"URL=ftp://user:secret@fleetlock.example\n", nil, 1, "<3>FleetLock URL ftp://fleetlock.example is not an http(s) URL; not rebooting\n"},
		{"URL=user:secret@fleetlock.example\n", nil, 1, "<3>FleetLock URL fleetlock.example is not an http(s) URL; not rebooting\n"},
		{"URL=http://x\nGROUP=web servers\n", nil, 1, "<3>FleetLock group web servers must match [a-zA-Z0-9.-]+; not rebooting\n"},
		{"URL=http://file\n", map[string]string{"group": "a b\n"}, 1, "FleetLock group a b must match"},
	} {
		h := lockHost(t, tc.conf, tc.creds)
		if got := Main(h.Host, []string{"acquire"}); got != tc.exit {
			t.Errorf("%q: exit %d", tc.conf, got)
		}
		want := strings.ReplaceAll(tc.log, "%conf", h.Getenv("BLUEFIN_REBOOT_LOCK_CONF"))
		if !strings.Contains(h.Out.String(), want) || strings.Contains(h.Out.String(), "secret") {
			t.Errorf("%q: log %q; want %q", tc.conf, h.Out.String(), want)
		}
		if strings.Contains(strings.Join(h.Fake.Calls(), "\n"), "systemd-id128") && tc.exit != 0 && strings.Contains(tc.log, "URL") {
			t.Errorf("%q: asked for a client id before refusing", tc.conf)
		}
	}
}

func TestPlainHTTPCredentialsWarn(t *testing.T) {
	srv, _ := server(t, 200, "")
	h := lockHost(t, "URL="+strings.Replace(srv.URL, "http://", "http://user:secret@", 1)+"/\n", nil)
	if got := Main(h.Host, []string{"acquire"}); got != 0 {
		t.Fatalf("exit %d: %s", got, h.Out)
	}
	if !strings.HasPrefix(h.Out.String(), "<4>FleetLock URL "+srv.URL+" carries credentials over plain http; they are sent in clear\n") ||
		strings.Contains(h.Out.String(), "secret") {
		t.Errorf("log %q", h.Out.String())
	}
}

func TestUnreachableIsExit3(t *testing.T) {
	srv, _ := server(t, 200, "")
	url := srv.URL
	srv.Close()
	h := lockHost(t, "URL="+url+"\n", nil)
	if got := Main(h.Host, []string{"release"}); got != 3 {
		t.Fatalf("exit %d: %s", got, h.Out)
	}
	if want := "<3>FleetLock server " + url + "/v1/steady-state is unreachable (id " + id + ", group default): curl exit 7\n"; h.Out.String() != want {
		t.Errorf("log %q", h.Out.String())
	}
}

func TestUsage(t *testing.T) {
	h := lockHost(t, "", nil)
	if got := Main(h.Host, []string{"reboot"}); got != 2 || h.Err.String() != "usage: bluefin-reboot-lock acquire|release\n" {
		t.Errorf("exit %d, %q", got, h.Err.String())
	}
}

func TestReleaseCondition(t *testing.T) {
	for status, want := range map[string]int{"good": 0, "clean": 0, "indeterminate": 1, "bad": 1, "dirty": 1, "": 1} {
		h := hosttest.New(map[string]string{"BLUEFIN_BLESS_BOOT": "bless"}, func(argv []string) (string, int) {
			if status == "" {
				return "", 1
			}
			return status + "\n", 0
		})
		got := ReleaseCondition(h.Host, nil)
		shown := status
		if shown == "" {
			shown = "unknown"
		}
		if got != want || (want == 1 && h.Out.String() != "boot is "+shown+": keeping the reboot slot\n") {
			t.Errorf("%q: exit %d, %q", status, got, h.Out.String())
		}
	}
}
