package request

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

func TestMethod(t *testing.T) {
	t.Parallel()

	valid := []string{"GET", "POST", "PUT", "PATCH", "DELETE", "HEAD", "OPTIONS", "TRACE", "QUERY", "PURGE", "PROPFIND", "get", "Get", "M-SEARCH", "X_1!#$%&'*+-.^_`|~"}
	for _, m := range valid {
		if err := Method(m); err != nil {
			t.Errorf("Method(%q) = %v", m, err)
		}
	}
	for _, m := range []string{"", "GE T", "GET\r\n", "GÉT", "GET/1", "{{m}}", "(GET)", "GET:"} {
		var e *errs.Error
		if err := Method(m); !errors.As(err, &e) || e.Kind != errs.InvalidArgument || e.Code != CodeInvalidMethod || e.Hint == "" {
			t.Errorf("Method(%q) = %v, want %s", m, err, CodeInvalidMethod)
		}
	}
	if err := Method(""); err == nil || err.Error() != "The request has no method." {
		t.Errorf("Method(\"\") = %v", err)
	}
	var e *errs.Error
	if err := Method("CONNECT"); !errors.As(err, &e) || e.Kind != errs.Unsupported || e.Code != CodeMethodUnsupported {
		t.Errorf("Method(CONNECT) = %v", err)
	}
	// connect is a custom method; only CONNECT opens a tunnel.
	if err := Method("connect"); err != nil {
		t.Errorf("Method(connect) = %v", err)
	}
}

func TestCanonicalMethod(t *testing.T) {
	t.Parallel()

	for m, want := range map[string]string{
		"GET": "", "get": "GET", "Post": "POST", "delete": "DELETE", "options": "OPTIONS",
		"query": "QUERY", "trace": "TRACE", "PURGE": "", "purge": "", "": "",
	} {
		if got := CanonicalMethod(m); got != want {
			t.Errorf("CanonicalMethod(%q) = %q, want %q", m, got, want)
		}
	}
}

func TestBodyForMethod(t *testing.T) {
	t.Parallel()

	body := httpclient.RawBody{Data: []byte("x")}
	for _, m := range []string{"GET", "POST", "DELETE", "OPTIONS", "PROPFIND", "head", "trace"} {
		if got, w := BodyForMethod(m, body); got == nil || w != nil {
			t.Errorf("BodyForMethod(%q) = %v, %v; want the body", m, got, w)
		}
	}
	for _, m := range []string{"HEAD", "TRACE"} {
		got, w := BodyForMethod(m, body)
		if got != nil || w == nil || w.Code != WarnBodyOmitted || w.Message == "" {
			t.Errorf("BodyForMethod(%q) = %v, %+v; want no body and a warning", m, got, w)
		}
		if got, w := BodyForMethod(m, nil); got != nil || w != nil {
			t.Errorf("BodyForMethod(%q, nil) = %v, %+v; want nothing", m, got, w)
		}
	}
}

// TestMethodsOnTheWire sends every standard method and custom ones with
// httpclient and checks that the server receives each unchanged, case
// included, with its body (none for HEAD).
func TestMethodsOnTheWire(t *testing.T) {
	type received struct {
		method string
		path   string
		body   string
	}
	got := make(chan received, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		data, _ := io.ReadAll(r.Body)
		got <- received{method: r.Method, path: r.URL.EscapedPath(), body: string(data)}
	}))
	defer srv.Close()
	c := httpclient.New(netx.NewDirect(netx.Options{}), httpclient.TransportOptions{})
	defer c.CloseIdleConnections()

	methods := append(append([]string(nil), StandardMethods...), "PURGE", "PROPFIND", "get", "Patch", "M-SEARCH")
	for _, method := range methods {
		t.Run(method, func(t *testing.T) {
			if err := Method(method); err != nil {
				t.Fatal(err)
			}
			raw, err := Body(projectfile.Body{Mode: "raw", Type: "text", Text: "payload"}, nil, "")
			if err != nil {
				t.Fatal(err)
			}
			body, warning := BodyForMethod(method, raw)
			url, err := SubstitutePath(srv.URL+"/items/:id/{sub}", []projectfile.KeyValue{kv("id", "a b/c"), kv("sub", "x")}, nil)
			if err != nil {
				t.Fatal(err)
			}
			resp, err := c.Send(context.Background(), httpclient.Request{Method: method, URL: url, Body: body, Settings: httpclient.DefaultSettings()})
			if err != nil {
				t.Fatalf("Send: %v", err)
			}
			if resp.StatusCode != http.StatusOK {
				t.Fatalf("status = %d", resp.StatusCode)
			}
			rec := <-got
			if rec.method != method {
				t.Errorf("method = %q, want %q", rec.method, method)
			}
			if rec.path != "/items/a%20b%2Fc/x" {
				t.Errorf("path = %q", rec.path)
			}
			wantBody := "payload"
			if method == "HEAD" {
				wantBody = ""
				if warning == nil {
					t.Error("no warning for the dropped HEAD body")
				}
			}
			if rec.body != wantBody {
				t.Errorf("body = %q, want %q", rec.body, wantBody)
			}
		})
	}
}
