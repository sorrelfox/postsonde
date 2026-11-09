package assert

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/jsonschema"
)

// schemaOptions: formats are checked (a date-time field that is not one
// fails the check), as users of a test tool expect, though 2020-12 makes
// format an annotation by default.
var schemaOptions = jsonschema.Options{FormatAssertion: true}

// maxShownSchemaErrors bounds the schema errors quoted in a message; all
// are in Result.SchemaErrors.
const maxShownSchemaErrors = 3

type compiledSchema struct {
	s   *jsonschema.Schema
	err error
}

// IsInlineSchema reports whether the expected value of a matches_schema
// assertion is the schema itself (JSON starting with "{", or true or
// false) rather than the path of a schema file.
func IsInlineSchema(expected string) bool {
	t := strings.TrimSpace(expected)
	return strings.HasPrefix(t, "{") || t == "true" || t == "false"
}

// schemaName names the schema in messages: "the inline JSON schema", "the
// JSON schema in schemas/user.json".
func schemaName(expected string) string {
	if IsInlineSchema(expected) || strings.TrimSpace(expected) == "" {
		return "the inline JSON schema"
	}
	return "the JSON schema in " + truncate(strings.TrimSpace(expected))
}

// schema compiles the schema of expected (inline, or read with
// LoadSchema), once per evaluation.
func (ev *evaluator) schema(expected string) (*jsonschema.Schema, error) {
	key := strings.TrimSpace(expected)
	if c, ok := ev.schemas[key]; ok {
		return c.s, c.err
	}
	s, err := ev.compileSchema(key)
	if ev.schemas == nil {
		ev.schemas = map[string]compiledSchema{}
	}
	ev.schemas[key] = compiledSchema{s, err}
	return s, err
}

func (ev *evaluator) compileSchema(key string) (*jsonschema.Schema, error) {
	if key == "" {
		return nil, invalid(CodeMissingSchema, "The JSON schema is missing: enter it, or the path of a schema file.")
	}
	data := []byte(key)
	if !IsInlineSchema(key) {
		if ev.opts.LoadSchema == nil {
			return nil, invalid(CodeSchemaFileUnavailable, "The schema file %s cannot be read here.", strconv.Quote(key))
		}
		var err error
		if data, err = ev.opts.LoadSchema(key); err != nil {
			return nil, err
		}
	}
	return jsonschema.CompileJSON(data, schemaOptions)
}

// evalSchema checks a matches_schema assertion.
func (ev *evaluator) evalSchema(a Assertion) Result {
	r := Result{Assertion: a}
	s, err := ev.schema(a.Expected)
	if err != nil {
		return invalidResult(r, err)
	}
	var val value
	if a.Target == TargetBody {
		doc, err := ev.bodyJSON()
		if err != nil {
			r.Outcome, r.Message = Failed, fmt.Sprintf("Expected %s to %s, but %s.", label(a), phrase(a), err.Error())
			return r
		}
		val = value{exists: true, v: doc}
	} else {
		val, err = ev.target(a)
		if err != nil {
			var fe *failErr
			if errors.As(err, &fe) {
				r.Outcome, r.Message = Failed, fmt.Sprintf("Expected %s to %s, but %s.", label(a), phrase(a), fe.msg)
				return r
			}
			return invalidResult(r, err)
		}
	}
	r.Exists = val.exists
	if !val.exists {
		r.Outcome = Failed
		r.Message = fmt.Sprintf("Expected %s to %s, but it does not exist.", label(a), phrase(a))
		return r
	}
	r.Actual = show(val)
	res, err := s.Validate(ev.ctx, val.v, jsonschema.DefaultLimits)
	if err != nil {
		return invalidResult(r, err)
	}
	if res.Valid {
		r.Outcome = Passed
		r.Message = fmt.Sprintf("%s %s.", upperFirst(label(a)), phrase3(a))
		return r
	}
	r.Outcome = Failed
	r.SchemaErrors = res.Errors
	r.Message = fmt.Sprintf("Expected %s to %s, but %s", label(a), phrase(a), schemaErrorSummary(res))
	return r
}

// schemaErrorSummary: `$.id must be integer, not string.`, `2 errors:
// $.id must be…; $ missing required property "name".`, with at most
// maxShownSchemaErrors quoted.
func schemaErrorSummary(res jsonschema.Result) string {
	n := len(res.Errors)
	count := strconv.Itoa(n) + " errors"
	if res.Truncated {
		count = "more than " + count
	}
	parts := make([]string, 0, maxShownSchemaErrors)
	for i, e := range res.Errors {
		if i == maxShownSchemaErrors {
			break
		}
		parts = append(parts, e.Path+" "+e.Message)
	}
	text := strings.Join(parts, "; ")
	if n > maxShownSchemaErrors || res.Truncated {
		text += "; …"
	}
	if n == 1 && !res.Truncated {
		return text + "."
	}
	return count + ": " + text + "."
}
