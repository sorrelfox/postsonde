package insomnia

import (
	"bytes"
	"encoding/json"
	"mime"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/oauth1"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
	"github.com/sorrelfox/postsonde/internal/core/yaml"
)

// Kinds of resources, after reading either version.
const (
	kindFolder    = "folder"
	kindRequest   = "request"
	kindGRPC      = "grpc"
	kindWebSocket = "websocket"
	kindSkipped   = "skipped"
)

// res is a folder or request of either export version, with the members
// both versions have under their own names.
type res struct {
	kind   string
	id     string
	parent string
	name   string
	desc   string
	loc    importer.Location
	// sort is Insomnia's metaSortKey (v4) or sortKey (v5).
	sort    float64
	hasSort bool

	url, method string
	body        *yaml.Node
	params      *yaml.Node
	pathParams  *yaml.Node
	headers     *yaml.Node
	auth        *yaml.Node
	// env is a folder's environment (its variables).
	env *yaml.Node

	// followRedirects is "global", "on" or "off".
	followRedirects string
	// off lists the settings that are turned off (offSendCookies…).
	off []string

	preScript, postScript string

	// gRPC.
	protoFile   string // the proto file's name, if the request has one
	protoMethod string
	metadata    *yaml.Node
	reflection  bool
	// WebSocket payloads (value, mode).
	payloads []*yaml.Node

	skipped  *importer.Skipped
	children []*res
}

// mapFolder maps a folder and everything inside.
func mapFolder(r *res) *importer.Folder {
	t := &templates{}
	f := &importer.Folder{Name: folderName(r.name), Source: r.loc}
	f.Description, f.Diagnostics = withScripts(r, "folder")
	for _, v := range flatten(r.env) {
		f.Variables = append(f.Variables, projectfile.Variable{Name: v.name, Value: t.conv(v.value, "a variable")})
	}
	if nestedIn(r.env) {
		f.Diagnostics = append(f.Diagnostics, nestedDiag(r.loc))
	}
	f.Headers = rows(r.headers, t, "a header")
	var ad []importer.Diagnostic
	f.Auth, ad = mapAuth(r.auth, t, r.loc)
	f.Diagnostics = append(f.Diagnostics, ad...)
	f.Diagnostics = append(f.Diagnostics, t.diagnostics(r.loc)...)
	f.Items = mapChildren(r.children)
	return f
}

func mapChildren(children []*res) []importer.Item {
	var out []importer.Item
	for _, c := range children {
		switch c.kind {
		case kindFolder:
			out = append(out, importer.FolderItem(mapFolder(c)))
		case kindSkipped:
			out = append(out, importer.SkippedItem(c.skipped))
		default:
			out = append(out, importer.RequestItem(mapRequest(c)))
		}
	}
	return out
}

func folderName(n string) string {
	if strings.TrimSpace(n) == "" {
		return "Folder"
	}
	return strings.TrimSpace(n)
}

// mapRequest maps a request of any kind.
func mapRequest(r *res) *importer.Request {
	t := &templates{}
	q := &importer.Request{Name: strings.TrimSpace(r.name), Source: r.loc}
	if q.Name == "" {
		q.Name = "Request"
	}
	desc, diags := withScripts(r, "request")
	switch r.kind {
	case kindGRPC:
		q.File, diags = mapGRPC(r, t, desc, diags)
	case kindWebSocket:
		q.File, diags = mapWebSocket(r, t, desc, diags)
	default:
		if mimeOf(sget(r.body, "mimeType")) == "application/graphql" {
			q.File, diags = mapGraphQL(r, t, desc, diags)
		} else {
			q.File, diags = mapHTTP(r, t, desc, diags)
		}
	}
	q.Diagnostics = append(diags, t.diagnostics(r.loc)...)
	return q
}

