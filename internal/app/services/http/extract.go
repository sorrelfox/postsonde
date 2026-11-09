package http

import (
	"context"
	"errors"
	nethttp "net/http"
	"slices"

	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/assert"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/extract"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/runtimevars"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// Extraction into variables (TEST-05): after every response of an
// http or graphql request, its enabled extraction rules take values from
// the response into variables, as the request's Variables tab lists them.
// See docs/extraction.md.

// Outcomes of an extraction rule after a send (ExtractionResultDTO.Outcome).
const (
	// ExtractionSet: the value was found and the variable set.
	ExtractionSet = "set"
	// ExtractionFailed: the response has no such value (extract.not_found,
	// extract.unreadable_body, extract.value_too_large), or the variable
	// could not be set (the runtime variables' error code,
	// CodeNoRuntimeVariables). The variable keeps its value.
	ExtractionFailed = "failed"
	// ExtractionInvalid: the rule is wrong (an unknown source or scope, no
	// expression or one that does not parse, a variable name that cannot
	// be used, a {{variable}} of the expression that cannot be rendered).
	ExtractionInvalid = "invalid"
	// ExtractionSkipped: a rule of the run scope, outside a folder run.
	ExtractionSkipped = "skipped"
)

// CodeNoRuntimeVariables: a runtime rule had nowhere to set its value (no
// project, or no runtime variables in this build).
const CodeNoRuntimeVariables = "http.no_runtime_variables"

// MaxExtractionShown bounds ExtractionResultDTO.Value.
const MaxExtractionShown = 1024

// ExtractionResultDTO is the outcome of one of the request's extraction
// rules after a send (TEST-05), as the response's Variables tab shows it.
type ExtractionResultDTO struct {
	// Index is the rule's position in the request's rules, disabled ones
	// counted, so the UI can jump to its row.
	Index int `json:"index"`
	// Source, Expr and Variable are the rule's; Expr has its {{variables}}
	// rendered, secret values masked.
	Source   string `json:"source"`
	Expr     string `json:"expr"`
	Variable string `json:"variable"`
	// Scope is "runtime" or "run".
	Scope   string `json:"scope"`
	Secret  bool   `json:"secret"`
	Persist bool   `json:"persist"`
	// Outcome is "set", "failed", "invalid" or "skipped".
	Outcome string `json:"outcome"`
	// Value is the value set, as text (a string as it is, other JSON
	// values as compact JSON), cut after MaxExtractionShown bytes with
	// ValueClipped set; "••••••" for a secret rule, "" unless set.
	Value        string `json:"value"`
	ValueClipped bool   `json:"valueClipped"`
	// Message is a sentence saying what was set, or why not: "Set the
	// runtime variable token from JSONPath $.token.", "JSONPath $.token
	// selects nothing in the body."
	Message string `json:"message"`
	// Code is the error code of a failed or invalid rule.
	Code string `json:"code"`
}

// runVarSetter is a folder run's variables (runner.Vars) as extraction
// sets them.
type runVarSetter interface {
	Set(name string, value template.Variable)
}

// extractItem is an enabled rule of the request, rendered before the send:
// a rule to evaluate, or its result when rendering failed.
type extractItem struct {
	index  int
	rule   extract.Rule
	preset *ExtractionResultDTO
}

// extractionsOf returns the extraction rules of the request src sends: an
// HTTP request's or a GraphQL request's. An SSE request's are not applied.
func extractionsOf(src collection.HTTPSource) []projectfile.Extraction {
	switch {
	case src.SSE:
		return nil
	case src.GraphQL != nil:
		return src.GraphQL.Extractions
	default:
		return src.Request.Extractions
	}
}

// prepareExtractions renders the {{variables}} of the enabled rules'
// expressions with r, the resolver of the send, and returns them with the
// secret values they used. A variable that cannot be rendered makes that
// rule invalid instead of stopping the send.
func prepareExtractions(r template.Resolver, list []projectfile.Extraction) ([]extractItem, []string) {
	var items []extractItem
	var secrets []string
	for i, pe := range list {
		if pe.Disabled {
			continue
		}
		rule := extract.Rule{Source: extract.Source(pe.Source), Expr: pe.Expr, Variable: pe.Variable, Scope: extract.ScopeOf(pe.Scope), Secret: pe.Secret, Persist: pe.Persist}
		item := extractItem{index: i}
		if rule.Expr != "" && rule.Source != extract.SourceStatus {
			res := r.Render(rule.Expr)
			collectSecrets(&secrets, res.Refs)
			if err := res.Err(); err != nil {
				dto := extractionDTO(i, rule)
				dto.Outcome, dto.Code, dto.Message = ExtractionInvalid, template.CodeUnresolved, "This rule cannot be applied: "+err.Error()
				if e, ok := errors.AsType[*errs.Error](err); ok {
					dto.Message, dto.Code = "This rule cannot be applied. "+e.Message, e.Code
				}
				item.preset = &dto
			} else {
				rule.Expr = res.Text
			}
		}
		item.rule = rule
		items = append(items, item)
	}
	return items, secrets
}

func extractionDTO(index int, r extract.Rule) ExtractionResultDTO {
	return ExtractionResultDTO{Index: index, Source: string(r.Source), Expr: r.Expr, Variable: r.Variable, Scope: string(r.Scope), Secret: r.Secret, Persist: r.Persist}
}

// extractInput is the response as the rules read it: r with the whole
// body (not the clipped text of the DTO), r.Body or, when sp is not nil,
// the spool's body read back from its file when a rule needs it (HTTP-37).
// problem says why the whole body could not be read.
func extractInput(r *httpclient.Response, sp *spool, needBody bool) (resp assert.Response, problem string) {
	header := nethttp.Header{}
	for _, h := range r.Headers {
		header.Add(h.Name, h.Value)
	}
	resp = assert.Response{Status: r.StatusCode, Header: header, Body: r.Body, Time: r.Duration}
	if sp == nil || sp.n == int64(len(r.Body)) || !needBody {
		return resp, ""
	}
	if sp.n > MaxEvalBodyBytes {
		return resp, tooLargeToEval("extract from", sp.n)
	}
	body, err := sp.readAll()
	if err != nil {
		return resp, "The body could not be read back: " + err.Error() + "."
	}
	resp.Body = body
	return resp, ""
}

// runExtractions applies the send's rules to the response r (TEST-05):
// each value found is set in the project's runtime variables or the
// run's, in the order of the rules. It returns the results, with the
// send's secrets and the secret values set masked, and those secret
// values, for the send to mask in its timeline, results and history. It
// returns an empty list, not nil, when there is nothing to apply.
func (x *send) runExtractions(ctx context.Context, r *httpclient.Response, sp *spool) ([]ExtractionResultDTO, []string) {
	out := make([]ExtractionResultDTO, 0, len(x.extractions))
	if len(x.extractions) == 0 {
		return out, nil
	}
	var rules []extract.Rule
	needBody := false
	for _, it := range x.extractions {
		if it.preset != nil {
			continue
		}
		rules = append(rules, it.rule)
		needBody = needBody || needsBody(it.rule.Source)
	}
	resp, bodyProblem := extractInput(r, sp, needBody)
	results := extract.Evaluate(ctx, resp, rules)

	var secrets []string
	var store *runtimevars.Store
	var storeErr error
	storeOpened := false
	for _, it := range x.extractions {
		if it.preset != nil {
			out = append(out, *it.preset)
			continue
		}
		res := results[0]
		results = results[1:]
		dto := extractionDTO(it.index, it.rule)
		switch {
		case bodyProblem != "" && needsBody(it.rule.Source) && res.Outcome != extract.Invalid:
			dto.Outcome, dto.Code, dto.Message = ExtractionFailed, CodeBodyTooLarge, bodyProblem
		case res.Outcome == extract.Invalid:
			dto.Outcome, dto.Code, dto.Message = ExtractionInvalid, res.Code, res.Message
		case res.Outcome == extract.Failed:
			dto.Outcome, dto.Code, dto.Message = ExtractionFailed, res.Code, res.Message
		case it.rule.Scope == extract.ScopeRun && x.runVars == nil:
			dto.Outcome = ExtractionSkipped
			dto.Message = "Not set: " + it.rule.Variable + " is a run variable, set during a folder run only."
		default:
			if it.rule.Scope == extract.ScopeRuntime && !storeOpened && x.runtimeStore != nil {
				storeOpened = true
				store, storeErr = x.runtimeStore()
			}
			x.setExtracted(&dto, it.rule, res, store, storeErr)
			if dto.Outcome == ExtractionSet && it.rule.Secret && res.Value != "" {
				secrets = append(secrets, res.Value)
			}
		}
		out = append(out, dto)
	}
	// The request's secrets, those its assertions and rules used, and the
	// secret values just set.
	all := append(append(slices.Clone(x.secrets), x.checkSecrets...), secrets...)
	masker := httpclient.NewSecretMasker(all)
	for i := range out {
		out[i].Expr = masker.MaskText(out[i].Expr)
		out[i].Message = masker.MaskText(out[i].Message)
		if out[i].Outcome != ExtractionSet {
			continue
		}
		if out[i].Secret {
			out[i].Value = httpclient.Mask
			continue
		}
		out[i].Value = masker.MaskText(out[i].Value)
		if len(out[i].Value) > MaxExtractionShown {
			out[i].Value, out[i].ValueClipped = clipShown(out[i].Value, MaxExtractionShown), true
		}
	}
	return out, secrets
}

// setExtracted sets the value res found for rule and says so in dto:
// in the runtime store (and, in a run, the run's variables), or in the
// run's variables only.
func (x *send) setExtracted(dto *ExtractionResultDTO, rule extract.Rule, res extract.Result, store *runtimevars.Store, storeErr error) {
	value := template.Variable{Value: res.Value, Secret: rule.Secret}
	if rule.Scope == extract.ScopeRun {
		x.runVars.Set(rule.Variable, value)
		dto.Outcome, dto.Value = ExtractionSet, res.Value
		dto.Message = "Set the run variable " + rule.Variable + " from " + res.Label + "."
		return
	}
	fail := func(code, msg string) {
		dto.Outcome, dto.Code, dto.Message = ExtractionFailed, code, msg
	}
	switch {
	case storeErr != nil:
		e, _ := errors.AsType[*errs.Error](storeErr)
		code := CodeNoRuntimeVariables
		if e != nil && e.Code != "" {
			code = e.Code
		}
		fail(code, "The runtime variable "+rule.Variable+" could not be set: "+errMessage(storeErr))
		return
	case store == nil:
		fail(CodeNoRuntimeVariables, "The runtime variable "+rule.Variable+" could not be set: the request has no open project.")
		return
	}
	if err := store.Set(runtimevars.Variable{Name: rule.Variable, Value: res.Value, Secret: rule.Secret, Persist: rule.Persist}); err != nil {
		e, _ := errors.AsType[*errs.Error](err)
		code := CodeNoRuntimeVariables
		if e != nil && e.Code != "" {
			code = e.Code
		}
		fail(code, "The runtime variable "+rule.Variable+" could not be set: "+errMessage(err))
		return
	}
	dto.Outcome, dto.Value = ExtractionSet, res.Value
	dto.Message = "Set the runtime variable " + rule.Variable + " from " + res.Label + "."
	if x.runVars != nil {
		// A run's own variable of the name would hide the new value from
		// the rest of the run (runtime < run): it gets it too.
		x.runVars.Set(rule.Variable, value)
		dto.Message = "Set the runtime variable " + rule.Variable + ", and the run's, from " + res.Label + "."
	}
}

// errMessage is an error's message, its errs.Error message when it has one.
func errMessage(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok && e.Message != "" {
		return e.Message
	}
	return err.Error()
}

// needsBody reports whether a source reads the body.
func needsBody(s extract.Source) bool {
	switch s {
	case extract.SourceJSONPath, extract.SourceXPath, extract.SourceRegex:
		return true
	}
	return false
}

// clipShown cuts s to at most n bytes on a character boundary and adds "…".
func clipShown(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n] + "…"
}
