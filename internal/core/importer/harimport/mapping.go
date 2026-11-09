package harimport

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"mime/multipart"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/yaml"
)

// maxSavedMessages caps the saved messages a WebSocket request gets from
// a recording.
const maxSavedMessages = 50

// dropped are request headers the client writes itself: HTTP/2 pseudo
// headers (":authority"…) are dropped too. Accept-Encoding is left to
// PostSonde, which asks for compression and decodes the response itself.
var dropped = map[string]bool{
	"host": true, "content-length": true, "connection": true, "keep-alive": true,
	"transfer-encoding": true, "te": true, "proxy-connection": true, "upgrade": true,
	"accept-encoding": true,
}

// keyHeaders are header names that carry an API key.
var keyHeaders = map[string]bool{"x-api-key": true, "api-key": true, "apikey": true, "x-auth-token": true}

// headerSet is a request's headers, sorted into rows to keep and the
// credentials to handle.
type headerSet struct {
	rows []projectfile.KeyValue
	// cookies are the names of the cookies sent, in order, once each.
	cookies []string
	// authorization is the first Authorization value (hasAuth: there is
	// one); proxyAuth is set for a Proxy-Authorization or second
	// Authorization header; keys are the API key headers.
	authorization string
	hasAuth       bool
	proxyAuth     bool
	keys          []projectfile.KeyValue
}

func readHeaders(rq *yaml.Node) *headerSet {
	hs := &headerSet{}
	seen := map[string]bool{}
	addCookie := func(name string) {
		if name != "" && !seen[name] {
			seen[name] = true
			hs.cookies = append(hs.cookies, name)
		}
	}
	for _, h := range items(get(rq, "headers")) {
		name := strings.TrimSpace(sget(h, "name"))
		value := sget(h, "value")
		lower := strings.ToLower(name)
		switch {
		case name == "", strings.HasPrefix(name, ":"), dropped[lower]:
		case lower == "cookie":
			if cs, err := http.ParseCookie(value); err == nil {
				for _, c := range cs {
					addCookie(c.Name)
				}
			} else {
				addCookie("(unreadable)")
			}
		case lower == "authorization" && !hs.hasAuth:
			hs.authorization, hs.hasAuth = strings.TrimSpace(value), true
		case lower == "authorization", lower == "proxy-authorization":
			hs.proxyAuth = true
		case keyHeaders[lower]:
			hs.keys = append(hs.keys, projectfile.KeyValue{Name: name, Value: value})
		default:
			hs.rows = append(hs.rows, projectfile.KeyValue{Name: name, Value: value})
		}
	}
	for _, c := range items(get(rq, "cookies")) {
		addCookie(sget(c, "name"))
	}
	return hs
}

// without returns rows without the headers named name.
func without(rows []projectfile.KeyValue, name string) []projectfile.KeyValue {
	out := rows[:0:0]
	for _, kv := range rows {
		if !strings.EqualFold(kv.Name, name) {
			out = append(out, kv)
		}
	}
	return out
}

// cookieDiag is the warning of the cookies left out; nil for none.
func (hs *headerSet) cookieDiag(at importer.Location) []importer.Diagnostic {
	if len(hs.cookies) == 0 {
		return nil
	}
	names := hs.cookies
	more := ""
	if len(names) > 5 {
		more = " and " + strconv.Itoa(len(names)-5) + " more"
		names = names[:5]
	}
	for i, n := range names {
		names[i] = clip(n)
	}
	return []importer.Diagnostic{{
		Code: CodeCookies, Severity: importer.SeverityWarning, Source: at,
		Message: "The browser's cookies (" + strings.Join(names, ", ") + more + ") were not imported: they are its session, not part of the request. PostSonde's cookie jar keeps the cookies servers set when you send.",
	}}
}

