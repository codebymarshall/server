// Package sys is what the bluefin-host helpers share: running host commands
// through a replaceable Runner, the environment, and the few shell idioms the
// helpers they replace relied on (`$(...)`, `read`, globs, `printf %q`), kept
// to the shell's behaviour so log lines and files stay byte-identical.
package sys

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Cmd is one host command.
type Cmd struct {
	Argv []string
	// Env is added to the inherited environment.
	Env   []string
	Stdin io.Reader
	// Capture returns stdout instead of passing it through.
	Capture bool
	// Quiet discards stderr, and stdout unless captured (2>/dev/null).
	Quiet bool
}

// Runner runs host commands; tests replace it. code is the exit status:
// 128+N for a signal, 127 or 126 when the command cannot be started.
type Runner interface {
	Run(c Cmd) (stdout []byte, code int)
}

// Host is one helper invocation: its commands, output and environment.
type Host struct {
	Runner Runner
	Stdout io.Writer
	Stderr io.Writer
	Getenv func(string) string
	// Sleep waits between retries; nil is time.Sleep.
	Sleep func(time.Duration)
}

// New is the real host: os.Stdout, os.Stderr, the process environment.
func New() *Host {
	return &Host{
		Runner: Exec{Stdout: os.Stdout, Stderr: os.Stderr},
		Stdout: os.Stdout,
		Stderr: os.Stderr,
		Getenv: os.Getenv,
	}
}

// Exec runs commands with os/exec.
type Exec struct{ Stdout, Stderr io.Writer }

// Run implements Runner.
func (e Exec) Run(c Cmd) ([]byte, int) {
	cmd := exec.Command(c.Argv[0], c.Argv[1:]...)
	if len(c.Env) > 0 {
		cmd.Env = append(os.Environ(), c.Env...)
	}
	cmd.Stdin = c.Stdin
	if cmd.Stdin == nil {
		cmd.Stdin = os.Stdin
	}
	var out bytes.Buffer
	switch {
	case c.Capture:
		cmd.Stdout = &out
	case !c.Quiet:
		cmd.Stdout = e.Stdout
	}
	if !c.Quiet {
		cmd.Stderr = e.Stderr
	}
	err := cmd.Run()
	if err == nil {
		return out.Bytes(), 0
	}
	var exit *exec.ExitError
	if errors.As(err, &exit) {
		if ws, ok := exit.Sys().(syscall.WaitStatus); ok && ws.Signaled() {
			return out.Bytes(), 128 + int(ws.Signal())
		}
		return out.Bytes(), exit.ExitCode()
	}
	if !c.Quiet {
		fmt.Fprintf(e.Stderr, "%s: %v\n", c.Argv[0], err)
	}
	if errors.Is(err, exec.ErrNotFound) || errors.Is(err, fs.ErrNotExist) {
		return out.Bytes(), 127
	}
	return out.Bytes(), 126
}

// Env is the variable's value, or def when it is unset or empty
// (`${NAME:-def}`).
func (h *Host) Env(name, def string) string {
	if v := h.Getenv(name); v != "" {
		return v
	}
	return def
}

// Output is `$(argv)`: stdout without NUL bytes and trailing newlines;
// quiet adds 2>/dev/null.
func (h *Host) Output(quiet bool, argv ...string) (string, int) {
	out, code := h.Runner.Run(Cmd{Argv: argv, Capture: true, Quiet: quiet})
	return Subst(out), code
}

// Exec runs argv with its output passed through.
func (h *Host) Exec(argv ...string) int {
	_, code := h.Runner.Run(Cmd{Argv: argv})
	return code
}

// Test runs argv for its status only: stdout discarded, stderr passed
// through unless quiet (`>/dev/null`, `>/dev/null 2>&1`).
func (h *Host) Test(quiet bool, argv ...string) bool {
	_, code := h.Runner.Run(Cmd{Argv: argv, Capture: true, Quiet: quiet})
	return code == 0
}

// Printf writes to stdout.
func (h *Host) Printf(format string, a ...any) { fmt.Fprintf(h.Stdout, format, a...) }

// Eprintf writes to stderr.
func (h *Host) Eprintf(format string, a ...any) { fmt.Fprintf(h.Stderr, format, a...) }

// Journal priorities, read by systemd from SyslogLevelPrefix=.
const (
	Err     = 3
	Warning = 4
	Notice  = 5
	Info    = 6
)