func mapHTTP(r *res, t *templates, desc string, diags []importer.Diagnostic) (*projectfile.HTTPRequest, []importer.Diagnostic) {
	h := &projectfile.HTTPRequest{Description: desc, Method: strings.TrimSpace(r.method)}
	if h.Method == "" {
		h.Method = "GET"
	}
	h.URL, h.Params = urlWithParams(t.conv(strings.TrimSpace(r.url), "the URL"), rows(r.params, t, "a query parameter"))
	h.PathParams = pathParams(h.URL, r.pathParams, t)
	h.Headers = rows(r.headers, t, "a header")
	var d []importer.Diagnostic
	h.Body, h.Headers, d = mapBody(r.body, h.Headers, t, r.loc)
	diags = append(diags, d...)
	h.Auth, d = mapAuth(r.auth, t, r.loc)
	diags = append(diags, d...)
	h.Settings = settings(r)
	diags = append(diags, settingsDiag(r, offSendCookies, offStoreCookies, offEncodeURL)...)
	return h, diags
}

func mapGraphQL(r *res, t *templates, desc string, diags []importer.Diagnostic) (*projectfile.GraphQLRequest, []importer.Diagnostic) {
	g := &projectfile.GraphQLRequest{Description: desc}
	if m := strings.ToUpper(strings.TrimSpace(r.method)); m != "" && m != "POST" {
		g.Method = m
	}
	g.URL, _ = urlWithParams(t.conv(strings.TrimSpace(r.url), "the URL"), rows(r.params, t, "a query parameter"))
	g.Headers = rows(r.headers, t, "a header")
	text := sget(r.body, "text")
	var body struct {
		Query         string          `json:"query"`
		Variables     json.RawMessage `json:"variables"`
		OperationName string          `json:"operationName"`
	}
	if err := json.Unmarshal([]byte(text), &body); err != nil && strings.TrimSpace(text) != "" {
		g.Query = t.conv(text, "the query")
		diags = append(diags, importer.Diagnostic{
			Code: CodeGraphQLBody, Severity: importer.SeverityWarning, Source: r.loc,
			Message: "The GraphQL body is not the JSON Insomnia writes; it was imported as the query.",
		})
	} else {
		g.Query = t.conv(body.Query, "the query")
		g.OperationName = body.OperationName
		if v := bytes.TrimSpace(body.Variables); len(v) > 0 && !bytes.Equal(v, []byte("null")) && !bytes.Equal(v, []byte("{}")) {
			var out bytes.Buffer
			if json.Indent(&out, v, "", "  ") == nil {
				v = out.Bytes()
			}
			g.Variables = t.conv(string(v), "the variables")
		}
	}
	var d []importer.Diagnostic
	g.Auth, d = mapAuth(r.auth, t, r.loc)
	diags = append(diags, d...)
	g.Settings = settings(r)
	diags = append(diags, settingsDiag(r, offSendCookies, offStoreCookies, offEncodeURL)...)
	return g, diags
}

func mapGRPC(r *res, t *templates, desc string, diags []importer.Diagnostic) (*projectfile.GRPCRequest, []importer.Diagnostic) {
	g := &projectfile.GRPCRequest{Description: desc}
	addr := t.conv(strings.TrimSpace(r.url), "the address")
	lower := strings.ToLower(addr)
	for _, p := range []string{"grpcs://", "https://"} {
		if strings.HasPrefix(lower, p) {
			addr, g.Settings.TLS = addr[len(p):], true
		}
	}
	for _, p := range []string{"grpc://", "http://"} {
		if strings.HasPrefix(strings.ToLower(addr), p) {
			addr = addr[len(p):]
		}
	}
	g.Address = strings.TrimSuffix(addr, "/")
	if m := strings.Trim(r.protoMethod, "/"); m != "" {
		if i := strings.LastIndexByte(m, '/'); i >= 0 {
			g.Service, g.Method = m[:i], m[i+1:]
		} else {
			g.Method = m
		}
	}
	g.Message = t.conv(sget(r.body, "text"), "the message")
	g.Metadata = rows(r.metadata, t, "the metadata")
	if r.protoFile != "" {
		diags = append(diags, importer.Diagnostic{
			Code: CodeProtoSkipped, Severity: importer.SeverityWarning, Source: r.loc,
			Message: "The .proto file “" + r.protoFile + "” is kept inside the Insomnia export and was not imported: save it as a file and add it to the request's proto setup.",
		})
	} else if r.reflection {
		diags = append(diags, importer.Diagnostic{
			Code: CodeProtoSkipped, Severity: importer.SeverityWarning, Source: r.loc,
			Message: "The request used the Buf reflection API, which PostSonde does not use: add the .proto files to the request's proto setup.",
		})
	}
	return g, diags
}

