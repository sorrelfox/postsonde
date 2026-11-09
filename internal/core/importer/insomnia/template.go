package insomnia

import (
	"regexp"
	"slices"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/importer"
)

// Insomnia texts are Nunjucks templates: {{ _.name }} (or the older
// {{ name }}) reads a variable, {% tag args %} runs a template tag
// (response, prompt, uuid, now, base64, a plugin's tag…). Variables become
// PostSonde's {{name}}; the uuid and now tags become dynamic values; other
// tags and expressions are left as written and reported.

var (
	// reIdent is a Nunjucks name.
	reIdent = regexp.MustCompile(`^[A-Za-z_$][A-Za-z0-9_$]*`)
	// reIndex is a [0] or ['key'] / ["key"] member access.
	reIndex = regexp.MustCompile(`^\[\s*(?:([0-9]+)|'([^'\\]*)'|"([^"\\]*)")\s*\]`)
	// reTagArg is a quoted tag argument.
	reTagArg = regexp.MustCompile(`'([^'\\]*)'|"([^"\\]*)"`)
)

// templates converts the texts of one item and remembers what it could
// not convert, for the item's diagnostics.
type templates struct {
	// unsupported are the tags left as written, by name, with where they
	// are.
	unsupported map[string][]string
	// converted are the tags that became dynamic values.
	converted map[string]string
	// exprs are the places of expressions left as written.
	exprs []string
}

// conv converts text; where says where the text is ("the URL", "a
// header") for the diagnostics.
func (t *templates) conv(text, where string) string {
	if !strings.Contains(text, "{{") && !strings.Contains(text, "{%") {
		return text
	}
	var b strings.Builder
	for i := 0; i < len(text); {
		open := strings.IndexByte(text[i:], '{')
		if open < 0 || i+open+1 >= len(text) {
			b.WriteString(text[i:])
			break
		}
		open += i
		b.WriteString(text[i:open])
		switch text[open+1] {
		case '{':
			end := strings.Index(text[open+2:], "}}")
			if end < 0 {
				b.WriteString(text[open:])
				return b.String()
			}
			expr := text[open+2 : open+2+end]
			if name, ok := variable(expr); ok {
				b.WriteString("{{" + name + "}}")
			} else {
				b.WriteString(text[open : open+4+end])
				t.addExpr(where)
			}
			i = open + 4 + end
		case '%':
			end := strings.Index(text[open+2:], "%}")
			if end < 0 {
				b.WriteString(text[open:])
				return b.String()
			}
			body := strings.TrimSpace(text[open+2 : open+2+end])
			raw := text[open : open+4+end]
			if dyn, tag := t.tag(body); dyn != "" {
				b.WriteString(dyn)
				if t.converted == nil {
					t.converted = map[string]string{}
				}
				t.converted[tag] = dyn
			} else {
				b.WriteString(raw)
				t.addTag(tag, where)
			}
			i = open + 4 + end
		default:
			b.WriteByte('{')
			i = open + 1
		}
	}
	return b.String()
}

func (t *templates) addExpr(where string) {
	if !slices.Contains(t.exprs, where) {
		t.exprs = append(t.exprs, where)
	}
}

func (t *templates) addTag(tag, where string) {
	if t.unsupported == nil {
		t.unsupported = map[string][]string{}
	}
	if !slices.Contains(t.unsupported[tag], where) {
		t.unsupported[tag] = append(t.unsupported[tag], where)
	}
}

// tag returns the dynamic value a tag becomes, or "", and the tag's name.
func (t *templates) tag(body string) (string, string) {
	name := body
	if i := strings.IndexAny(body, " \t\r\n"); i >= 0 {
		name = body[:i]
	}
	if name == "" {
		name = "(empty)"
	}
	var args []string
	for _, m := range reTagArg.FindAllStringSubmatch(body[len(name):], -1) {
		args = append(args, m[1]+m[2])
	}
	arg := func(i int) string {
		if i < len(args) {
			return args[i]
		}
		return ""
	}
	switch name {
	case "uuid":
		if a := arg(0); a == "" || a == "v4" || a == "v1" {
			return "{{$uuid}}", name
		}
	case "now":
		switch arg(0) {
		case "millis":
			return "{{$timestampMs}}", name
		case "unix":
			return "{{$timestamp}}", name
		case "iso-8601", "":
			return "{{$isoTimestamp}}", name
		}
	}
	return "", name
}

// variable returns the PostSonde name of a Nunjucks expression that only
// reads a variable: _.name, _['name'], _.a.b, _.list[0] or a bare name.
// Members are joined with dots, as environment values nested in objects
// are imported (a.b, list.0).
func variable(expr string) (string, bool) {
	s := strings.TrimSpace(expr)
	if s == "" {
		return "", false
	}
	var parts []string
	first := true
	for s != "" {
		switch {
		case first || s[0] == '.':
			if !first {
				s = s[1:]
			}
			id := reIdent.FindString(s)
			if id == "" {
				return "", false
			}
			parts = append(parts, id)
			s = s[len(id):]
		case s[0] == '[':
			m := reIndex.FindStringSubmatch(s)
			if m == nil {
				return "", false
			}
			parts = append(parts, m[1]+m[2]+m[3])
			s = s[len(m[0]):]
		default:
			return "", false
		}
		first = false
	}
	if len(parts) > 0 && parts[0] == "_" {
		parts = parts[1:]
	}
	if len(parts) == 0 {
		return "", false
	}
	for _, p := range parts {
		if p == "" || strings.ContainsAny(p, "{}") {
			return "", false
		}
	}
	return strings.Join(parts, "."), true
}

// diagnostics returns what t could not convert, and the tags it did, as
// diagnostics at loc. Messages name the tags and places, never values.
func (t *templates) diagnostics(loc importer.Location) []importer.Diagnostic {
	var out []importer.Diagnostic
	tags := make([]string, 0, len(t.unsupported))
	for tag := range t.unsupported {
		tags = append(tags, tag)
	}
	slices.Sort(tags)
	for _, tag := range tags {
		out = append(out, importer.Diagnostic{
			Code: CodeTemplateTag, Severity: importer.SeverityWarning, Source: loc,
			Message: "The Insomnia template tag “" + tag + "” in " + strings.Join(t.unsupported[tag], ", ") +
				" is left as written: PostSonde has no such tag. Replace it with a {{variable}}.",
		})
	}
	if len(t.exprs) > 0 {
		out = append(out, importer.Diagnostic{
			Code: CodeTemplateExpression, Severity: importer.SeverityWarning, Source: loc,
			Message: "An Insomnia template expression in " + strings.Join(t.exprs, ", ") +
				" is left as written: only variables such as {{ _.name }} are converted.",
		})
	}
	conv := make([]string, 0, len(t.converted))
	for tag := range t.converted {
		conv = append(conv, tag)
	}
	slices.Sort(conv)
	for _, tag := range conv {
		out = append(out, importer.Diagnostic{
			Code: CodeTemplateConverted, Severity: importer.SeverityInfo, Source: loc,
			Message: "The Insomnia “" + tag + "” tag became the dynamic value " + t.converted[tag] + ".",
		})
	}
	return out
}
