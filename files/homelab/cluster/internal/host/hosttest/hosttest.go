// Package hosttest fakes the host for the bluefin-host packages' tests: a
// Runner that records every command and answers from a handler, captured
// stdout and stderr, and a fixed environment.
package hosttest

import (
	"bytes"
	"strings"
	"sync"
	"time"

	"github.com/projectbluefin/server/files/homelab/cluster/internal/host/sys"
)

// Handler answers one command: its stdout and exit status.
type Handler func(argv []string) (stdout string, code int)

// Fake is a recording Runner.
type Fake struct {
	mu      sync.Mutex
	calls   []sys.Cmd
	handler Handler
}

// Run implements sys.Runner.
func (f *Fake) Run(c sys.Cmd) ([]byte, int) {
	f.mu.Lock()
	f.calls = append(f.calls, c)
	f.mu.Unlock()
	if f.handler == nil {
		return nil, 0
	}
	out, code := f.handler(c.Argv)
	return []byte(out), code
}

// Calls is every command run, as space-joined argv.
func (f *Fake) Calls() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.calls {
		out = append(out, strings.Join(c.Argv, " "))
	}
	return out
}

// Cmds is every command run.
func (f *Fake) Cmds() []sys.Cmd {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]sys.Cmd(nil), f.calls...)
}

// Host is a fake host and what it wrote.
type Host struct {
	*sys.Host
	Fake     *Fake
	Out, Err *bytes.Buffer
	Slept    []time.Duration
	sleepMu  sync.Mutex
}

// New is a host with env as its whole environment and handler answering
// its commands.
func New(env map[string]string, handler Handler) *Host {
	h := &Host{Fake: &Fake{handler: handler}, Out: &bytes.Buffer{}, Err: &bytes.Buffer{}}
	h.Host = &sys.Host{
		Runner: h.Fake,
		Stdout: h.Out,
		Stderr: h.Err,
		Getenv: func(k string) string { return env[k] },
		Sleep: func(d time.Duration) {
			h.sleepMu.Lock()
			h.Slept = append(h.Slept, d)
			h.sleepMu.Unlock()
		},
	}
	return h
}

// Commands is a Handler from argv prefixes to answers; anything else exits
// 127 like a missing command.
func Commands(answers map[string]Answer) Handler {
	return func(argv []string) (string, int) {
		line := strings.Join(argv, " ")
		best := ""
		for prefix := range answers {
			if (line == prefix || strings.HasPrefix(line, prefix+" ")) && len(prefix) > len(best) {
				best = prefix
			}
		}
		if best == "" {
			return "", 127
		}
		a := answers[best]
		return a.Out, a.Code
	}
}

// Answer is a command's stdout and exit status.
type Answer struct {
	Out  string
	Code int
}