func mapWebSocket(r *res, t *templates, desc string, diags []importer.Diagnostic) (*projectfile.WebSocketRequest, []importer.Diagnostic) {
	w := &projectfile.WebSocketRequest{Description: desc}
	w.URL, w.Params = urlWithParams(t.conv(strings.TrimSpace(r.url), "the URL"), rows(r.params, t, "a query parameter"))
	w.Headers = rows(r.headers, t, "a header")
	for i, p := range r.payloads {
		text := t.conv(sget(p, "value"), "the message")
		format := projectfile.MessageText
		if strings.Contains(strings.ToLower(sget(p, "mode")), "json") {
			format = projectfile.MessageJSON
		}
		if i == 0 {
			w.Message, w.MessageFormat = text, format
			continue
		}
		w.SavedMessages = append(w.SavedMessages, projectfile.WebSocketMessage{Name: "Message " + itoa(i+1), Text: text, Format: format})
	}
	if a := r.auth; isMap(a) {
		if typ := strings.ToLower(sget(a, "type")); typ != "" && typ != "none" && typ != "inherit" && !isTrue(get(a, "disabled")) {
			diags = append(diags, importer.Diagnostic{
				Code: CodeAuthUnsupported, Severity: importer.SeverityWarning, Source: r.loc,
				Message: "The WebSocket request's " + authName(typ) + " auth was not imported: PostSonde's WebSocket requests have no auth settings. Add an Authorization header instead.",
			})
		}
	}
	if slices.Contains(r.off, offSendCookies) {
		no := false
		w.Settings.SendCookies = &no
	}
	return w, append(diags, settingsDiag(r, offSendCookies)...)
}

// rows maps name/value rows (headers, parameters, metadata, form fields).
// Rows without a name and a value are left out.
func rows(n *yaml.Node, t *templates, where string) []projectfile.KeyValue {
	var out []projectfile.KeyValue
	for _, it := range items(n) {
		name, value := sget(it, "name"), sget(it, "value")
		if name == "" && value == "" {
			continue
		}
		out = append(out, projectfile.KeyValue{
			Name:        t.conv(name, where),
			Value:       t.conv(value, where),
			Description: sget(it, "description"),
			Disabled:    isTrue(get(it, "disabled")),
		})
	}
	return out
}

// urlWithParams returns the URL with the enabled parameter rows added to
// its query, as PostSonde keeps them (the URL holds the enabled rows), and
// the full parameter table: the URL's own query entries, then the rows.
func urlWithParams(u string, params []projectfile.KeyValue) (string, []projectfile.KeyValue) {
	table := queryRows(u)
	var add []string
	for _, p := range params {
		table = append(table, p)
		if p.Disabled {
			continue
		}
		entry := encodeQuery(p.Name, "&#=")
		if p.Value != "" {
			entry += "=" + encodeQuery(p.Value, "&#")
		}
		add = append(add, entry)
	}
	if len(add) == 0 {
		return u, table
	}
	frag := ""
	if i := strings.IndexByte(u, '#'); i >= 0 && !insideVar(u, i) {
		u, frag = u[:i], u[i:]
	}
	sep := "?"
	if strings.Contains(u, "?") {
		sep = "&"
		if strings.HasSuffix(u, "?") || strings.HasSuffix(u, "&") {
			sep = ""
		}
	}
	return u + sep + strings.Join(add, "&") + frag, table
}

