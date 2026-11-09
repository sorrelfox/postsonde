package altair

import (
	"bytes"
	"encoding/json"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/yaml"
)

// parseWindowFile reads a window export (.agq): one request in the
// import's folder.
func parseWindowFile(root *yaml.Node) *importer.Model {
	loc := importer.Location{Line: root.Line, Column: root.Column}
	r := window(root, loc)
	return &importer.Model{Name: r.Name, Root: importer.Folder{Name: r.Name, Items: []importer.Item{importer.RequestItem(r)}}}
}

// window maps an Altair window (a window export, or a query of a
// collection) to a graphql request. Members differ between Altair
// versions; missing ones are empty.
func window(n *yaml.Node, loc importer.Location) *importer.Request {
	r := &importer.Request{Name: windowName(n), Source: loc}
	g := &projectfile.GraphQLRequest{
		URL:              strings.TrimSpace(sget(n, "apiUrl")),
		Query:            sget(n, "query"),
		Variables:        jsonText(get(n, "variables")),
		Extensions:       jsonText(get(n, "requestExtensions")),
		ConnectionParams: jsonText(get(n, "subscriptionConnectionParams")),
		OperationName:    strings.TrimSpace(sget(n, "selectedOperation")),
		Headers:          headers(get(n, "headers")),
	}
	var diags []importer.Diagnostic
	add := func(code string, sev importer.Severity, msg string) {
		diags = append(diags, importer.Diagnostic{Code: code, Severity: sev, Source: loc, Message: msg})
	}

	// Method: Altair sends POST unless told otherwise.
	method := strings.ToUpper(strings.TrimSpace(sget(n, "selectedHttpVerb")))
	if method == "" {
		method = strings.ToUpper(strings.TrimSpace(sget(n, "httpVerb")))
	}
	switch method {
	case "", "POST":
	case "GET":
		g.Method = "GET"
	default:
		add(CodeMethod, importer.SeverityWarning, "The request used the "+clip(method)+" method; PostSonde sends GraphQL requests with POST or GET, so it is sent with POST.")
	}

	g.Auth, diags = auth(n, loc, diags)

	var d []importer.Diagnostic
	desc := ""
	desc, d = subscriptionURL(desc, g.URL, strings.TrimSpace(sget(n, "subscriptionUrl")), loc)
	diags = append(diags, d...)
	diags = append(diags, transports(n, loc)...)
	if g.Files = fileVariables(get(n, "fileVariables")); len(g.Files) > 0 {
		add(CodeFiles, importer.SeverityInfo, "The request's file variables are imported without their files, which Altair does not export: choose each file in the request's Files tab.")
	}
	desc, d = scripts(desc, "request", loc,
		script{"pre-request", get(n, "preRequestScript"), get(n, "preRequestScriptEnabled")},
		script{"post-request", get(n, "postRequestScript"), get(n, "postRequestScriptEnabled")})
	diags = append(diags, d...)
	g.Description = desc
	r.File, r.Diagnostics = g, diags
	return r
}

// reOperation finds the first named operation of a GraphQL document.
var reOperation = regexp.MustCompile(`(?m)^\s*(?:query|mutation|subscription)\s+([_A-Za-z][_0-9A-Za-z]*)`)

// windowName is the window's name (windowName; title in some versions),
// else the name of its first operation, else "Untitled".
func windowName(n *yaml.Node) string {
	for _, key := range []string{"windowName", "title", "name"} {
		if s := strings.TrimSpace(sget(n, key)); s != "" {
			return s
		}
	}
	if m := reOperation.FindStringSubmatch(sget(n, "query")); m != nil {
		return m[1]
	}
	return "Untitled"
}

// jsonText returns a JSON editor's text: Altair keeps variables,
// extensions and connection params as the text typed, usually a string;
// an object in the file is written as indented JSON. An empty object
// ("{}", Altair's default) is left out.
func jsonText(n *yaml.Node) string {
	n = n.Resolve()
	if n == nil {
		return ""
	}
	var text string
	if n.Kind == yaml.ScalarNode {
		text = str(n)
	} else {
		data, err := n.JSON()
		if err != nil {
			return ""
		}
		var buf bytes.Buffer
		if json.Indent(&buf, data, "", "  ") != nil {
			return string(data)
		}
		text = buf.String()
	}
	if t := strings.TrimSpace(text); t == "" || t == "{}" {
		return ""
	}
	return text
}

