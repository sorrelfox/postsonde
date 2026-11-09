// Package history is the request history store (NAV-02): a local log of
// every request the user sends, with the request as sent and a size-capped
// snapshot of the response, kept in a workspace's local directory, never
// in the project folder (STO-04).
//
// The log is append-only JSON Lines split into segment files that rotate
// at a size, with an in-memory index of every live entry. Retention keeps
// the newest entries within a count and a byte limit; a segment file is
// removed once none of its entries is live. A line cut short by a crash is
// dropped when the store opens. Callers mask secrets before they append
// (Snapshot, Headers); the store writes what it is given. See
// docs/history.md.
package history

import (
	"time"
)

// Outcomes of a send (Summary.Outcome).
const (
	// OutcomeOK: a response arrived, of any status.
	OutcomeOK = "ok"
	// OutcomeError: the send failed (connection, TLS, timeout...).
	OutcomeError = "error"
	// OutcomeCancelled: the user cancelled the send.
	OutcomeCancelled = "cancelled"
)

// Kinds of entries (Summary.Kind).
const (
	// KindHTTP: an HTTP request of the collection.
	KindHTTP = "http"
	// KindGraphQL: a GraphQL request sent over HTTP.
	KindGraphQL = "graphql"
	// KindReplay: a request that is not in the collection, such as a
	// webhook request replayed (MOCK-08).
	KindReplay = "replay"
)

// Summary is what the history list shows of an entry, and what the store
// keeps in memory for it.
type Summary struct {
	// ID names the entry ("h_" and 16 hex digits). Append sets it.
	ID string `json:"id"`
	// Time is when the send started. Append sets it when zero.
	Time time.Time `json:"time"`
	// Kind is KindHTTP, KindGraphQL or KindReplay.
	Kind string `json:"kind"`
	// RequestID and RequestName are the collection request sent, as it
	// was named then; "" for a replay.
	RequestID   string `json:"requestId,omitempty"`
	RequestName string `json:"requestName,omitempty"`
	// Environment names the environment that was active for the send;
	// "" for none, and in entries recorded before it was kept.
	Environment string `json:"environment,omitempty"`
	// Method and URL are what was sent (the URL after variables, before
	// redirects), secrets masked.
	Method string `json:"method"`
	URL    string `json:"url"`
	// Outcome is OutcomeOK, OutcomeError or OutcomeCancelled.
	Outcome string `json:"outcome"`
	// StatusCode and Status are the response's status (0 and "" without a
	// response).
	StatusCode int    `json:"statusCode,omitempty"`
	Status     string `json:"status,omitempty"`
	// ErrorCode and Error say why a send failed or was cancelled.
	ErrorCode string `json:"errorCode,omitempty"`
	Error     string `json:"error,omitempty"`
	// DurationMs is the whole send, redirects included; 0 when it failed.
	DurationMs int64 `json:"durationMs"`
	// RequestSize and ResponseSize count the body bytes sent and received.
	RequestSize  int64 `json:"requestSize"`
	ResponseSize int64 `json:"responseSize"`
	// Masked is set when a secret was hidden in the entry.
	Masked bool `json:"masked,omitempty"`
}

// Entry is one sent request: its Summary, the request as sent and the
// response snapshot.
type Entry struct {
	Summary
	Request RequestSnapshot `json:"request"`
	// Template is the request as its editor had it, {{variables}} as
	// written (NAV-03); nil for a replay and in older entries.
	Template *Template `json:"template,omitempty"`
	// Response is nil when no response arrived.
	Response *ResponseSnapshot `json:"response,omitempty"`
	// Trimmed is set when the bodies (and, if that was not enough, the
	// headers) were left out because the entry was over MaxEntryBytes.
	Trimmed bool `json:"trimmed,omitempty"`
}

// Header is a header field.
type Header struct {
	Name  string `json:"name"`
	Value string `json:"value"`
}

// Body is a body snapshot: at most MaxBody bytes of it, as text when they
// are UTF-8, else Base64.
type Body struct {
	// Text is the body, or its bytes in standard Base64 when Base64 is set.
	Text   string `json:"text"`
	Base64 bool   `json:"base64,omitempty"`
	// Size counts every byte of the body; Clipped is set when Text holds
	// only the first of them.
	Size    int64 `json:"size"`
	Clipped bool  `json:"clipped,omitempty"`
}

// RequestSnapshot is the request as it went over the wire (the first hop),
// or as it was built when the send failed before writing it.
type RequestSnapshot struct {
	Method string `json:"method"`
	URL    string `json:"url"`
	// Proto is "HTTP/1.1" or "HTTP/2.0"; "" when nothing was written.
	Proto   string   `json:"proto,omitempty"`
	Headers []Header `json:"headers"`
	Body    Body     `json:"body"`
}

// ResponseSnapshot is the final response.
type ResponseSnapshot struct {
	Proto      string `json:"proto"`
	StatusCode int    `json:"statusCode"`
	Status     string `json:"status"`
	// FinalURL is the URL of the response, after Redirects redirects.
	FinalURL  string   `json:"finalUrl"`
	Redirects int      `json:"redirects,omitempty"`
	Headers   []Header `json:"headers"`
	// ContentType is the Content-Type header, or "".
	ContentType string `json:"contentType,omitempty"`
	// Body is the body as received (decompressed when the transport
	// asked for compression itself); Truncated is set when the client
	// stopped reading it at its limit.
	Body      Body `json:"body"`
	Truncated bool `json:"truncated,omitempty"`
	// Timings are the phases of the final hop (DBG-03); nil in entries
	// written before they were recorded.
	Timings *Timings `json:"timings,omitempty"`
	// RemoteAddr and LocalAddr are the final hop's socket addresses,
	// "ip:port" ("" when unknown).
	RemoteAddr string `json:"remoteAddr,omitempty"`
	LocalAddr  string `json:"localAddr,omitempty"`
}

// Timings say where the time of the final hop went, in milliseconds
// (httpclient.Timing). A phase that did not happen (DNS for an IP
// address, TLS for http://, DNS, Connect and TLS on a reused connection)
// is -1. Total is the whole send, redirects included; the rest of it
// (request preparation, the connection pool, earlier hops) is in no
// phase.
type Timings struct {
	DNS     float64 `json:"dns"`
	Connect float64 `json:"connect"`
	TLS     float64 `json:"tls"`
	Send    float64 `json:"send"`
	Wait    float64 `json:"wait"`
	Receive float64 `json:"receive"`
	Total   float64 `json:"total"`
}
