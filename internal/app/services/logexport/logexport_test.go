package logexport

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	core "github.com/sorrelfox/postsonde/internal/core/logexport"
	"github.com/sorrelfox/postsonde/internal/core/secretstore"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// While the encrypted secrets are locked, their values cannot be
// read to mask a log that may hold them: copying and saving are refused.
func TestMaskRefusedWhileSecretsLocked(t *testing.T) {
	t.Parallel()
	locked := errs.New(errs.PermissionDenied, "Your stored secrets are locked.").WithCode(secretstore.CodeLocked)
	s := New(func(context.Context) ([]string, error) {
		return nil, errors.Join(errors.New("other"), locked)
	}, nil, nil)
	_, err := s.Mask(context.Background(), MaskInputDTO{Text: textLog, Format: "text"})
	if !secretstore.IsLocked(err) {
		t.Fatalf("Mask = %v", err)
	}
}

const (
	envSecret  = "s3cr3t-env-value"
	authSecret = "tok_live_abc123"
	mask       = "••••••"
)

func secrets(ctx context.Context) ([]string, error) {
	return []string{envSecret, authSecret}, errors.New("one side file could not be read")
}

// The entries as the UI formats them, holding secrets the way the logs
// can: in a WebSocket URL, a sent message, an Authorization line of a
// timeline's request head, gRPC metadata and a JSON field.
const textLog = `2026-09-19 14:03:27.042  System  connecting
Connecting to wss://api.example.com/ws?token=` + authSecret + `

2026-09-19 14:03:27.310  Sent  text  40 B
{"password":"` + envSecret + `"}

Request
GET /v1 HTTP/1.1
Host: api.example.com
Authorization: Basic dXNlcjpwYXNzd29yZA==

2026-09-19 14:03:28.000  Received  headers
authorization: Bearer ` + authSecret + `
`

