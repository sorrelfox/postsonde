package http

import (
	"context"
	"errors"
	"io"
	"io/fs"
	nethttp "net/http"
	"slices"
	"strconv"

	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/assert"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/fileaccess"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// TestResultDTO is the result of one of the request's assertions after a
// send (TEST-01, TEST-06), as the response's Tests tab shows it.
type TestResultDTO struct {
	// Index is the assertion's position in the request's assertions,
	// disabled ones counted, so the UI can jump to its row.
	Index int `json:"index"`
	// Target, Expr and Op are the assertion's; Expr and Expected have their
	// {{variables}} rendered, secret values masked.
	Target   string `json:"target"`
	Expr     string `json:"expr"`
	Op       string `json:"op"`
	Expected string `json:"expected"`
	// Outcome is "passed", "failed" or "invalid" (the assertion itself
	// cannot be evaluated; Code says why).
	Outcome string `json:"outcome"`
	// Actual is the target's value as shown ("404", "\"text/html\"",
	// "120 ms"); "" when it does not exist or the assertion is invalid.
	Actual string `json:"actual"`
	// Exists is false when the target is not in the response.
	Exists bool `json:"exists"`
	// Message is a sentence with the expected and the actual value:
	// "Expected status code to equal 200, but it was 404."
	Message string `json:"message"`
	// Code is the error code of an invalid result: assert.* codes,
	// jsonpath.invalid_query, xpath.invalid_query, or template.unresolved
	// (and the other template codes) when a {{variable}} of the assertion
	// cannot be rendered.
	Code string `json:"code"`
	// SchemaErrors are the ways the value does not match the JSON Schema
	// of a failed matches_schema assertion (TEST-07), secret values
	// masked; empty otherwise.
	SchemaErrors []SchemaErrorDTO `json:"schemaErrors"`
}

// SchemaErrorDTO is one way a value does not match a JSON Schema.
type SchemaErrorDTO struct {
	// Path is the value's location as a JSONPath: "$.items[0].id".
	Path string `json:"path"`
	// InstancePath is the same location as a JSON Pointer: "/items/0/id".
	InstancePath string `json:"instancePath"`
	// SchemaPath is the failing keyword in the schema: "#/$defs/id/type".
	SchemaPath string `json:"schemaPath"`
	// Keyword is the failing keyword ("type", "required"…).
	Keyword string `json:"keyword"`
	// Message says what the value should be: "must be integer, not string".
	Message string `json:"message"`
}

// Codes of the errors reading the schema file of a matches_schema
// assertion; they make that assertion invalid.
const (
	// CodeSchemaFileMissing: the file does not exist, or its relative
	// path has no open project to be resolved in.
	CodeSchemaFileMissing = "http.schema_file_missing"
	// CodeSchemaFileUnreadable: the file is not a regular file, cannot
	// be read, or is over MaxSchemaFileBytes.
	CodeSchemaFileUnreadable = "http.schema_file_unreadable"
)

// MaxSchemaFileBytes bounds a JSON Schema file read for an assertion.
const MaxSchemaFileBytes = 8 << 20

// schemaLoader reads the schema files of matches_schema assertions
// through p, the project folder and the allowed directories (PRV-10):
// relative paths ("/" separators) are resolved against the project root,
// like body files (docs/file-format.md), and a file outside them is
// refused with fileaccess.not_allowed.
func schemaLoader(p fileaccess.Policy) func(string) ([]byte, error) {
	p.Subject = "The schema file"
	return func(stored string) ([]byte, error) {
		f, info, err := p.Open(stored)
		if err != nil {
			var e *errs.Error
			errors.As(err, &e)
			switch {
			case fileaccess.IsNotAllowed(err):
				return nil, err
			case e != nil && e.Code == fileaccess.CodeInvalidPath:
				return nil, errs.New(errs.NotFound, "The schema file "+strconv.Quote(stored)+" cannot be found: a relative path needs the request's project to be open.").
					WithCode(CodeSchemaFileMissing).
					WithHint("Open the project the request belongs to, or use an absolute path.")
			case e != nil && e.Code == fileaccess.CodeUnreadable && !errors.Is(err, fs.ErrPermission) && e.Err == nil:
				return nil, errs.New(errs.InvalidArgument, "The schema file "+strconv.Quote(stored)+" is not a regular file.").
					WithCode(CodeSchemaFileUnreadable)
			}
			return nil, schemaFileError(err, stored)
		}
		defer f.Close()
		if info.Size() > MaxSchemaFileBytes {
			return nil, errs.New(errs.InvalidArgument, "The schema file "+strconv.Quote(stored)+" is "+formatBytes(info.Size())+", larger than "+formatBytes(MaxSchemaFileBytes)+".").
				WithCode(CodeSchemaFileUnreadable)
		}
		data, err := io.ReadAll(io.LimitReader(f, MaxSchemaFileBytes+1))
		if err != nil {
			return nil, schemaFileError(err, stored)
		}
		return data, nil
	}
}

func schemaFileError(err error, stored string) error {
	if errors.Is(err, fs.ErrNotExist) {
		return errs.Wrap(err, errs.NotFound, "The schema file "+strconv.Quote(stored)+" was not found.").
			WithCode(CodeSchemaFileMissing).
			WithHint("Choose the file again, or restore it at that path.")
	}
	kind := errs.IO
	if errors.Is(err, fs.ErrPermission) {
		kind = errs.PermissionDenied
	}
	return errs.Wrap(err, kind, "The schema file "+strconv.Quote(stored)+" could not be read.").
		WithCode(CodeSchemaFileUnreadable)
}

// checkItem is an enabled assertion of the request, rendered before the
// send: an assertion to evaluate, or its result when rendering failed.
type checkItem struct {
	index  int
	a      assert.Assertion
	preset *assert.Result
}

// assertionsOf returns the assertions of the request src sends: an HTTP
// request's or a GraphQL request's. An SSE request's are not checked.
func assertionsOf(src collection.HTTPSource) []projectfile.Assertion {
	switch {
	case src.SSE:
		return nil
	case src.GraphQL != nil:
		return src.GraphQL.Assertions
	default:
		return src.Request.Assertions
	}
}

// prepareChecks renders the {{variables}} of the enabled assertions with
// r, the resolver of the send, and returns them with the secret values
// they used. An undefined variable, or another template problem, makes
// that assertion invalid instead of stopping the send.
func prepareChecks(r template.Resolver, list []projectfile.Assertion) ([]checkItem, []string) {
	var items []checkItem
	var secrets []string
	for i, pa := range list {
		if pa.Disabled {
			continue
		}
		a := assert.Assertion{Target: assert.TargetKind(pa.Target), Op: assert.Operator(pa.Op)}
		item := checkItem{index: i}
		var fail error
		render := func(text string) string {
			if fail != nil || text == "" {
				return text
			}
			res := r.Render(text)
			collectSecrets(&secrets, res.Refs)
			if err := res.Err(); err != nil {
				fail = err
				return text
			}
			return res.Text
		}
		a.Expr = render(pa.Expr)
		if a.Op != assert.OpExists && a.Op != assert.OpNotExists {
			a.Expected = render(pa.Expected)
		} else {
			a.Expected = pa.Expected
		}
		item.a = a
		if fail != nil {
			res := assert.Result{Assertion: a, Outcome: assert.Invalid, Message: "This assertion cannot be checked: " + fail.Error(), Code: template.CodeUnresolved}
			if e, ok := errors.AsType[*errs.Error](fail); ok {
				res.Message, res.Code = "This assertion cannot be checked. "+e.Message, e.Code
			}
			item.preset = &res
		}
		items = append(items, item)
	}
	return items, secrets
}

// readsBody reports whether an assertion on target needs the body.
func readsBody(target assert.TargetKind) bool {
	switch target {
	case assert.TargetStatus, assert.TargetHeader, assert.TargetCookie, assert.TargetTime, assert.TargetSize:
		return false
	}
	return true
}

// runChecks evaluates items against the response r, with the whole body
// (not the clipped text of the DTO): r.Body, or, when sp is not nil, the
// spool's body, read back from its file only when an assertion needs it
// (HTTP-37). A body over MaxEvalBodyBytes makes the assertions on it
// invalid (CodeBodyTooLarge). The secret values in the results are masked
// with m. Schema files of matches_schema assertions are read with
// loadSchema (nil: none can be read). It returns an empty list, not nil,
// when there is nothing to check.
func runChecks(ctx context.Context, items []checkItem, r *httpclient.Response, sp *spool, m httpclient.Masker, loadSchema func(string) ([]byte, error)) []TestResultDTO {
	out := make([]TestResultDTO, 0, len(items))
	if len(items) == 0 {
		return out
	}
	header := nethttp.Header{}
	for _, h := range r.Headers {
		header.Add(h.Name, h.Value)
	}
	resp := assert.Response{Status: r.StatusCode, Header: header, Body: r.Body, Time: r.Duration}
	full := int64(len(r.Body))
	if sp != nil {
		full = sp.n
	}
	switch {
	case r.BodyTruncated:
		resp.Size = r.Size
	case full != int64(len(r.Body)):
		resp.Size = full
	}
	// The whole body is read back only for an assertion on it.
	var bodyProblem string
	if sp != nil && full != int64(len(r.Body)) && slices.ContainsFunc(items, func(it checkItem) bool { return it.preset == nil && readsBody(it.a.Target) }) {
		if full > MaxEvalBodyBytes {
			bodyProblem = tooLargeToEval("check", full)
		} else if body, err := sp.readAll(); err != nil {
			bodyProblem = "The body could not be read back: " + err.Error() + "."
		} else {
			resp.Body = body
		}
	}
	// One Evaluate, so the body is parsed once per format.
	var list []assert.Assertion
	for i, it := range items {
		if it.preset == nil && bodyProblem != "" && readsBody(it.a.Target) {
			items[i].preset = &assert.Result{Assertion: it.a, Outcome: assert.Invalid, Message: bodyProblem, Code: CodeBodyTooLarge}
			continue
		}
		if it.preset == nil {
			list = append(list, it.a)
		}
	}
	evaluated := assert.EvaluateWith(ctx, resp, list, assert.Options{LoadSchema: loadSchema})
	for _, it := range items {
		var res assert.Result
		if it.preset != nil {
			res = *it.preset
		} else {
			res, evaluated = evaluated[0], evaluated[1:]
		}
		schemaErrors := make([]SchemaErrorDTO, len(res.SchemaErrors))
		for i, e := range res.SchemaErrors {
			schemaErrors[i] = SchemaErrorDTO{
				Path:         m.MaskText(e.Path),
				InstancePath: m.MaskText(e.InstancePath),
				SchemaPath:   e.SchemaPath,
				Keyword:      e.Keyword,
				Message:      m.MaskText(e.Message),
			}
		}
		out = append(out, TestResultDTO{
			Index:        it.index,
			Target:       string(it.a.Target),
			Expr:         m.MaskText(it.a.Expr),
			Op:           string(it.a.Op),
			Expected:     m.MaskText(it.a.Expected),
			Outcome:      string(res.Outcome),
			Actual:       m.MaskText(res.Actual),
			Exists:       res.Exists,
			Message:      m.MaskText(res.Message),
			Code:         res.Code,
			SchemaErrors: schemaErrors,
		})
	}
	return out
}
