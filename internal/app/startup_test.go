package app

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"testing"
	"time"
)

// The startup record is what scripts/measure-footprint.sh looks for: its
// message and a whole number of milliseconds.
func TestLogStartupTime(t *testing.T) {
	var buf bytes.Buffer
	logStartupTime(slog.New(slog.NewJSONHandler(&buf, nil)), 734*time.Millisecond+600*time.Microsecond)
	var rec struct {
		Msg       string `json:"msg"`
		StartupMs *int64 `json:"startupMs"`
	}
	if err := json.Unmarshal(buf.Bytes(), &rec); err != nil {
		t.Fatalf("log record %q: %v", buf.String(), err)
	}
	if rec.Msg != startupMessage || rec.StartupMs == nil || *rec.StartupMs != 734 {
		t.Fatalf("log record %q, want msg %q and startupMs 734", buf.String(), startupMessage)
	}
}

func TestLogStartupNoWindow(t *testing.T) {
	logStartup(nil, nil) // tests of the services open no window
}
