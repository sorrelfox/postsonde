package collection

import (
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Response examples (HTTP-38, docs/file-format.md#response-examples): the
// responses saved with an http or graphql request. They travel in the
// RequestDTO like the other members, so saving, renaming and deleting one
// is an ordinary edit of the draft (auto-saved, undoable, versioned across
// windows). HistoryService.Example makes a new one from a history entry,
// secrets masked.

// ResponseExampleDTO is a response saved with a request.
type ResponseExampleDTO struct {
	Name string `json:"name"`
	// SavedAt is when it was saved, RFC 3339; "" when not known.
	SavedAt string `json:"savedAt"`
	// Request is the request as sent; null when not known.
	Request *ExampleRequestDTO `json:"request"`
	// StatusCode and StatusText are the status ("Created").
	StatusCode int    `json:"statusCode"`
	StatusText string `json:"statusText"`
	// Headers are the response's header fields in the order received.
	Headers []KeyValueDTO `json:"headers"`
	// Body is the body as text, or Base64 when BodyEncoding is "base64".
	Body         string `json:"body"`
	BodyEncoding string `json:"bodyEncoding"`
	// BodyClipped is set when Body holds only the start of the body, of
	// BodySize bytes.
	BodyClipped bool   `json:"bodyClipped"`
	BodySize    int64  `json:"bodySize"`
	Extra       string `json:"extra"`
}

// ExampleRequestDTO is the request of a response example, as sent.
type ExampleRequestDTO struct {
	Method  string        `json:"method"`
	URL     string        `json:"url"`
	Headers []KeyValueDTO `json:"headers"`
	// Body is the body sent: a raw body when saved from a send, any mode
	// when imported; mode "" for none.
	Body  BodyDTO `json:"body"`
	Extra string  `json:"extra"`
}

// ToResponseExampleDTO maps a response example; its lists are never null.
func ToResponseExampleDTO(e projectfile.ResponseExample) ResponseExampleDTO {
	out := ResponseExampleDTO{
		Name:         e.Name,
		SavedAt:      e.SavedAt,
		StatusCode:   e.StatusCode,
		StatusText:   e.StatusText,
		Headers:      toKeyValueDTOs(e.Headers),
		Body:         e.Body,
		BodyEncoding: e.BodyEncoding,
		BodyClipped:  e.BodyClipped,
		BodySize:     e.BodySize,
		Extra:        extraText(e.Unknown),
	}
	if r := e.Request; r != nil {
		out.Request = &ExampleRequestDTO{
			Method:  r.Method,
			URL:     r.URL,
			Headers: toKeyValueDTOs(r.Headers),
			Body:    toBodyDTO(r.Body),
			Extra:   extraText(r.Unknown),
		}
	}
	return out
}

// toResponseExampleDTOs maps the examples; nil when there are none, so
// the member is left out.
func toResponseExampleDTOs(list []projectfile.ResponseExample) []ResponseExampleDTO {
	if len(list) == 0 {
		return nil
	}
	out := make([]ResponseExampleDTO, len(list))
	for i, e := range list {
		out[i] = ToResponseExampleDTO(e)
	}
	return out
}

// responseExamples builds the examples of the file.
func (c *converter) responseExamples(list []ResponseExampleDTO) []projectfile.ResponseExample {
	if len(list) == 0 {
		return nil
	}
	out := make([]projectfile.ResponseExample, len(list))
	for i, e := range list {
		field := "responseExamples[" + strconv.Itoa(i) + "]"
		out[i] = projectfile.ResponseExample{
			Name:         e.Name,
			SavedAt:      e.SavedAt,
			StatusCode:   e.StatusCode,
			StatusText:   e.StatusText,
			Headers:      c.keyValues(field+".headers", e.Headers),
			Body:         e.Body,
			BodyEncoding: e.BodyEncoding,
			BodyClipped:  e.BodyClipped,
			BodySize:     e.BodySize,
			Unknown:      c.extra(field+".extra", e.Extra),
		}
		if r := e.Request; r != nil {
			req := &projectfile.ExampleRequest{
				Method:  r.Method,
				URL:     r.URL,
				Headers: c.keyValues(field+".request.headers", r.Headers),
				Unknown: c.extra(field+".request.extra", r.Extra),
			}
			// None is left out of the file.
			if r.Body.Mode != "" && r.Body.Mode != projectfile.BodyNone || r.Body.Extra != "" {
				req.Body = c.body(field+".request.body", r.Body)
			}
			out[i].Request = req
		}
	}
	return out
}