// Log writes "<level>msg" to stdout.
func (h *Host) Log(level int, msg string) { fmt.Fprintf(h.Stdout, "<%d>%s\n", level, msg) }

// Wait is h.Sleep(d) or time.Sleep(d).
func (h *Host) Wait(d time.Duration) {
	if h.Sleep != nil {
		h.Sleep(d)
		return
	}
	time.Sleep(d)
}

// Subst is what `$(...)` makes of a command's output: NUL bytes dropped,
// trailing newlines removed.
func Subst(b []byte) string {
	return strings.TrimRight(string(bytes.ReplaceAll(b, []byte{0}, nil)), "\n")
}

// ReadSubst is `$(<file)`.
func ReadSubst(file string) (string, error) {
	b, err := os.ReadFile(file)
	return Subst(b), err
}

// Lines splits data into lines. A last line without a newline is kept only
// when partial is set: `while read` drops it, `while read || [ -n ]` and
// sed do not.
func Lines(data []byte, partial bool) []string {
	s := string(data)
	lines := strings.Split(s, "\n")
	last := lines[len(lines)-1]
	lines = lines[:len(lines)-1]
	if partial && last != "" {
		lines = append(lines, last)
	}
	return lines
}

// SplitEq is `IFS='=' read -r key value` on one line: the key up to the
// first "=", the value after it, minus a single trailing "=" when that is
// the only one left.
func SplitEq(line string) (key, value string) {
	key, value, found := strings.Cut(line, "=")
	if !found {
		return line, ""
	}
	if strings.Count(value, "=") == 1 && strings.HasSuffix(value, "=") {
		value = value[:len(value)-1]
	}
	return key, value
}

func isIFS(r rune) bool { return r == ' ' || r == '\t' || r == '\n' }

// Fields is `read -r -a` with the default IFS: the first line, split on
// spaces and tabs.
func Fields(s string) []string {
	first, _, _ := strings.Cut(s, "\n")
	return strings.FieldsFunc(first, isIFS)
}

// ReadRest is `read -r first rest` with the default IFS on one line: the
// first word and the remainder, trimmed of spaces and tabs.
func ReadRest(line string) (first, rest string) {
	line = strings.TrimLeftFunc(line, isIFS)
	i := strings.IndexFunc(line, isIFS)
	if i < 0 {
		return line, ""
	}
	return line[:i], strings.TrimFunc(line[i:], isIFS)
}

// TrimIFS is `read -r line` with the default IFS: spaces, tabs and
// newlines trimmed from both ends.
func TrimIFS(s string) string { return strings.TrimFunc(s, isIFS) }

// IsSpace is [[:space:]] in the C locale.
func IsSpace(r rune) bool {
	return r == ' ' || r == '\t' || r == '\n' || r == '\v' || r == '\f' || r == '\r'
}

// TrimSpace trims [[:space:]] from both ends.
func TrimSpace(s string) string { return strings.TrimFunc(s, IsSpace) }

// ImageVersion is the last IMAGE_VERSION= in an os-release file, one pair
// of double quotes removed; "" when the file is unreadable.
func ImageVersion(file string) string {
	if !Readable(file) {
		return ""
	}
	data, err := os.ReadFile(file)
	if err != nil {
		return ""
	}
	version := ""
	for _, line := range Lines(data, false) {
		key, value := SplitEq(line)
		if key == "IMAGE_VERSION" {
			version = Unquote(value)
		}
	}
	return version
}

// Unquote removes one trailing and then one leading double quote.
func Unquote(s string) string {
	s = strings.TrimSuffix(s, `"`)
	return strings.TrimPrefix(s, `"`)
}

// Readable is `[ -r path ]`.
func Readable(p string) bool { return syscall.Access(p, 4) == nil }

// Writable is `[ -w path ]`.
func Writable(p string) bool { return syscall.Access(p, 2) == nil }

// Exists is `[ -e path ]`.
func Exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// IsFile is `[ -f path ]`.
func IsFile(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Mode().IsRegular()
}

// IsDir is `[ -d path ]`.
func IsDir(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.IsDir()
}

// NonEmpty is `[ -s path ]`.
func NonEmpty(p string) bool {
	st, err := os.Stat(p)
	return err == nil && st.Size() > 0
}

// Dir is `${p%/*}`.
func Dir(p string) string {
	if i := strings.LastIndexByte(p, '/'); i >= 0 {
		return p[:i]
	}
	return p
}

