package validate

import (
	"fmt"
	"math"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
)

// ValuesOfCorrectType reports literal values that their expected input type
// does not accept (spec 5.6.1): wrong scalar literals and out-of-range Ints,
// null for non-null types, unknown enum values and strings where an enum is
// expected, lists where no list is expected, and input objects with unknown
// or missing required fields or that break the @oneOf rules. It checks
// argument values and variable default values. A single value where a list
// is expected is checked as its item (input coercion). Custom scalars accept
// any literal. Variables are not checked here: whether a variable fits its
// position is a separate rule.
var ValuesOfCorrectType = Rule{
	Name: "ValuesOfCorrectType",
	New: func(c *Context) Visitor {
		vc := valueChecker{c: c}
		return Visitor{Enter: func(n ast.Node) bool {
			switch n := n.(type) {
			case *ast.Argument:
				vc.check(n.Value, c.TypeInfo.InputType())
			case *ast.VariableDefinition:
				if n.DefaultValue != nil {
					vc.check(n.DefaultValue, c.TypeInfo.InputType())
				}
			}
			return true
		}}
	},
}

type valueChecker struct{ c *Context }

// check reports the problems of v against the expected type t. A nil t
// (unknown argument, broken schema) is not checked.
func (vc valueChecker) check(v ast.Value, t *schema.TypeRef) {
	if t == nil || v == nil {
		return
	}
	switch v.(type) {
	case *ast.Variable, *ast.InvalidValue:
		return
	case *ast.NullValue:
		if t.IsNonNull() {
			vc.mismatch(v, t, "")
		}
		return
	}
	nt := t.Nullable()
	if nt == nil {
		return
	}
	if nt.Kind == schema.KindList {
		if lv, ok := v.(*ast.ListValue); ok {
			for _, item := range lv.Values {
				vc.check(item, nt.OfType)
			}
			return
		}
		vc.check(v, nt.OfType)
		return
	}
	named := vc.c.Schema.TypeOf(nt)
	if named == nil {
		return
	}
	switch named.Kind {
	case schema.KindInputObject:
		vc.inputObject(v, t, named)
	case schema.KindEnum:
		vc.enum(v, t, named)
	case schema.KindScalar:
		vc.scalar(v, t, named)
	}
}

func (vc valueChecker) mismatch(v ast.Value, t *schema.TypeRef, detail string) {
	msg := fmt.Sprintf("Expected value of type %q, found %s", t.String(), printValue(v))
	if detail != "" {
		msg += "; " + detail
	} else {
		msg += "."
	}
	vc.c.report(CodeInvalidValue, v, msg, nil)
}

func (vc valueChecker) inputObject(v ast.Value, t *schema.TypeRef, named *schema.Type) {
	ov, ok := v.(*ast.ObjectValue)
	if !ok {
		vc.mismatch(v, t, "")
		return
	}
	var given []*ast.ObjectField
	// While a field is being typed (the object is unclosed, a name or value
	// is missing, or the parser skipped tokens of an empty object), which
	// fields are missing is not known yet.
	typing := ov.RBrace < 0 || len(ov.Fields) == 0 && vc.hasText(ov.LBrace+1, ov.RBrace)
	for _, f := range ov.Fields {
		if _, invalid := f.Value.(*ast.InvalidValue); invalid {
			typing = true
		}
		if !present(f.Name) {
			typing = true
			continue
		}
		given = append(given, f)
		def := named.InputField(f.Name.Value)
		if def == nil {
			names := make([]string, len(named.InputFields))
			for i, d := range named.InputFields {
				names[i] = d.Name
			}
			sugg := suggest(f.Name.Value, names)
			vc.c.report(CodeUnknownInputField, f.Name, fmt.Sprintf(
				"Field %q is not defined by type %q.%s", f.Name.Value, named.Name, didYouMean("", sugg)), sugg)
			continue
		}
		vc.check(f.Value, def.Type)
	}
	if typing {
		return
	}
	for _, def := range named.InputFields {
		if !def.Type.IsNonNull() || def.DefaultValue != nil || objectHasField(ov, def.Name) {
			continue
		}
		vc.c.report(CodeMissingInputField, ov, fmt.Sprintf(
			"Field %q of required type %q was not provided.", named.Name+"."+def.Name, def.Type.String()), nil)
	}
	if !named.IsOneOf {
		return
	}
	switch {
	case len(given) != 1:
		vc.c.report(CodeOneOf, ov, fmt.Sprintf("OneOf Input Object %q must specify exactly one key.", named.Name), nil)
	case isNull(given[0].Value):
		vc.c.report(CodeOneOf, given[0].Value, fmt.Sprintf(
			"Field %q must be non-null.", named.Name+"."+given[0].Name.Value), nil)
	}
}

// hasText reports whether the source between start and end holds more than
// whitespace and commas.
func (vc valueChecker) hasText(start, end int) bool {
	src := vc.c.Doc.Source
	if start < 0 || end > len(src) || start >= end {
		return false
	}
	return strings.Trim(src[start:end], " \t\r\n,") != ""
}

