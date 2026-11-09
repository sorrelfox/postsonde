// Package serverdef converts between a saved server config, the "server"
// member of a folder's _folder.json (projectfile.FolderServer, SRV-04),
// and the Spec the servers Manager runs. See docs/servers.md (Saved server
// configs) and docs/file-format.md (Saved server config).
package serverdef

import (
	"cmp"
	"slices"

	"github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/graphqlmock"
	"github.com/sorrelfox/postsonde/internal/core/servers/grpcmock"
	"github.com/sorrelfox/postsonde/internal/core/servers/mockhttp"
	"github.com/sorrelfox/postsonde/internal/core/servers/sioserver"
	"github.com/sorrelfox/postsonde/internal/core/servers/sseserver"
	"github.com/sorrelfox/postsonde/internal/core/servers/udpserver"
	"github.com/sorrelfox/postsonde/internal/core/servers/webhook"
	"github.com/sorrelfox/postsonde/internal/core/servers/wsserver"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// ToSpec returns the Spec of the saved server def, with the configuration
// of its type (nil when the file has none: the type's default). The
// result is not checked; Manager.Check and Add do that. Owner is left
// zero.
func ToSpec(def *projectfile.FolderServer) servers.Spec {
	spec := servers.Spec{Type: def.Type, Name: def.Name, Host: def.Host, Port: def.Port, AllowLAN: def.AllowLAN}
	if t := def.TLS; t != nil {
		spec.TLS = &servers.TLS{Certificate: t.Certificate, CertFile: t.CertFile, KeyFile: t.KeyFile, PFXFile: t.PFXFile}
	}
	switch def.Type {
	case mockhttp.TypeName:
		if def.Mock != nil {
			spec.Config = mockConfig(def.Mock)
		}
	case wsserver.TypeName:
		if def.WebSocket != nil {
			w := def.WebSocket
			spec.Config = wsserver.Config{Path: w.Path, Subprotocols: slices.Clone(w.Subprotocols), Echo: w.Echo, MaxMessageSize: w.MaxMessageSize, Rules: wsRules(w.Rules)}
		}
	case webhook.TypeName:
		if def.Webhook != nil {
			spec.Config = webhook.Config{Status: def.Webhook.Status, Headers: webhookHeaders(def.Webhook.Headers), Body: def.Webhook.Body}
		}
	case sseserver.TypeName:
		if def.SSE != nil {
			spec.Config = sseConfig(def.SSE)
		}
	case udpserver.TypeName:
		if u := def.UDP; u != nil {
			spec.Config = udpserver.Config{Echo: u.Echo, FixedReply: u.FixedReply, Reply: u.Reply, ReplyFormat: u.ReplyFormat}
		}
	case grpcmock.TypeName:
		if def.GRPCMock != nil {
			spec.Config = grpcMockConfig(def.GRPCMock)
		}
	case sioserver.TypeName:
		if def.SocketIO != nil {
			spec.Config = sioConfig(def.SocketIO)
		}
	case graphqlmock.TypeName:
		if def.GraphQLMock != nil {
			spec.Config = graphQLMockConfig(def.GraphQLMock)
		}
	}
	return spec
}

// FromSpec returns the saved server config of spec, as normalized by the
// Manager. What spec does not hold is taken from prev (nil for a new
// one): members this version does not know at every level (a route's by
// route ID, a header row's by position when the name is the same), a
// row's description and the configuration members of other types. TLS
// (SRV-05) is written as spec has it: paths only, never a passphrase. A name
// equal to the type's label is written as "" so the file does not repeat
// the default.
func FromSpec(prev *projectfile.FolderServer, spec servers.Spec, typeLabel string) *projectfile.FolderServer {
	if prev == nil {
		prev = &projectfile.FolderServer{}
	}
	out := &projectfile.FolderServer{
		Type:        spec.Type,
		Name:        spec.Name,
		Host:        spec.Host,
		Port:        spec.Port,
		AllowLAN:    spec.AllowLAN,
		Mock:        prev.Mock,
		WebSocket:   prev.WebSocket,
		Webhook:     prev.Webhook,
		SSE:         prev.SSE,
		UDP:         prev.UDP,
		GRPCMock:    prev.GRPCMock,
		SocketIO:    prev.SocketIO,
		GraphQLMock: prev.GraphQLMock,
		Unknown:     prev.Unknown,
	}
	if out.Name == typeLabel {
		out.Name = ""
	}
	if t := spec.TLS; t != nil {
		out.TLS = &projectfile.ServerTLS{Certificate: t.Certificate, CertFile: t.CertFile, KeyFile: t.KeyFile, PFXFile: t.PFXFile}
		if prev.TLS != nil {
			out.TLS.Unknown = prev.TLS.Unknown
		}
	}
	switch c := spec.Config.(type) {
	case mockhttp.Config:
		out.Mock = fromMock(prev.Mock, c)
	case wsserver.Config:
		out.WebSocket = fromWebSocket(prev.WebSocket, c)
	case webhook.Config:
		out.Webhook = fromWebhook(prev.Webhook, c)
	case sseserver.Config:
		out.SSE = fromSSE(prev.SSE, c)
	case udpserver.Config:
		out.UDP = fromUDP(prev.UDP, c)
	case grpcmock.Config:
		out.GRPCMock = fromGRPCMock(prev.GRPCMock, c)
	case sioserver.Config:
		out.SocketIO = fromSocketIO(prev.SocketIO, c)
	case graphqlmock.Config:
		out.GraphQLMock = fromGraphQLMock(prev.GraphQLMock, c)
	}
	return out
}

func mockConfig(m *projectfile.ServerMock) mockhttp.Config {
	c := mockhttp.Config{StrictTrailingSlash: m.StrictTrailingSlash, CaseInsensitive: m.CaseInsensitive, Routes: make([]mockhttp.Route, 0, len(m.Routes))}
	for _, r := range m.Routes {
		route := mockhttp.Route{
			ID:        r.ID,
			Enabled:   !r.Disabled,
			Method:    r.Method,
			Path:      r.Path,
			Status:    r.Status,
			Headers:   mockHeaders(r.Headers),
			Body:      mockhttp.Body{Mode: r.Body.Mode, Type: r.Body.Type, Text: r.Body.Text},
			Delay:     mockhttp.Delay{Mode: r.Delay.Mode, Ms: r.Delay.Ms, MinMs: r.Delay.MinMs, MaxMs: r.Delay.MaxMs},
			Templated: r.Templated,
		}
		for _, alt := range r.Responses {
			resp := mockhttp.Response{
				Name:      alt.Name,
				Status:    alt.Status,
				Headers:   mockHeaders(alt.Headers),
				Body:      mockhttp.Body{Mode: alt.Body.Mode, Type: alt.Body.Type, Text: alt.Body.Text},
				Templated: alt.Templated,
			}
			for _, ru := range alt.Rules {
				resp.Rules = append(resp.Rules, mockhttp.Rule{Source: ru.Source, Name: ru.Name, Op: ru.Op, Value: ru.Value})
			}
			route.Responses = append(route.Responses, resp)
		}
		c.Routes = append(c.Routes, route)
	}
	return c
}

func mockHeaders(rows []projectfile.KeyValue) []mockhttp.Header {
	out := make([]mockhttp.Header, 0, len(rows))
	for _, h := range rows {
		out = append(out, mockhttp.Header{Enabled: !h.Disabled, Name: h.Name, Value: h.Value})
	}
	return out
}

// mockBody is the saved form of a response body; was is the saved body
// it replaces, whose unknown members are kept.
func mockBody(b mockhttp.Body, was projectfile.Body) projectfile.Body {
	out := projectfile.Body{Mode: b.Mode, Unknown: was.Unknown}
	if out.Mode == "" {
		out.Mode = mockhttp.BodyNone
	}
	if out.Mode == mockhttp.BodyRaw {
		out.Type, out.Text = b.Type, b.Text
	}
	return out
}

// fromResponses are the saved form of a route's other responses. A
// response keeps the unknown members of the old one at the same position
// when the name is the same, a rule those of the old rule at its position.
func fromResponses(old []projectfile.MockResponse, alts []mockhttp.Response) []projectfile.MockResponse {
	if len(alts) == 0 {
		return nil
	}
	out := make([]projectfile.MockResponse, len(alts))
	for j, alt := range alts {
		var was projectfile.MockResponse
		if j < len(old) && old[j].Name == alt.Name {
			was = old[j]
		}
		resp := projectfile.MockResponse{
			Name:      alt.Name,
			Status:    alt.Status,
			Headers:   rows(was.Headers, len(alt.Headers), func(i int) (bool, string, string) { h := alt.Headers[i]; return h.Enabled, h.Name, h.Value }),
			Body:      mockBody(alt.Body, was.Body),
			Templated: alt.Templated,
			Unknown:   was.Unknown,
		}
		for k, ru := range alt.Rules {
			rule := projectfile.MockRule{Source: ru.Source, Name: ru.Name, Op: ru.Op, Value: ru.Value}
			if k < len(was.Rules) {
				rule.Unknown = was.Rules[k].Unknown
			}
			resp.Rules = append(resp.Rules, rule)
		}
		out[j] = resp
	}
	return out
}

func fromMock(prev *projectfile.ServerMock, c mockhttp.Config) *projectfile.ServerMock {
	out := &projectfile.ServerMock{StrictTrailingSlash: c.StrictTrailingSlash, CaseInsensitive: c.CaseInsensitive}
	old := map[string]projectfile.MockRoute{}
	if prev != nil {
		out.Unknown = prev.Unknown
		for _, r := range prev.Routes {
			old[r.ID] = r
		}
	}
	for _, r := range c.Routes {
		was := old[r.ID]
		route := projectfile.MockRoute{
			ID:        r.ID,
			Method:    r.Method,
			Path:      r.Path,
			Status:    r.Status,
			Headers:   rows(was.Headers, len(r.Headers), func(i int) (bool, string, string) { h := r.Headers[i]; return h.Enabled, h.Name, h.Value }),
			Body:      mockBody(r.Body, was.Body),
			Responses: fromResponses(was.Responses, r.Responses),
			Templated: r.Templated,
			Disabled:  !r.Enabled,
			Unknown:   was.Unknown,
		}
		switch r.Delay.Mode {
		case mockhttp.DelayFixed:
			route.Delay = projectfile.MockDelay{Mode: r.Delay.Mode, Ms: r.Delay.Ms, Unknown: was.Delay.Unknown}
		case mockhttp.DelayRandom:
			route.Delay = projectfile.MockDelay{Mode: r.Delay.Mode, MinMs: r.Delay.MinMs, MaxMs: r.Delay.MaxMs, Unknown: was.Delay.Unknown}
		default:
			if len(was.Delay.Unknown) > 0 {
				route.Delay = projectfile.MockDelay{Mode: mockhttp.DelayNone, Unknown: was.Delay.Unknown}
			}
		}
		out.Routes = append(out.Routes, route)
	}
	return out
}

func fromWebSocket(prev *projectfile.ServerWebSocket, c wsserver.Config) *projectfile.ServerWebSocket {
	out := &projectfile.ServerWebSocket{Path: c.Path, Echo: c.Echo, MaxMessageSize: c.MaxMessageSize}
	if len(c.Subprotocols) > 0 {
		out.Subprotocols = slices.Clone(c.Subprotocols)
	}
	var was []projectfile.ServerWSRule
	if prev != nil {
		out.Unknown, was = prev.Unknown, prev.Rules
	}
	for i, r := range c.Rules {
		rule := projectfile.ServerWSRule{Match: r.Match, Pattern: r.Pattern, Reply: r.Reply, DelayMs: r.DelayMs, Disabled: !r.Enabled}
		if rule.Match == "" {
			rule.Match = wsserver.MatchExact
		}
		// Members this version does not know stay with the rule at the
		// same position when it matches the same way.
		if i < len(was) && cmp.Or(was[i].Match, wsserver.MatchExact) == rule.Match && was[i].Pattern == rule.Pattern {
			rule.Unknown = was[i].Unknown
		}
		out.Rules = append(out.Rules, rule)
	}
	return out
}

func wsRules(rules []projectfile.ServerWSRule) []wsserver.Rule {
	out := make([]wsserver.Rule, 0, len(rules))
	for _, r := range rules {
		out = append(out, wsserver.Rule{Enabled: !r.Disabled, Match: r.Match, Pattern: r.Pattern, Reply: r.Reply, DelayMs: r.DelayMs})
	}
	return out
}

func webhookHeaders(rows []projectfile.KeyValue) []webhook.Header {
	out := make([]webhook.Header, 0, len(rows))
	for _, h := range rows {
		out = append(out, webhook.Header{Enabled: !h.Disabled, Name: h.Name, Value: h.Value})
	}
	return out
}

func fromWebhook(prev *projectfile.ServerWebhook, c webhook.Config) *projectfile.ServerWebhook {
	out := &projectfile.ServerWebhook{Status: c.Status, Body: c.Body}
	var was []projectfile.KeyValue
	if prev != nil {
		out.Unknown, was = prev.Unknown, prev.Headers
	}
	out.Headers = rows(was, len(c.Headers), func(i int) (bool, string, string) {
		h := c.Headers[i]
		return h.Enabled, h.Name, h.Value
	})
	return out
}

// rows builds n header rows from row(i); a row keeps the description and
// unknown members of the old row at the same position when the name is
// the same.
func rows(old []projectfile.KeyValue, n int, row func(i int) (enabled bool, name, value string)) []projectfile.KeyValue {
	if n == 0 {
		return nil
	}
	out := make([]projectfile.KeyValue, n)
	for i := range n {
		enabled, name, value := row(i)
		kv := projectfile.KeyValue{Name: name, Value: value, Disabled: !enabled}
		if i < len(old) && old[i].Name == name {
			kv.Description, kv.Unknown = old[i].Description, old[i].Unknown
		}
		out[i] = kv
	}
	return out
}

func sseConfig(s *projectfile.ServerSSE) sseserver.Config {
	c := sseserver.Config{Path: s.Path, AllowOrigin: s.AllowOrigin, KeepAliveSec: s.KeepAliveSec, RetryMs: s.RetryMs, AutoID: s.AutoID, Timers: make([]sseserver.Timer, 0, len(s.Timers))}
	for _, t := range s.Timers {
		c.Timers = append(c.Timers, sseserver.Timer{Enabled: !t.Disabled, Event: t.Event, Data: t.Data, ID: t.ID, IntervalMs: t.IntervalMs})
	}
	return c
}

func fromSSE(prev *projectfile.ServerSSE, c sseserver.Config) *projectfile.ServerSSE {
	out := &projectfile.ServerSSE{Path: c.Path, AllowOrigin: c.AllowOrigin, KeepAliveSec: c.KeepAliveSec, RetryMs: c.RetryMs, AutoID: c.AutoID}
	var was []projectfile.ServerSSETimer
	if prev != nil {
		out.Unknown, was = prev.Unknown, prev.Timers
	}
	for i, t := range c.Timers {
		timer := projectfile.ServerSSETimer{Event: t.Event, Data: t.Data, ID: t.ID, IntervalMs: t.IntervalMs, Disabled: !t.Enabled}
		// Members this version does not know stay with the timer at the
		// same position while it sends the same event type.
		if i < len(was) && was[i].Event == t.Event {
			timer.Unknown = was[i].Unknown
		}
		out.Timers = append(out.Timers, timer)
	}
	return out
}

func fromUDP(prev *projectfile.ServerUDP, c udpserver.Config) *projectfile.ServerUDP {
	out := &projectfile.ServerUDP{Echo: c.Echo, FixedReply: c.FixedReply, Reply: c.Reply}
	if c.ReplyFormat != udpserver.FormatText {
		out.ReplyFormat = c.ReplyFormat
	}
	if prev != nil {
		out.Unknown = prev.Unknown
	}
	return out
}

func grpcMockConfig(g *projectfile.ServerGRPCMock) grpcmock.Config {
	c := grpcmock.Config{
		Files:       slices.Clone(g.Files),
		ImportPaths: slices.Clone(g.ImportPaths),
		Reflection:  !g.NoReflection,
		Methods:     make([]grpcmock.Method, 0, len(g.Methods)),
	}
	md := func(rows []projectfile.KeyValue) []grpcmock.Metadata {
		out := make([]grpcmock.Metadata, 0, len(rows))
		for _, r := range rows {
			out = append(out, grpcmock.Metadata{Enabled: !r.Disabled, Name: r.Name, Value: r.Value})
		}
		return out
	}
	for _, m := range g.Methods {
		c.Methods = append(c.Methods, grpcmock.Method{
			Service:       m.Service,
			Method:        m.Method,
			Messages:      slices.Clone(m.Messages),
			Status:        m.Status,
			StatusMessage: m.StatusMessage,
			Headers:       md(m.Headers),
			Trailers:      md(m.Trailers),
			DelayMs:       m.DelayMs,
			IntervalMs:    m.IntervalMs,
			Bidi:          m.Bidi,
		})
	}
	return c
}

func fromGRPCMock(prev *projectfile.ServerGRPCMock, c grpcmock.Config) *projectfile.ServerGRPCMock {
	out := &projectfile.ServerGRPCMock{NoReflection: !c.Reflection}
	if len(c.Files) > 0 {
		out.Files = slices.Clone(c.Files)
	}
	if len(c.ImportPaths) > 0 {
		out.ImportPaths = slices.Clone(c.ImportPaths)
	}
	// Members this version does not know stay with the method they were
	// written for (matched by service and method).
	was := map[string]projectfile.ServerGRPCMethod{}
	if prev != nil {
		out.Unknown = prev.Unknown
		for _, m := range prev.Methods {
			was[m.Service+"/"+m.Method] = m
		}
	}
	for _, m := range c.Methods {
		old := was[m.Service+"/"+m.Method]
		pm := projectfile.ServerGRPCMethod{
			Service:       m.Service,
			Method:        m.Method,
			Status:        m.Status,
			StatusMessage: m.StatusMessage,
			DelayMs:       m.DelayMs,
			IntervalMs:    m.IntervalMs,
			Unknown:       old.Unknown,
		}
		if len(m.Messages) > 0 {
			pm.Messages = slices.Clone(m.Messages)
		}
		if m.Bidi != grpcmock.BidiEach {
			pm.Bidi = m.Bidi
		}
		pm.Headers = rows(old.Headers, len(m.Headers), func(i int) (bool, string, string) {
			h := m.Headers[i]
			return h.Enabled, h.Name, h.Value
		})
		pm.Trailers = rows(old.Trailers, len(m.Trailers), func(i int) (bool, string, string) {
			h := m.Trailers[i]
			return h.Enabled, h.Name, h.Value
		})
		out.Methods = append(out.Methods, pm)
	}
	return out
}

func graphQLMockConfig(g *projectfile.ServerGraphQLMock) graphqlmock.Config {
	c := graphqlmock.Config{
		Path: g.Path, SchemaFile: g.SchemaFile, SchemaText: g.SchemaText, Introspection: !g.NoIntrospection,
		ListLength: graphqlmock.DefaultListLength, NullPercent: g.NullPercent, Seed: g.Seed, AllowOrigin: g.AllowOrigin,
		Overrides: make([]graphqlmock.Override, 0, len(g.Overrides)),
	}
	if g.ListLength != nil {
		c.ListLength = *g.ListLength
	}
	for _, o := range g.Overrides {
		c.Overrides = append(c.Overrides, graphqlmock.Override{
			Enabled: !o.Disabled, Target: o.Target, Args: o.Args, Mode: o.Mode, Value: o.Value, Error: o.Error, Code: o.Code,
		})
	}
	return c
}

// fromGraphQLMock writes the members that differ from a new server's
// defaults only, and the schema always.
func fromGraphQLMock(prev *projectfile.ServerGraphQLMock, c graphqlmock.Config) *projectfile.ServerGraphQLMock {
	out := &projectfile.ServerGraphQLMock{
		SchemaFile: c.SchemaFile, SchemaText: c.SchemaText, NoIntrospection: !c.Introspection,
		NullPercent: c.NullPercent, Seed: c.Seed, AllowOrigin: c.AllowOrigin,
	}
	if c.Path != graphqlmock.DefaultPath {
		out.Path = c.Path
	}
	if c.ListLength != graphqlmock.DefaultListLength {
		out.ListLength = new(c.ListLength)
	}
	var was []projectfile.ServerGraphQLOverride
	if prev != nil {
		out.Unknown, was = prev.Unknown, prev.Overrides
	}
	for i, o := range c.Overrides {
		po := projectfile.ServerGraphQLOverride{Target: o.Target, Args: o.Args, Value: o.Value, Error: o.Error, Code: o.Code, Disabled: !o.Enabled}
		if o.Mode != graphqlmock.ModeValue {
			po.Mode = o.Mode
		}
		// Members this version does not know stay with the override at the
		// same position while it has the same target.
		if i < len(was) && was[i].Target == o.Target {
			po.Unknown = was[i].Unknown
		}
		out.Overrides = append(out.Overrides, po)
	}
	return out
}

func sioConfig(s *projectfile.ServerSocketIO) sioserver.Config {
	c := sioserver.Config{
		Path: s.Path, Namespaces: slices.Clone(s.Namespaces), Auth: s.Auth, Transports: s.Transports, AllowOrigin: s.AllowOrigin,
		PingIntervalMs: s.PingIntervalMs, PingTimeoutMs: s.PingTimeoutMs, MaxPayload: s.MaxPayload, Echo: s.Echo, AckMode: s.AckMode,
		Rules: make([]sioserver.Rule, 0, len(s.Rules)),
	}
	for _, r := range s.Rules {
		c.Rules = append(c.Rules, sioserver.Rule{
			Enabled: !r.Disabled, Match: r.Match, Pattern: r.Pattern, Ack: r.Ack, Emit: r.Emit, EmitArgs: r.EmitArgs, DelayMs: r.DelayMs,
		})
	}
	return c
}

// fromSocketIO writes the members that differ from a new server's
// defaults only.
func fromSocketIO(prev *projectfile.ServerSocketIO, c sioserver.Config) *projectfile.ServerSocketIO {
	out := &projectfile.ServerSocketIO{Auth: c.Auth, Transports: c.Transports, AllowOrigin: c.AllowOrigin, Echo: c.Echo}
	if c.Path != sioserver.DefaultPath {
		out.Path = c.Path
	}
	if len(c.Namespaces) > 0 {
		out.Namespaces = slices.Clone(c.Namespaces)
	}
	if c.PingIntervalMs != sioserver.DefaultPingIntervalMs {
		out.PingIntervalMs = c.PingIntervalMs
	}
	if c.PingTimeoutMs != sioserver.DefaultPingTimeoutMs {
		out.PingTimeoutMs = c.PingTimeoutMs
	}
	if c.MaxPayload != sioserver.DefaultMaxPayload {
		out.MaxPayload = c.MaxPayload
	}
	if c.AckMode != sioserver.AckEcho {
		out.AckMode = c.AckMode
	}
	var was []projectfile.ServerSIORule
	if prev != nil {
		out.Unknown, was = prev.Unknown, prev.Rules
	}
	for i, r := range c.Rules {
		rule := projectfile.ServerSIORule{
			Match: cmp.Or(r.Match, sioserver.MatchExact), Pattern: r.Pattern, Ack: r.Ack, Emit: r.Emit, EmitArgs: r.EmitArgs,
			DelayMs: r.DelayMs, Disabled: !r.Enabled,
		}
		// Members this version does not know stay with the rule at the
		// same position when it matches the same way.
		if i < len(was) && cmp.Or(was[i].Match, sioserver.MatchExact) == rule.Match && was[i].Pattern == rule.Pattern {
			rule.Unknown = was[i].Unknown
		}
		out.Rules = append(out.Rules, rule)
	}
	return out
}