// Base is `${p##*/}`.
func Base(p string) string { return p[strings.LastIndexByte(p, '/')+1:] }

// MkdirP is `mkdir -p`.
func MkdirP(dir string) error {
	if dir == "" {
		return nil
	}
	return os.MkdirAll(dir, 0o777)
}

// Touch is `touch path`: create it, or update its times.
func Touch(p string) error {
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE, 0o666)
	if err != nil {
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	now := time.Now()
	return os.Chtimes(p, now, now)
}

// Truncate is `: > path`.
func Truncate(p string) error {
	f, err := os.OpenFile(p, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o666)
	if err != nil {
		return err
	}
	return f.Close()
}

// WriteFile is `printf ... > path`: truncate and write, mode from the umask.
func WriteFile(p string, data []byte) error {
	return os.WriteFile(p, data, 0o666)
}

// SymlinkForce is `ln -sfn target link`.
func SymlinkForce(target, link string) error {
	tmp := link + ".bluefin-new"
	_ = os.Remove(tmp)
	if err := os.Symlink(target, tmp); err != nil {
		return err
	}
	if err := os.Rename(tmp, link); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

// Glob is a shell glob of pattern (a single path component, no
// separators) in dir: the matching paths in byte order, names starting
// with "." only when the pattern does, and nothing when dir is unreadable.
// Unlike filepath.Glob, dir itself is literal.
func Glob(dir, pattern string) []string {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		name := e.Name()
		if strings.HasPrefix(name, ".") && !strings.HasPrefix(pattern, ".") {
			continue
		}
		if ok, _ := path.Match(pattern, name); ok {
			out = append(out, filepath.Join(dir, name))
		}
	}
	sort.Strings(out)
	return out
}

// BootPath is $BLUEFIN_BOOT_PATH or `bootctl --print-boot-path || echo /boot`.
func (h *Host) BootPath() string {
	if v := h.Getenv("BLUEFIN_BOOT_PATH"); v != "" {
		return v
	}
	out, code := h.Runner.Run(Cmd{Argv: []string{"bootctl", "--print-boot-path"}, Capture: true, Quiet: true})
	if code != 0 {
		out = append(out, "/boot\n"...)
	}
	return Subst(out)
}

// VersionGT is `systemd-analyze compare-versions a gt b`, quiet adding
// 2>/dev/null.
func (h *Host) VersionGT(a, b string, quiet bool) bool {
	return h.Test(quiet, "systemd-analyze", "compare-versions", a, "gt", b)
}

// StripControl is `tr -d '\000-\037\177'`.
func StripControl(s string) string {
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 0x20 && c != 0x7f {
			b = append(b, c)
		}
	}
	return string(b)
}

// CutBytes is `cut -c1-n` on a single line in the C locale.
func CutBytes(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}

// LowerASCII is `${s,,}` in the C locale.
func LowerASCII(s string) string {
	b := []byte(s)
	for i, c := range b {
		if 'A' <= c && c <= 'Z' {
			b[i] = c + 'a' - 'A'
		}
	}
	return string(b)
}

// ParseInt is a shell integer, as `[ a -gt b ]` reads it.
func ParseInt(s string) (int64, bool) {
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n, err == nil
}

// Quote is bash's `printf %q` in the C locale.
func Quote(s string) string {
	if s == "" {
		return "''"
	}
	for i := 0; i < len(s); i++ {
		if c := s[i]; c < 0x20 || c >= 0x7f {
			return ansicQuote(s)
		}
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case strings.IndexByte(" !\"$&'()*,;<>?[\\]^`{|}", c) >= 0:
			b.WriteByte('\\')
		case c == '#' && i == 0:
			b.WriteByte('\\')
		case c == '~' && (i == 0 || s[i-1] == '=' || s[i-1] == ':'):
			b.WriteByte('\\')
		}
		b.WriteByte(c)
	}
	return b.String()
}

func ansicQuote(s string) string {
	var b strings.Builder
	b.WriteString("$'")
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch c {
		case '\a':
			b.WriteString(`\a`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\v':
			b.WriteString(`\v`)
		case 0x1b:
			b.WriteString(`\E`)
		case '\\':
			b.WriteString(`\\`)
		case '\'':
			b.WriteString(`\'`)
		default:
			if c < 0x20 || c >= 0x7f {
				fmt.Fprintf(&b, "\\%03o", c)
			} else {
				b.WriteByte(c)
			}
		}
	}
	b.WriteByte('\'')
	return b.String()
}
