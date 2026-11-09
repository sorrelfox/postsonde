package request

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

func kv(name, value string) projectfile.KeyValue {
	return projectfile.KeyValue{Name: name, Value: value}
}

func off(name, value string) projectfile.KeyValue {
	return projectfile.KeyValue{Name: name, Value: value, Disabled: true}
}

func on(name, value string) httpclient.Header {
	return httpclient.Header{Name: name, Value: value, Enabled: true}
}

func TestHeadersMapping(t *testing.T) {
	tests := []struct {
		name string
		rows []projectfile.KeyValue
		want []httpclient.Header
	}{
		{"no rows", nil, []httpclient.Header{}},
		{"all disabled", []projectfile.KeyValue{off("Accept", "*/*")}, []httpclient.Header{}},
		{
			"skips disabled rows, keeps order",
			[]projectfile.KeyValue{kv("B", "2"), off("Skip", "x"), kv("A", "1")},
			[]httpclient.Header{on("B", "2"), on("A", "1")},
		},
		{
			"skips rows without a name, whatever the value",
			[]projectfile.KeyValue{kv("", ""), kv("", "orphan value"), kv("X", "y")},
			[]httpclient.Header{on("X", "y")},
		},
		{
			"keeps duplicates and spelling",
			[]projectfile.KeyValue{kv("x-dup", "a"), kv("X-DUP", "b"), kv("x-dup", "c"), kv("X-MiXeD-CaSe", "v")},
			[]httpclient.Header{on("x-dup", "a"), on("X-DUP", "b"), on("x-dup", "c"), on("X-MiXeD-CaSe", "v")},
		},
		{
			"keeps values as typed: spaces, tabs, empty, non-ASCII",
			[]projectfile.KeyValue{kv("A", "  padded\t"), kv("Empty", ""), kv("Utf", "Zoë")},
			[]httpclient.Header{on("A", "  padded\t"), on("Empty", ""), on("Utf", "Zoë")},
		},
		{
			"accepts every token character in names",
			[]projectfile.KeyValue{kv("!#$%&'*+-.^_`|~09azAZ", "v")},
			[]httpclient.Header{on("!#$%&'*+-.^_`|~09azAZ", "v")},
		},
		{
			"does not validate disabled rows",
			[]projectfile.KeyValue{off("Bad Name", "line\r\nbreak"), kv("Ok", "1")},
			[]httpclient.Header{on("Ok", "1")},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Headers(tt.rows, nil)
			if err != nil {
				t.Fatalf("Headers: %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("Headers = %#v, want %#v", got, tt.want)
			}
		})
	}
}

// wantRowErr checks err is an *errs.Error with kind, code and the row details.
func wantRowErr(t *testing.T, err error, kind errs.Kind, code, row, field, header string) *errs.Error {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) {
		t.Fatalf("error = %T %v, want *errs.Error", err, err)
	}
	if e.Kind != kind || e.Code != code {
		t.Fatalf("kind, code = %q, %q, want %q, %q (%v)", e.Kind, e.Code, kind, code, err)
	}
	want := map[string]string{DetailRow: row, DetailField: field, DetailHeader: header}
	for k, v := range want {
		if e.Details[k] != v {
			t.Errorf("detail %q = %q, want %q", k, e.Details[k], v)
		}
	}
	if e.Hint == "" {
		t.Error("no hint")
	}
	return e
}

func TestHeadersValidation(t *testing.T) {
	tests := []struct {
		name   string
		rows   []projectfile.KeyValue
		code   string
		row    string
		field  string
		header string
	}{
		{"space in name", []projectfile.KeyValue{kv("A", "1"), kv("Bad Name", "x")}, CodeInvalidHeaderName, "1", "name", "Bad Name"},
		{"trailing space in name", []projectfile.KeyValue{kv("Accept ", "x")}, CodeInvalidHeaderName, "0", "name", "Accept "},
		{"colon in name", []projectfile.KeyValue{kv("Accept:", "x")}, CodeInvalidHeaderName, "0", "name", "Accept:"},
		{"non-ASCII name", []projectfile.KeyValue{kv("Zoë", "x")}, CodeInvalidHeaderName, "0", "name", "Zoë"},
		{"unexpanded variable in name", []projectfile.KeyValue{kv("{{h}}", "x")}, CodeInvalidHeaderName, "0", "name", "{{h}}"},
		{"row index counts skipped rows", []projectfile.KeyValue{off("x", ""), kv("", ""), kv("A", "a\nb")}, CodeInvalidHeaderValue, "2", "value", "A"},
		{"CR in value", []projectfile.KeyValue{kv("A", "a\rb")}, CodeInvalidHeaderValue, "0", "value", "A"},
		{"CRLF injection", []projectfile.KeyValue{kv("A", "a\r\nX-Injected: 1")}, CodeInvalidHeaderValue, "0", "value", "A"},
		{"NUL in value", []projectfile.KeyValue{kv("A", "a\x00")}, CodeInvalidHeaderValue, "0", "value", "A"},
		{"other control in value", []projectfile.KeyValue{kv("A", "\x1b[0m")}, CodeInvalidHeaderValue, "0", "value", "A"},
		{"DEL in value", []projectfile.KeyValue{kv("A", "a\x7f")}, CodeInvalidHeaderValue, "0", "value", "A"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := Headers(tt.rows, nil)
			if got != nil {
				t.Errorf("headers = %#v, want nil on error", got)
			}
			wantRowErr(t, err, errs.InvalidArgument, tt.code, tt.row, tt.field, tt.header)
		})
	}
}

