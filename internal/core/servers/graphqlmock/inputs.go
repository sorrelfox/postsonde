package graphqlmock

import (
	"encoding/json"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/graphql/ast"
	"github.com/sorrelfox/postsonde/internal/core/graphql/parser"
	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
	"github.com/sorrelfox/postsonde/internal/core/graphql/validate"
)

// Input values: the variables of the request are coerced as the spec's
// CoerceVariableValues does (the validator does not see them), literals
// in the document are taken as they are (the validator checked them), and
// arguments get their defaults. Coerced values are JSON values (Int and
// Float as json.Number, ID as a string, enum values as strings, input
// objects as maps with their defaults filled in).

// coerceVariables fills e.vars from the request's variables.
func (e *executor) coerceVariables(op *ast.OperationDefinition, raw map[string]any) []gqlError {
	if op.VariableDefinitions == nil {
		return nil
	}
	var out []gqlError
	for _, def := range op.VariableDefinitions.Definitions {
		if def.Variable == nil || def.Variable.Name == nil {
			continue
		}
		name := def.Variable.Name.Value
		t := validate.TypeFromAST(e.sch, def.Type)
		if t == nil {
			continue // unknown types fail validation
		}
		fail := func(msg string) {
			out = append(out, gqlError{Message: msg, Locations: []ast.Position{e.doc.Position(def.Start)}, Code: ErrCodeBadInput})
		}
		v, present := raw[name]
		if !present {
			if def.DefaultValue != nil {
				dv, _ := e.literal(def.DefaultValue, t)
				e.vars[name] = dv
			} else if t.IsNonNull() {
				fail(fmt.Sprintf("Variable \"$%s\" of required type \"%s\" was not provided.", name, t))
			}
			continue
		}
		if v == nil && t.IsNonNull() {
			fail(fmt.Sprintf("Variable \"$%s\" of non-null type \"%s\" must not be null.", name, t))
			continue
		}
		c, err := e.coerceInput(v, t, nil)
		if err != nil {
			at := ""
			if len(err.path) > 0 {
				at = " at \"" + name + strings.Join(err.path, "") + "\""
			}
			fail(fmt.Sprintf("Variable \"$%s\" got invalid value %s%s; %s", name, inspect(err.value), at, err.msg))
			continue
		}
		e.vars[name] = c
	}
	return out
}

// inputError is why a variable's value does not fit its type: msg about
// value, at path (".name", "[2]") inside the variable.
type inputError struct {
	msg   string
	value any
	path  []string
}

