package template

import (
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

const fox = "The quick brown fox jumps over the lazy dog"

// filterScopes are the variables of the filter tests.
func filterScopes() Chain {
	return Chain{
		MapScope{Label: "Environment: dev", Variables: map[string]Variable{
			"hello":  {Value: "hello"},
			"fox":    {Value: fox},
			"key":    {Value: "key"},
			"token":  {Value: "s3cr3t", Secret: true},
			"sigKey": {Value: "key", Secret: true},
			"nested": {Value: "{{hello}}!"},
			"auth":   {Value: `{"user":"ada","pass":"x y"}`},
			"b64":    {Value: "aGVsbG8="},
			// A scope that defines a name with a "|" wins over the pipe.
			"a | base64": {Value: "defined"},
		}},
	}
}

// TestFilters checks every filter against known outputs (the digests were
// computed with openssl dgst).
func TestFilters(t *testing.T) {
	t.Parallel()
	tests := []struct{ src, want string }{
		{"{{hello | base64}}", "aGVsbG8="},
		{"{{ hello|base64 }}", "aGVsbG8="},
		{`{{"subjects?_d" | base64}}`, "c3ViamVjdHM/X2Q="},
		{`{{"subjects?_d" | base64url}}`, "c3ViamVjdHM_X2Q"},
		{`{{"" | base64}}`, ""},
		{"{{b64 | base64Decode}}", "hello"},
		{`{{"aGVsbG8" | base64Decode}}`, "hello"},
		{`{{"c3ViamVjdHM_X2Q" | base64Decode}}`, "subjects?_d"},
		{`{{"c3ViamVjdHM/X2Q=" | base64Decode}}`, "subjects?_d"},
		{`{{" aGVsbG8= " | base64Decode}}`, "hello"},
		{`{{"a b&c=d/é~" | urlEncode}}`, "a%20b%26c%3Dd%2F%C3%A9~"},
		{`{{"a+b!*'()" | urlEncode}}`, "a%2Bb%21%2A%27%28%29"},
		{`{{"a%20b+c%C3%A9" | urlDecode}}`, "a b+cé"},
		{"{{hello | hex}}", "68656c6c6f"},
		{`{{"68656C6c6f" | hexDecode}}`, "hello"},
		{"{{hello | md5}}", "5d41402abc4b2a76b9719d911017c592"},
		{"{{hello | sha1}}", "aaf4c61ddcc5e8a2dabede0f3b482cd9aea9434d"},
		{"{{hello | sha256}}", "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"},
		{"{{hello | sha256 hex}}", "2cf24dba5fb0a30e26e83b2ac5b9e29e1b161e5c1fa7425e73043362938b9824"},
		{"{{hello | sha256 base64}}", "LPJNul+wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ="},
		{"{{hello | sha256 base64url}}", "LPJNul-wow4m6DsqxbninhsWHlwfp0JecwQzYpOLmCQ"},
		{"{{hello | sha512}}", "9b71d224bd62f3785d96d46ad3ea3d73319bfbc2890caadae2dff72519673ca72323c3d99ba5c11d7c7acc6e14b8c5da0c4663475c2e5c3adef46f73bcdec043"},
		{"{{fox | hmacMd5 key}}", "80070713463e7749b90c2dc24911e275"},
		{"{{fox | hmacSha1 key}}", "de7c9b85b8b78aa6bc8a7a36f70a90701c9db4d9"},
		{"{{fox | hmacSha256 key}}", "f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8"},
		{`{{fox | hmacSha256 "key"}}`, "f7bc83f430538424b13298e6aa6fb143ef4d59a14946175997479dbc2d1a3cd8"},
		{"{{fox | hmacSha256 key base64}}", "97yD9DBThCSxMpjmqm+xQ+9NWaFJRhdZl0edvC0aPNg="},
		{"{{fox | hmacSha512 key}}", "b42af09057bac1e2d41708e48a902e09b5ff7f12ab428a4fe86653c73dd248fb82f948a549f7b791a5b41915ee4d1ec3935357e4e2317250d0372afa2ebeeb3a"},
		// Chains apply left to right.
		{"{{hello | base64 | base64Decode}}", "hello"},
		{"{{hello | hex | hexDecode | urlEncode}}", "hello"},
		{`{{"x|y" | urlEncode}}`, "x%7Cy"},
		// Inputs: nested variables, dot notation, dynamic values.
		{"{{nested | base64}}", "aGVsbG8h"},
		{"{{auth.pass | urlEncode}}", "x%20y"},
		{"{{$randomInt 7 7 | md5}}", "8f14e45fceea167a5a36dedd4bea2543"},
		{`{{$datetime "YYYY|MM" | base64}}`, "MjAyNnwwOQ=="},
		// A defined name wins; a valid dynamic value is not a pipe.
		{"{{a | base64}}", "defined"},
		{"{{$datetime YYYY|MM}}", "2026|09"},
	}
	for _, tt := range tests {
		t.Run(tt.src, func(t *testing.T) {
			t.Parallel()
			r := dynResolver(filterScopes()...)
			if got := renderOne(t, r, tt.src); got != tt.want {
				t.Errorf("Render(%q) = %q, want %q", tt.src, got, tt.want)
			}
		})
	}
}

func TestFilterRefs(t *testing.T) {
	t.Parallel()
	r := dynResolver(filterScopes()...)

	res := r.Render("{{ hello | base64 }}")
	ref := res.Refs[0]
	if !ref.Resolved || ref.Scope != "Environment: dev" || ref.Secret || res.Secret {
		t.Errorf("ref = %+v", ref)
	}
	if len(ref.Nested) != 1 || ref.Nested[0].Name != "hello" || ref.Nested[0].Value != "hello" {
		t.Errorf("nested = %+v, want the input", ref.Nested)
	}

	res = r.Render(`{{"x" | base64}}`)
	if ref := res.Refs[0]; !ref.Resolved || ref.Scope != DynamicScope || len(ref.Nested) != 0 {
		t.Errorf("quoted input ref = %+v", ref)
	}
}

// TestFilterSecrecy: an encoding or a hash of a secret is secret, and so is
// an HMAC with a secret key, so the timeline masks them.
func TestFilterSecrecy(t *testing.T) {
	t.Parallel()
	tests := []struct {
		src    string
		secret bool
	}{
		{"{{token | base64}}", true},
		{"{{token | sha256}}", true},
		{"{{token | base64 | base64Decode}}", true},
		{"{{fox | hmacSha256 sigKey}}", true},
		{"{{token | hmacSha256 key}}", true},
		{`{{"x" | hmacSha256 sigKey}}`, true},
		{"{{fox | hmacSha256 key}}", false},
		{`{{fox | hmacSha256 "key"}}`, false},
		{"{{hello | md5}}", false},
	}
	for _, tt := range tests {
		t.Run(tt.src, func(t *testing.T) {
			t.Parallel()
			res := dynResolver(filterScopes()...).Render(tt.src)
			if err := res.Err(); err != nil {
				t.Fatal(err)
			}
			if res.Secret != tt.secret || res.Refs[0].Secret != tt.secret {
				t.Errorf("Secret = %v (ref %v), want %v", res.Secret, res.Refs[0].Secret, tt.secret)
			}
		})
	}

	// A secret key is a nested reference, so its value can be masked too.
	res := dynResolver(filterScopes()...).Render("{{fox | hmacSha256 sigKey}}")
	n := res.Refs[0].Nested
	if len(n) != 2 || n[0].Name != "fox" || n[1].Name != "sigKey" || !n[1].Secret || n[1].Value != "key" {
		t.Errorf("nested = %+v, want the input and the secret key", n)
	}
	// Secrecy also propagates through a variable whose value is a pipe.
	r := dynResolver(Vars("s", map[string]string{"sig": "{{fox | hmacSha256 sigKey}}"}), filterScopes()[0])
	if res := r.Render("{{sig}}"); res.Err() != nil || !res.Secret {
		t.Errorf("{{sig}}: err %v, secret %v", res.Err(), res.Secret)
	}
}

func TestFilterInvalid(t *testing.T) {
	t.Parallel()
	tests := []struct{ src, msg string }{
		{"{{hello | nope}}", `"nope" is not a filter`},
		{`{{hello | "base64"}}`, `"base64" is not a filter`},
		{"{{hello | }}", "a filter name is missing after a |"},
		{"{{hello || base64}}", "a filter name is missing after a |"},
		{"{{ | base64}}", "a variable or a quoted text must come before the first |"},
		{`{{"a" b | base64}}`, "the quoted text before the first | is not closed or has text after it"},
		{`{{hello | hmacSha256 "k}}`, ""}, // an unclosed quote: not a pipe
		{"{{hello | base64 x}}", "base64 takes no arguments"},
		{"{{hello | sha256 hex x}}", "sha256 takes at most an output encoding"},
		{"{{hello | sha256 b32}}", `sha256: the output encoding "b32" is not hex, base64 or base64url`},
		{"{{hello | hmacSha256}}", "hmacSha256 needs a key: a variable name or a quoted text"},
		{"{{hello | hmacSha256 key hex x}}", "hmacSha256 takes a key and at most an output encoding"},
		{`{{"%%" | base64Decode}}`, "base64Decode: the value is not valid Base64"},
		{`{{"%zz" | urlDecode}}`, "urlDecode: the value has a % not followed by two hex digits"},
		{`{{"abc" | hexDecode}}`, "hexDecode: the value is not an even number of hex digits"},
	}
	for _, tt := range tests {
		t.Run(tt.src, func(t *testing.T) {
			t.Parallel()
			res := dynResolver(filterScopes()...).Render(tt.src)
			if res.Text != tt.src {
				t.Errorf("Text = %q, want the reference left as written", res.Text)
			}
			name := strings.Trim(strings.TrimSuffix(strings.TrimPrefix(tt.src, "{{"), "}}"), spaces)
			if tt.msg == "" {
				if len(res.Problems) != 1 || res.Problems[0].Kind != ProblemUnresolved {
					t.Errorf("problems = %+v, want unresolved", res.Problems)
				}
				return
			}
			if len(res.Problems) != 1 {
				t.Fatalf("Problems = %+v, want one", res.Problems)
			}
			p := res.Problems[0]
			if p.Kind != ProblemInvalidFilter || p.Message != tt.msg || p.Name != name || p.Start != 0 || p.End != len(tt.src) {
				t.Errorf("problem = %+v, want %s %q", p, ProblemInvalidFilter, tt.msg)
			}
			var e *errs.Error
			if err := res.Err(); !errors.As(err, &e) || e.Kind != errs.InvalidArgument || e.Code != CodeInvalidFilter {
				t.Fatalf("Err() = %#v, want %s", err, CodeInvalidFilter)
			}
			if want := "The filter " + "{{" + name + "}} is not valid: " + tt.msg + "."; e.Message != want {
				t.Errorf("message = %q, want %q", e.Message, want)
			}
		})
	}
}

func TestFilterUnresolvedOperands(t *testing.T) {
	t.Parallel()
	r := dynResolver(filterScopes()...)
	res := r.Render("{{missing | base64}}")
	if len(res.Problems) != 1 || res.Problems[0].Kind != ProblemUnresolved || res.Problems[0].Name != "missing" || res.Refs[0].Resolved {
		t.Errorf("unresolved input: %+v", res.Problems)
	}
	var e *errs.Error
	if !errors.As(res.Err(), &e) || e.Code != CodeUnresolved || !strings.Contains(e.Message, "{{missing}}") {
		t.Errorf("Err() = %v", res.Err())
	}
	res = r.Render("{{hello | hmacSha256 nokey}}")
	if len(res.Problems) != 1 || res.Problems[0].Name != "nokey" || res.Refs[0].Resolved {
		t.Errorf("unresolved key: %+v", res.Problems)
	}
	// An unknown dynamic input is an unresolved name.
	res = r.Render("{{$nope 1 | base64}}")
	if len(res.Problems) != 1 || res.Problems[0].Kind != ProblemUnresolved {
		t.Errorf("unknown dynamic input: %+v", res.Problems)
	}
	// Cycles through a pipe are caught.
	c := dynResolver(Vars("s", map[string]string{"a": "{{a | base64}}"}))
	res = c.Render("{{a}}")
	if len(res.Problems) != 1 || res.Problems[0].Kind != ProblemCycle {
		t.Errorf("cycle: %+v", res.Problems)
	}
}

func TestIsPipe(t *testing.T) {
	t.Parallel()
	for _, n := range []string{"a | base64", `"x" | sha256 base64`, "a|base64Decode|base64", "a | hmacSha256 k", "$uuid | md5"} {
		if !IsPipe(n) {
			t.Errorf("IsPipe(%q) = false", n)
		}
	}
	for _, n := range []string{"", "a", "a |", `"a|b"`, "a|b64Decode|base64", "$datetime YYYY|MM", "a | hmacSha256", `a | "x`} {
		if IsPipe(n) {
			t.Errorf("IsPipe(%q) = true", n)
		}
	}
	names := FilterNames()
	if !slices.IsSorted(names) || len(names) != len(filters) || !slices.Contains(names, "hmacSha256") {
		t.Errorf("FilterNames() = %v", names)
	}
	for _, n := range names {
		if FilterDescription(n) == "" {
			t.Errorf("FilterDescription(%q) is empty", n)
		}
	}
	if FilterDescription("nope") != "" {
		t.Error("FilterDescription(nope) is not empty")
	}
}

// FuzzPipe parses and renders arbitrary pipes: rendering never panics, a
// pipe that IsPipe accepts with a quoted input always resolves, and a
// problem always stays within the text.
func FuzzPipe(f *testing.F) {
	for _, s := range []string{
		"a | base64", `"x" | sha256 base64`, `"a|b" | urlEncode`, "a || b", "| x",
		`a | hmacSha256 "k" hex`, `a | hmacSha256 "k`, `"`, `"a"b | hex`, "$uuid | md5",
		"a | base64Decode | hexDecode", "\t|\t", `a | sha1 "hex"`,
	} {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, name string) {
		stages, ok := splitPipe(name)
		if ok {
			if len(stages) < 2 {
				t.Fatalf("splitPipe(%q) = %q", name, stages)
			}
			_, _ = parsePipe(stages)
		}
		src := "{{" + name + "}}"
		segs := Parse(src)
		if len(segs) != 1 || segs[0].Kind != Var {
			return
		}
		r := Resolver{Scopes: Chain{Vars("s", map[string]string{"a": "hello"})}, MaxLength: 1 << 16}
		res := r.Render(src)
		for _, p := range res.Problems {
			if p.Start < 0 || p.End > len(src) || p.Start >= p.End {
				t.Fatalf("problem %+v out of range for %q", p, src)
			}
		}
		n := segs[0].Name
		if IsPipe(n) && strings.HasPrefix(n, `"`) && !strings.Contains(n, "Decode") {
			if _, isDefined := r.Scopes.Lookup(n); !isDefined && hasOnlyQuotedKeys(n) && res.Err() != nil {
				t.Fatalf("Render(%q).Err() = %v for a valid pipe of literals", src, res.Err())
			}
		}
	})
}

// hasOnlyQuotedKeys reports whether every HMAC key of the pipe name is a
// quoted text (so nothing needs to resolve).
func hasOnlyQuotedKeys(name string) bool {
	stages, _ := splitPipe(name)
	p, err := parsePipe(stages)
	if err != nil {
		return false
	}
	for _, s := range p.steps {
		for _, k := range s.keys {
			if !k.quoted {
				return false
			}
		}
	}
	return true
}
