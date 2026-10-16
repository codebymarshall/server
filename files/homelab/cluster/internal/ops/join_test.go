package ops

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"errors"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const k0sToken = "QUJDREVGR0hJSktMTU5PUFFSU1RVVldYWVo="

// fakeRun replaces Run for one test. It records every command line and
// answers through fn, which may be nil.
func fakeRun(t *testing.T, fn func(name string, args []string) ([]byte, error)) *[]string {
	t.Helper()
	calls := &[]string{}
	orig := Run
	t.Cleanup(func() { Run = orig })
	Run = func(_ context.Context, name string, args ...string) ([]byte, error) {
		*calls = append(*calls, strings.Join(append([]string{name}, args...), " "))
		if fn == nil {
			return nil, nil
		}
		return fn(name, args)
	}
	return calls
}

func touch(t *testing.T, root, p string) {
	t.Helper()
	file := filepath.Join(root, p)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeFileT(t *testing.T, root, p, body string) {
	t.Helper()
	file := filepath.Join(root, p)
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(file, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
}

func writeCA(t *testing.T, root string) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now(), NotAfter: time.Now().Add(time.Hour)}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	writeFileT(t, root, KubeadmCACert, string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})))
}

func kubeadmGrant() Grant {
	return Grant{
		Runtime:      RuntimeKubeadm,
		Endpoint:     "cp1.local:6443",
		Token:        "abcdef.0123456789abcdef",
		CACertHashes: []string{"sha256:" + strings.Repeat("a", 64)},
		TTLSeconds:   900,
	}
}

func TestJoinK0sWritesTokenAndStartsFirstBoot(t *testing.T) {
	root := withRoot(t)
	calls := fakeRun(t, nil)
	if err := Join(context.Background(), Grant{Runtime: RuntimeK0s, K0sToken: k0sToken, TTLSeconds: 900}); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(root, K0sTokenFile)
	data, err := os.ReadFile(file)
	if err != nil || string(data) != k0sToken+"\n" {
		t.Fatalf("token file %q %v", data, err)
	}
	if st, _ := os.Stat(file); st.Mode().Perm() != 0o600 {
		t.Fatalf("token file mode %v, want 0600", st.Mode().Perm())
	}
	if want := []string{"systemctl enable --now k0s-first-boot.service"}; strings.Join(*calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commands %q, want %q", *calls, want)
	}
}

func TestJoinK0sReportsFirstBootFailure(t *testing.T) {
	withRoot(t)
	fakeRun(t, func(string, []string) ([]byte, error) { return nil, errors.New("unit failed") })
	err := Join(context.Background(), Grant{Runtime: RuntimeK0s, K0sToken: k0sToken, TTLSeconds: 900})
	if err == nil || !strings.Contains(err.Error(), "k0s-first-boot.service") {
		t.Fatalf("got %v, want a k0s-first-boot.service error", err)
	}
}

func TestJoinKubeadmJoinsWithTheRenderedConfig(t *testing.T) {
	root := withRoot(t)
	g := kubeadmGrant()
	cfg := filepath.Join(root, "run/bluefin-cluster/join.yaml")
	var seen string
	var seenMode os.FileMode
	calls := fakeRun(t, func(name string, args []string) ([]byte, error) {
		if name == "kubeadm" && len(args) > 0 && args[0] == "join" {
			data, err := os.ReadFile(cfg)
			if err != nil {
				t.Errorf("join config missing while kubeadm join runs: %v", err)
			}
			seen = string(data)
			if st, err := os.Stat(cfg); err == nil {
				seenMode = st.Mode().Perm()
			}
		}
		return nil, nil
	})
	if err := Join(context.Background(), g); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"systemctl enable --now containerd.service",
		"systemctl enable kubelet.service",
		"kubeadm join --config " + cfg,
	}
	if strings.Join(*calls, "\n") != strings.Join(want, "\n") {
		t.Fatalf("commands\n%s\nwant\n%s", strings.Join(*calls, "\n"), strings.Join(want, "\n"))
	}
	if seen != JoinConfiguration(g) {
		t.Fatalf("kubeadm join read\n%s\nwant\n%s", seen, JoinConfiguration(g))
	}
	if seenMode != 0o600 {
		t.Fatalf("join config mode %v, want 0600 (it holds the bootstrap token)", seenMode)
	}
	if st, err := os.Stat(filepath.Dir(cfg)); err != nil || st.Mode().Perm() != 0o700 {
		t.Fatalf("join config dir: %v %v", st, err)
	}
	if _, err := os.Stat(cfg); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("join config left behind after join: %v", err)
	}
}

func TestJoinKubeadmResetsAfterAFailedJoin(t *testing.T) {
	root := withRoot(t)
	cfg := filepath.Join(root, "run/bluefin-cluster/join.yaml")
	calls := fakeRun(t, func(name string, args []string) ([]byte, error) {
		if name == "kubeadm" && args[0] == "join" {
			return nil, errors.New("exit status 1")
		}
		return nil, nil
	})
	err := Join(context.Background(), kubeadmGrant())
	if err == nil || !strings.Contains(err.Error(), "kubeadm join") {
		t.Fatalf("got %v, want a kubeadm join error", err)
	}
	if n := len(*calls); n == 0 || (*calls)[n-1] != "kubeadm reset --force --cri-socket "+CRISocket {
		t.Fatalf("a failed join must reset the node; commands %q", *calls)
	}
	if _, err := os.Stat(cfg); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("join config left behind after a failed join: %v", err)
	}
}