// headers maps Altair's header rows ({key, value, enabled}); rows without
// a name and value (the editor's empty row) are left out.
func headers(n *yaml.Node) []projectfile.KeyValue {
	var out []projectfile.KeyValue
	for _, h := range items(n) {
		key, value := strings.TrimSpace(sget(h, "key")), sget(h, "value")
		if key == "" && value == "" {
			continue
		}
		out = append(out, projectfile.KeyValue{Name: key, Value: value, Disabled: isFalse(get(h, "enabled"))})
	}
	return out
}

// fileVariables maps Altair's file variables ({name, isMultiple, data})
// to GraphQL upload rows (GQL-07), bound where Altair binds them:
// variables.<name>, or variables.<name>.0, .1… for a list, one per file
// of data (at least one). The files are left to choose: data is a
// browser File, exported as an empty object. Rows without a name (the
// editor's empty row) are left out.
func fileVariables(n *yaml.Node) []projectfile.Part {
	var out []projectfile.Part
	for _, f := range items(n) {
		name := strings.TrimSpace(sget(f, "name"))
		if name == "" {
			continue
		}
		path := "variables." + name
		if !isTrue(get(f, "isMultiple")) {
			out = append(out, projectfile.Part{Name: path})
			continue
		}
		for i := range max(1, len(items(get(f, "data")))) {
			out = append(out, projectfile.Part{Name: path + "." + strconv.Itoa(i)})
		}
	}
	return out
}

// subscriptionURL returns desc with a note of Altair's subscription URL
// when it is not the one PostSonde uses: the endpoint with ws:// or
// wss:// (docs/graphql-subscriptions.md).
func subscriptionURL(desc, endpoint, sub string, loc importer.Location) (string, []importer.Diagnostic) {
	if sub == "" || sameSubscriptionURL(endpoint, sub) {
		return desc, nil
	}
	desc = appendNote(desc, "Altair subscription URL: <"+sub+">")
	return desc, []importer.Diagnostic{{
		Code: CodeSubscriptionURL, Severity: importer.SeverityWarning, Source: loc,
		Message: "The request's subscription URL is not its endpoint with ws:// or wss://, which PostSonde uses for subscriptions. It is kept in the description; make a separate request with it as the endpoint to run subscriptions there.",
	}}
}

// sameSubscriptionURL reports whether sub is the URL PostSonde opens for
// endpoint's subscriptions: http becomes ws and https wss.
func sameSubscriptionURL(endpoint, sub string) bool {
	if endpoint == sub {
		return true
	}
	e, err1 := url.Parse(endpoint)
	s, err2 := url.Parse(sub)
	if err1 != nil || err2 != nil {
		return false
	}
	switch strings.ToLower(e.Scheme) {
	case "http":
		e.Scheme = "ws"
	case "https":
		e.Scheme = "wss"
	}
	s.Scheme = strings.ToLower(s.Scheme)
	return e.String() == s.String()
}

// Altair's request handlers (requestHandlerId, subscriptionRequestHandlerId)
// and the older subscriptionProvider names.
var handlerNames = map[string]string{
	"websocket":    "WebSocket (subscriptions-transport-ws)",
	"graphql-ws":   "graphql-ws",
	"app-sync":     "AWS AppSync",
	"action-cable": "Action Cable",
	"socket-io":    "Socket.IO",
	"graphql-sse":  "GraphQL over SSE",
}

// transports reports handlers PostSonde does not use: queries are sent
// over HTTP; subscriptions run over WebSocket with graphql-transport-ws
// (graphql-ws), so another subscription protocol may not work with the
// server.
func transports(n *yaml.Node, loc importer.Location) []importer.Diagnostic {
	var out []importer.Diagnostic
	if h := strings.TrimSpace(sget(n, "requestHandlerId")); h != "" && h != "http" {
		out = append(out, importer.Diagnostic{Code: CodeTransport, Severity: importer.SeverityWarning, Source: loc,
			Message: "Altair sent the request's queries with the " + handlerName(h) + " handler; PostSonde sends them over HTTP."})
	}
	sub := strings.TrimSpace(sget(n, "subscriptionRequestHandlerId"))
	if isTrue(get(n, "subscriptionUseDefaultRequestHandler")) {
		sub = ""
	}
	if sub == "" {
		sub = strings.TrimSpace(sget(n, "subscriptionProvider"))
	}
	switch sub {
	case "", "graphql-ws":
	case "websocket":
		out = append(out, importer.Diagnostic{Code: CodeTransport, Severity: importer.SeverityInfo, Source: loc,
			Message: "Altair ran the request's subscriptions with the legacy subscriptions-transport-ws protocol; PostSonde uses graphql-transport-ws, which the server must support."})
	default:
		out = append(out, importer.Diagnostic{Code: CodeTransport, Severity: importer.SeverityWarning, Source: loc,
			Message: "Altair ran the request's subscriptions with the " + handlerName(sub) + " handler, which PostSonde does not have; PostSonde runs subscriptions over WebSocket with graphql-transport-ws."})
	}
	return out
}

