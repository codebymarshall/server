package sys

import (
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestSplitEq(t *testing.T) {
	for _, tc := range []struct{ line, key, value string }{
		{"K=V", "K", "V"},
		{"K=V=", "K", "V"},
		{"K=V==", "K", "V=="},
		{"K=V=W=", "K", "V=W="},
		{"K=", "K", ""},
		{" K = V ", " K ", " V "},
		{"K", "K", ""},
		{`IMAGE_VERSION="26.09.3"`, "IMAGE_VERSION", `"26.09.3"`},
	} {
		if k, v := SplitEq(tc.line); k != tc.key || v != tc.value {
			t.Errorf("SplitEq(%q) = %q, %q; want %q, %q", tc.line, k, v, tc.key, tc.value)
		}
	}
}

func TestLines(t *testing.T) {
	if got := Lines([]byte("a\nb\nc"), false); !reflect.DeepEqual(got, []string{"a", "b"}) {
		t.Errorf("while read drops an unterminated line: %q", got)
	}
	if got := Lines([]byte("a\nb\nc"), true); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("partial keeps it: %q", got)
	}
	if got := Lines([]byte("a\n\n"), true); !reflect.DeepEqual(got, []string{"a", ""}) {
		t.Errorf("blank lines stay: %q", got)
	}
}

func TestQuote(t *testing.T) {
	for in, want := range map[string]string{
		"":                                 "''",
		"bad_name":                         "bad_name",
		"bad name":                         `bad\ name`,
		"host\r":                           `$'host\r'`,
		"h\x01\x1b\n\t\x7f\\'\xc3\xb4 z\"": `$'h\001\E\n\t\177\\\'\303\264 z"'`,
		"a=b":                              "a=b",
		"~x":                               `\~x`,
		"a=~x":                             `a=\~x`,
		"a~x":                              "a~x",
		"#x":                               `\#x`,
		"x#y":                              "x#y",
		"a!b$c":                            `a\!b\$c`,
	} {
		if got := Quote(in); got != want {
			t.Errorf("Quote(%q) = %s; want %s", in, got, want)
		}
	}
}

func TestReadRestAndFields(t *testing.T) {
	if f, r := ReadRest("  aaaa  *bluefin-server_1.raw \t"); f != "aaaa" || r != "*bluefin-server_1.raw" {
		t.Errorf("ReadRest = %q, %q", f, r)
	}
	if got := Fields(" a\tb  c\nd e"); !reflect.DeepEqual(got, []string{"a", "b", "c"}) {
		t.Errorf("Fields reads the first line only: %q", got)
	}
}

func TestStripControlKeepsInvalidUTF8(t *testing.T) {
	if got := StripControl("a\x1b[2J\x7f\xffb\n"); got != "a[2J\xffb" {
		t.Errorf("StripControl = %q", got)
	}
}

func TestSubst(t *testing.T) {
	if got := Subst([]byte("a\x00b\n\n")); got != "ab" {
		t.Errorf("Subst = %q", got)
	}
}

func TestImageVersion(t *testing.T) {
	dir := t.TempDir()
	file := filepath.Join(dir, "os-release")
	for content, want := range map[string]string{
		"ID=x\nIMAGE_VERSION=\"26.09.3\"\n":  "26.09.3",
		"IMAGE_VERSION=1\nIMAGE_VERSION=2\n": "2",
		"IMAGE_VERSION=1\nIMAGE_VERSION=2":   "1",
		"ID=x\n":                             "",
		"IMAGE_VERSION = 3\n":                "",
		"IMAGE_VERSION=\"\"4\"\"\n":          "\"4\"",
		"IMAGE_VERSION=26.09.3=\n":           "26.09.3",
		"IMAGE_VERSION=a=b\n":                "a=b",
	} {
		if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if got := ImageVersion(file); got != want {
			t.Errorf("ImageVersion(%q) = %q; want %q", content, got, want)
		}
	}
	if got := ImageVersion(filepath.Join(dir, "missing")); got != "" {
		t.Errorf("missing file: %q", got)
	}
}

func TestGlobSkipsDotfilesAndKeepsTheDirectoryLiteral(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "b[o]ot*")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"b.efi", "a.efi", ".hidden.efi", "c.conf"} {
		if err := os.WriteFile(filepath.Join(dir, name), nil, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want := []string{filepath.Join(dir, "a.efi"), filepath.Join(dir, "b.efi")}
	if got := Glob(dir, "*.efi"); !reflect.DeepEqual(got, want) {
		t.Errorf("Glob = %q", got)
	}
	if got := Glob(filepath.Join(dir, "missing"), "*"); got != nil {
		t.Errorf("missing dir: %q", got)
	}
}

func TestSymlinkForceReplaces(t *testing.T) {
	link := filepath.Join(t.TempDir(), "localtime")
	for _, target := range []string{"../a", "../b"} {
		if err := SymlinkForce(target, link); err != nil {
			t.Fatal(err)
		}
	}
	if got, _ := os.Readlink(link); got != "../b" {
		t.Errorf("link -> %q", got)
	}
}

type answer struct {
	out  string
	code int
}

type runner map[string]answer

func (r runner) Run(c Cmd) ([]byte, int) {
	a := r[c.Argv[0]]
	return []byte(a.out), a.code
}

func TestBootPath(t *testing.T) {
	for _, tc := range []struct {
		env    string
		answer answer
		want   string
	}{
		{"/override", answer{}, "/override"},
		{"", answer{"/efi\n", 0}, "/efi"},
		{"", answer{"", 1}, "/boot"},
	} {
		h := &Host{Runner: runner{"bootctl": tc.answer}, Getenv: func(k string) string {
			if k == "BLUEFIN_BOOT_PATH" {
				return tc.env
			}
			return ""
		}}
		if got := h.BootPath(); got != tc.want {
			t.Errorf("BootPath = %q; want %q", got, tc.want)
		}
	}
}

func TestExecRunnerExitCodes(t *testing.T) {
	r := Exec{Stdout: os.Stdout, Stderr: os.Stderr}
	if _, code := r.Run(Cmd{Argv: []string{"/nonexistent/command"}, Quiet: true}); code != 127 {
		t.Errorf("missing command: %d", code)
	}
}