// coerceInput is the spec's input coercion of a JSON value to t.
func (e *executor) coerceInput(v any, t *schema.TypeRef, path []string) (any, *inputError) {
	fail := func(msg string) (any, *inputError) {
		return nil, &inputError{msg: msg, value: v, path: path}
	}
	if t.Kind == schema.KindNonNull {
		if v == nil {
			return fail(fmt.Sprintf("Expected non-nullable type \"%s\" not to be null.", t))
		}
		return e.coerceInput(v, t.OfType, path)
	}
	if v == nil {
		return nil, nil
	}
	if t.Kind == schema.KindList {
		list, ok := v.([]any)
		if !ok {
			item, err := e.coerceInput(v, t.OfType, path)
			if err != nil {
				return nil, err
			}
			return []any{item}, nil
		}
		out := make([]any, len(list))
		for i, item := range list {
			c, err := e.coerceInput(item, t.OfType, append(path[:len(path):len(path)], "["+strconv.Itoa(i)+"]"))
			if err != nil {
				return nil, err
			}
			out[i] = c
		}
		return out, nil
	}
	named := e.sch.Type(t.Name)
	if named == nil {
		return fail(fmt.Sprintf("Unknown type \"%s\".", t.Name))
	}
	switch named.Kind {
	case schema.KindScalar:
		c, msg := coerceScalar(named.Name, v)
		if msg != "" {
			return fail(msg)
		}
		return c, nil
	case schema.KindEnum:
		s, ok := v.(string)
		if !ok {
			return fail(fmt.Sprintf("Enum \"%s\" cannot represent non-string value: %s.", named.Name, inspect(v)))
		}
		if named.EnumValue(s) == nil {
			return fail(fmt.Sprintf("Value %s does not exist in \"%s\" enum.", inspect(s), named.Name))
		}
		return s, nil
	case schema.KindInputObject:
		m, ok := v.(map[string]any)
		if !ok {
			return fail(fmt.Sprintf("Expected type \"%s\" to be an object.", named.Name))
		}
		out := make(map[string]any, len(named.InputFields))
		for _, f := range named.InputFields {
			fv, present := m[f.Name]
			fpath := append(path[:len(path):len(path)], "."+f.Name)
			if !present {
				if f.DefaultValue != nil {
					out[f.Name] = e.defaultValue(f)
				} else if f.Type.IsNonNull() {
					return nil, &inputError{msg: fmt.Sprintf("Field \"%s\" of required type \"%s\" was not provided.", f.Name, f.Type), value: v, path: path}
				}
				continue
			}
			c, err := e.coerceInput(fv, f.Type, fpath)
			if err != nil {
				return nil, err
			}
			out[f.Name] = c
		}
		for k := range m {
			if named.InputField(k) == nil {
				return fail(fmt.Sprintf("Field \"%s\" is not defined by type \"%s\".", k, named.Name))
			}
		}
		if named.IsOneOf {
			if len(m) != 1 {
				return fail(fmt.Sprintf("Exactly one key must be specified for OneOf type \"%s\".", named.Name))
			}
			for k, fv := range m {
				if fv == nil {
					return fail(fmt.Sprintf("Field \"%s\" must be non-null.", k))
				}
			}
		}
		return out, nil
	}
	return fail(fmt.Sprintf("\"%s\" is not an input type.", named.Name))
}

// coerceScalar coerces a variable's JSON value to a scalar; msg is set
// when it does not fit.
func coerceScalar(name string, v any) (any, string) {
	switch name {
	case "Int":
		n, ok := v.(json.Number)
		if !ok {
			return nil, "Int cannot represent non-integer value: " + inspect(v)
		}
		f, err := n.Float64()
		if err != nil || f != math.Trunc(f) {
			return nil, "Int cannot represent non-integer value: " + inspect(v)
		}
		if f > math.MaxInt32 || f < math.MinInt32 {
			return nil, "Int cannot represent non 32-bit signed integer value: " + inspect(v)
		}
		return json.Number(strconv.FormatInt(int64(f), 10)), ""
	case "Float":
		n, ok := v.(json.Number)
		if !ok {
			return nil, "Float cannot represent non numeric value: " + inspect(v)
		}
		return n, ""
	case "String":
		s, ok := v.(string)
		if !ok {
			return nil, "String cannot represent a non string value: " + inspect(v)
		}
		return s, ""
	case "Boolean":
		b, ok := v.(bool)
		if !ok {
			return nil, "Boolean cannot represent a non boolean value: " + inspect(v)
		}
		return b, ""
	case "ID":
		switch x := v.(type) {
		case string:
			return x, ""
		case json.Number:
			if _, err := strconv.ParseInt(x.String(), 10, 64); err == nil {
				return x.String(), ""
			}
		}
		return nil, "ID cannot represent value: " + inspect(v)
	}
	return v, "" // a custom scalar takes any JSON
}