// auth makes the request's typed auth from its credential headers: the
// Authorization header (Bearer or another token scheme, Basic), else an
// API key header. Credentials that have nowhere to go are dropped with a
// warning, never kept as header rows.
func (hs *headerSet) auth(at importer.Location) (projectfile.Auth, []importer.Diagnostic) {
	a := projectfile.Auth{Type: projectfile.AuthInherit}
	var diags []importer.Diagnostic
	became := func(what string) {
		diags = append(diags, importer.Diagnostic{
			Code: CodeAuth, Severity: importer.SeverityInfo, Source: at,
			Message: "The " + what + " became the request's auth; its secret is kept on this machine only, not in the project's files.",
		})
	}
	drop := func(msg string) {
		diags = append(diags, importer.Diagnostic{Code: CodeAuthDropped, Severity: importer.SeverityWarning, Source: at, Message: msg})
	}
	if hs.hasAuth {
		scheme, cred, _ := strings.Cut(hs.authorization, " ")
		cred = strings.TrimSpace(cred)
		switch {
		case hs.authorization == "":
		case strings.EqualFold(scheme, "Bearer") && cred != "":
			a = projectfile.Auth{Type: projectfile.AuthBearer, Token: cred}
			became("Authorization header (Bearer)")
		case strings.EqualFold(scheme, "Basic"):
			raw, err := base64.StdEncoding.DecodeString(cred)
			user, pass, ok := strings.Cut(string(raw), ":")
			if err != nil || !ok || !utf8.Valid(raw) {
				drop("The Authorization header's Basic credentials do not decode; they were not imported. Set up Basic auth on the request.")
				break
			}
			a = projectfile.Auth{Type: projectfile.AuthBasic, Username: user, Password: pass}
			became("Authorization header (Basic)")
		case strings.EqualFold(scheme, "Digest"):
			drop("The Authorization header answered a Digest challenge and cannot be sent again; it was not imported. Set up Digest auth with the user's password.")
		case cred != "":
			prefix := scheme
			a = projectfile.Auth{Type: projectfile.AuthBearer, Token: cred, Prefix: &prefix}
			became("Authorization header (" + clip(scheme) + " token)")
		default:
			empty := ""
			a = projectfile.Auth{Type: projectfile.AuthBearer, Token: scheme, Prefix: &empty}
			became("Authorization header")
		}
	}
	for _, k := range hs.keys {
		if a.Type == projectfile.AuthInherit {
			a = projectfile.Auth{Type: projectfile.AuthAPIKey, Key: k.Name, Value: k.Value, In: projectfile.APIKeyInHeader}
			became(clip(k.Name) + " header")
			continue
		}
		drop("The " + clip(k.Name) + " header was not imported: the request already has its auth, and PostSonde keeps one credential per request. Add it as a secret variable.")
	}
	if hs.proxyAuth {
		drop("A second Authorization or a Proxy-Authorization header was not imported: its credentials would be written into the project.")
	}
	return a, diags
}

// mapEntry maps an entry to an http, graphql or websocket request.
func mapEntry(e, rq *yaml.Node, method string, u *url.URL, at importer.Location) *importer.Request {
	hs := readHeaders(rq)
	diags := hs.cookieDiag(at)
	if isWebSocket(e, u) {
		return mapWebSocket(e, hs, u, at, diags)
	}
	q := &importer.Request{Name: requestName(method, u), Source: at}
	auth, d := hs.auth(at)
	diags = append(diags, d...)
	desc := recorded(get(e, "response"))
	pd := get(rq, "postData")
	if g := graphQL(method, pd); g != nil {
		g.URL, g.Headers, g.Auth, g.Description = u.String(), without(hs.rows, "Content-Type"), auth, desc
		if g.OperationName != "" {
			q.Name = clipName(g.OperationName)
		}
		q.File = g
		q.Diagnostics = diags
		return q
	}
	h := &projectfile.HTTPRequest{Description: desc, Method: method, URL: u.String(), Params: queryRows(u.RawQuery), Auth: auth}
	h.Body, h.Headers, d = mapBody(pd, hs.rows, at)
	q.File = h
	q.Diagnostics = append(diags, d...)
	return q
}

