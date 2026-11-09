package schema

import (
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/graphql/printer"
)

// SDL prints s as a GraphQL schema document (GQL-20), in graphql-js's
// printSchema layout: a schema definition when the roots are not the
// types named Query, Mutation and Subscription (or the schema has a
// description), then the declared directives and types in the schema's
// order. Built-in scalars, introspection types and spec directives are
// left out, as ParseSDL supplies them. Descriptions are block strings
// when that keeps them exactly, else quoted strings, so ParseSDL(SDL(s))
// gives s back (without the built-ins). The text ends with a line break;
// an empty schema prints as "".
func SDL(s *Schema) string {
	if s == nil {
		return ""
	}
	src := rawSDL(s)
	if src == "" {
		return ""
	}
	// The layout (argument lists broken over lines past 80 columns or
	// with descriptions) is the printer's; the text it gets is always
	// valid SDL, but should it not parse, the raw text is still right.
	if out, perrs := printer.PrettifySchema(src); len(perrs) == 0 && out != "" {
		return out + "\n"
	}
	return src
}

// rawSDL prints the definitions one after another, members one per line.
func rawSDL(s *Schema) string {
	var b strings.Builder
	sep := func() {
		if b.Len() > 0 {
			b.WriteString("\n")
		}
	}
	if needsSchemaDefinition(s) {
		writeDescription(&b, "", s.Description)
		b.WriteString("schema {\n")
		if s.QueryType != "" {
			b.WriteString("  query: " + s.QueryType + "\n")
		}
		if s.MutationType != "" {
			b.WriteString("  mutation: " + s.MutationType + "\n")
		}
		if s.SubscriptionType != "" {
			b.WriteString("  subscription: " + s.SubscriptionType + "\n")
		}
		b.WriteString("}\n")
	}
	for _, d := range s.Directives {
		if d.IsBuiltin() {
			continue
		}
		sep()
		writeDescription(&b, "", d.Description)
		b.WriteString("directive @" + d.Name)
		writeArgs(&b, "  ", d.Args)
		if d.IsRepeatable {
			b.WriteString(" repeatable")
		}
		locs := make([]string, len(d.Locations))
		for i, l := range d.Locations {
			locs[i] = string(l)
		}
		b.WriteString(" on " + strings.Join(locs, " | ") + "\n")
	}
	for _, t := range s.Types {
		if t.IsBuiltin() || t.Name == "" {
			continue
		}
		sep()
		writeType(&b, t)
	}
	return b.String()
}

// needsSchemaDefinition reports whether ParseSDL would pick other roots
// than s has without a schema definition: it takes the types named Query,
// Mutation and Subscription when they exist.
func needsSchemaDefinition(s *Schema) bool {
	if s.Description != "" {
		return true
	}
	byName := func(name string) string {
		for _, t := range s.Types {
			if t.Name == name {
				return name
			}
		}
		return ""
	}
	return s.QueryType != byName("Query") ||
		s.MutationType != byName("Mutation") ||
		s.SubscriptionType != byName("Subscription")
}

func writeType(b *strings.Builder, t *Type) {
	writeDescription(b, "", t.Description)
	switch t.Kind {
	case KindScalar:
		b.WriteString("scalar " + t.Name)
		if t.SpecifiedByURL != "" {
			b.WriteString(" @specifiedBy(url: " + quoteString(t.SpecifiedByURL) + ")")
		}
	case KindObject, KindInterface:
		if t.Kind == KindInterface {
			b.WriteString("interface " + t.Name)
		} else {
			b.WriteString("type " + t.Name)
		}
		if len(t.Interfaces) > 0 {
			b.WriteString(" implements " + strings.Join(t.Interfaces, " & "))
		}
		if len(t.Fields) > 0 {
			b.WriteString(" {\n")
			for _, f := range t.Fields {
				writeDescription(b, "  ", f.Description)
				b.WriteString("  " + f.Name)
				writeArgs(b, "    ", f.Args)
				b.WriteString(": " + typeString(f.Type))
				writeDeprecation(b, f.Deprecation)
				b.WriteString("\n")
			}
			b.WriteString("}")
		}
	case KindUnion:
		b.WriteString("union " + t.Name)
		if len(t.PossibleTypes) > 0 {
			b.WriteString(" = " + strings.Join(t.PossibleTypes, " | "))
		}
	case KindEnum:
		b.WriteString("enum " + t.Name)
		if len(t.EnumValues) > 0 {
			b.WriteString(" {\n")
			for _, v := range t.EnumValues {
				writeDescription(b, "  ", v.Description)
				b.WriteString("  " + v.Name)
				writeDeprecation(b, v.Deprecation)
				b.WriteString("\n")
			}
			b.WriteString("}")
		}
	case KindInputObject:
		b.WriteString("input " + t.Name)
		if t.IsOneOf {
			b.WriteString(" @oneOf")
		}
		if len(t.InputFields) > 0 {
			b.WriteString(" {\n")
			for _, f := range t.InputFields {
				writeDescription(b, "  ", f.Description)
				b.WriteString("  ")
				writeInputValue(b, f)
				b.WriteString("\n")
			}
			b.WriteString("}")
		}
	default:
		// An unknown kind cannot be written as SDL; a comment keeps the
		// name visible without changing the schema.
		b.WriteString("# " + t.Name + ": unknown kind " + string(t.Kind))
	}
	b.WriteString("\n")
}

