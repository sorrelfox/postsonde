package template

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Response references (DYN-06): {{$response REQUEST SOURCE [EXPR]
// [send=WHEN] [secret]}} is a value taken from the last response of
// another request of the project, such as a login's token. The resolver
// does not know where responses are kept, nor how to send a request:
// Resolver.Responses does (the app reads its last responses and the
// request history, and sends the request first when the reference asks
// for it). See docs/templates.md#response-references.

// ResponseName is the dynamic value name of a response reference.
const ResponseName = "$response"

// ResponseScope is Ref.Scope of a reference read from a response:
// "Response", or "Response: <request name>" when the reader names the
// request.
const ResponseScope = "Response"

// ResponseSource says which part of the response a reference reads.
type ResponseSource string

// Sources of a response reference: those of extraction rules
// (internal/core/extract), and the whole body.
const (
	// ResponseJSONPath: an RFC 9535 JSONPath of the body.
	ResponseJSONPath ResponseSource = "jsonpath"
	// ResponseXPath: an XPath 1.0 expression of the body.
	ResponseXPath ResponseSource = "xpath"
	// ResponseRegex: an RE2 regular expression on the body: its first
	// capture group, or the whole match.
	ResponseRegex ResponseSource = "regex"
	// ResponseHeader: a header by name, any case, its values joined
	// with ", ".
	ResponseHeader ResponseSource = "header"
	// ResponseCookie: a cookie of the Set-Cookie headers, by name.
	ResponseCookie ResponseSource = "cookie"
	// ResponseStatus: the status code ("200"); no expression.
	ResponseStatus ResponseSource = "status"
	// ResponseBody: the whole body as text; no expression.
	ResponseBody ResponseSource = "body"
)

// ResponseSources lists the sources in the order completion offers them.
var ResponseSources = []ResponseSource{ResponseJSONPath, ResponseHeader, ResponseStatus, ResponseBody, ResponseCookie, ResponseXPath, ResponseRegex}

// TakesExpr reports whether the source needs an expression (a JSONPath,
// a header name...).
func (s ResponseSource) TakesExpr() bool { return s != ResponseStatus && s != ResponseBody }

func knownResponseSource(s ResponseSource) bool {
	for _, k := range ResponseSources {
		if s == k {
			return true
		}
	}
	return false
}

// SendWhen says when the referenced request is sent before its response
// is read.
type SendWhen string

const (
	// SendNever: the stored response is read as it is; the request is
	// never sent for the reference (the default).
	SendNever SendWhen = "never"
	// SendMissing: the request is sent first when it has no response
	// yet.
	SendMissing SendWhen = "missing"
	// SendStale: the request is sent first when it has no response, or
	// when its response is older than ResponseRef.MaxAge.
	SendStale SendWhen = "stale"
	// SendAlways: the request is sent first every time.
	SendAlways SendWhen = "always"
)

// ResponseRef is a parsed {{$response}} reference.
type ResponseRef struct {
	// RequestID is the referenced request's ID ("req_…"), as written.
	RequestID string
	Source    ResponseSource
	// Expr is the JSONPath, header name...; "" for status and body.
	Expr string
	// Send says when the request is sent first; MaxAge is the age above
	// which a response is stale (SendStale only).
	Send   SendWhen
	MaxAge time.Duration
	// Secret masks the value like a secret variable's.
	Secret bool
}

// ResponseValue is the value a ResponseReader found.
type ResponseValue struct {
	Value string
	// Secret is set when the value holds a secret of the response (a
	// value the referenced send masked); the reference's own secret flag
	// adds to it.
	Secret bool
	// Request names the referenced request ("Login"), for Ref.Scope; ""
	// when unknown.
	Request string
}

// ResponseReader finds the value of a {{$response}} reference, sending
// the referenced request first when ref.Send asks for it and the reader
// can. Its error is shown as it is, so it should be an *errs.Error with a
// message for the user; it must guard against requests that refer to
// each other's responses in a loop.
type ResponseReader interface {
	ReadResponse(ref ResponseRef) (ResponseValue, error)
}

// ProblemResponse: a {{$response}} reference whose value cannot be read.
// Problem.Message says why and Problem.Cause is the reader's error,
// which Result.Err returns. The reference is left as written.
const ProblemResponse ProblemKind = "response"

// CodeResponseUnavailable: this kind of request does not read other
// requests' responses (Resolver.Responses is nil; kind unsupported).
const CodeResponseUnavailable = "template.response_unavailable"

// ErrResponsesUnavailable is the Problem.Cause of a {{$response}}
// reference rendered without a Resolver.Responses.
var ErrResponsesUnavailable = errs.New(errs.Unsupported, "Values from another request's response are read only by HTTP, GraphQL and SSE requests.").
	WithCode(CodeResponseUnavailable).
	WithHint("Extract the value into a runtime variable instead (the request's Extract tab), and use the variable here.")

func init() {
	// Listed with the dynamic values (completion, the editor's chips), but
	// resolved by resolveResponse: IsDynamic and the renderer check
	// ParseResponse first.
	dynamicFuncs[ResponseName] = func(*dynamicGen, []string) (string, error) {
		return "", errors.New("it is read from the request's last response")
	}
	dynamicDescriptions[ResponseName] = "A value from another request's last response: {{$response REQUEST jsonpath $.token}}, header NAME, status or body; send=missing, 10m or always sends it first"
}

// responseArgsErr says how a response reference is written.
var responseArgsErr = errors.New(`write {{$response REQUEST SOURCE EXPR}}, as in {{$response req_0123456789abcdef jsonpath $.token}}, with send=missing, send=10m or send=always and secret after it if needed`)

// ParseResponse reads a {{$response}} reference, as written between the
// braces (Segment.Name): ok is false when name is not a response
// reference at all; err says why its arguments are not valid. Arguments
// are separated by whitespace, one with spaces in double quotes: the
// request's ID, the source, its expression (not for status and body),
// then in any order send=WHEN and the bare word secret. An unquoted "|" is
// not part of an argument, so {{$response r body | base64}} is a pipe.
func ParseResponse(name string) (ref ResponseRef, ok bool, err error) {
	rest, found := strings.CutPrefix(name, ResponseName)
	if !found || (rest != "" && strings.IndexByte(spaces, rest[0]) < 0) {
		return ResponseRef{}, false, nil
	}
	toks, valid := splitTokens(name)
	if !valid {
		return ResponseRef{}, true, errors.New("a quote is not closed, or has text right after it")
	}
	args := toks[1:]
	for _, a := range args {
		if !a.quoted && strings.Contains(a.text, "|") {
			return ResponseRef{}, true, responseArgsErr
		}
	}
	if len(args) < 2 {
		return ResponseRef{}, true, responseArgsErr
	}
	ref.RequestID = args[0].text
	if strings.TrimSpace(ref.RequestID) == "" {
		return ResponseRef{}, true, errors.New("the request is empty")
	}
	ref.Source = ResponseSource(args[1].text)
	if args[1].quoted || !knownResponseSource(ref.Source) {
		return ResponseRef{}, true, fmt.Errorf("%q is not a source: use jsonpath, header, status, body, cookie, xpath or regex", args[1].text)
	}
	args = args[2:]
	if ref.Source.TakesExpr() {
		if len(args) == 0 || strings.TrimSpace(args[0].text) == "" {
			return ResponseRef{}, true, fmt.Errorf("the %s source needs an expression after it", ref.Source)
		}
		ref.Expr, args = args[0].text, args[1:]
	}
	ref.Send = SendNever
	seenSend := false
	for _, a := range args {
		switch {
		case !a.quoted && a.text == "secret" && !ref.Secret:
			ref.Secret = true
		case !a.quoted && strings.HasPrefix(a.text, "send=") && !seenSend:
			seenSend = true
			when, age, err := parseSendWhen(strings.TrimPrefix(a.text, "send="))
			if err != nil {
				return ResponseRef{}, true, err
			}
			ref.Send, ref.MaxAge = when, age
		default:
			return ResponseRef{}, true, responseArgsErr
		}
	}
	return ref, true, nil
}

// MaxResponseAge bounds the age of send=AGE: 365 days.
const MaxResponseAge = 365 * 24 * time.Hour

// parseSendWhen reads the value of send=: never, missing, always, or an
// age of one or more terms such as 30s, 10m, 1h30m, 2d.
func parseSendWhen(v string) (SendWhen, time.Duration, error) {
	switch SendWhen(v) {
	case SendNever, SendMissing, SendAlways:
		return SendWhen(v), 0, nil
	}
	age, ok := parseAge(v)
	if !ok {
		return "", 0, fmt.Errorf("send=%s is not valid: use send=never, send=missing, send=always or an age such as send=10m (s, m, h, d)", v)
	}
	if age <= 0 || age > MaxResponseAge {
		return "", 0, fmt.Errorf("send=%s is not valid: the age must be more than 0 and at most 365 days", v)
	}
	return SendStale, age, nil
}

// parseAge reads terms of digits and a unit (s, m, h, d), at most 6
// digits each: "90s", "1h30m".
func parseAge(v string) (time.Duration, bool) {
	if v == "" {
		return 0, false
	}
	var total time.Duration
	for v != "" {
		n := 0
		for n < len(v) && v[n] >= '0' && v[n] <= '9' {
			n++
		}
		if n == 0 || n > 6 || n == len(v) {
			return 0, false
		}
		num, _ := strconv.Atoi(v[:n])
		var unit time.Duration
		switch v[n] {
		case 's':
			unit = time.Second
		case 'm':
			unit = time.Minute
		case 'h':
			unit = time.Hour
		case 'd':
			unit = 24 * time.Hour
		default:
			return 0, false
		}
		total += time.Duration(num) * unit
		v = v[n+1:]
	}
	return total, true
}

// FormatAge writes an age as send= accepts it, in its largest exact
// units: "10m", "1h30m", "2d".
func FormatAge(d time.Duration) string {
	if d <= 0 {
		return "0s"
	}
	var b strings.Builder
	for _, u := range []struct {
		d    time.Duration
		name string
	}{{24 * time.Hour, "d"}, {time.Hour, "h"}, {time.Minute, "m"}, {time.Second, "s"}} {
		if n := d / u.d; n > 0 {
			b.WriteString(strconv.FormatInt(int64(n), 10) + u.name)
			d -= n * u.d
		}
	}
	if b.Len() == 0 {
		return "0s"
	}
	return b.String()
}

// resolveResponse resolves a {{$response}} reference with
// Resolver.Responses.
func (st *renderer) resolveResponse(seg Segment, ref Ref, rr ResponseRef, err error) Ref {
	if err != nil {
		st.reportMessage(ProblemInvalidDynamic, seg.Name, err.Error(), seg.Start, seg.End)
		return ref
	}
	if st.responses == nil {
		st.reportCause(ProblemResponse, seg.Name, ErrResponsesUnavailable, seg.Start, seg.End)
		return ref
	}
	v, err := st.responses.ReadResponse(rr)
	if err != nil {
		st.reportCause(ProblemResponse, seg.Name, err, seg.Start, seg.End)
		return ref
	}
	scope := ResponseScope
	if v.Request != "" {
		scope += ": " + v.Request
	}
	// Used as is: never parsed for variables, like a file's text.
	ref.Resolved, ref.Scope, ref.Secret, ref.Value = true, scope, rr.Secret || v.Secret, v.Value
	return ref
}

// IsResponseScope reports whether a Ref.Scope is that of a response
// reference.
func IsResponseScope(scope string) bool {
	return scope == ResponseScope || strings.HasPrefix(scope, ResponseScope+": ")
}

// responseErr is the error of Result.Err for a ProblemResponse.
func responseErr(p *Problem) *errs.Error {
	var e *errs.Error
	if !errors.As(p.Cause, &e) {
		e = errs.Wrap(p.Cause, errs.KindOf(p.Cause), "The value of "+braces(p.Name)+" cannot be read from the response.")
	}
	return e.WithDetail("reference", p.Name)
}