// requestName is "METHOD /path", or "METHOD host" for the root path.
func requestName(method string, u *url.URL) string {
	p := u.EscapedPath()
	if p == "" || p == "/" {
		return clipName(method + " " + u.Host)
	}
	if s, err := url.PathUnescape(p); err == nil {
		p = s
	}
	return clipName(method + " " + p)
}

// recorded is the description noting the recorded response's status.
func recorded(resp *yaml.Node) string {
	status, _ := strconv.Atoi(str(get(resp, "status")))
	if status <= 0 {
		return ""
	}
	text := strings.TrimSpace(sget(resp, "statusText"))
	if text == "" {
		text = http.StatusText(status)
	}
	desc := "Recorded response: " + strconv.Itoa(status)
	if text != "" {
		desc += " " + text
	}
	if mt := strings.TrimSpace(sget(get(resp, "content"), "mimeType")); mt != "" && mt != "x-unknown" {
		if parsed, _, err := mime.ParseMediaType(mt); err == nil {
			mt = parsed
		}
		desc += " (" + mt + ")"
	}
	return desc + "."
}

// queryRows returns the entries of a raw query as rows, as written.
func queryRows(q string) []projectfile.KeyValue {
	var out []projectfile.KeyValue
	for entry := range strings.SplitSeq(q, "&") {
		if entry == "" {
			continue
		}
		k, v, _ := strings.Cut(entry, "=")
		out = append(out, projectfile.KeyValue{Name: k, Value: v})
	}
	return out
}

// graphQL returns the GraphQL request of a POST whose JSON body has a
// query; nil otherwise (persisted queries without a query text and
// batches stay http requests).
func graphQL(method string, pd *yaml.Node) *projectfile.GraphQLRequest {
	if method != "POST" || !isMap(pd) || !strings.Contains(strings.ToLower(sget(pd, "mimeType")), "json") {
		return nil
	}
	var body struct {
		Query         *string         `json:"query"`
		Variables     json.RawMessage `json:"variables"`
		OperationName *string         `json:"operationName"`
	}
	if err := json.Unmarshal([]byte(sget(pd, "text")), &body); err != nil || body.Query == nil || strings.TrimSpace(*body.Query) == "" {
		return nil
	}
	g := &projectfile.GraphQLRequest{Query: *body.Query}
	if body.OperationName != nil {
		g.OperationName = *body.OperationName
	}
	if v := bytes.TrimSpace(body.Variables); len(v) > 0 && !bytes.Equal(v, []byte("null")) && !bytes.Equal(v, []byte("{}")) {
		var buf bytes.Buffer
		if json.Indent(&buf, v, "", "  ") == nil {
			g.Variables = buf.String()
		}
	}
	return g
}