func TestJoinKubeadmStopsWhenContainerdDoesNotStart(t *testing.T) {
	root := withRoot(t)
	calls := fakeRun(t, func(string, []string) ([]byte, error) { return nil, errors.New("failed") })
	err := Join(context.Background(), kubeadmGrant())
	if err == nil || !strings.Contains(err.Error(), "starting containerd") {
		t.Fatalf("got %v, want a containerd error", err)
	}
	if len(*calls) != 1 {
		t.Fatalf("nothing may run after containerd fails; commands %q", *calls)
	}
	if _, err := os.Stat(filepath.Join(root, "run/bluefin-cluster/join.yaml")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("join config written although containerd did not start")
	}
}

func TestJoinKubeadmStopsWhenKubeletCannotBeEnabled(t *testing.T) {
	withRoot(t)
	calls := fakeRun(t, func(_ string, args []string) ([]byte, error) {
		if len(args) > 1 && args[1] == "kubelet.service" {
			return nil, errors.New("failed")
		}
		return nil, nil
	})
	err := Join(context.Background(), kubeadmGrant())
	if err == nil || !strings.Contains(err.Error(), "enabling kubelet") {
		t.Fatalf("got %v, want a kubelet error", err)
	}
	for _, c := range *calls {
		if strings.HasPrefix(c, "kubeadm") {
			t.Fatalf("kubeadm ran although kubelet was not enabled: %q", *calls)
		}
	}
}

func TestJoinRejectsAnUnknownRuntime(t *testing.T) {
	withRoot(t)
	calls := fakeRun(t, nil)
	if err := Join(context.Background(), Grant{Runtime: "nomad"}); err == nil {
		t.Fatal("unknown runtime accepted")
	}
	if len(*calls) != 0 {
		t.Fatalf("commands ran for an unknown runtime: %q", *calls)
	}
}

func TestHostMinterK0s(t *testing.T) {
	root := withRoot(t)
	touch(t, root, K0sAdminConf)
	calls := fakeRun(t, func(string, []string) ([]byte, error) { return []byte("  " + k0sToken + "\n"), nil })
	g, err := HostMinter{}.Mint(context.Background(), "node-1")
	if err != nil {
		t.Fatal(err)
	}
	if g.Runtime != RuntimeK0s || g.K0sToken != k0sToken || g.TTLSeconds != 900 {
		t.Fatalf("grant %+v", g)
	}
	if err := g.Validate(); err != nil {
		t.Fatalf("minted k0s grant does not validate: %v", err)
	}
	if want := "k0s token create --role=worker --expiry=15m0s"; len(*calls) != 1 || (*calls)[0] != want {
		t.Fatalf("commands %q, want %q", *calls, want)
	}
}

func TestHostMinterK0sReportsTokenFailure(t *testing.T) {
	root := withRoot(t)
	touch(t, root, K0sAdminConf)
	fakeRun(t, func(string, []string) ([]byte, error) { return nil, errors.New("exit status 1") })
	if _, err := (HostMinter{}).Mint(context.Background(), "node-1"); err == nil || !strings.Contains(err.Error(), "k0s token create") {
		t.Fatalf("got %v, want a k0s token create error", err)
	}
}

func TestHostMinterWithoutAControlPlane(t *testing.T) {
	withRoot(t)
	calls := fakeRun(t, nil)
	if _, err := (HostMinter{}).Mint(context.Background(), "node-1"); err == nil || !strings.Contains(err.Error(), "no control plane") {
		t.Fatalf("got %v, want a no-control-plane error", err)
	}
	if len(*calls) != 0 {
		t.Fatalf("commands ran without a control plane: %q", *calls)
	}
}

func TestHostMinterKubeadmFailures(t *testing.T) {
	for _, tc := range []struct {
		name, adminConf, want string
		out                   string
		runErr                error
		wantRun               bool
	}{
		{name: "plain http server", adminConf: "clusters:\n- cluster:\n    server: http://cp1.local:6443\n", want: "no https server"},
		{name: "token create fails", adminConf: "    server: https://cp1.local:6443\n", runErr: errors.New("exit status 1"), want: "kubeadm token create", wantRun: true},
		{name: "token create prints nothing", adminConf: "    server: https://cp1.local:6443\n", out: " \n", want: "printed nothing", wantRun: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := withRoot(t)
			writeFileT(t, root, KubeadmAdminConf, tc.adminConf)
			writeCA(t, root)
			calls := fakeRun(t, func(string, []string) ([]byte, error) { return []byte(tc.out), tc.runErr })
			_, err := HostMinter{}.Mint(context.Background(), "node-1")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want %q", err, tc.want)
			}
			if ran := len(*calls) > 0; ran != tc.wantRun {
				t.Fatalf("kubeadm ran=%v, want %v (%q)", ran, tc.wantRun, *calls)
			}
		})
	}
}