// writeArgs writes an argument list; indent is where the printer puts
// each argument when it breaks the list over lines.
func writeArgs(b *strings.Builder, indent string, args []*InputValue) {
	if len(args) == 0 {
		return
	}
	b.WriteString("(")
	for i, a := range args {
		if i > 0 {
			b.WriteString(", ")
		}
		if a.Description != "" {
			b.WriteString(descriptionString(indent, a.Description) + " ")
		}
		writeInputValue(b, a)
	}
	b.WriteString(")")
}

func writeInputValue(b *strings.Builder, v *InputValue) {
	b.WriteString(v.Name + ": " + typeString(v.Type))
	if v.DefaultValue != nil {
		b.WriteString(" = " + *v.DefaultValue)
	}
	writeDeprecation(b, v.Deprecation)
}

func writeDeprecation(b *strings.Builder, d Deprecation) {
	if !d.IsDeprecated {
		return
	}
	if d.DeprecationReason == defaultDeprecationReason {
		b.WriteString(" @deprecated")
		return
	}
	b.WriteString(" @deprecated(reason: " + quoteString(d.DeprecationReason) + ")")
}

// defaultDeprecationReason is @deprecated's default reason.
const defaultDeprecationReason = "No longer supported"

func typeString(r *TypeRef) string {
	if r == nil {
		return "Unknown"
	}
	return r.String()
}

// writeDescription writes a description on its own line before what it
// describes, at indent.
func writeDescription(b *strings.Builder, indent, d string) {
	if d == "" {
		return
	}
	b.WriteString(indent + descriptionString(indent, d) + "\n")
}

// descriptionString is d as a GraphQL string: a block string when that
// reads back as exactly d (see blockStringSafe), else a quoted string.
// The lines of a multi-line block string are indented by indent, which
// the block string's dedent removes again.
func descriptionString(indent, d string) string {
	if !blockStringSafe(d) {
		return quoteString(d)
	}
	if !strings.Contains(d, "\n") {
		return `"""` + d + `"""`
	}
	var b strings.Builder
	b.WriteString(`"""` + "\n")
	for _, line := range strings.Split(d, "\n") {
		if line != "" {
			b.WriteString(indent + line)
		}
		b.WriteString("\n")
	}
	b.WriteString(indent + `"""`)
	return b.String()
}

// blockStringSafe reports whether d survives a block string unchanged.
// The spec's block string value removes the common indentation of the
// lines after the first and leading and trailing blank lines, and `\"""`
// is an escape, so d must have no `"""`, no control characters but line
// feeds and tabs, a first and last line that are not blank, and (when it
// has several lines) no indentation common to all its non-blank lines; a
// single line must also not end with `"` or `\`, which would run into the
// closing quotes.
func blockStringSafe(d string) bool {
	if strings.Contains(d, `"""`) {
		return false
	}
	for _, r := range d {
		if r < 0x20 && r != '\n' && r != '\t' || r == 0x7f {
			return false
		}
	}
	lines := strings.Split(d, "\n")
	if isBlank(lines[0]) || isBlank(lines[len(lines)-1]) {
		return false
	}
	if len(lines) == 1 {
		return !strings.HasSuffix(d, `"`) && !strings.HasSuffix(d, `\`)
	}
	for _, line := range lines {
		if !isBlank(line) && line[0] != ' ' && line[0] != '\t' {
			return true
		}
	}
	return false
}

func isBlank(s string) bool { return strings.Trim(s, " \t") == "" }
