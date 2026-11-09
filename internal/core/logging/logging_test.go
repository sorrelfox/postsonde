package logging

import (
	"bufio"
	"bytes"
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNew(t *testing.T) {
	tests := []struct {
		name  string
		opts  Options
		log   func(l *slog.Logger)
		check func(t *testing.T, out string)
	}{
		{
			name: "JSON by default, info level",
			log: func(l *slog.Logger) {
				l.Debug("hidden")
				l.Info("shown", "k", "v")
			},
			check: func(t *testing.T, out string) {
				var m map[string]any
				if err := json.Unmarshal([]byte(out), &m); err != nil {
					t.Fatalf("not one JSON record: %v\n%s", err, out)
				}
				if m["msg"] != "shown" || m["k"] != "v" || m["level"] != "INFO" {
					t.Errorf("record = %v", m)
				}
			},
		},
		{
			name: "debug level",
			opts: Options{Level: slog.LevelDebug},
			log:  func(l *slog.Logger) { l.Debug("hidden?") },
			check: func(t *testing.T, out string) {
				if !strings.Contains(out, `"msg":"hidden?"`) {
					t.Errorf("debug record missing: %s", out)
				}
			},
		},
		{
			name: "text format",
			opts: Options{Format: FormatText},
			log:  func(l *slog.Logger) { l.Warn("careful", "k", "v") },
			check: func(t *testing.T, out string) {
				if !strings.Contains(out, "level=WARN msg=careful k=v") {
					t.Errorf("text output = %q", out)
				}
			},
		},
		{
			name: "sensitive keys are redacted without a Redactor",
			log:  func(l *slog.Logger) { l.Info("login", "password", "hunter22") },
			check: func(t *testing.T, out string) {
				if strings.Contains(out, "hunter22") || !strings.Contains(out, `"password":"[REDACTED]"`) {
					t.Errorf("password not redacted: %s", out)
				}
			},
		},
		{
			name: "registered secrets are redacted",
			opts: Options{Redactor: func() *Redactor { r := NewRedactor(); r.AddSecret("env-secret"); return r }()},
			log:  func(l *slog.Logger) { l.Info("using env-secret") },
			check: func(t *testing.T, out string) {
				if !strings.Contains(out, `"msg":"using [REDACTED]"`) {
					t.Errorf("secret not redacted: %s", out)
				}
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			tt.opts.Output = &buf
			tt.log(New(tt.opts))
			tt.check(t, strings.TrimSpace(buf.String()))
		})
	}
}

// TestNewWithRotatingFile logs through slog into a rotating file and checks
// that the file rotates at its cap and every file holds whole JSON records.
func TestNewWithRotatingFile(t *testing.T) {
	const maxSize = 512
	dir := t.TempDir()
	rf, err := OpenFile(FileOptions{Path: filepath.Join(dir, FileName), MaxSize: maxSize, MaxBackups: 2})
	if err != nil {
		t.Fatal(err)
	}
	l := New(Options{Output: rf})
	for i := range 100 {
		l.Info("request sent", "i", i, "Authorization", "Bearer xyz")
	}
	if err := rf.Close(); err != nil {
		t.Fatal(err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	if want := []string{FileName, FileName + ".1", FileName + ".2"}; strings.Join(names, ",") != strings.Join(want, ",") {
		t.Fatalf("files = %v, want %v", names, want)
	}
	for _, name := range names {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if len(b) > maxSize {
			t.Errorf("%s is %d bytes, over the %d cap", name, len(b), maxSize)
		}
		if bytes.Contains(b, []byte("xyz")) {
			t.Errorf("%s contains the unredacted token", name)
		}
		sc := bufio.NewScanner(bytes.NewReader(b))
		for sc.Scan() {
			if !json.Valid(sc.Bytes()) {
				t.Errorf("%s: invalid JSON record %q", name, sc.Text())
			}
		}
	}
}
