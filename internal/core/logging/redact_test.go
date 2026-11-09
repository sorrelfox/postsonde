package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"sync"
	"testing"
	"testing/slogtest"
)

const secret = "s3cr3t-value"

// testLogger returns a logger writing JSON without timestamps to a buffer,
// redacted by r.
func testLogger(r *Redactor) (*slog.Logger, *bytes.Buffer) {
	var buf bytes.Buffer
	h := slog.NewJSONHandler(&buf, &slog.HandlerOptions{
		ReplaceAttr: func(groups []string, a slog.Attr) slog.Attr {
			if len(groups) == 0 && a.Key == slog.TimeKey {
				return slog.Attr{}
			}
			return a
		},
	})
	return slog.New(r.Handler(h)), &buf
}

type secretBox struct{ v string }

func (b secretBox) LogValue() slog.Value { return slog.StringValue(b.v) }

func TestRedaction(t *testing.T) {
	tests := []struct {
		name string
		log  func(t *testing.T, l *slog.Logger, r *Redactor)
		want string
	}{
		{
			name: "sensitive key",
			log: func(t *testing.T, l *slog.Logger, r *Redactor) {
				l.Info("req", "Authorization", "Bearer abc", "user", "bob")
			},
			want: `{"level":"INFO","msg":"req","Authorization":"[REDACTED]","user":"bob"}`,
		},
		{
			name: "key match ignores case and separators",
			log: func(t *testing.T, l *slog.Logger, r *Redactor) {
				l.Info("m", "API-KEY", "k1", "Set_Cookie", "c=1", "X-Api-Key", "k2", "ClientSecret", "cs", "PROXY-AUTHORIZATION", "p")
			},
			want: `{"level":"INFO","msg":"m","API-KEY":"[REDACTED]","Set_Cookie":"[REDACTED]","X-Api-Key":"[REDACTED]","ClientSecret":"[REDACTED]","PROXY-AUTHORIZATION":"[REDACTED]"}`,
		},
		{
			name: "non-string values under sensitive keys",
			log: func(t *testing.T, l *slog.Logger, r *Redactor) {
				l.Info("m", "token", 12345, slog.Group("password", "old", "a", "new", "b"))
			},
			want: `{"level":"INFO","msg":"m","token":"[REDACTED]","password":"[REDACTED]"}`,
		},
		{
			name: "similar keys are kept",
			log: func(t *testing.T, l *slog.Logger, r *Redactor) {
				l.Info("m", "token_count", 3, "author", "ann", "cookies_enabled", true)
			},
			want: `{"level":"INFO","msg":"m","token_count":3,"author":"ann","cookies_enabled":true}`,
		},
		{
			name: "sensitive key in nested group",
			log: func(t *testing.T, l *slog.Logger, r *Redactor) {
				l.Info("m", slog.Group("req", "method", "GET", slog.Group("headers", "Cookie", "sid=1", "Accept", "*/*")))
			},
			want: `{"level":"INFO","msg":"m","req":{"method":"GET","headers":{"Cookie":"[REDACTED]","Accept":"*/*"}}}`,
		},
		{
			name: "custom key",
			log: func(t *testing.T, l *slog.Logger, r *Redactor) {
				r.AddKey("X-Session")
				l.Info("m", "x_session", "v1")
			},
			want: `{"level":"INFO","msg":"m","x_session":"[REDACTED]"}`,
		},
		{
			name: "secret in message",
			log: func(t *testing.T, l *slog.Logger, r *Redactor) {
				r.AddSecret(secret)
				l.Info("sending " + secret + " to server")
			},
			want: `{"level":"INFO","msg":"sending [REDACTED] to server"}`,
		},
		{
			name: "secret in string attrs and nested groups",
			log: func(t *testing.T, l *slog.Logger, r *Redactor) {
				r.AddSecret(secret)
				l.Info("m", "url", "https://x/?k="+secret, slog.Group("a", slog.Group("b", "body", `{"k":"`+secret+`"}`)))
			},
			want: `{"level":"INFO","msg":"m","url":"https://x/?k=[REDACTED]","a":{"b":{"body":"{\"k\":\"[REDACTED]\"}"}}}`,
		},
		{
			name: "secret in error",
			log: func(t *testing.T, l *slog.Logger, r *Redactor) {
				r.AddSecret(secret)
				l.Error("failed", "err", fmt.Errorf("send: %w", errors.New("auth failed for "+secret)))
			},
			want: `{"level":"ERROR","msg":"failed","err":"send: auth failed for [REDACTED]"}`,
		},
		{
			name: "secret and sensitive key in a header map",
			log: func(t *testing.T, l *slog.Logger, r *Redactor) {
				r.AddSecret(secret)
				l.Info("m", "headers", map[string][]string{
					"Authorization": {"Basic eDp5"},
					"X-Trace":       {"a", secret},
					"Accept":        {"*/*"},
				})
			},
			want: `{"level":"INFO","msg":"m","headers":{"Accept":["*/*"],"Authorization":"[REDACTED]","X-Trace":["a","[REDACTED]"]}}`,
		},
		{
			name: "secret in bound attrs registered after With",
			log: func(t *testing.T, l *slog.Logger, r *Redactor) {
				l2 := l.With("url", "https://h/"+secret)
				l2.Info("before")
				r.AddSecret(secret)
				l2.Info("after")
			},
			want: `{"level":"INFO","msg":"before","url":"https://h/` + secret + `"}` + "\n" +
				`{"level":"INFO","msg":"after","url":"https://h/[REDACTED]"}`,
		},
		{
			name: "secret under WithGroup",
			log: func(t *testing.T, l *slog.Logger, r *Redactor) {
				r.AddSecret(secret)
				l.WithGroup("req").With("hint", secret, "Cookie", "c").Info("m", "q", secret)
			},
			want: `{"level":"INFO","msg":"m","req":{"hint":"[REDACTED]","Cookie":"[REDACTED]","q":"[REDACTED]"}}`,
		},
		{
			name: "LogValuer is resolved before redaction",
			log: func(t *testing.T, l *slog.Logger, r *Redactor) {
				r.AddSecret(secret)
				l.Info("m", "box", secretBox{"x" + secret + "x"})
			},
			want: `{"level":"INFO","msg":"m","box":"x[REDACTED]x"}`,
		},
		{
			name: "struct containing a secret",
			log: func(t *testing.T, l *slog.Logger, r *Redactor) {
				r.AddSecret(secret)
				l.Info("m", "cfg", struct{ URL string }{"https://" + secret}, "plain", struct{ N int }{1})
			},
			want: `{"level":"INFO","msg":"m","cfg":"{URL:https://[REDACTED]}","plain":{"N":1}}`,
		},
		{
			name: "URL-escaped secret",
			log: func(t *testing.T, l *slog.Logger, r *Redactor) {
				r.AddSecret("p@ss word!/")
				l.Info("m", "query", "pw="+url.QueryEscape("p@ss word!/"), "path", "/u/"+url.PathEscape("p@ss word!/"))
			},
			want: `{"level":"INFO","msg":"m","query":"pw=[REDACTED]","path":"/u/[REDACTED]"}`,
		},
		{
			name: "longest secret wins",
			log: func(t *testing.T, l *slog.Logger, r *Redactor) {
				r.AddSecret("abcd")
				r.AddSecret("abcdefgh")
				l.Info("abcdefgh abcd")
			},
			want: `{"level":"INFO","msg":"[REDACTED] [REDACTED]"}`,
		},
		{
			name: "short values are not registered",
			log: func(t *testing.T, l *slog.Logger, r *Redactor) {
				for _, v := range []string{"", "a", "abc", "äöü"} {
					if r.AddSecret(v) {
						t.Errorf("AddSecret(%q) = true, want false", v)
					}
				}
				if !r.AddSecret("äöüß") {
					t.Error("AddSecret of 4 characters = false, want true")
				}
				l.Info("abc äöü yes")
			},
			want: `{"level":"INFO","msg":"abc äöü yes"}`,
		},
		{
			name: "removed secret is no longer masked",
			log: func(t *testing.T, l *slog.Logger, r *Redactor) {
				r.AddSecret(secret)
				r.RemoveSecret(secret)
				l.Info(secret)
			},
			want: `{"level":"INFO","msg":"` + secret + `"}`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			r := NewRedactor()
			l, buf := testLogger(r)
			tt.log(t, l, r)
			if got := strings.TrimSpace(buf.String()); got != tt.want {
				t.Errorf("got\n  %s\nwant\n  %s", got, tt.want)
			}
		})
	}
}

