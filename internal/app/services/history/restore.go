package history

import (
	"context"

	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/history"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
)

// CodeInvalidFilter: List got a since or until that is not RFC 3339, or
// ClearRequest no request ID (kind invalid_argument). Details: field,
// value.
const CodeInvalidFilter = "history.invalid_filter"

// HiddenDTO is a value of a restored request that is not restored as it
// was sent.
type HiddenDTO struct {
	// Section is "url", "params", "pathParams", "headers", "body",
	// "bodyFields", "bodyParts", "query" or "variables".
	Section string `json:"section"`
	// Name is the row's name, for a row.
	Name string `json:"name"`
	// Reason is "masked" (it held a secret, masked in the history: the
	// masks are taken out, the rest kept), "clipped" (the history kept
	// only its start, or none of it) or "binary" (a body that is not
	// text).
	Reason string `json:"reason"`
}

// RestoreDTO is a history entry as a request to open again (NAV-03), in
// the request editor's shapes: the UI writes these fields into the
// request the entry was sent from, as one undoable edit, or into a new
// scratch request. Masked secrets are never in it: the masks are taken
// out and Hidden says where they were.
type RestoreDTO struct {
	ID          string `json:"id"`
	RequestID   string `json:"requestId"`
	RequestName string `json:"requestName"`
	// Kind is "http" or "graphql" (a replay is restored as http).
	Kind string `json:"kind"`
	// Templated is set when the fields are the request as its editor had
	// it, {{variables}} as written. Else (a replay, an entry recorded by an
	// older PostSonde) they are the request as sent: variables resolved,
	// no auth, the query string only in the URL (Params is empty).
	Templated bool `json:"templated"`
	// Method is the HTTP method; for graphql "" (POST) or "GET".
	Method     string                   `json:"method"`
	URL        string                   `json:"url"`
	Params     []collection.KeyValueDTO `json:"params"`
	PathParams []collection.KeyValueDTO `json:"pathParams"`
	Headers    []collection.KeyValueDTO `json:"headers"`
	// Body is http only.
	Body collection.BodyDTO `json:"body"`
	// Query, Variables and OperationName are graphql only.
	Query         string      `json:"query"`
	Variables     string      `json:"variables"`
	OperationName string      `json:"operationName"`
	Hidden        []HiddenDTO `json:"hidden"`
}

// Restore returns history entry id as a request to open again (NAV-03):
// the request as its editor had it when the entry has it, else the
// request as sent. Like Example it is masked again with every secret
// PostSonde keeps now (WithSecrets), and then every mask is taken out, so
// "••••••" is never written into a request. Nothing is written here.
//
// Errors: history.not_found, workspace.none_open.
func (s *HistoryService) Restore(ctx context.Context, id string) (RestoreDTO, error) {
	st, _, err := s.current(ctx)
	if err != nil {
		return RestoreDTO{}, err
	}
	e, err := st.Get(id)
	if err != nil {
		return RestoreDTO{}, err
	}
	m := s.masker(ctx)
	if m == nil {
		// The credentials of Authorization fields, by name.
		m = &httpclient.SecretMasker{}
	}
	return toRestoreDTO(e, history.Restore(e, m)), nil
}

func toRestoreDTO(e history.Entry, r history.Restored) RestoreDTO {
	out := RestoreDTO{
		ID: e.ID, RequestID: e.RequestID, RequestName: e.RequestName,
		Kind: r.Kind, Templated: r.Templated,
		Method: r.Method, URL: r.URL,
		Params: toKeyValueDTOs(r.Params), PathParams: toKeyValueDTOs(r.PathParams), Headers: toKeyValueDTOs(r.Headers),
		Body:  collection.BodyDTO{Mode: "none", Fields: []collection.KeyValueDTO{}, Parts: []collection.PartDTO{}},
		Query: r.Query, Variables: r.Variables, OperationName: r.OperationName,
		Hidden: make([]HiddenDTO, 0, len(r.Hidden)),
	}
	if b := r.Body; b != nil {
		out.Body = collection.BodyDTO{
			Mode: b.Mode, Type: b.Type, Text: b.Text, Fields: toKeyValueDTOs(b.Fields), Parts: make([]collection.PartDTO, 0, len(b.Parts)),
			Path: b.Path, ContentType: b.ContentType,
		}
		for _, p := range b.Parts {
			out.Body.Parts = append(out.Body.Parts, collection.PartDTO{
				Name: p.Name, Value: p.Value, File: p.File, ContentType: p.ContentType, Description: p.Description, Enabled: !p.Disabled,
			})
		}
	}
	for _, h := range r.Hidden {
		out.Hidden = append(out.Hidden, HiddenDTO{Section: h.Section, Name: h.Name, Reason: h.Reason})
	}
	return out
}

func toKeyValueDTOs(fs []history.Field) []collection.KeyValueDTO {
	out := make([]collection.KeyValueDTO, 0, len(fs))
	for _, f := range fs {
		out = append(out, collection.KeyValueDTO{Name: f.Name, Value: f.Value, Description: f.Description, Enabled: !f.Disabled})
	}
	return out
}
