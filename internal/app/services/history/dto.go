package history

import (
	"time"

	"github.com/sorrelfox/postsonde/internal/core/history"
)

// ListInputDTO is what List takes: a filter and a page (NAV-02).
type ListInputDTO struct {
	// Text matches entries whose method, URL, request name, status, error
	// or environment contain it, ignoring case, as kept (masked) and
	// masked again with the secrets known now.
	Text string `json:"text"`
	// Deep also looks for Text in the header fields and text bodies of
	// the request and the response and in the request as the editor had
	// it, reading the entries from disk.
	Deep bool `json:"deep,omitempty"`
	// Method matches the method, ignoring case; "" for all.
	Method string `json:"method"`
	// Status is "1xx" to "5xx" for a status class, "error" for sends
	// without a response (failed or cancelled), "" for all.
	Status string `json:"status"`
	// Kind is "http", "graphql" or "replay"; "" for all.
	Kind string `json:"kind,omitempty"`
	// Environment is the name of the environment that was active; "" for
	// all.
	Environment string `json:"environment,omitempty"`
	// Since and Until bound the send's start time, RFC 3339: Since
	// included, Until not; "" for no bound.
	Since string `json:"since,omitempty"`
	Until string `json:"until,omitempty"`
	// RequestID matches the entries of one collection request; "" for all.
	RequestID string `json:"requestId"`
	// Offset skips the first matching entries; Limit caps how many come
	// back (50 when 0, at most 500).
	Offset int `json:"offset"`
	Limit  int `json:"limit"`
}

// PageDTO is what List returns.
type PageDTO struct {
	// Entries are the matching entries, newest first.
	Entries []SummaryDTO `json:"entries"`
	// Total counts the matching entries; All counts every kept entry.
	Total int `json:"total"`
	All   int `json:"all"`
	// Environments are the names of the environments of every kept
	// entry, sorted, for the filter.
	Environments []string `json:"environments"`
}

// SummaryDTO is an entry as the history list shows it. URL and Error
// have secrets masked.
type SummaryDTO struct {
	ID string `json:"id"`
	// Time is when the send started, RFC 3339 in UTC.
	Time string `json:"time"`
	// Kind is "http", "graphql" or "replay" (a request not in the
	// collection, such as a replayed webhook request).
	Kind        string `json:"kind"`
	RequestID   string `json:"requestId"`
	RequestName string `json:"requestName"`
	// Environment names the environment that was active; "" for none (or
	// not known: entries recorded before it was kept).
	Environment string `json:"environment"`
	Method      string `json:"method"`
	URL         string `json:"url"`
	// Outcome is "ok" (a response of any status), "error" or "cancelled".
	Outcome    string `json:"outcome"`
	StatusCode int    `json:"statusCode"`
	Status     string `json:"status"`
	ErrorCode  string `json:"errorCode"`
	Error      string `json:"error"`
	DurationMs int64  `json:"durationMs"`
	// RequestSize and ResponseSize count the body bytes sent and received.
	RequestSize  int64 `json:"requestSize"`
	ResponseSize int64 `json:"responseSize"`
	// Masked is set when a secret was hidden in the entry.
	Masked bool `json:"masked"`
}

// EntryDTO is a whole entry: what Get returns.
type EntryDTO struct {
	SummaryDTO
	Request RequestDTO `json:"request"`
	// Response is null when no response arrived.
	Response *ResponseDTO `json:"response"`
	// Trimmed is set when the entry was too large and its bodies (or its
	// headers too) were left out.
	Trimmed bool `json:"trimmed"`
}

// HeaderDTO is a header field.
type HeaderDTO struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// BodyDTO is a body snapshot: at most 64 KiB of it.
type BodyDTO struct {
	// Text is the body, or its bytes in standard Base64 when Base64 is set.
	Text   string `json:"text"`
	Base64 bool   `json:"base64"`
	// Size counts every byte of the body; Clipped is set when Text holds
	// only the first of them.
	Size    int64 `json:"size"`
	Clipped bool  `json:"clipped"`
}

// RequestDTO is the request as sent (its first hop), secrets masked.
type RequestDTO struct {
	Method string `json:"method"`
	URL    string `json:"url"`
	// Proto is "HTTP/1.1" or "HTTP/2.0"; "" when nothing was written.
	Proto   string      `json:"proto"`
	Headers []HeaderDTO `json:"headers"`
	Body    BodyDTO     `json:"body"`
}

// ResponseDTO is the snapshot of the final response, secrets masked.
type ResponseDTO struct {
	Proto       string      `json:"proto"`
	StatusCode  int         `json:"statusCode"`
	Status      string      `json:"status"`
	FinalURL    string      `json:"finalUrl"`
	Redirects   int         `json:"redirects"`
	Headers     []HeaderDTO `json:"headers"`
	ContentType string      `json:"contentType"`
	Body        BodyDTO     `json:"body"`
	// Truncated is set when the client stopped reading the body at its
	// limit.
	Truncated bool `json:"truncated"`
}

func toSummaryDTO(s history.Summary) SummaryDTO {
	return SummaryDTO{
		ID:           s.ID,
		Time:         s.Time.UTC().Format(time.RFC3339Nano),
		Kind:         s.Kind,
		RequestID:    s.RequestID,
		RequestName:  s.RequestName,
		Environment:  s.Environment,
		Method:       s.Method,
		URL:          s.URL,
		Outcome:      s.Outcome,
		StatusCode:   s.StatusCode,
		Status:       s.Status,
		ErrorCode:    s.ErrorCode,
		Error:        s.Error,
		DurationMs:   s.DurationMs,
		RequestSize:  s.RequestSize,
		ResponseSize: s.ResponseSize,
		Masked:       s.Masked,
	}
}

func toEntryDTO(e history.Entry) EntryDTO {
	out := EntryDTO{
		SummaryDTO: toSummaryDTO(e.Summary),
		Request: RequestDTO{
			Method:  e.Request.Method,
			URL:     e.Request.URL,
			Proto:   e.Request.Proto,
			Headers: toHeaderDTOs(e.Request.Headers),
			Body:    toBodyDTO(e.Request.Body),
		},
		Trimmed: e.Trimmed,
	}
	if r := e.Response; r != nil {
		out.Response = &ResponseDTO{
			Proto:       r.Proto,
			StatusCode:  r.StatusCode,
			Status:      r.Status,
			FinalURL:    r.FinalURL,
			Redirects:   r.Redirects,
			Headers:     toHeaderDTOs(r.Headers),
			ContentType: r.ContentType,
			Body:        toBodyDTO(r.Body),
			Truncated:   r.Truncated,
		}
	}
	return out
}

func toHeaderDTOs(hs []history.Header) []HeaderDTO {
	out := make([]HeaderDTO, 0, len(hs))
	for _, h := range hs {
		out = append(out, HeaderDTO{Name: h.Name, Value: h.Value})
	}
	return out
}

func toBodyDTO(b history.Body) BodyDTO {
	return BodyDTO{Text: b.Text, Base64: b.Base64, Size: b.Size, Clipped: b.Clipped}
}