// mapBody maps postData. A multipart body's Content-Type header (its
// boundary) is dropped: PostSonde writes its own.
func mapBody(pd *yaml.Node, rows []projectfile.KeyValue, at importer.Location) (projectfile.Body, []projectfile.KeyValue, []importer.Diagnostic) {
	none := projectfile.Body{Mode: projectfile.BodyNone}
	if !isMap(pd) {
		return none, rows, nil
	}
	mimeType := sget(pd, "mimeType")
	text := sget(pd, "text")
	mt, mparams, _ := mime.ParseMediaType(mimeType)
	mt = strings.ToLower(mt)
	skipped := func(msg string) (projectfile.Body, []projectfile.KeyValue, []importer.Diagnostic) {
		return none, rows, []importer.Diagnostic{{Code: CodeBody, Severity: importer.SeverityWarning, Source: at, Message: msg}}
	}
	if enc := strings.ToLower(sget(pd, "encoding") + sget(pd, "_encoding")); enc == "base64" {
		return skipped("The body is binary (Base64 in the file) and was not imported. Send it from a file with the Binary body type.")
	}
	switch mt {
	case "application/x-www-form-urlencoded":
		b := projectfile.Body{Mode: projectfile.BodyURLEncoded}
		if text != "" {
			for entry := range strings.SplitSeq(text, "&") {
				if entry == "" {
					continue
				}
				k, v, _ := strings.Cut(entry, "=")
				b.Fields = append(b.Fields, projectfile.KeyValue{Name: unescape(k), Value: unescape(v)})
			}
		} else {
			for _, p := range items(get(pd, "params")) {
				b.Fields = append(b.Fields, projectfile.KeyValue{Name: sget(p, "name"), Value: sget(p, "value")})
			}
		}
		if len(b.Fields) == 0 {
			return none, rows, nil
		}
		return b, rows, nil
	case "multipart/form-data":
		rows = without(rows, "Content-Type")
		b := projectfile.Body{Mode: projectfile.BodyMultipart}
		files := 0
		if ps := items(get(pd, "params")); len(ps) > 0 {
			for _, p := range ps {
				part := projectfile.Part{Name: sget(p, "name"), ContentType: sget(p, "contentType")}
				if fn := sget(p, "fileName"); fn != "" {
					part.File, part.ContentType = fn, sget(p, "contentType")
					files++
				} else {
					part.Value = sget(p, "value")
				}
				b.Parts = append(b.Parts, part)
			}
		} else if text != "" {
			parts, n, err := multipartParts(text, mparams["boundary"])
			if err != nil {
				return skipped("The multipart body does not parse (" + err.Error() + ") and was not imported.")
			}
			b.Parts, files = parts, n
		}
		if len(b.Parts) == 0 {
			return none, rows, nil
		}
		var diags []importer.Diagnostic
		if files > 0 {
			diags = append(diags, importer.Diagnostic{
				Code: CodeFilePart, Severity: importer.SeverityInfo, Source: at,
				Message: plural(files, "part uploads a file", "parts upload files") + " the recording names but does not hold; choose the file on this machine before sending.",
			})
		}
		return b, rows, diags
	}
	if text == "" {
		return none, rows, nil
	}
	return projectfile.Body{Mode: projectfile.BodyRaw, Type: typeOf(mt), Text: text}, rows, nil
}

// multipartParts reads a multipart body's parts: a file part keeps its
// file name (the content is not imported), a text part its value.
func multipartParts(text, boundary string) ([]projectfile.Part, int, error) {
	if boundary == "" {
		return nil, 0, errors.New("the content type names no boundary")
	}
	partReader := multipart.NewReader(strings.NewReader(text), boundary)
	var out []projectfile.Part
	files := 0
	for {
		p, err := partReader.NextRawPart()
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, 0, errors.New("a part is cut short")
		}
		part := projectfile.Part{Name: p.FormName()}
		if fn := p.FileName(); fn != "" {
			part.File, part.ContentType = fn, p.Header.Get("Content-Type")
			files++
		} else {
			data, err := io.ReadAll(io.LimitReader(p, 1<<20))
			if err != nil {
				return nil, 0, errors.New("a part is cut short")
			}
			part.Value = string(data)
			if ct := p.Header.Get("Content-Type"); ct != "" && !strings.HasPrefix(strings.ToLower(ct), "text/plain") {
				part.ContentType = ct
			}
		}
		out = append(out, part)
	}
	return out, files, nil
}

func unescape(s string) string {
	if d, err := url.QueryUnescape(s); err == nil {
		return d
	}
	return s
}

// typeOf returns the raw body type of a media type.
func typeOf(mt string) string {
	switch {
	case strings.Contains(mt, "json"):
		return projectfile.RawJSON
	case strings.Contains(mt, "xml"):
		return projectfile.RawXML
	case strings.Contains(mt, "html"):
		return projectfile.RawHTML
	case strings.Contains(mt, "yaml"):
		return projectfile.RawYAML
	}
	return projectfile.RawText
}

