// Package nodeharness runs the Node scripts of scripts/sio-interop for the
// opt-in interop tests against the official Socket.IO implementations
// (docs/socketio.md#interop-with-the-official-implementations).
//
// The tests are skipped unless EnvDir names the directory that
// scripts/sio-interop.sh installed the npm packages into (v2/, v3/, v4/),
// so a default `go test ./...` never needs Node or the network. EnvNode is
// the node binary, "node" on the PATH by default.
//
// A script writes one JSON object per line to stdout, the first one
// {"ready": true, …}, and exits when its stdin closes.
package nodeharness

import (
	"bufio"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

// Environment variables of the interop tests.
const (
	EnvDir  = "POSTSONDE_SIO_INTEROP"
	EnvNode = "POSTSONDE_NODE"
)

// Timeout is how long Wait waits for a line.
const Timeout = 15 * time.Second

// Dir returns the install directory of the npm packages (EnvDir, made
// absolute), or skips the test when it is not set.
func Dir(t testing.TB) string {
	t.Helper()
	dir := os.Getenv(EnvDir)
	if dir == "" {
		t.Skip(EnvDir + " is not set: the interop tests against the official Node implementations are opt-in (scripts/sio-interop.sh)")
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// Line is one JSON line of a script.
type Line = map[string]any

// Process is a running script.
type Process struct {
	mu    sync.Mutex
	lines []Line
	wake  chan struct{}
	ended chan struct{}
	stdin io.WriteCloser
}

// Start runs scripts/sio-interop/<script> with args, waits for its ready
// line and returns it. The script is stopped at the end of the test by
// closing its stdin (killed if it has not exited 5 s later).
func Start(t testing.TB, script string, args ...string) (*Process, Line) {
	t.Helper()
	path := filepath.Join(repoRoot(t), "scripts", "sio-interop", script)
	node := os.Getenv(EnvNode)
	if node == "" {
		node = "node"
	}
	cmd := exec.Command(node, append([]string{path}, args...)...)
	cmd.Stderr = os.Stderr
	stdin, err := cmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting %s: %v", node, err)
	}
	p := &Process{wake: make(chan struct{}, 1), ended: make(chan struct{}), stdin: stdin}
	go p.read(t, stdout)
	t.Cleanup(func() {
		_ = stdin.Close()
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			_ = cmd.Process.Kill()
			<-done
		}
	})
	ready := p.Wait(t, "ready", func(l Line) bool { return l["ready"] == true })
	return p, ready
}

// repoRoot is the directory of go.mod, above the test's directory.
func repoRoot(t testing.TB) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found above the test's directory")
		}
		dir = parent
	}
}

func (p *Process) read(t testing.TB, r io.Reader) {
	defer close(p.ended)
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64<<10), 16<<20)
	for sc.Scan() {
		var l Line
		if err := json.Unmarshal(sc.Bytes(), &l); err != nil {
			t.Logf("node: %s", sc.Text())
			continue
		}
		p.mu.Lock()
		p.lines = append(p.lines, l)
		p.mu.Unlock()
		select {
		case p.wake <- struct{}{}:
		default:
		}
	}
}

// Lines returns the lines so far.
func (p *Process) Lines() []Line {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]Line(nil), p.lines...)
}

// Wait returns the first line that matches, failing the test when the
// script exits first or after Timeout.
func (p *Process) Wait(t testing.TB, what string, match func(Line) bool) Line {
	t.Helper()
	deadline := time.After(Timeout)
	for {
		for _, l := range p.Lines() {
			if match(l) {
				return l
			}
		}
		select {
		case <-p.wake:
		case <-p.ended:
			for _, l := range p.Lines() {
				if match(l) {
					return l
				}
			}
			t.Fatalf("node exited before a %s line; lines: %v", what, p.Lines())
		case <-deadline:
			t.Fatalf("node: no %s line within %s; lines: %v", what, Timeout, p.Lines())
		}
	}
}

// Ended is closed when the script's stdout closes (it exited).
func (p *Process) Ended() <-chan struct{} { return p.ended }

// Send writes one JSON line to the script's stdin.
func (p *Process) Send(t testing.TB, l Line) {
	t.Helper()
	b, err := json.Marshal(l)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := p.stdin.Write(append(b, '\n')); err != nil {
		t.Fatalf("writing to node: %v", err)
	}
}
