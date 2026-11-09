package assert

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Query names a value of a response the way an assertion's target does
// (TEST-02), without an operator: what extraction into variables reads
// (TEST-05, internal/core/extract).
type Query struct {
	Target TargetKind
	// Expr is the header or cookie name, the JSONPath, the XPath or the
	// regular expression; unused by the other targets.
	Expr string
}

// Reading is the value a Query found, or why there is none.
type Reading struct {
	// Label names the target in messages: "JSONPath $.token", "header
	// X-Token", "status code".
	Label string
	// Exists is false when the target is not in the response (and when
	// Problem or Code is set).
	Exists bool
	// Value is the value in the model of jsonpath.Decode: string,
	// json.Number, bool, nil, []any or *jsonpath.Object.
	Value any
	// Text is Value as text: a string as it is, a number as written, true,
	// false and null as words, arrays and objects as compact JSON (member
	// order kept, no HTML escaping).
	Text string
	// Problem is set when the response cannot provide the target: "the
	// body is not valid JSON", "the body is not valid XML".
	Problem string
	// Message is set when the query cannot be evaluated: an unknown
	// target, no expression, a JSONPath, XPath or regular expression that
	// does not parse, evaluation over the work limits. Code is its code,
	// those of an invalid assertion (Invalid reports either).
	Code    string
	Message string
}

// Read reads every query from resp, in order, the way Evaluate reads an
// assertion's target: the body is parsed at most once per format, and ctx
// bounds JSONPath and XPath evaluation.
func Read(ctx context.Context, resp Response, list []Query) []Reading {
	ev := &evaluator{ctx: ctx, resp: resp}
	out := make([]Reading, len(list))
	for i, q := range list {
		out[i] = ev.read(q)
	}
	return out
}

func (ev *evaluator) read(q Query) Reading {
	a := Assertion{Target: q.Target, Expr: q.Expr}
	r := Reading{Label: label(a)}
	val, err := ev.target(a)
	if err != nil {
		var fe *failErr
		if errors.As(err, &fe) {
			r.Problem = fe.msg
			return r
		}
		res := invalidResult(Result{}, err)
		r.Code, r.Message = res.Code, res.Message
		return r
	}
	if !val.exists {
		return r
	}
	r.Exists, r.Value, r.Text = true, val.v, plainText(val.v)
	return r
}

// Invalid reports whether the query could not be evaluated.
func (r Reading) Invalid() bool { return r.Code != "" || r.Message != "" }

// plainText is text without HTML escaping in arrays and objects.
func plainText(v any) string {
	switch v := v.(type) {
	case string:
		return v
	case json.Number:
		return string(v)
	case nil:
		return "null"
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return fmt.Sprint(v)
	}
	return strings.TrimSuffix(b.String(), "\n")
}