func TestRedactorDirect(t *testing.T) {
	var r Redactor // the zero value works
	if !r.IsSensitiveKey("Proxy-Authorization") || r.IsSensitiveKey("user") {
		t.Error("IsSensitiveKey on zero Redactor gives wrong answers")
	}
	r.AddSecret(secret)
	if got, want := r.Redact("a "+secret+" b"), "a [REDACTED] b"; got != want {
		t.Errorf("Redact = %q, want %q", got, want)
	}
	a := r.Attr(slog.Group("g", "password", "x", "v", secret))
	if got, want := a.String(), "g=[password=[REDACTED] v=[REDACTED]]"; got != want {
		t.Errorf("Attr = %q, want %q", got, want)
	}
}

// TestRedactHandlerConformance runs the standard slog handler test suite
// against the redacting wrapper.
func TestRedactHandlerConformance(t *testing.T) {
	var mu sync.Mutex
	bufs := map[*testing.T]*bytes.Buffer{}
	newHandler := func(t *testing.T) slog.Handler {
		mu.Lock()
		defer mu.Unlock()
		buf := new(bytes.Buffer)
		bufs[t] = buf
		r := NewRedactor()
		r.AddSecret("never-logged")
		return r.Handler(slog.NewJSONHandler(buf, nil))
	}
	result := func(t *testing.T) map[string]any {
		mu.Lock()
		defer mu.Unlock()
		var m map[string]any
		if err := json.Unmarshal(bufs[t].Bytes(), &m); err != nil {
			t.Fatal(err)
		}
		return m
	}
	slogtest.Run(t, newHandler, result)
}

// TestRedactionConcurrent logs from many goroutines through derived loggers
// while secrets are added and removed; run it with -race.
func TestRedactionConcurrent(t *testing.T) {
	r := NewRedactor()
	var buf syncBuffer
	l := New(Options{Output: &buf, Redactor: r})

	var wg sync.WaitGroup
	for g := range 8 {
		wg.Go(func() {
			lg := l.With("g", g, "bound", secret).WithGroup("req")
			for i := range 200 {
				lg.Info("msg "+secret, "i", i, "headers", map[string]string{"Cookie": "c"})
			}
		})
	}
	wg.Go(func() {
		for i := range 200 {
			r.AddSecret(fmt.Sprintf("other-secret-%d", i))
			r.RemoveSecret(fmt.Sprintf("other-secret-%d", i-1))
		}
	})
	wg.Wait()

	r.AddSecret(secret)
	buf.Reset()
	l.With("bound", secret).Info("final " + secret)
	if out := buf.String(); strings.Contains(out, secret) || strings.Count(out, Mask) != 2 {
		t.Errorf("secret not redacted after concurrent use: %s", out)
	}
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) Reset() {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.buf.Reset()
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

var _ io.Writer = (*syncBuffer)(nil)
