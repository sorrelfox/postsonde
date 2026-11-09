package http

import (
	"context"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// ResponseValueDTO is what the editor shows of a {{$response}} reference
// (DYN-06) when it is hovered: the request it reads by name, the value its
// stored last response has, and whether the next send sends the request
// first.
type ResponseValueDTO struct {
	// Name is the reference as written, without braces.
	Name string `json:"name"`
	// RequestID is the referenced request; RequestName its name, "" when
	// it is not a request of the project.
	RequestID   string `json:"requestId"`
	RequestName string `json:"requestName"`
	// Resolved is set when the stored response has the value.
	Resolved bool `json:"resolved"`
	// Value is the value; "" when Secret.
	Value  string `json:"value"`
	Secret bool   `json:"secret"`
	// ReceivedAt is when the stored response arrived (RFC 3339, UTC), ""
	// without one; Status is its status code.
	ReceivedAt string `json:"receivedAt"`
	Status     int    `json:"status"`
	// FromHistory is set when the response is the history's copy (not
	// sent since PostSonde started).
	FromHistory bool `json:"fromHistory"`
	// Send is the reference's send= ("never", "missing", "stale",
	// "always"), MaxAgeMs its age for "stale".
	Send     string `json:"send"`
	MaxAgeMs int64  `json:"maxAgeMs"`
	// SendFirst is set when the next send of the request sends the
	// referenced request first.
	SendFirst bool `json:"sendFirst"`
	// Problem says why there is no value; Code is its error code.
	Problem string `json:"problem"`
	Code    string `json:"code"`
}

// ResponseValue returns what reference name (as written between the
// braces, "$response req_… jsonpath $.token") of request id reads from the
// stored last response of the request it names, for the editor's hover.
// It never sends anything. A reference that is not valid has its Problem
// (template.invalid_dynamic); a name that is not a {{$response}} reference
// fails with template.invalid_dynamic.
func (s *HTTPService) ResponseValue(ctx context.Context, id, name string) (ResponseValueDTO, error) {
	if err := ctx.Err(); err != nil {
		return ResponseValueDTO{}, errs.Wrap(err, errs.KindOf(err), "Reading the response was interrupted.")
	}
	rr, ok, perr := template.ParseResponse(name)
	if !ok {
		return ResponseValueDTO{}, errs.New(errs.InvalidArgument, "{{"+name+"}} is not a response reference.").WithCode(template.CodeInvalidDynamic)
	}
	out := ResponseValueDTO{Name: name, RequestID: rr.RequestID, Send: string(rr.Send), MaxAgeMs: rr.MaxAge.Milliseconds()}
	if perr != nil {
		out.Problem, out.Code = "The reference is not valid: "+perr.Error()+".", template.CodeInvalidDynamic
		out.RequestID = ""
		return out, nil
	}
	root := ""
	if s.load != nil {
		if src, err := s.load(ctx, id); err == nil {
			root = workspaceRoot(src)
		}
	}
	if root == "" && s.loadVariables != nil {
		if src, err := s.loadVariables(ctx, id); err == nil && src.Workspace != nil {
			root = src.Workspace.Root
		}
	}
	r := &responseReader{s: s, ctx: ctx, root: root, chain: []string{id}, names: []string{""},
		flow: &responseFlow{mode: responsesStored}, preview: true, now: s.clock()}
	if n, err := r.referenced(rr.RequestID); err == nil {
		out.RequestName = n
	}
	last := s.lastResponse(root, rr.RequestID)
	if last != nil {
		out.ReceivedAt, out.Status, out.FromHistory = last.at.UTC().Format(time.RFC3339), last.status, last.fromHistory
	}
	out.SendFirst = out.RequestName != "" && r.needsSending(rr, last)
	v, err := r.ReadResponse(rr)
	if err != nil {
		out.Problem, out.Code = errMessage(err), codeOf(err)
		return out, nil
	}
	out.Resolved, out.Secret = true, rr.Secret || v.Secret
	if !out.Secret {
		out.Value = v.Value
	}
	return out, nil
}
