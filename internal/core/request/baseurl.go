package request

import (
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// BaseURLVariable is the variable a relative request URL is sent to
// (COL-08): a URL that starts with one "/" goes to {{baseUrl}} followed by
// that path. It is an ordinary variable, so each environment defines its
// own server (and the global variables, the base environment, a default),
// a collection or folder can override it, a local value points this
// computer elsewhere, and switching environments switches servers.
const BaseURLVariable = "baseUrl"

// CodeInvalidBaseURL: a relative URL's {{baseUrl}} is not an absolute URL
// without a query or fragment (kind invalid_argument). Details: field
// ("url").
const CodeInvalidBaseURL = "request.invalid_base_url"

// IsRelativeURL reports whether url, rendered and trimmed, is relative:
// a path that starts with exactly one "/" ("/users", "/users?page=2").
// "//host/path" (a network path) and URLs without a leading "/" are not.
func IsRelativeURL(url string) bool {
	return strings.HasPrefix(url, "/") && !strings.HasPrefix(url, "//")
}

// JoinBaseURL returns base followed by path, with one "/" between them:
// the trailing slashes of base are dropped, so "https://h/api/" and "/v1"
// give "https://h/api/v1". It is a join, not RFC 3986 resolution: a path
// in the base is kept.
func JoinBaseURL(base, path string) string {
	return strings.TrimRight(base, "/") + path
}

// ExpandURL is a request URL as it is sent: raw with its {{variables}}
// rendered by expand (nil sends it as typed) and surrounding spaces
// trimmed, then, when that is relative (IsRelativeURL), joined to the
// rendered {{baseUrl}} (JoinBaseURL). The base is rendered with the same
// expand, so an undefined baseUrl is an undefined variable like any other
// (left as written where expand allows it: "{{baseUrl}}/users"), a secret
// one is masked, and a snippet that keeps references keeps it as
// {{baseUrl}}. A base that renders to anything but an http(s) URL without
// a query or fragment is refused with CodeInvalidBaseURL.
func ExpandURL(raw string, expand Expand) (string, error) {
	url := raw
	if expand != nil {
		var err error
		if url, err = expand(raw); err != nil {
			return "", err
		}
	}
	url = strings.TrimSpace(url)
	if expand == nil || !IsRelativeURL(url) {
		return url, nil
	}
	base, err := expand("{{" + BaseURLVariable + "}}")
	if err != nil {
		return "", err
	}
	base = strings.TrimSpace(base)
	// A reference kept as written (undefined, or a templated snippet) is
	// left for the caller to report or show.
	if !strings.Contains(base, "{{") && !validBaseURL(base) {
		return "", errs.New(errs.InvalidArgument, "The URL is relative, and {{"+BaseURLVariable+"}} is not an absolute URL to send it to.").
			WithCode(CodeInvalidBaseURL).
			WithHint("Set "+BaseURLVariable+" to the server's address, such as https://api.example.com or https://api.example.com/v1, without a query.").
			WithDetail(DetailField, "url")
	}
	return JoinBaseURL(base, url), nil
}

// validBaseURL reports whether base can take a path: http:// or https://,
// a host, and no query or fragment.
func validBaseURL(base string) bool {
	scheme, rest, ok := strings.Cut(base, "://")
	if !ok || (!strings.EqualFold(scheme, "http") && !strings.EqualFold(scheme, "https")) {
		return false
	}
	host, _, _ := strings.Cut(rest, "/")
	return host != "" && !strings.ContainsAny(rest, "?#")
}
