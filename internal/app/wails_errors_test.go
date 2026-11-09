package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"testing"

	"github.com/wailsapp/wails/v3/pkg/application"
	wailserrs "github.com/wailsapp/wails/v3/pkg/errs"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// waitMethodID is the binding ID of SystemService.Wait, the one the
// generated frontend/bindings/.../services/system/systemservice.ts calls
// ($Call.ByID(711690568, ms)).
const waitMethodID = 711690568

// failingService stands for a service returning an error that is not an
// *errs.Error.
type failingService struct{}

// Fail returns a plain wrapped error.
func (*failingService) Fail(context.Context) error {
	return fmt.Errorf("render template: %w", errors.New("unexpected nil map"))
}

// unwiredService is registered without serviceOptions.
type unwiredService struct{}

// Fail returns a plain error.
func (*unwiredService) Fail(context.Context) error { return errors.New("boom") }

// TestWailsErrorRoundTrip proves how errors cross the Wails boundary. It
// registers the app's real services (services()) with Wails' own binding
// code, calls them (application.BoundMethod.Call), then wraps and encodes
// the error the way Wails v3.0.0 does before the frontend sees it:
// the message processor wraps the *application.CallError
// (messageprocessor_call.go) and the HTTP transport answers 422 with
// json.Marshal of it (transport_http.go, httpError). The runtime turns that
// body into `new RuntimeError(body.message)` with `cause = body.cause`.
//
// The bodies are golden files that frontend/src/lib/errors.test.ts and
// SystemDemo.test.tsx replay through the real @wailsio/runtime.
//
// Not parallel: application.New sets Wails' global application.
func TestWailsErrorRoundTrip(t *testing.T) {
	// Binding registration logs through Wails' global application.
	_ = application.New(application.Options{Logger: slog.New(slog.DiscardHandler)})

	logger := slog.New(slog.DiscardHandler)
	bindings := application.NewBindings(nil, nil)
	for _, s := range services(Config{Logger: logger}) {
		if err := bindings.Add(s); err != nil {
			t.Fatal(err)
		}
	}
	if err := bindings.Add(application.NewServiceWithOptions(&failingService{}, serviceOptions(logger))); err != nil {
		t.Fatal(err)
	}

	wait := bindings.GetByID(waitMethodID)
	if wait == nil {
		t.Fatalf("no bound method has ID %d; if SystemService.Wait was renamed or moved, regenerate the bindings and update waitMethodID", waitMethodID)
	}
	fail := bindings.Get(&application.CallOptions{MethodName: "github.com/sorrelfox/postsonde/internal/app.failingService.Fail"})
	if fail == nil {
		t.Fatal("failingService.Fail is not bound")
	}

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()

	tests := []struct {
		name      string
		method    *application.BoundMethod
		ctx       context.Context
		args      []string
		wantClass application.ErrorKind // the JS error class Wails picks
		wantKind  string                // ErrorDTO kind in the cause
		golden    string                // "" for no golden file
	}{
		{"invalid argument", wait, t.Context(), []string{"-1"}, application.RuntimeError, "invalid_argument", "wails/wait_invalid_argument"},
		{"cancelled", wait, cancelled, []string{"60000"}, application.RuntimeError, "cancelled", ""},
		{"unknown error", fail, t.Context(), nil, application.RuntimeError, "internal", "wails/unknown_error"},
		{"undecodable argument", wait, t.Context(), []string{`"soon"`}, application.TypeError, "internal", ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := callErrorBody(t, tt.method, tt.ctx, tt.args...)

			var got struct {
				Message string                `json:"message"`
				Kind    application.ErrorKind `json:"kind"`
				Cause   apperr.ErrorDTO       `json:"cause"`
			}
			if err := json.Unmarshal(body, &got); err != nil {
				t.Fatalf("body is not JSON: %v\n%s", err, body)
			}
			if got.Kind != tt.wantClass {
				t.Errorf("kind = %q, want %q", got.Kind, tt.wantClass)
			}
			if got.Cause.Kind != tt.wantKind || got.Cause.Message == "" || got.Cause.Details == nil || got.Cause.Causes == nil {
				t.Errorf("cause = %s, want a complete ErrorDTO of kind %q", body, tt.wantKind)
			}
			if tt.golden != "" {
				var pretty bytes.Buffer
				if err := json.Indent(&pretty, body, "", "  "); err != nil {
					t.Fatal(err)
				}
				golden.Assert(t, tt.golden, append(pretty.Bytes(), '\n'))
			}
		})
	}

	// Pins the v3.0.0 behaviour serviceOptions works around: the
	// application-wide marshaler passed to NewBindings (what
	// application.Options.MarshalError becomes) is not used for methods of
	// a service registered without its own MarshalError. If this starts
	// failing, Wails fixed it; the per-service option can then go.
	t.Run("app-level MarshalError is ignored", func(t *testing.T) {
		b := application.NewBindings(serviceOptions(logger).MarshalError, nil)
		if err := b.Add(application.NewService(&unwiredService{})); err != nil {
			t.Fatal(err)
		}
		m := b.Get(&application.CallOptions{MethodName: "github.com/sorrelfox/postsonde/internal/app.unwiredService.Fail"})
		if m == nil {
			t.Fatal("unwiredService.Fail is not bound")
		}
		body := callErrorBody(t, m, t.Context())
		if want := `{"message":"boom","cause":{},"kind":"RuntimeError"}`; string(body) != want {
			t.Errorf("body = %s, want %s", body, want)
		}
	})
}

// callErrorBody calls m, which must fail, and returns the body Wails' HTTP
// transport would answer with.
func callErrorBody(t *testing.T, m *application.BoundMethod, ctx context.Context, args ...string) []byte {
	t.Helper()
	raw := make([]json.RawMessage, len(args))
	for i, a := range args {
		raw[i] = json.RawMessage(a)
	}
	_, callErr := m.Call(ctx, raw)
	if callErr == nil {
		t.Fatal("call succeeded, want an error")
	}
	// As in MessageProcessor.processCallMethod.
	err := wailserrs.WrapBindingCallFailedErrorf(callErr, "Bound method returned an error")

	// As in HTTPTransport.httpError.
	cerr, ok := errors.AsType[*application.CallError](err)
	if !ok {
		t.Fatalf("%v does not wrap an *application.CallError; Wails would send it as plain text", err)
	}
	data, err := json.Marshal(cerr)
	if err != nil {
		t.Fatal(err)
	}
	return data
}