// queryRows returns the query entries of u as rows, as written.
func queryRows(u string) []projectfile.KeyValue {
	if i := strings.IndexByte(u, '#'); i >= 0 {
		u = u[:i]
	}
	_, q, ok := strings.Cut(u, "?")
	if !ok {
		return nil
	}
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

// encodeQuery percent-encodes chars outside {{variables}}, as the editor
// does when it writes a row into the URL.
func encodeQuery(s, chars string) string {
	if !strings.ContainsAny(s, chars) {
		return s
	}
	var b strings.Builder
	for _, seg := range template.Parse(s) {
		raw := s[seg.Start:seg.End]
		if seg.Kind == template.Var {
			b.WriteString(raw)
			continue
		}
		for i := 0; i < len(raw); i++ {
			switch c := raw[i]; {
			case c == '&' && strings.IndexByte(chars, c) >= 0:
				b.WriteString("%26")
			case c == '#' && strings.IndexByte(chars, c) >= 0:
				b.WriteString("%23")
			case c == '=' && strings.IndexByte(chars, c) >= 0:
				b.WriteString("%3D")
			default:
				b.WriteByte(c)
			}
		}
	}
	return b.String()
}

// insideVar reports whether byte i of s is inside a {{variable}}.
func insideVar(s string, i int) bool {
	for _, seg := range template.Parse(s) {
		if seg.Kind == template.Var && seg.Start <= i && i < seg.End {
			return true
		}
	}
	return false
}

// pathParams returns the path parameter rows: Insomnia's values for the
// URL's :name placeholders, and an empty row for each other placeholder.
func pathParams(u string, n *yaml.Node, t *templates) []projectfile.KeyValue {
	var out []projectfile.KeyValue
	names := request.PathParamNames(u)
	for _, it := range items(n) {
		name := sget(it, "name")
		if name == "" {
			continue
		}
		out = append(out, projectfile.KeyValue{Name: name, Value: t.conv(sget(it, "value"), "a path parameter")})
	}
	for _, name := range names {
		if !slices.ContainsFunc(out, func(kv projectfile.KeyValue) bool { return kv.Name == name }) {
			out = append(out, projectfile.KeyValue{Name: name})
		}
	}
	return out
}

func mimeOf(s string) string {
	s = strings.TrimSpace(s)
	if mt, _, err := mime.ParseMediaType(s); err == nil {
		return mt
	}
	return strings.ToLower(s)
}

// mapBody maps an HTTP body. A Content-Type Insomnia would send and
// PostSonde would not is added to the headers.
func mapBody(n *yaml.Node, headers []projectfile.KeyValue, t *templates, loc importer.Location) (projectfile.Body, []projectfile.KeyValue, []importer.Diagnostic) {
	var diags []importer.Diagnostic
	raw := sget(n, "mimeType")
	mt := mimeOf(raw)
	text := sget(n, "text")
	file := sget(n, "fileName")
	switch {
	case mt == "application/x-www-form-urlencoded":
		return projectfile.Body{Mode: projectfile.BodyURLEncoded, Fields: rows(get(n, "params"), t, "the form")}, headers, nil
	case mt == "multipart/form-data":
		b := projectfile.Body{Mode: projectfile.BodyMultipart}
		files := false
		for _, it := range items(get(n, "params")) {
			p := projectfile.Part{
				Name:        t.conv(sget(it, "name"), "the form"),
				Description: sget(it, "description"),
				Disabled:    isTrue(get(it, "disabled")),
			}
			if sget(it, "type") == "file" {
				p.File = sget(it, "fileName")
				files = files || p.File != ""
			} else {
				p.Value = t.conv(sget(it, "value"), "the form")
			}
			if p.Name == "" && p.Value == "" && p.File == "" {
				continue
			}
			b.Parts = append(b.Parts, p)
		}
		if files {
			diags = append(diags, filePathDiag(loc))
		}
		return b, headers, diags
	case file != "" || mt == "application/octet-stream":
		if file == "" {
			return projectfile.Body{Mode: projectfile.BodyNone}, headers, nil
		}
		b := projectfile.Body{Mode: projectfile.BodyFile, Path: file}
		if mt != "" && mt != "application/octet-stream" {
			b.ContentType = raw
		}
		return b, headers, append(diags, filePathDiag(loc))
	case text == "":
		return projectfile.Body{Mode: projectfile.BodyNone}, headers, nil
	}
	typ := projectfile.RawText
	switch {
	case strings.Contains(mt, "json"):
		typ = projectfile.RawJSON
	case strings.Contains(mt, "xml"):
		typ = projectfile.RawXML
	case strings.Contains(mt, "html"):
		typ = projectfile.RawHTML
	case strings.Contains(mt, "yaml") || strings.Contains(mt, "yml"):
		typ = projectfile.RawYAML
	}
	if raw != "" && mt != request.RawMediaType(typ) && !hasHeader(headers, "Content-Type") {
		headers = append(headers, projectfile.KeyValue{Name: "Content-Type", Value: raw})
	}
	return projectfile.Body{Mode: projectfile.BodyRaw, Type: typ, Text: t.conv(text, "the body")}, headers, diags
}

func hasHeader(rows []projectfile.KeyValue, name string) bool {
	return slices.ContainsFunc(rows, func(kv projectfile.KeyValue) bool {
		return !kv.Disabled && strings.EqualFold(strings.TrimSpace(kv.Name), name)
	})
}

func filePathDiag(loc importer.Location) importer.Diagnostic {
	return importer.Diagnostic{
		Code: CodeFilePath, Severity: importer.SeverityInfo, Source: loc,
		Message: "The body sends a file by its path on the machine the export was made on; check that the path exists here.",
	}
}

// mapAuth maps Insomnia's authentication object. An empty object is
// "inherit" (Insomnia's "Inherit from parent", and no auth in versions
// without folder auth); a type PostSonde lacks becomes "none" with a
// warning.
func mapAuth(n *yaml.Node, t *templates, loc importer.Location) (projectfile.Auth, []importer.Diagnostic) {
	typ := strings.ToLower(strings.TrimSpace(sget(n, "type")))
	disabled := isTrue(get(n, "disabled"))
	c := func(key string) string { return t.conv(sget(n, key), "the auth") }
	warn := func(code, msg string) []importer.Diagnostic {
		return []importer.Diagnostic{{Code: code, Severity: importer.SeverityWarning, Source: loc, Message: msg}}
	}
	switch typ {
	case "", "inherit":
		return projectfile.Auth{Type: projectfile.AuthInherit}, nil
	case "none":
		return projectfile.Auth{Type: projectfile.AuthNone}, nil
	case "basic", "digest":
		a := projectfile.Auth{Type: projectfile.AuthBasic, Username: c("username"), Password: c("password"), Disabled: disabled}
		if typ == "digest" {
			a.Type = projectfile.AuthDigest
		}
		if isTrue(get(n, "useISO88591")) {
			return a, warn(CodeAuthChanged, "The Basic auth was set to encode the credentials as ISO-8859-1; PostSonde encodes them as UTF-8.")
		}
		return a, nil
	case "bearer":
		a := projectfile.Auth{Type: projectfile.AuthBearer, Token: c("token"), Disabled: disabled}
		if p := c("prefix"); strings.TrimSpace(p) != "" && p != projectfile.DefaultBearerPrefix {
			a.Prefix = &p
		}
		return a, nil
	case "apikey":
		a := projectfile.Auth{Type: projectfile.AuthAPIKey, Key: c("key"), Value: c("value"), In: projectfile.APIKeyInHeader, Disabled: disabled}
		switch sget(n, "addTo") {
		case "queryParams":
			a.In = projectfile.APIKeyInQuery
		case "cookie":
			a.Key, a.Value = "Cookie", a.Key+"="+a.Value
			return a, warn(CodeAuthChanged, "The API key was sent as a cookie; it is imported as a Cookie header holding the key.")
		}
		return a, nil
	case "oauth1":
		return oauth1Auth(n, c, disabled, warn)
	case "oauth2":
		return oauth2Auth(n, c, disabled, warn)
	case "netrc":
		// Insomnia's netrc auth has no settings: curl reads ~/.netrc, which
		// is the file PostSonde's netrc auth reads without netrcFile.
		return projectfile.Auth{Type: projectfile.AuthNetrc, Disabled: disabled}, nil
	}
	if disabled {
		return projectfile.Auth{Type: projectfile.AuthNone}, []importer.Diagnostic{{
			Code: CodeAuthUnsupported, Severity: importer.SeverityInfo, Source: loc,
			Message: "The " + authName(typ) + " auth was turned off, and PostSonde has no such auth: the request sends no auth.",
		}}
	}
	return projectfile.Auth{Type: projectfile.AuthNone}, warn(CodeAuthUnsupported,
		"PostSonde has no "+authName(typ)+" auth: the request sends no auth. Set up the credentials by hand, e.g. as a header.")
}

// oauth1Auth maps Insomnia's OAuth 1.0 auth (AUTH-15), which Insomnia sends
// in the Authorization header: the consumer key and secret, tokenKey (the
// access token) and the token secret, the signature method (RSA-SHA1
// signs with privateKey, which goes in the consumer secret member), the
// fixed timestamp and nonce, realm, callback, verifier and
// includeBodyHash. Insomnia sends oauth_version 1.0 also when version is
// empty; another version is a warning.
func oauth1Auth(n *yaml.Node, c func(string) string, disabled bool, warn func(code, msg string) []importer.Diagnostic) (projectfile.Auth, []importer.Diagnostic) {
	a := projectfile.Auth{
		Type:            projectfile.AuthOAuth1,
		ConsumerKey:     c("consumerKey"),
		ConsumerSecret:  c("consumerSecret"),
		AccessToken:     c("tokenKey"),
		TokenSecret:     c("tokenSecret"),
		SignatureMethod: strings.ToUpper(strings.TrimSpace(sget(n, "signatureMethod"))),
		Callback:        c("callback"),
		Verifier:        c("verifier"),
		Realm:           c("realm"),
		Nonce:           c("nonce"),
		Timestamp:       c("timestamp"),
		BodyHash:        isTrue(get(n, "includeBodyHash")),
		Disabled:        disabled,
	}
	if strings.HasPrefix(a.SignatureMethod, "RSA-") {
		// The token secret is not used by RSA signatures.
		a.ConsumerSecret, a.TokenSecret = c("privateKey"), ""
	}
	var changed []string
	if v := strings.TrimSpace(sget(n, "version")); v != "" && v != oauth1.Version {
		changed = append(changed, "It sent oauth_version "+strconv.Quote(clipText(v))+"; PostSonde sends 1.0.")
	}
	if a.SignatureMethod != "" && !oauth1.Supported(a.SignatureMethod) {
		changed = append(changed, "PostSonde cannot sign with "+clipText(a.SignatureMethod)+": choose HMAC-SHA1, HMAC-SHA256, RSA-SHA1 or PLAINTEXT before sending.")
	}
	if len(changed) == 0 {
		return a, nil
	}
	return a, warn(CodeAuthChanged, "The OAuth 1.0 auth was imported with a change. "+strings.Join(changed, " "))
}

// clipText shortens a value quoted in a message to at most 40 bytes, cut
// between characters.
func clipText(s string) string {
	if len(s) <= 40 {
		return s
	}
	i := 40
	for i > 0 && !utf8.RuneStart(s[i]) {
		i--
	}
	return s[:i] + "…"
}

// authName names an Insomnia auth type for messages.
func authName(typ string) string {
	switch typ {
	case "oauth2":
		return "OAuth 2.0"
	case "oauth1":
		return "OAuth 1.0"
	case "iam":
		return "AWS IAM"
	case "hawk":
		return "Hawk"
	case "ntlm":
		return "NTLM"
	case "asap":
		return "Atlassian ASAP"
	case "netrc":
		return "Netrc file"
	case "basic":
		return "Basic"
	case "digest":
		return "Digest"
	case "bearer":
		return "Bearer"
	case "apikey":
		return "API key"
	}
	if len(typ) > 40 {
		typ = typ[:40]
	}
	return "“" + typ + "”"
}

func settings(r *res) projectfile.RequestSettings {
	var s projectfile.RequestSettings
	switch r.followRedirects {
	case "on":
		v := true
		s.FollowRedirects = &v
	case "off":
		v := false
		s.FollowRedirects = &v
	}
	no := false
	for _, o := range r.off {
		switch o {
		case offSendCookies:
			s.SendCookies = &no
		case offStoreCookies:
			s.StoreCookies = &no
		case offEncodeURL:
			// Insomnia sends the URL as typed.
			s.URLEncoding = projectfile.URLEncodingNone
		}
	}
	return s
}

// Settings Insomnia turns off, in res.off.
const (
	offSendCookies  = "send cookies"
	offStoreCookies = "store cookies"
	offEncodeURL    = "encode the URL"
	offRebuildPath  = "rebuild the path"
)

// settingsDiag reports the turned-off settings PostSonde has no
// equivalent of; mapped says which ones the request kind maps.
func settingsDiag(r *res, mapped ...string) []importer.Diagnostic {
	var lost []string
	for _, o := range r.off {
		if !slices.Contains(mapped, o) {
			lost = append(lost, o)
		}
	}
	if len(lost) == 0 {
		return nil
	}
	return []importer.Diagnostic{{
		Code: CodeSettingSkipped, Severity: importer.SeverityInfo, Source: r.loc,
		Message: "Insomnia settings PostSonde does not have were turned off and are not imported: " + strings.Join(lost, ", ") + ".",
	}}
}

// withScripts returns the description with the scripts appended, and a
// warning for each script.
func withScripts(r *res, what string) (string, []importer.Diagnostic) {
	desc := strings.TrimSpace(r.desc)
	var diags []importer.Diagnostic
	for _, s := range []struct{ name, text string }{{"pre-request", r.preScript}, {"after-response", r.postScript}} {
		if strings.TrimSpace(s.text) == "" {
			continue
		}
		if desc != "" {
			desc += "\n\n"
		}
		desc += "Insomnia " + s.name + " script (not run by PostSonde):\n\n```js\n" + strings.TrimRight(s.text, "\n") + "\n```"
		diags = append(diags, importer.Diagnostic{
			Code: CodeScript, Severity: importer.SeverityWarning, Source: r.loc,
			Message: "The " + what + "'s " + s.name + " script is kept in its description: PostSonde does not run Insomnia scripts.",
		})
	}
	return desc, diags
}

// value is a variable read from an Insomnia environment.
type value struct {
	name, value string
}

// flatten returns an environment object's variables in order. Values
// nested in objects and lists become variables with dotted names (a.b,
// list.0), as {{ _.a.b }} and {{ _.list[0] }} are converted.
func flatten(n *yaml.Node) []value {
	var out []value
	var walk func(prefix string, n *yaml.Node, depth int)
	walk = func(prefix string, n *yaml.Node, depth int) {
		n = n.Resolve()
		if n == nil || depth > 64 {
			return
		}
		switch n.Kind {
		case yaml.MappingNode:
			for _, p := range n.Pairs {
				k := str(p.Key)
				if k == "" {
					continue
				}
				walk(join(prefix, k), p.Value, depth+1)
			}
		case yaml.SequenceNode:
			for i, it := range n.Items {
				walk(join(prefix, itoa(i)), it, depth+1)
			}
		default:
			if prefix != "" {
				out = append(out, value{name: prefix, value: str(n)})
			}
		}
	}
	if isMap(n) {
		walk("", n, 0)
	}
	return out
}

// nestedIn reports whether an environment object has nested values.
func nestedIn(n *yaml.Node) bool {
	n = n.Resolve()
	if !isMap(n) {
		return false
	}
	for _, p := range n.Pairs {
		if v := p.Value.Resolve(); v != nil && (v.Kind == yaml.MappingNode || v.Kind == yaml.SequenceNode) {
			return true
		}
	}
	return false
}

func nestedDiag(loc importer.Location) importer.Diagnostic {
	return importer.Diagnostic{
		Code: CodeNestedValue, Severity: importer.SeverityInfo, Source: loc,
		Message: "Values nested in objects or lists became variables with dotted names (a.b, list.0), which the converted {{ _.a.b }} references use.",
	}
}

func join(prefix, k string) string {
	if prefix == "" {
		return k
	}
	return prefix + "." + k
}

func itoa(i int) string { return strconv.Itoa(i) }
