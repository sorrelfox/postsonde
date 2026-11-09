package history

import (
	"encoding/json"
	"net/url"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Reasons a value of a Restored request is not what was sent
// (Hidden.Reason).
const (
	// HiddenMasked: the value held a secret, masked in the history. The
	// masks are taken out, so what is left of it is kept.
	HiddenMasked = "masked"
	// HiddenClipped: the history kept only the first MaxBody bytes of it,
	// or none (Entry.Trimmed).
	HiddenClipped = "clipped"
	// HiddenBinary: a body that is not text, which a raw body cannot hold.
	HiddenBinary = "binary"
)

// Hidden is a value of a Restored request that is not restored as it
// was: its section (SectionURL, SectionHeaders...), the row's name for a
// row, and why.
type Hidden struct {
	Section string `json:"section"`
	Name    string `json:"name,omitempty"`
	Reason  string `json:"reason"`
}

// Restored is a history entry as a request to open again (NAV-03): the
// fields of a Template, no masked value in them.
type Restored struct {
	// Kind is KindHTTP or KindGraphQL; a replay is restored as HTTP.
	Kind string
	// Templated is set when the fields come from the entry's Template,
	// {{variables}} as written; else they are the request as sent, with
	// the values the variables had and without the auth's fields.
	Templated bool
	Template
	// Hidden lists the values that are not restored as they were.
	Hidden []Hidden
}

// Restore returns entry e as a request to open again. The fields come from
// the entry's Template when it has one, else from the request as sent
// (Templated false), where the fields the client or the auth wrote are
// left out (Host, Content-Length, Authorization, cookies...). m masks
// them again, so a secret known now but not when the entry was recorded
// is not restored either (as Example does); a nil m masks nothing more.
// Then every mask is taken out: a masked secret is never written back as
// "••••••", and Hidden says where one was.
func Restore(e Entry, m httpclient.Masker) Restored {
	r := Restored{Kind: e.Kind}
	if r.Kind != KindGraphQL {
		r.Kind = KindHTTP
	}
	if e.Template != nil {
		r.Templated = true
		r.Template = e.Template.Clone()
		for _, c := range r.Template.Clipped {
			r.Hidden = append(r.Hidden, Hidden{Section: c, Reason: HiddenClipped})
		}
		r.Template.Clipped = nil
	} else {
		r.Template, r.Hidden = sentTemplate(e, r.Kind)
	}
	if m != nil {
		MaskTemplate(&r.Template, m)
		r.Template.Clipped = nil
	}
	r.Hidden = append(r.Hidden, unmask(&r.Template)...)
	return r
}

// Clone returns a copy of t that shares none of its rows.
func (t Template) Clone() Template {
	clone := func(fs []Field) []Field { return append([]Field(nil), fs...) }
	t.Params, t.PathParams, t.Headers = clone(t.Params), clone(t.PathParams), clone(t.Headers)
	if t.Body != nil {
		b := *t.Body
		b.Fields, b.Parts = clone(b.Fields), clone(b.Parts)
		t.Body = &b
	}
	t.Clipped = append([]string(nil), t.Clipped...)
	return t
}

// sentHeaders are the header fields of a request as sent that are not
// restored: the client, the transport, the cookie jar or the auth wrote
// them, not the request's rows.
var sentHeaders = map[string]bool{
	"host": true, "content-length": true, "transfer-encoding": true, "connection": true,
	"accept-encoding": true, "cookie": true, "authorization": true, "proxy-authorization": true,
	"te": true, "upgrade": true, "keep-alive": true,
}

// sentTemplate makes a template of the request as sent, for an entry
// without one.
func sentTemplate(e Entry, kind string) (Template, []Hidden) {
	q := e.Request
	t := Template{Method: q.Method, URL: q.URL}
	var hidden []Hidden
	for _, h := range q.Headers {
		name := strings.ToLower(h.Name)
		if strings.HasPrefix(name, ":") || sentHeaders[name] || (name == "user-agent" && strings.HasPrefix(h.Value, "PostSonde")) {
			continue
		}
		t.Headers = append(t.Headers, Field{Name: h.Name, Value: h.Value})
	}
	clipped := q.Body.Clipped || e.Trimmed && q.Body.Size > 0
	if kind == KindGraphQL {
		graphQLFromSent(&t, q)
		if t.Method != "GET" {
			// The editor's method: "" is POST with a JSON body.
			t.Method = ""
		}
		if clipped {
			hidden = append(hidden, Hidden{Section: SectionQuery, Reason: HiddenClipped})
		}
		return t, hidden
	}
	t.Body = &TemplateBody{Mode: projectfile.BodyNone}
	switch {
	case q.Body.Base64:
		hidden = append(hidden, Hidden{Section: SectionBody, Reason: HiddenBinary})
	case q.Body.Text != "" || clipped:
		t.Body = &TemplateBody{Mode: projectfile.BodyRaw, Type: rawType(headerValue(q.Headers, "Content-Type")), Text: q.Body.Text}
		if clipped {
			hidden = append(hidden, Hidden{Section: SectionBody, Reason: HiddenClipped})
		}
	}
	return t, hidden
}

// graphQLFromSent fills the query, variables and operation name of t from
// a GraphQL request as sent: a JSON body, or the URL's query string of a
// GET, which is then taken out of the URL.
func graphQLFromSent(t *Template, q RequestSnapshot) {
	var payload struct {
		Query         string          `json:"query"`
		Variables     json.RawMessage `json:"variables"`
		OperationName string          `json:"operationName"`
	}
	if !q.Body.Base64 && json.Unmarshal([]byte(q.Body.Text), &payload) == nil {
		t.Query, t.OperationName = payload.Query, payload.OperationName
		if v := string(payload.Variables); v != "" && v != "null" {
			t.Variables = v
		}
		return
	}
	u, err := url.Parse(q.URL)
	if err != nil {
		return
	}
	values := u.Query()
	if !values.Has("query") {
		return
	}
	t.Query, t.Variables, t.OperationName = values.Get("query"), values.Get("variables"), values.Get("operationName")
	u.RawQuery = ""
	t.URL = u.String()
}

// unmask takes every Mask out of t and returns where it found one.
func unmask(t *Template) []Hidden {
	var hidden []Hidden
	text := func(s *string, section string) {
		if strings.Contains(*s, httpclient.Mask) {
			*s = strings.ReplaceAll(*s, httpclient.Mask, "")
			hidden = append(hidden, Hidden{Section: section, Reason: HiddenMasked})
		}
	}
	rows := func(fs []Field, section string) {
		for i := range fs {
			f := &fs[i]
			if strings.Contains(f.Name, httpclient.Mask) || strings.Contains(f.Value, httpclient.Mask) {
				f.Name = strings.ReplaceAll(f.Name, httpclient.Mask, "")
				f.Value = strings.ReplaceAll(f.Value, httpclient.Mask, "")
				hidden = append(hidden, Hidden{Section: section, Name: f.Name, Reason: HiddenMasked})
			}
		}
	}
	text(&t.URL, SectionURL)
	rows(t.Params, SectionParams)
	rows(t.PathParams, SectionPathParams)
	rows(t.Headers, SectionHeaders)
	if b := t.Body; b != nil {
		text(&b.Text, SectionBody)
		text(&b.Path, SectionBody)
		rows(b.Fields, SectionFields)
		rows(b.Parts, SectionParts)
	}
	text(&t.Query, SectionQuery)
	text(&t.Variables, SectionVariables)
	text(&t.OperationName, SectionQuery)
	return hidden
}