// isWebSocket reports whether an entry is a WebSocket connection.
func isWebSocket(e *yaml.Node, u *url.URL) bool {
	switch strings.ToLower(u.Scheme) {
	case "ws", "wss":
		return true
	}
	return strings.EqualFold(sget(e, "_resourceType"), "websocket") || get(e, "_webSocketMessages") != nil
}

// mapWebSocket maps a WebSocket entry: its handshake headers (without
// the ones the client writes), subprotocols, and the messages sent as
// saved messages. WebSocket requests have no auth, so credentials are
// dropped.
func mapWebSocket(e *yaml.Node, hs *headerSet, u *url.URL, at importer.Location, diags []importer.Diagnostic) *importer.Request {
	ws := *u
	switch strings.ToLower(ws.Scheme) {
	case "http":
		ws.Scheme = "ws"
	case "https":
		ws.Scheme = "wss"
	}
	w := &projectfile.WebSocketRequest{URL: ws.String(), Params: queryRows(ws.RawQuery)}
	for _, kv := range hs.rows {
		lower := strings.ToLower(kv.Name)
		switch {
		case lower == "sec-websocket-protocol":
			for p := range strings.SplitSeq(kv.Value, ",") {
				if p = strings.TrimSpace(p); p != "" {
					w.Subprotocols = append(w.Subprotocols, p)
				}
			}
		case strings.HasPrefix(lower, "sec-websocket-"):
		default:
			w.Headers = append(w.Headers, kv)
		}
	}
	if hs.hasAuth || len(hs.keys) > 0 || hs.proxyAuth {
		diags = append(diags, importer.Diagnostic{
			Code: CodeAuthDropped, Severity: importer.SeverityWarning, Source: at,
			Message: "The handshake's credential headers were not imported: WebSocket requests have no auth in PostSonde, and a header would write them into the project. Add a header with a secret variable.",
		})
	}
	sent, received := 0, 0
	seen := map[string]bool{}
	for _, m := range items(get(e, "_webSocketMessages")) {
		if !strings.EqualFold(sget(m, "type"), "send") {
			received++
			continue
		}
		sent++
		data := sget(m, "data")
		format := ""
		if str(get(m, "opcode")) == "2" {
			format = projectfile.MessageBase64
		} else if isJSON(data) {
			format = projectfile.MessageJSON
		}
		if seen[format+"\n"+data] || len(w.SavedMessages) >= maxSavedMessages {
			continue
		}
		seen[format+"\n"+data] = true
		w.SavedMessages = append(w.SavedMessages, projectfile.WebSocketMessage{
			Name: "Sent message " + strconv.Itoa(sent), Text: data, Format: format,
		})
	}
	if sent+received > 0 {
		msg := plural(sent, "message was", "messages were") + " sent in the recording"
		if sent > 0 {
			msg += "; they are the request's saved messages (" + strconv.Itoa(len(w.SavedMessages)) + ", repeats left out)"
		}
		if received > 0 {
			msg += ". " + plural(received, "message received is", "messages received are") + " not imported"
		}
		diags = append(diags, importer.Diagnostic{Code: CodeWebSocket, Severity: importer.SeverityInfo, Source: at, Message: msg + "."})
	}
	name := ws.EscapedPath()
	if name == "" || name == "/" {
		name = ws.Host
	}
	return &importer.Request{Name: clipName("WebSocket " + name), File: w, Source: at, Diagnostics: diags}
}

// isJSON reports whether s is a JSON object or array.
func isJSON(s string) bool {
	t := strings.TrimSpace(s)
	return (strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")) && json.Valid([]byte(t))
}

// headerValue returns the value of a request's header name, or "".
func headerValue(rq *yaml.Node, name string) string {
	for _, h := range items(get(rq, "headers")) {
		if strings.EqualFold(strings.TrimSpace(sget(h, "name")), name) {
			return sget(h, "value")
		}
	}
	return ""
}
