package request

import (
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
)

// Methods (HTTP-01, HTTP-02): the method is sent exactly as typed, any
// RFC 9110 token. docs/http-client.md#methods lists the rules.

// Error codes of Method.
const (
	// CodeInvalidMethod: the method is empty or not an RFC 9110 token.
	CodeInvalidMethod = "request.invalid_method"
	// CodeMethodUnsupported: CONNECT, which opens a tunnel instead of
	// sending a request.
	CodeMethodUnsupported = "request.method_unsupported"
)

// WarnBodyOmitted is the code of the warning BodyForMethod returns when it
// drops the body of a HEAD or TRACE request.
const WarnBodyOmitted = "request.body_omitted"

// StandardMethods are the methods the editor's picker offers, in its order.
var StandardMethods = []string{"GET", "POST", "PUT", "PATCH", "DELETE", "QUERY", "HEAD", "OPTIONS"}

// registeredMethods are the methods whose upper-case spelling
// CanonicalMethod suggests: the standard ones and the other common
// registered methods.
var registeredMethods = append(append([]string(nil), StandardMethods...), "TRACE", "CONNECT")

// Warning is a problem that does not stop a send; the UI shows it next to
// the response.
type Warning struct {
	Code    string
	Message string
}

// Method checks the method of a request before it is sent. Any RFC 9110
// token (letters, digits and !#$%&'*+-.^_`|~) is valid and is sent as
// typed, case included: methods are case-sensitive, so "get" is a custom
// method, not GET. CONNECT, in that spelling, is not supported. Errors are
// *errs.Error with CodeInvalidMethod (InvalidArgument) or
// CodeMethodUnsupported (Unsupported).
func Method(m string) error {
	if !isToken(m) {
		msg := "The method " + strconv.Quote(m) + " is not valid."
		if m == "" {
			msg = "The request has no method."
		}
		return errs.New(errs.InvalidArgument, msg).
			WithCode(CodeInvalidMethod).
			WithHint("Use a method such as GET or POST; custom methods may contain letters, digits and !#$%&'*+-.^_`|~, with no spaces.")
	}
	if m == "CONNECT" {
		return errs.New(errs.Unsupported, "CONNECT requests are not supported.").
			WithCode(CodeMethodUnsupported).
			WithHint("To reach a server through a proxy, configure the proxy instead.")
	}
	return nil
}

// CanonicalMethod returns the registered method m is a different spelling
// of ("GET" for "get" or "Get"), or "" when m is spelled like one, or is no
// registered method. The editor uses the same rule to warn that such a
// method is sent as typed.
func CanonicalMethod(m string) string {
	for _, r := range registeredMethods {
		if m != r && strings.EqualFold(m, r) {
			return r
		}
	}
	return ""
}

// BodyForMethod returns the body to send with method: body itself, except
// for HEAD and TRACE (exact spelling), whose requests must not have content
// (RFC 9110 sections 9.3.2 and 9.3.8). Their body is dropped, with a
// WarnBodyOmitted warning when there was one. GET, DELETE, OPTIONS and
// custom methods send their body: RFC 9110 gives it no meaning there, but
// does not forbid it, and some APIs use it.
func BodyForMethod(method string, body httpclient.Body) (httpclient.Body, *Warning) {
	if body == nil || (method != "HEAD" && method != "TRACE") {
		return body, nil
	}
	return nil, &Warning{
		Code:    WarnBodyOmitted,
		Message: "The body was not sent: " + method + " requests cannot have one.",
	}
}
