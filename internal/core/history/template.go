package history

import (
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Template is the request as its editor had it when it was sent (NAV-03):
// the {{variables}} as written, not resolved, so restoring the entry gives
// back the request and not one send of it. It holds the fields a restore
// writes: the method, URL, parameter and header rows and the body of an
// HTTP request, or the URL, method, header rows, query, variables and
// operation name of a GraphQL one. The auth, settings, assertions and
// examples are not kept. Secrets are masked by value like the rest of the
// entry (MaskTemplate). Entries of replays, and entries recorded before
// templates were, have none.
type Template struct {
	Method     string  `json:"method,omitempty"`
	URL        string  `json:"url"`
	Params     []Field `json:"params,omitempty"`
	PathParams []Field `json:"pathParams,omitempty"`
	Headers    []Field `json:"headers,omitempty"`
	// Body is an HTTP request's body; nil for GraphQL.
	Body *TemplateBody `json:"body,omitempty"`
	// Query, Variables and OperationName are GraphQL only.
	Query         string `json:"query,omitempty"`
	Variables     string `json:"variables,omitempty"`
	OperationName string `json:"operationName,omitempty"`
	// Clipped names the text fields cut at MaxBody bytes: "body", "query"
	// or "variables".
	Clipped []string `json:"clipped,omitempty"`
}

// Field is a row of a Template: a parameter, header, form field or
// multipart part.
type Field struct {
	Name        string `json:"name"`
	Value       string `json:"value"`
	Description string `json:"description,omitempty"`
	// File and ContentType are a multipart part's.
	File        string `json:"file,omitempty"`
	ContentType string `json:"contentType,omitempty"`
	Disabled    bool   `json:"disabled,omitempty"`
}

// TemplateBody is the body of a Template, as projectfile.Body has it.
type TemplateBody struct {
	Mode        string  `json:"mode"`
	Type        string  `json:"type,omitempty"`
	Text        string  `json:"text,omitempty"`
	Fields      []Field `json:"fields,omitempty"`
	Parts       []Field `json:"parts,omitempty"`
	Path        string  `json:"path,omitempty"`
	ContentType string  `json:"contentType,omitempty"`
}

// Template field names (Template.Clipped, Hidden.Section).
const (
	SectionURL        = "url"
	SectionParams     = "params"
	SectionPathParams = "pathParams"
	SectionHeaders    = "headers"
	SectionBody       = "body"
	SectionFields     = "bodyFields"
	SectionParts      = "bodyParts"
	SectionQuery      = "query"
	SectionVariables  = "variables"
)

// HTTPTemplate returns the template of HTTP request r, not masked yet.
func HTTPTemplate(r projectfile.HTTPRequest) *Template {
	b := r.Body
	t := &Template{
		Method:     r.Method,
		URL:        r.URL,
		Params:     fieldsOf(r.Params),
		PathParams: fieldsOf(r.PathParams),
		Headers:    fieldsOf(r.Headers),
		Body: &TemplateBody{
			Mode: b.Mode, Type: b.Type, Text: b.Text, Fields: fieldsOf(b.Fields),
			Path: b.Path, ContentType: b.ContentType,
		},
	}
	if t.Body.Mode == "" {
		t.Body.Mode = projectfile.BodyNone
	}
	for _, p := range b.Parts {
		t.Body.Parts = append(t.Body.Parts, Field{Name: p.Name, Value: p.Value, Description: p.Description, File: p.File, ContentType: p.ContentType, Disabled: p.Disabled})
	}
	return t
}

// GraphQLTemplate returns the template of GraphQL request g, not masked
// yet.
func GraphQLTemplate(g projectfile.GraphQLRequest) *Template {
	return &Template{
		Method:        g.Method,
		URL:           g.URL,
		Headers:       fieldsOf(g.Headers),
		Query:         g.Query,
		Variables:     g.Variables,
		OperationName: g.OperationName,
	}
}

func fieldsOf(kvs []projectfile.KeyValue) []Field {
	var out []Field
	for _, kv := range kvs {
		out = append(out, Field{Name: kv.Name, Value: kv.Value, Description: kv.Description, Disabled: kv.Disabled})
	}
	return out
}

// MaskTemplate masks the secrets m knows in t, in place, and clips its
// long texts to MaxBody bytes; masked reports whether m hid anything.
// Values are masked by value (MaskText), so a {{reference}} is kept as
// written: the value it stood for is what the send's secrets are. Only
// an Authorization or Proxy-Authorization row without a reference is
// masked by name, as the timeline does. A nil t is left alone.
func MaskTemplate(t *Template, m httpclient.Masker) (masked bool) {
	if t == nil {
		return false
	}
	text := func(s *string) {
		if m == nil || *s == "" {
			return
		}
		v := m.MaskText(*s)
		masked = masked || v != *s
		*s = v
	}
	rows := func(fs []Field, headers bool) {
		for i := range fs {
			f := &fs[i]
			text(&f.Name)
			if headers && m != nil && !strings.Contains(f.Value, "{{") && isCredentialHeader(f.Name) {
				v := m.MaskHeader(f.Name, f.Value)
				masked = masked || v != f.Value
				f.Value = v
				continue
			}
			text(&f.Value)
		}
	}
	text(&t.URL)
	rows(t.Params, false)
	rows(t.PathParams, false)
	rows(t.Headers, true)
	if b := t.Body; b != nil {
		text(&b.Text)
		rows(b.Fields, false)
		rows(b.Parts, false)
		if clipText(&b.Text) {
			t.Clipped = append(t.Clipped, SectionBody)
		}
	}
	text(&t.Query)
	text(&t.Variables)
	if clipText(&t.Query) {
		t.Clipped = append(t.Clipped, SectionQuery)
	}
	if clipText(&t.Variables) {
		t.Clipped = append(t.Clipped, SectionVariables)
	}
	return masked
}

// clipText cuts s to MaxBody bytes, on a character boundary; true when it
// did.
func clipText(s *string) bool {
	if len(*s) <= MaxBody {
		return false
	}
	*s = string(clipUTF8([]byte(*s), MaxBody))
	return true
}

// isCredentialHeader reports whether name is Authorization or
// Proxy-Authorization.
func isCredentialHeader(name string) bool {
	return strings.EqualFold(name, "Authorization") || strings.EqualFold(name, "Proxy-Authorization")
}