func jsonLog(t *testing.T) string {
	t.Helper()
	b, err := json.MarshalIndent(map[string]any{
		"format":  "postsonde-log",
		"version": 1,
		"entries": []any{
			map[string]any{"direction": "sent", "text": `{"password":"` + envSecret + `"}`},
			map[string]any{"head": "GET /v1 HTTP/1.1\r\nAuthorization: Bearer opaque-credential\r\n\r\n"},
			map[string]any{"meta": map[string]any{"authorization": "Basic dXNlcjpwYXNzd29yZA=="}},
		},
	}, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

var leaks = []string{envSecret, authSecret, "dXNlcjpwYXNzd29yZA==", "opaque-credential"}

func assertNoSecrets(t *testing.T, where, text string) {
	t.Helper()
	for _, s := range leaks {
		if strings.Contains(text, s) {
			t.Errorf("%s holds the secret %q:\n%s", where, s, text)
		}
	}
	if !strings.Contains(text, mask) {
		t.Errorf("%s has no mask:\n%s", where, text)
	}
}

func TestMask(t *testing.T) {
	t.Parallel()
	s := New(secrets, nil, nil)
	ctx := context.Background()

	res, err := s.Mask(ctx, MaskInputDTO{Text: textLog, Format: "text"})
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecrets(t, "text", res.Text)
	if !res.Masked {
		t.Error("Masked = false")
	}
	if !strings.Contains(res.Text, "wss://api.example.com/ws?token="+mask) || !strings.Contains(res.Text, "Authorization: Basic "+mask) {
		t.Errorf("text:\n%s", res.Text)
	}

	res, err = s.Mask(ctx, MaskInputDTO{Text: jsonLog(t), Format: "json"})
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecrets(t, "json", res.Text)
	if !json.Valid([]byte(res.Text)) {
		t.Errorf("masked JSON does not parse:\n%s", res.Text)
	}

	res, err = s.Mask(ctx, MaskInputDTO{Text: "nothing to hide", Format: "text"})
	if err != nil || res.Masked || res.Text != "nothing to hide" {
		t.Errorf("plain = %+v, %v", res, err)
	}
}

func TestMaskErrors(t *testing.T) {
	t.Parallel()
	s := New(nil, nil, nil)
	tests := []struct {
		name string
		in   MaskInputDTO
		kind errs.Kind
		code string
	}{
		{"unknown format", MaskInputDTO{Text: "x", Format: "csv"}, errs.InvalidArgument, CodeInvalidFormat},
		{"bad JSON", MaskInputDTO{Text: "{", Format: "json"}, errs.InvalidArgument, CodeInvalidJSON},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := s.Mask(context.Background(), tt.in)
			var e *errs.Error
			if !errors.As(err, &e) || errs.KindOf(err) != tt.kind || e.Code != tt.code {
				t.Errorf("err = %v, want %s %s", err, tt.kind, tt.code)
			}
		})
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := s.Mask(ctx, MaskInputDTO{Text: "x", Format: "text"}); !errors.Is(err, context.Canceled) {
		t.Errorf("cancelled err = %v", err)
	}
}

func TestSave(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	var asked SaveDialog
	target := filepath.Join(dir, "ws.log.txt")
	pick := func(_ context.Context, d SaveDialog) (string, error) {
		asked = d
		return target, nil
	}
	s := New(secrets, pick, nil)

	res, err := s.Save(context.Background(), SaveInputDTO{Title: "WebSocket log", FileName: "Echo: ws", Format: "text", Content: strings.TrimSuffix(textLog, "\n")})
	if err != nil {
		t.Fatal(err)
	}
	if !res.Saved || res.Path != target || !res.Masked {
		t.Errorf("result = %+v", res)
	}
	if asked.Filename != "Echo- ws.txt" || asked.Title != "Save WebSocket log" || asked.Message != "Save the WebSocket log as plain text, secrets masked." {
		t.Errorf("dialog = %+v", asked)
	}
	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecrets(t, "saved text", string(data))
	if int64(len(data)) != res.Size || !strings.HasSuffix(string(data), "\n") {
		t.Errorf("size %d, file %d bytes", res.Size, len(data))
	}

	target = filepath.Join(dir, "timeline.json")
	res, err = s.Save(context.Background(), SaveInputDTO{Title: "Timeline", FileName: "timeline.json", Format: "json", Content: jsonLog(t)})
	if err != nil {
		t.Fatal(err)
	}
	if asked.Filename != "timeline.json" {
		t.Errorf("json file name = %q", asked.Filename)
	}
	data, err = os.ReadFile(target)
	if err != nil {
		t.Fatal(err)
	}
	assertNoSecrets(t, "saved JSON", string(data))
	if !json.Valid(data) || !res.Saved {
		t.Errorf("saved JSON:\n%s", data)
	}
}

func TestSaveCancelledAndFailures(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	cancelled := New(secrets, func(context.Context, SaveDialog) (string, error) { return "", nil }, nil)
	res, err := cancelled.Save(context.Background(), SaveInputDTO{Format: "text", Content: "x"})
	if err != nil || res.Saved {
		t.Errorf("cancelled = %+v, %v", res, err)
	}
	if entries, _ := os.ReadDir(dir); len(entries) != 0 {
		t.Errorf("files written: %v", entries)
	}

	missing := New(secrets, func(context.Context, SaveDialog) (string, error) {
		return filepath.Join(dir, "no", "such", "dir", "log.txt"), nil
	}, nil)
	_, err = missing.Save(context.Background(), SaveInputDTO{Format: "text", Content: "x"})
	var e *errs.Error
	if !errors.As(err, &e) || e.Code != CodeSaveFailed || e.Hint == "" {
		t.Errorf("unwritable err = %v", err)
	}

	_, err = New(nil, nil, nil).Save(context.Background(), SaveInputDTO{Format: "text"})
	if !errors.As(err, &e) || e.Code != CodeUnavailable {
		t.Errorf("no dialog err = %v", err)
	}
	// A bad format fails before the dialog shows.
	shown := false
	_, err = New(nil, func(context.Context, SaveDialog) (string, error) { shown = true; return "", nil }, nil).
		Save(context.Background(), SaveInputDTO{Format: "yaml"})
	if !errors.As(err, &e) || e.Code != CodeInvalidFormat || shown {
		t.Errorf("bad format err = %v, dialog shown %v", err, shown)
	}
}

func TestFileName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		f    core.Format
		want string
	}{
		{"", core.Text, "postsonde-log.txt"},
		{"  Console  ", core.JSON, "Console.json"},
		{"a/b:c", core.Text, "a-b-c.txt"},
		{"log.TXT", core.Text, "log.txt"},
		{"log.txt", core.JSON, "log.txt.json"},
		{"...", core.Text, "postsonde-log.txt"},
		{strings.Repeat("x", 150), core.Text, strings.Repeat("x", 100) + ".txt"},
	}
	for _, tt := range tests {
		if got := FileName(tt.name, tt.f); got != tt.want {
			t.Errorf("FileName(%q, %s) = %q, want %q", tt.name, tt.f, got, tt.want)
		}
	}
}

// TestDTOJSON pins the JSON shape of the DTOs, the contract with the
// generated TypeScript models.
func TestDTOJSON(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		dto  any
	}{
		{"mask_input", MaskInputDTO{Text: "Connecting to wss://h/ws", Format: "text"}},
		{"mask_result", MaskResultDTO{Text: "token=" + mask, Masked: true}},
		{"save_input", SaveInputDTO{Title: "Console", FileName: "console", Format: "json", Content: "{}"}},
		{"save_result", SaveResultDTO{Saved: true, Path: "/Users/me/console.json", Size: 2, Masked: false}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := json.MarshalIndent(tt.dto, "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			golden.Assert(t, "dto/"+tt.name, append(got, '\n'))
		})
	}
}
