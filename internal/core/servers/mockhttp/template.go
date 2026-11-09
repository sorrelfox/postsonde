package mockhttp

import (
	"errors"
	"fmt"
	"net/http"
	"slices"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// Templated responses (MOCK-06). A response with Templated set has its
// body and header values rendered for every request by the template
// engine (docs/templates.md), with one scope: the request, under the
// names request.method, request.path, request.params.NAME and so on
// (RequestNames). Dynamic values ({{$uuid}}, fake data, timestamps) and
// filter pipes work as in a request; the environments and the project's
// variables are not seen. See docs/mock-routes.md (Templates).

// MaxRenderedBytes bounds a templated body or header value after
// rendering; a longer one is a template error.
const MaxRenderedBytes = 16 << 20

// RequestScopeName is the Scope name of the request's values.
const RequestScopeName = "Request"

// RequestName is a name of the request scope, for completion lists.
type RequestName struct {
	// Name is the name as written between the braces. A name ending in
	// "." takes a parameter, header or JSON path after it
	// ("request.query." + "id").
	Name        string
	Description string
}

// requestNames are the request scope's names.
var requestNames = []RequestName{
	{"request.method", "The request method: GET, POST…"},
	{"request.path", "The request path, decoded, without the query: /users/42"},
	{"request.url", "The request target as sent: /users/42?full=1"},
	{"request.host", "The Host the request was sent to: localhost:8080"},
	{"request.body", "The request body as text (its first 1 MiB)"},
	{"request.body.", "A field of the request body read as JSON: request.body.user.name, request.body.items.0.id"},
	{"request.params.", "A path parameter of the route: request.params.id for /users/:id"},
	{"request.wildcard", "What ** or *name matched in the path: a/b.txt"},
	{"request.query.", "A query parameter (its first value): request.query.page"},
	{"request.headers.", "A request header (any letter case): request.headers.Authorization"},
}

// RequestNames returns the names a templated response can use from the
// request, in the order they are documented.
func RequestNames() []RequestName { return slices.Clone(requestNames) }

// requestScope is the template scope of a request's values. Values are
// escaped, so text sent by the client is never read as a template.
type requestScope struct {
	d *requestData
}

func (requestScope) Name() string { return RequestScopeName }

func (s requestScope) Lookup(name string) (template.Variable, bool) {
	v, ok := s.d.lookup(name)
	if !ok {
		return template.Variable{}, false
	}
	return template.Variable{Value: template.Escape(v)}, true
}

// lookup returns the value of a request.* name. A parameter, header or
// field the request does not have is "" (found); a name outside the
// request.* names is not found.
func (d *requestData) lookup(name string) (string, bool) {
	rest, ok := strings.CutPrefix(name, "request.")
	if !ok || strings.ContainsAny(rest, "| \t\r\n") {
		// A pipe ({{request.body | sha256}}) is left to the resolver,
		// which looks its input up again.
		return "", false
	}
	switch rest {
	case "method":
		return d.method, true
	case "path":
		return d.path, true
	case "url":
		return d.url, true
	case "host":
		return d.host, true
	case "body":
		return string(d.body), true
	case "wildcard":
		return d.wildcard, true
	}
	kind, key, ok := strings.Cut(rest, ".")
	if !ok || key == "" {
		return "", false
	}
	switch kind {
	case "params":
		for _, p := range d.params {
			if p.Name == key {
				return p.Value, true
			}
		}
		return "", true
	case "query":
		return d.query.Get(key), true
	case "headers":
		if http.CanonicalHeaderKey(key) == "Host" {
			return d.host, true
		}
		return d.header.Get(key), true
	case "body":
		path := strings.Split(key, ".")
		if slices.Contains(path, "") {
			return "", false
		}
		v, err := template.Select("request.body", string(d.body), path)
		if err != nil {
			return "", true // not JSON, or no such field
		}
		return v, true
	}
	return "", false
}

// renderError is a templated response that cannot be rendered.
type renderError struct {
	// where is "the body" or "the header X-Name".
	where string
	err   error
}

func (e *renderError) Error() string {
	msg := e.err.Error()
	if ee, ok := errors.AsType[*errs.Error](e.err); ok {
		msg = ee.Message
	}
	return fmt.Sprintf("The template in %s cannot be rendered: %s", e.where, msg)
}

// render is the body and headers of the response for the request: as
// written, or rendered with the request's values when the response is
// templated. The headers must not be changed.
func (p *reply) render(d *requestData) ([]byte, http.Header, error) {
	if !p.templated {
		return p.body, p.header, nil
	}
	r := template.Resolver{Scopes: template.Chain{requestScope{d}}, MaxLength: MaxRenderedBytes}
	var body []byte
	if p.body != nil {
		res := r.Render(string(p.body))
		if err := res.Err(); err != nil {
			return nil, nil, &renderError{where: "the body", err: err}
		}
		body = []byte(res.Text)
	}
	header := make(http.Header, len(p.header))
	keys := make([]string, 0, len(p.header))
	for k := range p.header {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	for _, k := range keys {
		vals := make([]string, len(p.header[k]))
		for i, v := range p.header[k] {
			if !strings.Contains(v, "{{") {
				vals[i] = v
				continue
			}
			res := r.Render(v)
			if err := res.Err(); err != nil {
				return nil, nil, &renderError{where: "the header " + k, err: err}
			}
			if !validHeaderValue(res.Text) {
				return nil, nil, &renderError{where: "the header " + k, err: errors.New("The value has a line break or control character after rendering.")}
			}
			vals[i] = res.Text
		}
		header[k] = vals
	}
	return body, header, nil
}
