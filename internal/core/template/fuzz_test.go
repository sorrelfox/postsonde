package template

import (
	"strings"
	"testing"
)

var fuzzSeeds = []string{
	"", "plain", "{{a}}", "x{{ host }}/y", "{{}}", "{{", "}}", "{{a", "a}}",
	`\{{a}}`, `\\{{a}}`, `\\\{{a}}`, `{"a":{{{x}}}}`, "{{{{x}}", "{{a {{b}}",
	"{{a}}}", "{{\n}}", "ё{{имя}}", "\xff{{\xfe}}", `\`, `{\{{`, "{{a}b}}",
	"{{$uuid}}", "{{ $randomInt 1 x }}", `{{$datetime "YYYY [a" +1d}}`,
	"{{a | base64}}", `{{"x" | sha256 base64}}`, "{{a | hmacSha256 b}}", "{{$randomEmail}}",
	`{{"a|b" | nope}}`, "{{ | }}",
}

// checkSegments verifies the invariants every Parse result must hold.
func checkSegments(t *testing.T, src string, segs []Segment) {
	t.Helper()
	pos := 0
	for i, s := range segs {
		if s.Start != pos || s.End <= s.Start || s.End > len(src) {
			t.Fatalf("Parse(%q): segment %d %+v does not continue at %d", src, i, s, pos)
		}
		raw := src[s.Start:s.End]
		switch s.Kind {
		case Text:
			if i > 0 && segs[i-1].Kind == Text {
				t.Fatalf("Parse(%q): adjacent text segments at %d", src, i)
			}
			if !strings.Contains(raw, `\`) && s.Text != raw {
				t.Fatalf("Parse(%q): text %q differs from raw %q", src, s.Text, raw)
			}
			if len(s.Text) > len(raw) {
				t.Fatalf("Parse(%q): text %q longer than raw %q", src, s.Text, raw)
			}
		case Var:
			if !strings.HasPrefix(raw, "{{") || !strings.HasSuffix(raw, "}}") || len(raw) < 4 {
				t.Fatalf("Parse(%q): var raw %q", src, raw)
			}
			inner := raw[2 : len(raw)-2]
			if strings.Contains(inner, "{{") || strings.Contains(inner, "}}") {
				t.Fatalf("Parse(%q): var inner %q has braces", src, inner)
			}
			if strings.HasPrefix(inner, "{") {
				t.Fatalf("Parse(%q): var %q opens inside a brace run", src, raw)
			}
			if s.Name != strings.Trim(inner, spaces) {
				t.Fatalf("Parse(%q): name %q, inner %q", src, s.Name, inner)
			}
			if s.NameStart < s.Start+2 || s.NameEnd > s.End-2 || s.NameStart > s.NameEnd || src[s.NameStart:s.NameEnd] != s.Name {
				t.Fatalf("Parse(%q): name span [%d,%d) of %+v", src, s.NameStart, s.NameEnd, s)
			}
		case Unterminated:
			if raw != "{{" || s.Text != "{{" {
				t.Fatalf("Parse(%q): unterminated raw %q", src, raw)
			}
		default:
			t.Fatalf("Parse(%q): segment %d has kind %d", src, i, s.Kind)
		}
		pos = s.End
	}
	if pos != len(src) {
		t.Fatalf("Parse(%q): segments end at %d, want %d", src, pos, len(src))
	}
}

func FuzzParse(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s)
	}
	f.Fuzz(func(t *testing.T, src string) {
		segs := Parse(src)
		checkSegments(t, src, segs)

		// Escaping any text gives back exactly that text.
		esc := Escape(src)
		var lit strings.Builder
		for _, s := range Parse(esc) {
			if s.Kind != Text {
				t.Fatalf("Parse(Escape(%q)) has a %s segment", src, s.Kind)
			}
			lit.WriteString(s.Text)
		}
		if lit.String() != src {
			t.Fatalf("Parse(Escape(%q)) = %q", src, lit.String())
		}

		// With no scopes nothing is substituted: every variable stays as
		// written and is reported. Dynamic values ({{$uuid}}) and filter
		// pipes (a quoted input resolves without a scope) are tested
		// separately.
		for _, s := range segs {
			if _, _, dyn := splitDynamic(s.Name); s.Kind == Var && dyn {
				return
			}
			if _, pipe := splitPipe(s.Name); s.Kind == Var && pipe {
				return
			}
		}
		res := Resolver{}.Render(src)
		var want strings.Builder
		vars, problems := 0, 0
		for _, s := range segs {
			switch s.Kind {
			case Var:
				want.WriteString(src[s.Start:s.End])
				vars++
				problems++
			case Unterminated:
				want.WriteString(s.Text)
				problems++
			default:
				want.WriteString(s.Text)
			}
		}
		if vars > DefaultMaxExpansions {
			return // the expansion limit stops reporting further references
		}
		if res.Text != want.String() || len(res.Refs) != vars || len(res.Problems) != problems {
			t.Fatalf("Render(%q) = %q, %d refs, %d problems; want %q, %d, %d",
				src, res.Text, len(res.Refs), len(res.Problems), want.String(), vars, problems)
		}
		if (res.Err() != nil) != (vars > 0) {
			t.Fatalf("Render(%q).Err() = %v with %d vars", src, res.Err(), vars)
		}
	})
}

// FuzzRender resolves a template whose variables all refer to one another,
// checking that rendering terminates and stays consistent.
func FuzzRender(f *testing.F) {
	for _, s := range fuzzSeeds {
		f.Add(s, "{{b}}{{b}}", "{{a}}")
	}
	// Dot notation (ENV-13): paths into JSON values, also through cycles.
	f.Add("{{a.x}}{{b.0.y}}", `{"x":"{{b.0.y}}"}`, `[{"y":"{{a.x}}"}]`)
	f.Add("{{a.x.y}} {{a.z}}", `{"x":{"y":1},"z":"{{b}}"}`, `{{a.x}}`)
	f.Fuzz(func(t *testing.T, src, a, b string) {
		r := Resolver{
			Scopes:        Chain{MapScope{Label: "s", Variables: map[string]Variable{"a": {Value: a}, "b": {Value: b, Secret: true}}}},
			MaxDepth:      4,
			MaxExpansions: 200,
			MaxLength:     1 << 16,
		}
		res := r.Render(src)
		for _, p := range res.Problems {
			if p.Start < 0 || p.End > len(src) || p.Start >= p.End {
				t.Fatalf("problem %+v out of range for %q", p, src)
			}
		}
		if len(res.Text) > len(src)+r.MaxLength {
			t.Fatalf("Render output of %d bytes from a %d-byte template", len(res.Text), len(src))
		}
		blocking := false
		for _, p := range res.Problems {
			blocking = blocking || p.Kind.Blocking()
		}
		if (res.Err() != nil) != blocking {
			t.Fatalf("Err() = %v, blocking problems %v", res.Err(), blocking)
		}
	})
}
