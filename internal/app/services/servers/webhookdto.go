package servers

import (
	"net/url"
	"strconv"
	"strings"

	httpservice "github.com/sorrelfox/postsonde/internal/app/services/http"
	core "github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/webhook"
)

// The DTOs of the webhook receiver (MOCK-08): its response, the requests
// it keeps and replaying one. See docs/servers.md.

// WebhookConfigDTO is the response a webhook receiver answers every
// request with.
type WebhookConfigDTO struct {
	// Status is 200–599.
	Status int `json:"status"`
	// Headers are sent in order; rows not enabled are skipped.
	// Content-Length, Transfer-Encoding and Connection cannot be set.
	Headers []MockHeaderDTO `json:"headers"`
	// Body is sent as is, at most 1 MiB.
	Body string `json:"body"`
}

// CaptureSummaryDTO is one kept request, for the list.
type CaptureSummaryDTO struct {
	// Seq is the seq of its request event in the traffic.
	Seq int64 `json:"seq"`
	// Time is RFC 3339 in UTC.
	Time   string `json:"time"`
	Peer   string `json:"peer"`
	Method string `json:"method"`
	// Target is the request target as sent: path and query.
	Target      string `json:"target"`
	ContentType string `json:"contentType"`
	// Size is the body's length; Truncated says the receiver kept only
	// its first bytes (it cannot be replayed then).
	Size      int64 `json:"size"`
	Truncated bool  `json:"truncated"`
}

// CaptureDTO is one kept request in full.
type CaptureDTO struct {
	ServerID string `json:"serverId"`
	// Summary is the request as the list shows it.
	Summary CaptureSummaryDTO `json:"summary"`
	Proto   string            `json:"proto"`
	// Host is the Host header.
	Host string `json:"host"`
	// Path is the target before "?", as sent; Query its parameters in
	// order, decoded (a part that does not decode is shown as sent).
	Path  string      `json:"path"`
	Query []HeaderDTO `json:"query"`
	// Headers are the headers as received, Host first, then by name.
	Headers []HeaderDTO `json:"headers"`
	// Body is the body as the response view shows one: only its body
	// fields (body, bodyLanguage, bodyPretty, charset, bodyHex, …) and
	// contentType, size and bodyTruncated are set.
	Body httpservice.ResponseDTO `json:"body"`
}

// CaptureInputDTO names one kept request.
type CaptureInputDTO struct {
	ServerID string `json:"serverId"`
	Seq      int64  `json:"seq"`
}

// ReplayInputDTO replays a kept request to URL.
type ReplayInputDTO struct {
	ServerID string `json:"serverId"`
	Seq      int64  `json:"seq"`
	// URL is an absolute http:// or https:// URL, sent as typed.
	URL string `json:"url"`
}

// CapturesEventDTO is the payload of servers:captures: the requests one
// webhook receiver keeps now ([] after its traffic was cleared).
type CapturesEventDTO struct {
	ServerID string              `json:"serverId"`
	Captures []CaptureSummaryDTO `json:"captures"`
}

func (c WebhookConfigDTO) toCore() webhook.Config {
	out := webhook.Config{Status: c.Status, Body: c.Body, Headers: make([]webhook.Header, 0, len(c.Headers))}
	for _, h := range c.Headers {
		out.Headers = append(out.Headers, webhook.Header{Enabled: h.Enabled, Name: h.Name, Value: h.Value})
	}
	return out
}

func toWebhookConfigDTO(c webhook.Config) *WebhookConfigDTO {
	out := &WebhookConfigDTO{Status: c.Status, Body: c.Body, Headers: make([]MockHeaderDTO, 0, len(c.Headers))}
	for _, h := range c.Headers {
		out.Headers = append(out.Headers, MockHeaderDTO{Enabled: h.Enabled, Name: h.Name, Value: h.Value})
	}
	return out
}

func contentTypeOf(headers []core.Header) string {
	for _, h := range headers {
		if strings.EqualFold(h.Name, "Content-Type") {
			return h.Value
		}
	}
	return ""
}

func toCaptureSummaryDTO(c core.Capture) CaptureSummaryDTO {
	return CaptureSummaryDTO{
		Seq:         int64(c.Seq),
		Time:        formatTime(c.Time),
		Peer:        c.Peer,
		Method:      c.Method,
		Target:      c.Target,
		ContentType: contentTypeOf(c.Headers),
		Size:        c.Size,
		Truncated:   c.Truncated,
	}
}

func toCaptureSummaryDTOs(list []core.Capture) []CaptureSummaryDTO {
	out := make([]CaptureSummaryDTO, 0, len(list))
	for _, c := range list {
		out = append(out, toCaptureSummaryDTO(c))
	}
	return out
}

func toCaptureDTO(serverID string, c core.Capture) CaptureDTO {
	summary := toCaptureSummaryDTO(c)
	path, rawQuery, _ := strings.Cut(c.Target, "?")
	out := CaptureDTO{
		ServerID: serverID,
		Summary:  summary,
		Proto:    c.Proto,
		Host:     c.Host,
		Path:     path,
		Query:    queryPairs(rawQuery),
		Headers:  make([]HeaderDTO, 0, len(c.Headers)+1),
		Body:     httpservice.BodyView(summary.ContentType, c.Body, c.Size, c.Truncated),
	}
	if c.Host != "" {
		out.Headers = append(out.Headers, HeaderDTO{Name: "Host", Value: c.Host})
	}
	for _, h := range c.Headers {
		out.Headers = append(out.Headers, HeaderDTO{Name: h.Name, Value: h.Value})
	}
	return out
}

// queryPairs splits a raw query into its parameters in order, decoding
// each part; a part that does not decode is kept as sent.
func queryPairs(raw string) []HeaderDTO {
	out := []HeaderDTO{}
	if raw == "" {
		return out
	}
	decode := func(s string) string {
		if d, err := url.QueryUnescape(s); err == nil {
			return d
		}
		return s
	}
	for part := range strings.SplitSeq(raw, "&") {
		if part == "" {
			continue
		}
		name, value, _ := strings.Cut(part, "=")
		out = append(out, HeaderDTO{Name: decode(name), Value: decode(value)})
	}
	return out
}

func formatInt(n int64) string { return strconv.FormatInt(n, 10) }