func handlerName(id string) string {
	if n := handlerNames[id]; n != "" {
		return n
	}
	return "“" + clip(id) + "”"
}

// auth maps Altair's authorization (authorizationType, authorizationData).
// No auth, and Altair's "none" (every window's default), inherit: Altair
// has no auth above a window, so nothing is sent unless a folder gets one.
func auth(n *yaml.Node, loc importer.Location, diags []importer.Diagnostic) (projectfile.Auth, []importer.Diagnostic) {
	typ := strings.ToLower(strings.TrimSpace(sget(n, "authorizationType")))
	data := get(n, "authorizationData")
	switch typ {
	case "", "none":
		return projectfile.Auth{Type: projectfile.AuthInherit}, diags
	case "basic":
		return projectfile.Auth{Type: projectfile.AuthBasic, Username: sget(data, "username"), Password: sget(data, "password")}, diags
	case "bearer":
		return projectfile.Auth{Type: projectfile.AuthBearer, Token: sget(data, "token")}, diags
	case "api-key", "apikey", "api_key":
		key, value := sget(data, "headerName"), sget(data, "headerValue")
		if key == "" {
			key = sget(data, "key")
		}
		if value == "" {
			value = sget(data, "value")
		}
		return projectfile.Auth{Type: projectfile.AuthAPIKey, Key: key, Value: value, In: projectfile.APIKeyInHeader}, diags
	}
	name := "“" + clip(typ) + "”"
	if typ == "oauth2" {
		name = "OAuth 2.0"
	}
	return projectfile.Auth{Type: projectfile.AuthNone}, append(diags, importer.Diagnostic{
		Code: CodeAuthUnsupported, Severity: importer.SeverityWarning, Source: loc,
		Message: "The request's " + name + " authorization is not imported: PostSonde has no such auth. Set up Bearer auth with a token instead.",
	})
}

// script is one of an item's scripts: its text and its enabled flag.
type script struct {
	name    string
	text    *yaml.Node
	enabled *yaml.Node
}

// scripts returns desc with each non-empty script appended as a code
// block, and a warning per script. what is "request" or "collection".
func scripts(desc, what string, loc importer.Location, list ...script) (string, []importer.Diagnostic) {
	var diags []importer.Diagnostic
	for _, s := range list {
		text := str(s.text)
		if strings.TrimSpace(text) == "" {
			continue
		}
		state := "not run by PostSonde"
		if isFalse(s.enabled) {
			state = "turned off in Altair; not run by PostSonde"
		}
		desc = appendNote(desc, "Altair "+s.name+" script ("+state+"):\n\n"+fenced("js", text))
		diags = append(diags, importer.Diagnostic{
			Code: CodeScript, Severity: importer.SeverityWarning, Source: loc,
			Message: "The " + what + "'s " + s.name + " script is kept in its description: PostSonde does not run Altair scripts.",
		})
	}
	return desc, diags
}

// appendNote adds a section to a description, after a blank line.
func appendNote(desc, note string) string {
	if desc == "" {
		return note
	}
	return desc + "\n\n" + note
}

// fenced returns text in a Markdown code block of language lang, with a
// fence longer than any run of backticks in text.
func fenced(lang, text string) string {
	longest, run := 0, 0
	for i := 0; i < len(text); i++ {
		if text[i] == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", max(3, longest+1))
	return fence + lang + "\n" + strings.TrimRight(text, "\n") + "\n" + fence
}

// clip shortens a name from the file for a message.
func clip(s string) string {
	if len(s) <= 40 {
		return s
	}
	n := 40
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n] + "…"
}
