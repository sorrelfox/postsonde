package request

import (
	"errors"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

func TestFormURLEncoded(t *testing.T) {
	t.Parallel()

	rows := func(r ...projectfile.KeyValue) []projectfile.KeyValue { return r }
	tests := []struct {
		name string
		rows []projectfile.KeyValue
		want string
	}{
		{"no rows", nil, ""},
		{"only disabled rows", rows(off("a", "1")), ""},
		{"simple", rows(kv("user", "mike"), kv("page", "2")), "user=mike&page=2"},
		{"space is plus", rows(kv("full name", "Ada  Lovelace ")), "full+name=Ada++Lovelace+"},
		{"ampersand and equals are encoded", rows(kv("a&b=c", "x=1&y=2")), "a%26b%3Dc=x%3D1%26y%3D2"},
		{"plus is encoded", rows(kv("q", "1+1")), "q=1%2B1"},
		{"percent is encoded, not decoded", rows(kv("p", "100%"), kv("pre", "a%20b")), "p=100%25&pre=a%2520b"},
		{"unreserved stay as they are", rows(kv("AZaz09", "*-._")), "AZaz09=*-._"},
		{"tilde and others are encoded", rows(kv("s", "~!'()/?:@#[]$,;\"<>\\^`{|}")), "s=%7E%21%27%28%29%2F%3F%3A%40%23%5B%5D%24%2C%3B%22%3C%3E%5C%5E%60%7B%7C%7D"},
		{"unicode as UTF-8 bytes", rows(kv("имя", "Zoë 😀")), "%D0%B8%D0%BC%D1%8F=Zo%C3%AB+%F0%9F%98%80"},
		{"line breaks and tab", rows(kv("t", "a\r\nb\tc")), "t=a%0D%0Ab%09c"},
		{"empty value keeps the equals sign", rows(kv("flag", "")), "flag="},
		{"empty name with a value", rows(kv("", "orphan")), "=orphan"},
		{"blank rows are skipped", rows(kv("", ""), kv("a", "1"), kv("", "")), "a=1"},
		{"duplicates keep order", rows(kv("tag", "x"), kv("id", "1"), kv("tag", "y")), "tag=x&id=1&tag=y"},
		{"disabled rows are skipped anywhere", rows(off("first", "0"), kv("a", "1"), off("mid", "m"), kv("b", "2"), off("last", "9")), "a=1&b=2"},
		{"unexpanded variables are encoded as text", rows(kv("{{k}}", "{{v}}")), "%7B%7Bk%7D%7D=%7B%7Bv%7D%7D"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := FormURLEncoded(tt.rows, nil)
			if err != nil {
				t.Fatalf("FormURLEncoded: %v", err)
			}
			if got != tt.want {
				t.Errorf("FormURLEncoded = %q, want %q", got, tt.want)
			}
		})
	}
}

// TestFormURLEncodedRoundTrip decodes the encoding with net/url: every
// enabled row comes back as typed, in order.
func TestFormURLEncodedRoundTrip(t *testing.T) {
	t.Parallel()

	in := []projectfile.KeyValue{
		kv("a b", "c+d"), kv("x&y", "=%="), off("gone", "1"), kv("ü", "😀 é"), kv("a b", ""), kv("*-._~", "\x00\x7f"),
	}
	encoded, err := FormURLEncoded(in, nil)
	if err != nil {
		t.Fatal(err)
	}
	q, err := url.ParseQuery(encoded)
	if err != nil {
		t.Fatalf("ParseQuery(%q): %v", encoded, err)
	}
	want := url.Values{"a b": {"c+d", ""}, "x&y": {"=%="}, "ü": {"😀 é"}, "*-._~": {"\x00\x7f"}}
	if !reflect.DeepEqual(q, want) {
		t.Errorf("decoded %q = %#v, want %#v", encoded, q, want)
	}
}

func TestFormURLEncodedExpand(t *testing.T) {
	t.Parallel()

	resolver := template.Resolver{Scopes: template.Chain{template.Vars("env", map[string]string{
		"field": "user name",
		"pass":  "p&ss=w0rd%",
	})}}
	expand := func(s string) (string, error) {
		r := resolver.Render(s)
		return r.Text, r.Err()
	}

	// Variables are rendered first, then the rendered text is encoded;
	// disabled rows are not rendered.
	got, err := FormURLEncoded([]projectfile.KeyValue{kv("{{field}}", "mike"), kv("password", "{{pass}}"), off("{{missing}}", "{{missing}}")}, expand)
	if err != nil {
		t.Fatalf("FormURLEncoded: %v", err)
	}
	if want := "user+name=mike&password=p%26ss%3Dw0rd%25"; got != want {
		t.Errorf("FormURLEncoded = %q, want %q", got, want)
	}

	_, err = FormURLEncoded([]projectfile.KeyValue{kv("a", "1"), kv("token", "{{missing}}")}, expand)
	wantFormErr(t, err, "template.unresolved", "1", "value", "token")

	_, err = FormURLEncoded([]projectfile.KeyValue{kv("{{missing}}", "x")}, expand)
	wantFormErr(t, err, "template.unresolved", "0", "name", "{{missing}}")

	plain := errors.New("boom")
	_, err = FormURLEncoded([]projectfile.KeyValue{kv("", "secret-value")}, func(string) (string, error) { return "", plain })
	e := wantFormErr(t, err, "", "0", "name", "")
	if !errors.Is(err, plain) {
		t.Error("the expand error is not kept as the cause")
	}
	for _, s := range append(errs.Chain(err), e.Message, e.Hint) {
		if strings.Contains(s, "secret-value") {
			t.Errorf("error text %q contains the value", s)
		}
	}

	// Through Body.
	b, err := Body(projectfile.Body{Mode: "urlencoded", Fields: []projectfile.KeyValue{kv("p", "{{missing}}")}}, expand, "")
	if b != nil {
		t.Errorf("Body = %#v, want nil on error", b)
	}
	wantFormErr(t, err, "template.unresolved", "0", "value", "p")
}

func wantFormErr(t *testing.T, err error, code, row, field, name string) *errs.Error {
	t.Helper()
	var e *errs.Error
	if !errors.As(err, &e) {
		t.Fatalf("error = %T %v, want *errs.Error", err, err)
	}
	if e.Kind != errs.InvalidArgument || e.Code != code {
		t.Fatalf("kind, code = %q, %q, want invalid_argument, %q (%v)", e.Kind, e.Code, code, err)
	}
	for k, v := range map[string]string{DetailRow: row, DetailField: field, DetailFormField: name} {
		if e.Details[k] != v {
			t.Errorf("detail %q = %q, want %q", k, e.Details[k], v)
		}
	}
	if e.Hint == "" {
		t.Error("no hint")
	}
	return e
}