// arguments is CoerceArgumentValues: the value of each argument given
// (a literal, or a variable's value), else its default. An argument whose
// variable was not given and that has no default is left out.
func (e *executor) arguments(defs []*schema.InputValue, list *ast.ArgumentList) (map[string]any, error) {
	out := make(map[string]any, len(defs))
	for _, def := range defs {
		var arg *ast.Argument
		if list != nil {
			for _, a := range list.Arguments {
				if a.Name != nil && a.Name.Value == def.Name {
					arg = a
					break
				}
			}
		}
		if arg != nil {
			if v, ok := arg.Value.(*ast.Variable); ok && v.Name != nil {
				if val, given := e.vars[v.Name.Value]; given {
					if val == nil && def.Type.IsNonNull() {
						return nil, &fieldError{msg: fmt.Sprintf("Argument \"%s\" of non-null type \"%s\" must not be null.", def.Name, def.Type), code: ErrCodeBadInput}
					}
					out[def.Name] = val
					continue
				}
			} else {
				val, _ := e.literal(arg.Value, def.Type)
				out[def.Name] = val
				continue
			}
		}
		if def.DefaultValue != nil {
			out[def.Name] = e.defaultValue(def)
		} else if def.Type.IsNonNull() {
			return nil, &fieldError{msg: fmt.Sprintf("Argument \"%s\" of required type \"%s\" was not provided.", def.Name, def.Type), code: ErrCodeBadInput}
		}
	}
	return out, nil
}

// literal is the value of a literal in the document for type t (the
// validator checked it); ok is false for a variable that was not given.
func (e *executor) literal(v ast.Value, t *schema.TypeRef) (any, bool) {
	if t != nil && t.Kind == schema.KindNonNull {
		t = t.OfType
	}
	switch v := v.(type) {
	case *ast.Variable:
		if v.Name == nil {
			return nil, false
		}
		val, ok := e.vars[v.Name.Value]
		return val, ok
	case *ast.IntValue:
		if t != nil && !t.Kind.IsWrapper() && t.Name == "ID" {
			return v.Raw, true
		}
		return json.Number(v.Raw), true
	case *ast.FloatValue:
		return json.Number(v.Raw), true
	case *ast.StringValue:
		return v.Value, true
	case *ast.BooleanValue:
		return v.Value, true
	case *ast.NullValue:
		return nil, true
	case *ast.EnumValue:
		return v.Value, true
	case *ast.ListValue:
		var elem *schema.TypeRef
		if t != nil && t.Kind == schema.KindList {
			elem = t.OfType
		}
		out := make([]any, 0, len(v.Values))
		for _, item := range v.Values {
			x, ok := e.literal(item, elem)
			if !ok {
				x = nil
			}
			out = append(out, x)
		}
		return out, true
	case *ast.ObjectValue:
		var it *schema.Type
		if t != nil {
			if t.Kind == schema.KindList {
				// A single item where a list is expected.
				x, ok := e.literal(v, t.OfType)
				return []any{x}, ok
			}
			it = e.sch.Type(t.Name)
		}
		out := make(map[string]any, len(v.Fields))
		for _, f := range v.Fields {
			if f.Name == nil {
				continue
			}
			var ft *schema.TypeRef
			if fd := it.InputField(f.Name.Value); fd != nil {
				ft = fd.Type
			}
			if x, ok := e.literal(f.Value, ft); ok {
				out[f.Name.Value] = x
			}
		}
		if it != nil {
			for _, fd := range it.InputFields {
				if _, given := out[fd.Name]; !given && fd.DefaultValue != nil {
					out[fd.Name] = e.defaultValue(fd)
				}
			}
		}
		return out, true
	}
	return nil, false
}

// defaultValue is the value of an argument's or input field's default,
// a GraphQL literal in the schema.
func (e *executor) defaultValue(iv *schema.InputValue) any {
	if iv.DefaultValue == nil {
		return nil
	}
	doc, errs := parser.Parse("{f(a: " + *iv.DefaultValue + ")}")
	if len(errs) > 0 || len(doc.Definitions) == 0 {
		return nil
	}
	op, ok := doc.Definitions[0].(*ast.OperationDefinition)
	if !ok || op.SelectionSet == nil || len(op.SelectionSet.Selections) == 0 {
		return nil
	}
	f, ok := op.SelectionSet.Selections[0].(*ast.Field)
	if !ok || f.Arguments == nil || len(f.Arguments.Arguments) == 0 {
		return nil
	}
	v, _ := e.literal(f.Arguments.Arguments[0].Value, iv.Type)
	return v
}