func TestHostMinterKubeadmNeedsTheCACert(t *testing.T) {
	root := withRoot(t)
	writeFileT(t, root, KubeadmAdminConf, "    server: https://cp1.local:6443\n")
	writeFileT(t, root, KubeadmCACert, "not a certificate\n")
	calls := fakeRun(t, nil)
	if _, err := (HostMinter{}).Mint(context.Background(), "node-1"); err == nil {
		t.Fatal("minted a grant without a usable CA certificate")
	}
	if len(*calls) != 0 {
		t.Fatalf("token created without a CA hash to pin: %q", *calls)
	}
}

func TestLocalRuntime(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []string
		want  string
	}{
		{"none", nil, ""},
		{"kubeadm", []string{KubeadmAdminConf}, RuntimeKubeadm},
		{"k0s", []string{K0sAdminConf}, RuntimeK0s},
		{"both prefers kubeadm", []string{KubeadmAdminConf, K0sAdminConf}, RuntimeKubeadm},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := withRoot(t)
			for _, f := range tc.files {
				touch(t, root, f)
			}
			if got := LocalRuntime(); got != tc.want {
				t.Fatalf("LocalRuntime() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestNodeRuntime(t *testing.T) {
	for _, tc := range []struct {
		name  string
		files []string
		want  string
	}{
		{"none", nil, ""},
		{"kubeadm sysext merged", []string{"/usr/bin/kubeadm"}, RuntimeKubeadm},
		{"k0s merged", []string{"/usr/bin/k0s"}, RuntimeK0s},
		{"k0s sysext image only", []string{"/var/lib/k0s/k0s.raw"}, RuntimeK0s},
		{"both prefers kubeadm", []string{"/usr/bin/kubeadm", "/var/lib/k0s/k0s.raw"}, RuntimeKubeadm},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := withRoot(t)
			for _, f := range tc.files {
				touch(t, root, f)
			}
			if got := NodeRuntime(); got != tc.want {
				t.Fatalf("NodeRuntime() = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestAPIServerHost(t *testing.T) {
	for _, tc := range []struct {
		name, conf, want string
		wantErr          bool
	}{
		{name: "mDNS name", conf: "clusters:\n- cluster:\n    server: https://cp1.local:6443\n", want: "cp1.local"},
		{name: "IPv6 literal", conf: "    server: https://[2001:db8::10]:6443\n", want: "2001:db8::10"},
		{name: "no port", conf: "    server: https://cp1.local\n", wantErr: true},
		{name: "no server", conf: "clusters: []\n", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := withRoot(t)
			writeFileT(t, root, KubeadmKubelet, tc.conf)
			got, err := APIServerHost()
			if (err != nil) != tc.wantErr || got != tc.want {
				t.Fatalf("APIServerHost() = %q, %v; want %q, err=%v", got, err, tc.want, tc.wantErr)
			}
		})
	}
	t.Run("missing kubelet.conf", func(t *testing.T) {
		withRoot(t)
		if _, err := APIServerHost(); err == nil {
			t.Fatal("no error without kubelet.conf")
		}
	})
}

func TestGrantValidateLifetimeAndK0sToken(t *testing.T) {
	ipv6 := kubeadmGrant()
	ipv6.Endpoint = "[2001:db8::10]:6443"
	for _, tc := range []struct {
		name string
		g    Grant
		ok   bool
	}{
		{"kubeadm at the TTL limit", kubeadmGrant(), true},
		{"kubeadm IPv6 endpoint", ipv6, true},
		{"k0s", Grant{Runtime: RuntimeK0s, K0sToken: k0sToken, TTLSeconds: 1}, true},
		{"zero lifetime", Grant{Runtime: RuntimeK0s, K0sToken: k0sToken}, false},
		{"negative lifetime", Grant{Runtime: RuntimeK0s, K0sToken: k0sToken, TTLSeconds: -1}, false},
		{"lifetime past TokenTTL", Grant{Runtime: RuntimeK0s, K0sToken: k0sToken, TTLSeconds: int(TokenTTL/time.Second) + 1}, false},
		{"k0s token too short", Grant{Runtime: RuntimeK0s, K0sToken: "QUJD", TTLSeconds: 900}, false},
		{"k0s token with newline", Grant{Runtime: RuntimeK0s, K0sToken: k0sToken + "\nExecStart=/bin/sh", TTLSeconds: 900}, false},
		{"k0s token oversized", Grant{Runtime: RuntimeK0s, K0sToken: strings.Repeat("A", 16385), TTLSeconds: 900}, false},
		{"k0s token empty", Grant{Runtime: RuntimeK0s, TTLSeconds: 900}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.g.Validate(); (err == nil) != tc.ok {
				t.Fatalf("Validate() = %v, want ok=%v", err, tc.ok)
			}
		})
	}
}