func objectHasField(ov *ast.ObjectValue, name string) bool {
	for _, f := range ov.Fields {
		if f.Name != nil && f.Name.Value == name {
			return true
		}
	}
	return false
}

func isNull(v ast.Value) bool {
	_, ok := v.(*ast.NullValue)
	return ok
}

func (vc valueChecker) enum(v ast.Value, t *schema.TypeRef, named *schema.Type) {
	names := func() []string {
		out := make([]string, len(named.EnumValues))
		for i, e := range named.EnumValues {
			out[i] = e.Name
		}
		return out
	}
	switch v := v.(type) {
	case *ast.EnumValue:
		if named.EnumValue(v.Value) != nil {
			return
		}
		sugg := suggest(v.Value, names())
		vc.c.report(CodeUnknownEnumValue, v, fmt.Sprintf(
			"Value %q does not exist in %q enum.%s", v.Value, named.Name, didYouMean("the enum value", sugg)), sugg)
	case *ast.StringValue:
		sugg := suggest(v.Value, names())
		msg := fmt.Sprintf("Expected value of type %q, found %s; Enum %q cannot represent non-enum value: %s.%s",
			t.String(), printValue(v), named.Name, printValue(v), didYouMean("the enum value", sugg))
		vc.c.report(CodeInvalidValue, v, msg, sugg)
	default:
		vc.mismatch(v, t, "")
	}
}

// Int bounds of the spec's 32-bit Int.
const (
	minInt = math.MinInt32
	maxInt = math.MaxInt32
)

func (vc valueChecker) scalar(v ast.Value, t *schema.TypeRef, named *schema.Type) {
	switch named.Name {
	case "Int":
		iv, ok := v.(*ast.IntValue)
		if !ok {
			vc.mismatch(v, t, "")
			return
		}
		if n, err := strconv.ParseInt(iv.Raw, 10, 64); err != nil || n < minInt || n > maxInt {
			vc.mismatch(v, t, "Int cannot represent non 32-bit signed integer value: "+iv.Raw)
		}
	case "Float":
		var raw string
		switch v := v.(type) {
		case *ast.IntValue:
			raw = v.Raw
		case *ast.FloatValue:
			raw = v.Raw
		default:
			vc.mismatch(v, t, "")
			return
		}
		if f, err := strconv.ParseFloat(raw, 64); err != nil || math.IsInf(f, 0) {
			vc.mismatch(v, t, "Float cannot represent non numeric value: "+raw)
		}
	case "String":
		if _, ok := v.(*ast.StringValue); !ok {
			vc.mismatch(v, t, "")
		}
	case "Boolean":
		if _, ok := v.(*ast.BooleanValue); !ok {
			vc.mismatch(v, t, "")
		}
	case "ID":
		switch v.(type) {
		case *ast.StringValue, *ast.IntValue:
		default:
			vc.mismatch(v, t, "")
		}
	}
	// Custom scalars accept any literal.
}

// maxPrinted caps the length of a value printed in a message.
const maxPrinted = 60

// printValue prints v compactly for a message, like graphql-js's print:
// `"text"`, `[1, 2]`, `{a: B}`. Long values are shortened with "...".
func printValue(v ast.Value) string {
	var b strings.Builder
	writeValue(&b, v)
	s := b.String()
	if utf8.RuneCountInString(s) > maxPrinted {
		r := []rune(s)
		s = string(r[:maxPrinted-3]) + "..."
	}
	return s
}

func writeValue(b *strings.Builder, v ast.Value) {
	if b.Len() > 4*maxPrinted {
		return // printValue shortens it anyway
	}
	switch v := v.(type) {
	case *ast.Variable:
		b.WriteByte('$')
		if v.Name != nil {
			b.WriteString(v.Name.Value)
		}
	case *ast.IntValue:
		b.WriteString(v.Raw)
	case *ast.FloatValue:
		b.WriteString(v.Raw)
	case *ast.StringValue:
		writeString(b, v.Value)
	case *ast.BooleanValue:
		b.WriteString(strconv.FormatBool(v.Value))
	case *ast.NullValue:
		b.WriteString("null")
	case *ast.EnumValue:
		b.WriteString(v.Value)
	case *ast.ListValue:
		b.WriteByte('[')
		for i, item := range v.Values {
			if i > 0 {
				b.WriteString(", ")
			}
			writeValue(b, item)
		}
		b.WriteByte(']')
	case *ast.ObjectValue:
		b.WriteByte('{')
		for i, f := range v.Fields {
			if i > 0 {
				b.WriteString(", ")
			}
			if f.Name != nil {
				b.WriteString(f.Name.Value)
			}
			b.WriteString(": ")
			writeValue(b, f.Value)
		}
		b.WriteByte('}')
	default:
		b.WriteString("<invalid>")
	}
}

// writeString writes s as a GraphQL string literal.
func writeString(b *strings.Builder, s string) {
	b.WriteByte('"')
	for _, r := range s {
		switch {
		case r == '"':
			b.WriteString(`\"`)
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\r':
			b.WriteString(`\r`)
		case r == '\t':
			b.WriteString(`\t`)
		case r < 0x20 || r == 0x7f:
			fmt.Fprintf(b, `\u%04X`, r)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteByte('"')
}
