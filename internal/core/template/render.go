package template

import (
	"fmt"
	"io"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Default limits of a Resolver.
const (
	// DefaultMaxDepth is how many levels of variables inside variable
	// values are expanded.
	DefaultMaxDepth = 10
	// DefaultMaxExpansions bounds the number of lookups in one Render, so
	// values that repeat other values many times cannot blow up.
	DefaultMaxExpansions = 10_000
	// DefaultMaxLength bounds the length of any rendered value, including
	// the result, that grows because of substitutions.
	DefaultMaxLength = 64 << 20
)

// Error codes returned by Result.Err.
const (
	CodeUnresolved = "template.unresolved"
	CodeCycle      = "template.cycle"
	CodeTooDeep    = "template.too_deep"
	CodeEmptyName  = "template.empty_name"
	CodeLimit      = "template.limit"
	// CodeInvalidDynamic: a dynamic value such as {{$randomInt 5 1}} has
	// invalid arguments.
	CodeInvalidDynamic = "template.invalid_dynamic"
	// CodeInvalidFilter: a filter pipe such as {{token | nope}} names an
	// unknown filter, has invalid arguments, or a filter failed (a value
	// that is not Base64 for base64Decode).
	CodeInvalidFilter = "template.invalid_filter"
)

// ProblemKind says what is wrong with a reference.
type ProblemKind string

const (
	// ProblemUnresolved: no scope defines the name.
	ProblemUnresolved ProblemKind = "unresolved"
	// ProblemCycle: the value refers back to a variable being expanded.
	ProblemCycle ProblemKind = "cycle"
	// ProblemTooDeep: values nest deeper than the resolver's MaxDepth.
	ProblemTooDeep ProblemKind = "too_deep"
	// ProblemEmptyName: {{}} or {{ }}.
	ProblemEmptyName ProblemKind = "empty_name"
	// ProblemLimit: MaxExpansions or MaxLength was reached.
	ProblemLimit ProblemKind = "limit"
	// ProblemInvalidDynamic: a dynamic value ($randomInt, $datetime...)
	// with invalid arguments. Problem.Message says what is wrong.
	ProblemInvalidDynamic ProblemKind = "invalid_dynamic"
	// ProblemInvalidFilter: a filter pipe ({{token | base64}}) that is not
	// valid or whose filter failed. Problem.Message says what is wrong.
	ProblemInvalidFilter ProblemKind = "invalid_filter"
	// ProblemUnterminated: "{{" without "}}". It is a warning: the braces
	// are sent literally.
	ProblemUnterminated ProblemKind = "unterminated"
)

// Blocking reports whether a problem of this kind must stop a send.
func (k ProblemKind) Blocking() bool { return k != ProblemUnterminated }

// Problem is something wrong found while rendering.
type Problem struct {
	Kind ProblemKind
	// Name is the variable the problem is about; empty for
	// ProblemUnterminated and ProblemEmptyName.
	Name string
	// Path is the chain of names from the reference in the rendered text
	// down to Name: ["url", "host"] when {{url}} has the value
	// "https://{{host}}" and host is undefined. For a cycle it ends with
	// the repeated name.
	Path []string
	// Start and End locate the problem in the text passed to Render: the
	// reference itself, or for a problem inside a variable's value the
	// top-level reference that led to it.
	Start, End int
	// Message explains a ProblemInvalidDynamic ("the minimum is greater
	// than the maximum"), a ProblemInvalidFilter ("\"b64\" is not a
	// filter"), a ProblemUnresolved dotted reference whose base
	// variable is defined but has no such field ("{{auth}} has no field
	// \"token\"") and a ProblemFile or ProblemResponse (the error's
	// text); empty otherwise.
	Message string
	// Cause is the error behind a ProblemFile (Resolver.Files's, or
	// ErrFilesUnavailable) or a ProblemResponse (Resolver.Responses's, or
	// ErrResponsesUnavailable); nil otherwise.
	Cause error
}

// Ref is one variable reference and how it was resolved.
type Ref struct {
	Name string
	// Start, End, NameStart and NameEnd are byte offsets in the text that
	// contains the reference: the text passed to Render for top-level ones,
	// the parent's raw value for nested ones.
	Start, End         int
	NameStart, NameEnd int
	// Resolved is true when the value was substituted. An unresolved
	// reference is left in the output as written.
	Resolved bool
	// Scope is the name of the scope that supplied the value.
	Scope string
	// Value is the rendered value (nested variables expanded).
	Value string
	// Secret is true when the value or any value nested in it is secret.
	// A filter pipe is secret when its input or a key is: a hash or an
	// encoding of a secret is secret too.
	Secret bool
	// Nested are the references inside the variable's raw value. For a
	// filter pipe ({{body | hmacSha256 key}}) they are the references it
	// reads: the input, then the keys.
	Nested []Ref
	// Base is set for a dotted reference no scope defines by name
	// ({{auth.token}}): the variable whose JSON value the rest of the name
	// was selected from ("auth"). Scope, Secret and Nested are then those
	// of the base.
	Base string
}

// Result is the outcome of Render.
type Result struct {
	// Text is the rendered text. Unresolved references are left as written,
	// so it is fine for a preview but must not be sent when Err is not nil.
	Text string
	// Refs are the top-level references, in order.
	Refs []Ref
	// Problems are blocking problems and warnings, in the order found.
	Problems []Problem
	// Secret is true when a secret value was substituted into Text.
	Secret bool
}

// Resolver renders templates against a chain of scopes. The zero limits
// mean the defaults. A Resolver is safe for concurrent use if its scopes
// and Rand are.
//
// A name that no scope defines and that IsDynamic accepts ({{$uuid}},
// {{$timestamp +1d}}, {{$randomInt 1 6}}) is generated afresh for every
// reference, with Ref.Scope set to DynamicScope. A scope that defines the
// same name wins.
//
// A name with a "|" outside double quotes that is neither defined nor a
// dynamic value is a filter pipe ({{token | base64}}, {{body | hmacSha256
// key}}): its input is resolved, then encoded or hashed (see filter.go).
type Resolver struct {
	Scopes        Chain
	MaxDepth      int
	MaxExpansions int
	MaxLength     int
	// Now is the clock of the timestamp values; nil means time.Now.
	Now func() time.Time
	// Rand is the random source of $uuid and the $random values; nil
	// means crypto/rand.Reader. Tests pass a deterministic reader.
	Rand io.Reader
	// Prompts are the values typed for {{$prompt LABEL}} references, by
	// label (DYN-07, prompt.go); a label without one is a ProblemPrompt.
	Prompts map[string]string
	// Files reads the files of {{$file PATH}} references (DYN-08,
	// file.go) and decides which may be read; nil refuses them with
	// ErrFilesUnavailable.
	Files FileReader
	// Responses reads the values of {{$response REQUEST SOURCE EXPR}}
	// references (DYN-06, response.go) from other requests' last
	// responses; nil refuses them with ErrResponsesUnavailable.
	Responses ResponseReader
}

// Render substitutes every variable in src. Values that contain variables
// are rendered too, with the same chain, up to MaxDepth levels; a cycle is
// reported instead of being followed.
func (r Resolver) Render(src string) Result {
	st := &renderer{
		chain:     r.Scopes,
		maxDep:    orDefault(r.MaxDepth, DefaultMaxDepth),
		maxExp:    orDefault(r.MaxExpansions, DefaultMaxExpansions),
		maxLen:    orDefault(r.MaxLength, DefaultMaxLength),
		dyn:       newDynamicGen(r.Now, r.Rand),
		prompts:   r.Prompts,
		files:     r.Files,
		responses: r.Responses,
	}
	var b strings.Builder
	refs, secret := st.render(src, Parse(src), &b)
	return Result{Text: b.String(), Refs: refs, Problems: st.problems, Secret: secret}
}

func orDefault(v, def int) int {
	if v <= 0 {
		return def
	}
	return v
}

type renderer struct {
	chain                  Chain
	maxDep, maxExp, maxLen int
	dyn                    *dynamicGen
	prompts                map[string]string
	files                  FileReader
	responses              ResponseReader
	// pipeInput is set while the input of a pipe is resolved: a
	// {{$file}} input may be binary, as the filters turn it into text.
	pipeInput bool

	expansions int
	limitHit   bool
	stack      []string // names being expanded, outermost first
	topStart   int      // span of the top-level reference being expanded
	topEnd     int
	problems   []Problem
	seen       map[string]struct{} // problems already reported
}

func (st *renderer) render(src string, segs []Segment, b *strings.Builder) (refs []Ref, secret bool) {
	for _, seg := range segs {
		switch seg.Kind {
		case Text:
			b.WriteString(seg.Text)
		case Unterminated:
			b.WriteString(seg.Text)
			st.report(ProblemUnterminated, "", seg.Start, seg.End)
		case Var:
			if len(st.stack) == 0 {
				st.topStart, st.topEnd = seg.Start, seg.End
			}
			ref := st.resolve(seg)
			if ref.Resolved && b.Len()+len(ref.Value) > st.maxLen {
				st.limit(seg)
				ref.Resolved, ref.Value = false, ""
			}
			if ref.Resolved {
				b.WriteString(ref.Value)
				secret = secret || ref.Secret
			} else {
				b.WriteString(src[seg.Start:seg.End])
			}
			refs = append(refs, ref)
		}
	}
	return refs, secret
}

func (st *renderer) resolve(seg Segment) Ref {
	ref := Ref{Name: seg.Name, Start: seg.Start, End: seg.End, NameStart: seg.NameStart, NameEnd: seg.NameEnd}
	pipeInput := st.pipeInput
	st.pipeInput = false // only for the reference itself, not the ones inside it
	switch {
	case seg.Name == "":
		st.report(ProblemEmptyName, "", seg.Start, seg.End)
		return ref
	case slices.Contains(st.stack, seg.Name):
		st.report(ProblemCycle, seg.Name, seg.Start, seg.End)
		return ref
	case st.expansions >= st.maxExp:
		st.limit(seg)
		return ref
	}
	st.expansions++

	bind, ok := st.chain.Lookup(seg.Name)
	if !ok {
		if stages, pipe := splitPipe(seg.Name); pipe && !IsDynamic(seg.Name) {
			return st.resolvePipe(seg, ref, stages)
		}
		if p, prompt, err := ParsePrompt(seg.Name); prompt {
			return st.resolvePrompt(seg, ref, p, err)
		}
		if path, file, err := ParseFile(seg.Name); file {
			return st.resolveFile(seg, ref, path, err, pipeInput)
		}
		if rr, resp, err := ParseResponse(seg.Name); resp {
			return st.resolveResponse(seg, ref, rr, err)
		}
		v, dyn, err := st.dyn.value(seg.Name)
		switch {
		case !dyn:
			if split, ok := st.chain.pathBase(seg.Name); ok {
				return st.resolvePath(seg, ref, split)
			}
			st.report(ProblemUnresolved, seg.Name, seg.Start, seg.End)
		case err != nil:
			st.reportMessage(ProblemInvalidDynamic, seg.Name, err.Error(), seg.Start, seg.End)
		default:
			// A generated value is used as is: it is never parsed for
			// variables.
			ref.Resolved, ref.Scope, ref.Value = true, DynamicScope, v
		}
		return ref
	}
	ref.Scope, ref.Secret = bind.Scope, bind.Secret

	var segs []Segment
	if strings.Contains(bind.Value, "{{") {
		segs = Parse(bind.Value)
	}
	if !hasRefs(segs) {
		ref.Resolved = true
		ref.Value = literal(bind.Value, segs)
		return ref
	}
	if len(st.stack) >= st.maxDep {
		st.report(ProblemTooDeep, seg.Name, seg.Start, seg.End)
		return ref
	}

	st.stack = append(st.stack, seg.Name)
	var vb strings.Builder
	nested, secret := st.render(bind.Value, segs, &vb)
	st.stack = st.stack[:len(st.stack)-1]

	ref.Resolved = true
	ref.Value = vb.String()
	ref.Secret = ref.Secret || secret
	ref.Nested = nested
	return ref
}

// resolvePath resolves a dotted reference such as {{auth.token}} that no
// scope defines by name: the base variable (auth) is resolved as any
// reference, with its own nesting, cycle and limit checks, and path is then
// selected from its value as JSON. A failed selection is an unresolved
// reference whose Problem.Message says which step failed.
func (st *renderer) resolvePath(seg Segment, ref Ref, split PathSplit) Ref {
	baseSeg := seg
	baseSeg.Name = split.Base
	base := st.resolve(baseSeg)
	ref.Base = split.Base
	ref.Nested = base.Nested
	if !base.Resolved {
		return ref // the base's problem is already reported
	}
	v, err := Select(split.Base, base.Value, split.Path)
	if err != nil {
		st.reportMessage(ProblemUnresolved, seg.Name, err.Error(), seg.Start, seg.End)
		return ref
	}
	ref.Resolved, ref.Scope, ref.Secret, ref.Value = true, base.Scope, base.Secret, v
	return ref
}

func hasRefs(segs []Segment) bool {
	for _, s := range segs {
		if s.Kind != Text {
			return true
		}
	}
	return false
}

// literal joins text-only segments; with no segments the value is used as is.
func literal(value string, segs []Segment) string {
	if segs == nil {
		return value
	}
	var b strings.Builder
	for _, s := range segs {
		b.WriteString(s.Text)
	}
	return b.String()
}

func (st *renderer) limit(seg Segment) {
	if st.limitHit {
		return
	}
	st.limitHit = true
	st.report(ProblemLimit, seg.Name, seg.Start, seg.End)
}

// report records a problem at [start, end) of the current text. Inside a
// value, the position is that of the top-level reference. A problem with
// the same kind, position and path is recorded once.
func (st *renderer) report(kind ProblemKind, name string, start, end int) {
	st.reportMessage(kind, name, "", start, end)
}

func (st *renderer) reportMessage(kind ProblemKind, name, msg string, start, end int) {
	st.reportProblem(Problem{Kind: kind, Name: name, Message: msg}, start, end)
}

// reportCause records a problem caused by err (a ProblemFile), with the
// error's text as its message.
func (st *renderer) reportCause(kind ProblemKind, name string, err error, start, end int) {
	st.reportProblem(Problem{Kind: kind, Name: name, Message: err.Error(), Cause: err}, start, end)
}

// reportProblem records p (Kind, Name, Message and Cause set) at
// [start, end), as report does.
func (st *renderer) reportProblem(p Problem, start, end int) {
	kind, name := p.Kind, p.Name
	path := append(make([]string, 0, len(st.stack)+1), st.stack...)
	if name != "" {
		path = append(path, name)
	}
	if len(st.stack) > 0 {
		start, end = st.topStart, st.topEnd
	}
	key := string(kind) + "\x00" + strconv.Itoa(start) + "\x00" + strings.Join(path, "\x00")
	if _, dup := st.seen[key]; dup {
		return // the same problem reached again through repeated references
	}
	if st.seen == nil {
		st.seen = make(map[string]struct{})
	}
	st.seen[key] = struct{}{}
	p.Path, p.Start, p.End = path, start, end
	st.problems = append(st.problems, p)
}

// Err returns nil when nothing blocks sending the rendered text. Otherwise
// it returns an errs.InvalidArgument error: when names are undefined it
// lists them (code template.unresolved), else it describes the first
// blocking problem. Names are not secret; values never appear in the error.
func (r Result) Err() error {
	var unresolved []string
	var prompts []Problem
	var first, why *Problem // why: an unresolved dotted reference's explanation
	count := 0
	for i := range r.Problems {
		p := &r.Problems[i]
		if !p.Kind.Blocking() {
			continue
		}
		count++
		if first == nil {
			first = p
		}
		if p.Kind == ProblemUnresolved && !slices.Contains(unresolved, p.Name) {
			unresolved = append(unresolved, p.Name)
		}
		if p.Kind == ProblemUnresolved && p.Message != "" && why == nil {
			why = p
		}
		if p.Kind == ProblemPrompt {
			prompts = append(prompts, *p)
		}
	}
	if first == nil {
		return nil
	}

	const escapeHint = `, or write \{{ to send the braces as they are.`
	var e *errs.Error
	switch {
	case len(unresolved) == 1 && why != nil:
		e = errs.New(errs.InvalidArgument, fmt.Sprintf("The variable %s cannot be resolved: %s.", braces(unresolved[0]), why.Message)).
			WithCode(CodeUnresolved).
			WithHint("Check the name and the value it reads" + escapeHint)
	case len(unresolved) == 1:
		e = errs.New(errs.InvalidArgument, fmt.Sprintf("The variable %s is not defined.", braces(unresolved[0]))).
			WithCode(CodeUnresolved).
			WithHint("Define it in the active environment" + escapeHint)
	case len(unresolved) > 1:
		e = errs.New(errs.InvalidArgument, fmt.Sprintf("%d variables are not defined: %s.", len(unresolved), bracesList(unresolved))).
			WithCode(CodeUnresolved).
			WithHint("Define them in the active environment" + escapeHint)
	case first.Kind == ProblemPrompt:
		e = promptErr(PromptsOf(prompts))
	case first.Kind == ProblemCycle:
		e = errs.New(errs.InvalidArgument, fmt.Sprintf("The variable %s refers to itself.", braces(first.Path[0]))).
			WithCode(CodeCycle).
			WithHint("Change the values so that they do not refer to each other in a loop.")
	case first.Kind == ProblemTooDeep:
		e = errs.New(errs.InvalidArgument, fmt.Sprintf("The variable %s nests other variables too deeply.", braces(first.Path[0]))).
			WithCode(CodeTooDeep).
			WithHint("Use fewer levels of variables inside variables.")
	case first.Kind == ProblemInvalidDynamic:
		e = errs.New(errs.InvalidArgument, fmt.Sprintf("The dynamic value %s is not valid: %s.", braces(first.Name), first.Message)).
			WithCode(CodeInvalidDynamic).
			WithHint("Fix its arguments; docs/templates.md lists the dynamic values" + escapeHint)
	case first.Kind == ProblemInvalidFilter:
		e = errs.New(errs.InvalidArgument, fmt.Sprintf("The filter %s is not valid: %s.", braces(first.Name), first.Message)).
			WithCode(CodeInvalidFilter).
			WithHint("Fix the filters; docs/templates.md lists them" + escapeHint)
	case first.Kind == ProblemFile:
		e = fileErr(first)
	case first.Kind == ProblemResponse:
		e = responseErr(first)
	case first.Kind == ProblemEmptyName:
		e = errs.New(errs.InvalidArgument, "A variable has no name.").
			WithCode(CodeEmptyName).
			WithHint("Type a name between the braces" + escapeHint)
	default:
		e = errs.New(errs.InvalidArgument, "The text is too large after its variables are replaced.").
			WithCode(CodeLimit).
			WithHint("Check for variables that repeat other variables many times.")
	}
	switch {
	case first.Kind == ProblemPrompt && len(unresolved) == 0:
		// details.prompts says what to ask for.
	case len(unresolved) > 0:
		e = e.WithDetail("names", strings.Join(unresolved, ","))
	case len(first.Path) > 0:
		e = e.WithDetail("path", strings.Join(first.Path, ","))
	}
	return e.WithDetail("problems", strconv.Itoa(count))
}

func braces(name string) string { return "{{" + name + "}}" }

func bracesList(names []string) string {
	const max = 5
	parts := make([]string, 0, max+1)
	for i, n := range names {
		if i == max {
			parts = append(parts, fmt.Sprintf("and %d more", len(names)-max))
			break
		}
		parts = append(parts, braces(n))
	}
	return strings.Join(parts, ", ")
}