func TestHeadersErrorsNeverShowValues(t *testing.T) {
	const secret = "s3cr3t-token"
	_, err := Headers([]projectfile.KeyValue{kv("Authorization", "Bearer "+secret+"\n")}, nil)
	if err == nil {
		t.Fatal("no error")
	}
	for _, s := range append(errs.Chain(err), err.Error(), err.(*errs.Error).Hint) {
		if strings.Contains(s, secret) {
			t.Errorf("error text %q contains the value", s)
		}
	}
	for k, v := range err.(*errs.Error).Details {
		if strings.Contains(v, secret) {
			t.Errorf("detail %q contains the value", k)
		}
	}
}

func TestHeadersExpand(t *testing.T) {
	resolver := template.Resolver{Scopes: template.Chain{template.Vars("env", map[string]string{
		"h":     "X-Trace",
		"id":    "abc",
		"empty": "",
		"crlf":  "a\r\nb",
	})}}
	expand := func(s string) (string, error) {
		r := resolver.Render(s)
		return r.Text, r.Err()
	}

	got, err := Headers([]projectfile.KeyValue{kv("{{h}}", "id-{{id}}"), off("{{missing}}", "{{missing}}"), kv("", "{{missing}}")}, expand)
	if err != nil {
		t.Fatalf("Headers: %v", err)
	}
	if want := []httpclient.Header{on("X-Trace", "id-abc")}; !reflect.DeepEqual(got, want) {
		t.Errorf("Headers = %#v, want %#v", got, want)
	}

	_, err = Headers([]projectfile.KeyValue{kv("A", "{{missing}}")}, expand)
	e := wantRowErr(t, err, errs.InvalidArgument, "template.unresolved", "0", "value", "A")
	if !strings.Contains(e.Message, "missing") {
		t.Errorf("message %q does not name the variable", e.Message)
	}

	_, err = Headers([]projectfile.KeyValue{kv("{{empty}}", "x")}, expand)
	wantRowErr(t, err, errs.InvalidArgument, CodeInvalidHeaderName, "0", "name", "{{empty}}")

	_, err = Headers([]projectfile.KeyValue{kv("A", "{{crlf}}")}, expand)
	wantRowErr(t, err, errs.InvalidArgument, CodeInvalidHeaderValue, "0", "value", "A")

	plain := errors.New("boom")
	_, err = Headers([]projectfile.KeyValue{kv("A", "v")}, func(string) (string, error) { return "", plain })
	wantRowErr(t, err, errs.InvalidArgument, "", "0", "name", "A")
	if !errors.Is(err, plain) {
		t.Error("the expand error is not kept as the cause")
	}
}

// TestHeadersOnTheWire sends mapped rows with httpclient: disabled rows do
// not arrive, duplicates do.
func TestHeadersOnTheWire(t *testing.T) {
	got := make(chan http.Header, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Header.Clone()
	}))
	defer srv.Close()

	headers, err := Headers([]projectfile.KeyValue{
		kv("Accept", "application/json"),
		off("X-Disabled", "nope"),
		kv("X-Dup", "a"),
		kv("", "no name"),
		kv("X-Dup", "b"),
	}, nil)
	if err != nil {
		t.Fatalf("Headers: %v", err)
	}
	c := httpclient.New(netx.NewDirect(netx.Options{}), httpclient.TransportOptions{})
	defer c.CloseIdleConnections()
	resp, err := c.Send(context.Background(), httpclient.Request{Method: "GET", URL: srv.URL + "/", Headers: headers, Settings: httpclient.DefaultSettings()})
	if err != nil {
		t.Fatalf("Send: %v", err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d", resp.StatusCode)
	}
	h := <-got
	if v := h.Values("X-Disabled"); len(v) != 0 {
		t.Errorf("X-Disabled was sent: %q", v)
	}
	if v := h.Values("X-Dup"); !reflect.DeepEqual(v, []string{"a", "b"}) {
		t.Errorf("X-Dup = %q, want [a b]", v)
	}
	if v := h.Get("Accept"); v != "application/json" {
		t.Errorf("Accept = %q", v)
	}
}
