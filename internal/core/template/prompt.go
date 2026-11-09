package template

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Prompts at send time (DYN-07): {{$prompt LABEL [DEFAULT] [secret]}} is
// a value the user types when the request is sent. The resolver never
// asks: it takes the answers from Resolver.Prompts, by label, and reports
// a ProblemPrompt for a label without one, so the sender can ask the user
// and render again with the answers (docs/templates.md#prompts).

// PromptName is the dynamic value name of a prompt.
const PromptName = "$prompt"

// PromptScope is Ref.Scope of a reference answered from Resolver.Prompts.
const PromptScope = "Prompt"

// DefaultPromptLabel is the label of {{$prompt}} written without one.
const DefaultPromptLabel = "Value"

// CodePrompt: the text has {{$prompt}} references without an answer in
// Resolver.Prompts. Details: prompts, a JSON array of Prompt, one per
// label, in the order met.
const CodePrompt = "template.prompt"

// ProblemPrompt: a {{$prompt}} reference whose label has no answer in
// Resolver.Prompts. The reference is left as written. Problem.Name is the
// reference as written; ParsePrompt reads its label.
const ProblemPrompt ProblemKind = "prompt"

// Prompt is one value asked for at send time.
type Prompt struct {
	// Label names the value in the dialog, and is its key in
	// Resolver.Prompts: references with the same label share one answer.
	Label string `json:"label"`
	// Default fills the field when the dialog opens; "" for none.
	Default string `json:"default,omitempty"`
	// Secret masks the field, and the value in the timeline and history
	// like a secret variable's.
	Secret bool `json:"secret,omitempty"`
}

func init() {
	// Listed with the dynamic values (completion, the editor's chips), but
	// resolved by resolvePrompt: IsDynamic and the renderer check
	// ParsePrompt first, as the arguments' quotes matter.
	dynamicFuncs[PromptName] = func(*dynamicGen, []string) (string, error) {
		return "", errors.New("it is answered when the request is sent")
	}
	dynamicDescriptions[PromptName] = "Asks for a value when the request is sent: {{$prompt LABEL [DEFAULT] [secret]}}"
}

// promptArgsErr says how a prompt is written.
var promptArgsErr = errors.New(`write {{$prompt LABEL}}, with an optional default and the word secret: {{$prompt "API token" "" secret}}`)

// ParsePrompt reads a {{$prompt}} reference, as written between the braces
// (Segment.Name): ok is false when name is not a prompt at all; err says
// why its arguments are not valid. Arguments are separated by whitespace,
// and one with spaces is written in double quotes. The first is the label
// (DefaultPromptLabel without one), then an optional default, and the
// bare word secret anywhere after the label ("secret" in quotes is a
// default).
func ParsePrompt(name string) (p Prompt, ok bool, err error) {
	rest, found := strings.CutPrefix(name, PromptName)
	if !found || (rest != "" && strings.IndexByte(spaces, rest[0]) < 0) {
		return Prompt{}, false, nil
	}
	toks, valid := splitTokens(name)
	if !valid {
		return Prompt{}, true, errors.New("a quote is not closed, or has text right after it")
	}
	args := toks[1:]
	p.Label = DefaultPromptLabel
	if len(args) == 0 {
		return p, true, nil
	}
	if strings.TrimSpace(args[0].text) == "" {
		return Prompt{}, true, errors.New("the label is empty")
	}
	p.Label = args[0].text
	hasDefault := false
	for _, a := range args[1:] {
		switch {
		case !a.quoted && a.text == "secret":
			if p.Secret {
				return Prompt{}, true, promptArgsErr
			}
			p.Secret = true
		case !hasDefault:
			hasDefault, p.Default = true, a.text
		default:
			return Prompt{}, true, promptArgsErr
		}
	}
	return p, true, nil
}

// resolvePrompt resolves a {{$prompt}} reference from the answers.
func (st *renderer) resolvePrompt(seg Segment, ref Ref, p Prompt, err error) Ref {
	if err != nil {
		st.reportMessage(ProblemInvalidDynamic, seg.Name, err.Error(), seg.Start, seg.End)
		return ref
	}
	v, answered := st.prompts[p.Label]
	if !answered {
		st.report(ProblemPrompt, seg.Name, seg.Start, seg.End)
		return ref
	}
	// Typed for this send only: kept out of saved files by construction,
	// and masked like a secret when marked secret.
	ref.Resolved, ref.Scope, ref.Secret, ref.Value = true, PromptScope, p.Secret, v
	return ref
}

// PromptsOf returns the prompts of the ProblemPrompt problems, one per
// label in the order met: a label marked secret anywhere is secret, and
// the first non-empty default wins.
func PromptsOf(problems []Problem) []Prompt {
	var out []Prompt
	index := map[string]int{}
	for _, pr := range problems {
		if pr.Kind != ProblemPrompt {
			continue
		}
		p, ok, err := ParsePrompt(pr.Name)
		if !ok || err != nil {
			continue
		}
		i, seen := index[p.Label]
		if !seen {
			index[p.Label] = len(out)
			out = append(out, p)
			continue
		}
		out[i].Secret = out[i].Secret || p.Secret
		if out[i].Default == "" {
			out[i].Default = p.Default
		}
	}
	return out
}

// promptErr is the template.prompt error of Result.Err.
func promptErr(prompts []Prompt) *errs.Error {
	var msg string
	if len(prompts) == 1 {
		msg = fmt.Sprintf("The value %q is asked for when the request is sent.", prompts[0].Label)
	} else {
		labels := make([]string, len(prompts))
		for i, p := range prompts {
			labels[i] = fmt.Sprintf("%q", p.Label)
		}
		msg = fmt.Sprintf("%d values are asked for when the request is sent: %s.", len(prompts), strings.Join(labels, ", "))
	}
	e := errs.New(errs.InvalidArgument, msg).
		WithCode(CodePrompt).
		WithHint("Values are asked for when an HTTP or GraphQL request is sent, not by streams, subscriptions or connections; use a variable there instead.")
	if b, err := json.Marshal(prompts); err == nil {
		e = e.WithDetail("prompts", string(b))
	}
	return e
}
