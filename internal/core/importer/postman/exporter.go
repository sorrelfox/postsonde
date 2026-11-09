package postman

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// Export to Postman Collection v2.1 (EXP-03, docs/import.md#postman-export):
// the reverse of the importer. A folder tree, in the import model
// (importer.Folder), becomes a collection: folders item groups, HTTP and
// GraphQL requests items. What Postman cannot hold is left out or changed
// and reported, never silently dropped. Secrets are never written: a
// secret variable has no value in the file, and an auth secret is either
// a {{variable}} reference or empty.

// SchemaV21 is the schema URL of a Postman Collection v2.1.
const SchemaV21 = "https://schema.getpostman.com/json/collection/v2.1.0/collection.json"

// Diagnostic codes of the export (docs/import.md#postman-export).
const (
	// CodeExportSkipped: an item Postman collections cannot hold (a gRPC,
	// WebSocket, Socket.IO, TCP or SSE request, a file that did not load)
	// was left out (warning).
	CodeExportSkipped = "postman.export_skipped"
	// CodeExportDynamic: a template feature Postman lacks (a dynamic value
	// of PostSonde's own, a filter pipe, a prompt) was written as it is
	// (warning).
	CodeExportDynamic = "postman.export_dynamic"
	// CodeExportAssertions: the assertions of a request were not exported
	// (warning).
	CodeExportAssertions = "postman.export_assertions"
	// CodeExportExtractions: the extraction rules of a request (TEST-05)
	// were not exported (warning).
	CodeExportExtractions = "postman.export_extractions"
	// CodeExportAuth: an auth was exported with a change, or as no auth
	// (warning).
	CodeExportAuth = "postman.export_auth"
	// CodeExportSecret: a secret kept on this computer was exported empty
	// (info).
	CodeExportSecret = "postman.export_secret"
	// CodeExportSetting: a request setting Postman does not have was not
	// exported (warning).
	CodeExportSetting = "postman.export_setting"
	// CodeExportChanged: something was exported in another form: folder
	// headers copied into the requests, a Content-Type header added, file
	// paths as written, named bodies or GraphQL options left out.
	CodeExportChanged = "postman.export_changed"
)

// ExportOptions tune Export.
type ExportOptions struct {
	// ID identifies the exported folder or project: the collection's
	// _postman_id is derived from it, so exporting the same folder again
	// gives the same ID. "" gives a random one.
	ID string
}

// ExportNote is a line of the export's report: an item left out, or
// exported with a change.
type ExportNote struct {
	// Path names the item from the exported folder down; empty for the
	// collection itself.
	Path []string
	// Kind is "collection", "folder", "request" or, for a skipped item,
	// what it was ("WebSocket request").
	Kind     string
	Code     string
	Severity importer.Severity
	// Message never contains a header, body or credential value.
	Message string
	// Skipped is set when the item is not in the file.
	Skipped bool
}

// ExportResult is what Export made.
type ExportResult struct {
	// Data is the collection JSON.
	Data []byte
	// Folders and Requests count what the file holds (the collection
	// itself not counted); Skipped the items left out.
	Folders  int
	Requests int
	Skipped  int
	Notes    []ExportNote
}

// Export writes root as a Postman Collection v2.1: root's name, description,
// variables and auth become the collection's, its items the collection's
// items. It never fails on content: what Postman cannot hold is reported
// in the result's notes.
func Export(root importer.Folder, opts ExportOptions) (*ExportResult, error) {
	e := &exporter{}
	name := strings.TrimSpace(root.Name)
	if name == "" {
		name = "PostSonde"
	}
	c := pmCollection{Info: pmInfo{ID: postmanID(opts.ID), Name: name, Description: root.Description, Schema: SchemaV21}}
	for _, d := range root.Diagnostics {
		e.note(nil, "collection", d.Code, d.Severity, d.Message, false)
	}
	c.Item, c.Auth, c.Variable = e.folderContent(&root, nil, nil, "collection")
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "\t")
	if err := enc.Encode(c); err != nil {
		return nil, err
	}
	return &ExportResult{Data: buf.Bytes(), Folders: e.folders, Requests: e.requests, Skipped: e.skipped, Notes: e.notes}, nil
}

// ExportFileName returns a file name for a collection named name: the name
// with the characters file systems refuse replaced, and the
// ".postman_collection.json" extension Postman gives its exports.
func ExportFileName(name string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(name) {
		switch {
		case r < 0x20, strings.ContainsRune(`/\:*?"<>|`, r):
			b.WriteByte('-')
		default:
			b.WriteRune(r)
		}
	}
	out := strings.Trim(b.String(), " .-")
	if out == "" {
		out = "collection"
	}
	if r := []rune(out); len(r) > 100 {
		out = string(r[:100])
	}
	return out + ".postman_collection.json"
}

// postmanID returns a UUID for a collection: derived from id (SHA-256,
// version 5 layout) or random when id is "".
func postmanID(id string) string {
	var b [16]byte
	if id == "" {
		_, _ = rand.Read(b[:])
		b[6] = b[6]&0x0f | 0x40
	} else {
		sum := sha256.Sum256([]byte("postsonde.postman:" + id))
		copy(b[:], sum[:16])
		b[6] = b[6]&0x0f | 0x50
	}
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b[:])
	return h[:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// exporter collects the report while the tree is written.
type exporter struct {
	notes    []ExportNote
	folders  int
	requests int
	skipped  int
}

func (e *exporter) note(path []string, kind, code string, sev importer.Severity, msg string, skipped bool) {
	e.notes = append(e.notes, ExportNote{Path: slices.Clone(path), Kind: kind, Code: code, Severity: sev, Message: msg, Skipped: skipped})
}

// folderContent writes f's items, auth and variables. levels are the
// header rows of the folders above f, top first; f's own are added for
// its requests, since Postman folders have no headers.
func (e *exporter) folderContent(f *importer.Folder, path []string, levels [][]projectfile.KeyValue, kind string) ([]pmItem, *pmAuth, []pmVariable) {
	add := func(code string, sev importer.Severity, msg string) { e.note(path, kind, code, sev, msg, false) }
	t := &scan{}
	vars := e.variables(f.Variables, t, add)
	auth := e.auth(f.Auth, t, add, authContext{what: kind})
	if n := len(enabledRows(f.Headers)); n > 0 {
		levels = append(slices.Clip(levels), f.Headers)
		add(CodeExportChanged, importer.SeverityInfo, "The "+kind+"'s "+pick(n == 1, "header was", strconv.Itoa(n)+" headers were")+
			" added to each of its HTTP and GraphQL requests: Postman folders have no headers.")
	}
	t.headers(f.Headers)
	t.report(add)
	items := []pmItem{}
	for _, it := range f.Items {
		switch {
		case it.Folder != nil:
			sub := it.Folder
			p := append(slices.Clip(path), displayName(sub.Name, "Folder"))
			e.folders++
			for _, d := range sub.Diagnostics {
				e.note(p, "folder", d.Code, d.Severity, d.Message, false)
			}
			children, a, v := e.folderContent(sub, p, levels, "folder")
			items = append(items, pmItem{Name: displayName(sub.Name, "Folder"), Description: sub.Description, Item: &children, Auth: a, Variable: v})
		case it.Request != nil:
			if item, ok := e.request(it.Request, path, levels); ok {
				items = append(items, item)
			}
		case it.Skipped != nil:
			s := it.Skipped
			e.skipped++
			e.note(append(slices.Clip(path), displayName(s.Name, s.Kind)), s.Kind, CodeExportSkipped, importer.SeverityWarning, s.Reason, true)
		}
	}
	return items, auth, vars
}

func displayName(name, fallback string) string {
	if n := strings.TrimSpace(name); n != "" {
		return n
	}
	return fallback
}

// variables writes folder or collection variables. A secret variable is
// written without its value, as type "secret".
func (e *exporter) variables(vars []projectfile.Variable, t *scan, add func(string, importer.Severity, string)) []pmVariable {
	var out []pmVariable
	var secret []string
	for _, v := range vars {
		pv := pmVariable{Key: v.Name, Value: v.Value, Type: "string", Description: v.Description, Disabled: v.Disabled}
		if v.Secret {
			pv.Type = "secret"
			if v.Value != "" && !requestsecrets.IsReference(v.Value) {
				pv.Value = ""
			}
			secret = append(secret, v.Name)
		}
		t.check(pv.Value, "a variable")
		out = append(out, pv)
	}
	if len(secret) > 0 {
		add(CodeExportSecret, importer.SeverityInfo, pick(len(secret) == 1, "The secret variable ", "The secret variables ")+quoteList(secret)+
			" "+pick(len(secret) == 1, "is", "are")+" exported without a value: secrets stay on this computer. Fill "+pick(len(secret) == 1, "it", "them")+" in Postman.")
	}
	return out
}

// request writes an HTTP or GraphQL request; other kinds are skipped.
func (e *exporter) request(r *importer.Request, parent []string, levels [][]projectfile.KeyValue) (pmItem, bool) {
	name := displayName(r.Name, "Request")
	path := append(slices.Clip(parent), name)
	add := func(code string, sev importer.Severity, msg string) { e.note(path, "request", code, sev, msg, false) }
	for _, d := range r.Diagnostics {
		add(d.Code, d.Severity, d.Message)
	}
	t := &scan{}
	var item pmItem
	switch f := r.File.(type) {
	case *projectfile.HTTPRequest:
		item = e.http(name, f, levels, t, add)
	case *projectfile.GraphQLRequest:
		item = e.graphql(name, f, levels, t, add)
	default:
		kind := kindName(r.File)
		e.skipped++
		e.note(path, kind, CodeExportSkipped, importer.SeverityWarning,
			"Postman collections (v2.1) cannot hold "+article(kind)+"; it is not exported.", true)
		return pmItem{}, false
	}
	t.report(add)
	e.requests++
	return item, true
}

// kindName names the kind of a request file Postman cannot hold.
func kindName(f projectfile.File) string {
	switch f.(type) {
	case *projectfile.WebSocketRequest:
		return "WebSocket request"
	case *projectfile.GRPCRequest:
		return "gRPC request"
	case *projectfile.TCPRequest:
		return "TCP connection"
	case *projectfile.UDPRequest:
		return "UDP request"
	case *projectfile.SocketIORequest:
		return "Socket.IO request"
	case *projectfile.SSERequest:
		return "Server-Sent Events request"
	}
	return "request of this kind"
}

func article(s string) string {
	if s == "" {
		return s
	}
	switch s[0] {
	case 'a', 'e', 'i', 'o', 'u', 'A', 'E', 'I', 'O', 'U':
		return "an " + s
	}
	return "a " + s
}

func (e *exporter) http(name string, r *projectfile.HTTPRequest, levels [][]projectfile.KeyValue, t *scan, add func(string, importer.Severity, string)) pmItem {
	method := strings.TrimSpace(r.Method)
	if method == "" {
		method = "GET"
	}
	q := &pmRequest{Method: method, URL: urlObject(r.URL, r.Params, r.PathParams, t, add), Description: r.Description}
	own := slices.Clone(r.Headers)
	q.Body, own = body(r.Body, own, t, add)
	if len(r.BodyExamples) > 1 {
		add(CodeExportChanged, importer.SeverityWarning, "Only the active body is exported; the request's other named bodies are not: Postman requests have one body.")
	}
	q.Header = headers(own, levels, t)
	q.Auth = e.auth(r.Auth, t, add, authContext{what: "request", method: method, bodyMode: r.Body.Mode})
	assertions(r.Assertions, add)
	extractions(r.Extractions, add)
	return pmItem{Name: name, ProtocolProfileBehavior: behaviorOf(r.Settings, false, add), Request: q, Response: responses(r.ResponseExamples, add)}
}

func (e *exporter) graphql(name string, r *projectfile.GraphQLRequest, levels [][]projectfile.KeyValue, t *scan, add func(string, importer.Severity, string)) pmItem {
	method := "POST"
	if strings.EqualFold(r.Method, "GET") {
		method = "GET"
	}
	t.check(r.Query, "the query")
	t.check(r.Variables, "the variables")
	q := &pmRequest{
		Method: method, URL: urlObject(r.URL, nil, nil, t, add), Description: r.Description,
		Body: &pmBody{Mode: "graphql", GraphQL: &pmGraphQL{Query: r.Query, Variables: r.Variables}},
	}
	q.Header = headers(r.Headers, levels, t)
	q.Auth = e.auth(r.Auth, t, add, authContext{what: "request", method: method})
	var lost []string
	if strings.TrimSpace(r.OperationName) != "" {
		lost = append(lost, "the operation name")
	}
	if strings.TrimSpace(r.Extensions) != "" {
		lost = append(lost, "the extensions")
	}
	if len(r.Files) > 0 {
		lost = append(lost, "the uploaded files")
	}
	if strings.TrimSpace(r.ConnectionParams) != "" {
		lost = append(lost, "the connection params")
	}
	if r.SchemaFile != "" {
		lost = append(lost, "the schema file")
	}
	if len(lost) > 0 {
		add(CodeExportChanged, importer.SeverityWarning, "Postman's GraphQL body has only the query and the variables: "+joinAnd(lost)+" "+pick(len(lost) == 1, "is", "are")+" not exported.")
	}
	assertions(r.Assertions, add)
	extractions(r.Extractions, add)
	return pmItem{Name: name, ProtocolProfileBehavior: behaviorOf(r.Settings, true, add), Request: q, Response: responses(r.ResponseExamples, add)}
}

func assertions(list []projectfile.Assertion, add func(string, importer.Severity, string)) {
	if len(list) > 0 {
		add(CodeExportAssertions, importer.SeverityWarning, "The request's "+pick(len(list) == 1, "assertion is", strconv.Itoa(len(list))+" assertions are")+
			" not exported: Postman checks responses with test scripts.")
	}
}

func extractions(list []projectfile.Extraction, add func(string, importer.Severity, string)) {
	if len(list) > 0 {
		add(CodeExportExtractions, importer.SeverityWarning, "The request's "+pick(len(list) == 1, "extraction rule is", strconv.Itoa(len(list))+" extraction rules are")+
			" not exported: Postman sets variables from responses with test scripts.")
	}
}

// headers returns a request's header list: the folders' rows it inherits
// (top folder first, those a nearer level overrides by name left out,
// as a send does), then its own rows.
func headers(own []projectfile.KeyValue, levels [][]projectfile.KeyValue, t *scan) []pmHeader {
	taken := map[string]bool{}
	for _, h := range enabledRows(own) {
		taken[strings.ToLower(strings.TrimSpace(h.Name))] = true
	}
	kept := make([][]projectfile.KeyValue, len(levels))
	for i := len(levels) - 1; i >= 0; i-- {
		var level []projectfile.KeyValue
		for _, h := range enabledRows(levels[i]) {
			if !taken[strings.ToLower(strings.TrimSpace(h.Name))] {
				level = append(level, h)
			}
		}
		for _, h := range level {
			taken[strings.ToLower(strings.TrimSpace(h.Name))] = true
		}
		kept[i] = level
	}
	out := []pmHeader{}
	for _, level := range kept {
		for _, h := range level {
			out = append(out, pmHeader{Key: h.Name, Value: h.Value, Description: h.Description, Type: "text"})
		}
	}
	for _, h := range own {
		t.check(h.Name, "a header")
		t.check(h.Value, "a header")
		out = append(out, pmHeader{Key: h.Name, Value: h.Value, Description: h.Description, Type: "text", Disabled: h.Disabled})
	}
	return out
}

// enabledRows returns the enabled rows with a name.
func enabledRows(rows []projectfile.KeyValue) []projectfile.KeyValue {
	var out []projectfile.KeyValue
	for _, r := range rows {
		if !r.Disabled && strings.TrimSpace(r.Name) != "" {
			out = append(out, r)
		}
	}
	return out
}

// exportLanguages are the Postman raw languages of the raw body types.
// YAML has none: its Content-Type goes in a header.
var exportLanguages = map[string]string{
	projectfile.RawJSON: "json",
	projectfile.RawXML:  "xml",
	projectfile.RawHTML: "html",
	projectfile.RawText: "text",
	"":                  "text",
}

// body writes an HTTP request's body, and returns the headers with a
// Content-Type Postman would not send by itself added.
func body(b projectfile.Body, hdrs []projectfile.KeyValue, t *scan, add func(string, importer.Severity, string)) (*pmBody, []projectfile.KeyValue) {
	switch b.Mode {
	case "", projectfile.BodyNone:
		return nil, hdrs
	case projectfile.BodyRaw:
		t.check(b.Text, "the body")
		text := b.Text
		out := &pmBody{Mode: "raw", Raw: &text}
		if lang, ok := exportLanguages[b.Type]; ok {
			out.Options = &pmBodyOptions{Raw: &pmRawOptions{Language: lang}}
		} else if ct := request.RawMediaType(b.Type); ct != "" && !hasHeader(hdrs, "Content-Type") {
			hdrs = append(hdrs, projectfile.KeyValue{Name: "Content-Type", Value: ct})
			add(CodeExportChanged, importer.SeverityInfo, "A Content-Type: "+ct+" header was added: Postman has no "+strings.ToUpper(b.Type)+" body type.")
		}
		return out, hdrs
	case projectfile.BodyURLEncoded:
		out := &pmBody{Mode: "urlencoded", URLEncoded: []pmFormField{}}
		for _, f := range b.Fields {
			t.check(f.Name, "the form")
			t.check(f.Value, "the form")
			v := f.Value
			out.URLEncoded = append(out.URLEncoded, pmFormField{Key: f.Name, Value: &v, Type: "text", Description: f.Description, Disabled: f.Disabled})
		}
		return out, hdrs
	case projectfile.BodyMultipart:
		out := &pmBody{Mode: "formdata", FormData: []pmFormField{}}
		files := false
		for _, p := range b.Parts {
			t.check(p.Name, "the form")
			f := pmFormField{Key: p.Name, Type: "text", ContentType: p.ContentType, Description: p.Description, Disabled: p.Disabled}
			if p.File != "" {
				f.Type, f.Src, files = "file", p.File, true
			} else {
				t.check(p.Value, "the form")
				v := p.Value
				f.Value = &v
			}
			out.FormData = append(out.FormData, f)
		}
		if files {
			add(CodeExportChanged, importer.SeverityInfo, "The form sends files by their paths as written; a relative path is relative to the project folder. Choose the files again in Postman if it cannot find them.")
		}
		return out, hdrs
	case projectfile.BodyFile:
		out := &pmBody{Mode: "file", File: &pmFile{Src: b.Path}}
		add(CodeExportChanged, importer.SeverityInfo, "The body sends a file by its path as written; a relative path is relative to the project folder. Choose the file again in Postman if it cannot find it.")
		if b.ContentType != "" && !hasHeader(hdrs, "Content-Type") {
			hdrs = append(hdrs, projectfile.KeyValue{Name: "Content-Type", Value: b.ContentType})
			add(CodeExportChanged, importer.SeverityInfo, "The file body's content type was added as a Content-Type header.")
		}
		return out, hdrs
	}
	add(CodeExportChanged, importer.SeverityWarning, "The body's mode “"+clip(b.Mode)+"” is not one Postman knows; the body is not exported.")
	return nil, hdrs
}

// authContext is where an auth is: a request (with its method and body
// mode) or a folder or the collection.
type authContext struct {
	what     string
	method   string
	bodyMode string
}

// auth writes an auth. inherit is no auth member (Postman inherits a
// missing auth); none is noauth; a type Postman lacks is noauth with a
// warning.
func (e *exporter) auth(a projectfile.Auth, t *scan, add func(string, importer.Severity, string), ctx authContext) *pmAuth {
	if a.Type == "" || a.Type == projectfile.AuthInherit {
		return nil
	}
	if a.Type == projectfile.AuthNone {
		return &pmAuth{Type: "noauth"}
	}
	if a.Disabled {
		add(CodeExportAuth, importer.SeverityWarning, "The "+ctx.what+"'s auth is turned off, so it is exported as no auth; its settings are not exported.")
		return &pmAuth{Type: "noauth"}
	}
	var emptied []string
	secret := func(member, label string) string {
		v := a.SecretOf(member)
		if (v == "" && a.IsLocal(member)) || (v != "" && !requestsecrets.IsReference(v)) {
			emptied = append(emptied, label)
			return ""
		}
		t.check(v, "the auth")
		return v
	}
	plain := func(v string) string {
		t.check(v, "the auth")
		return v
	}
	var changed []string
	out := &pmAuth{Type: a.Type}
	switch a.Type {
	case projectfile.AuthBasic, projectfile.AuthDigest:
		out.Params = []pmAuthParam{textParam("username", plain(a.Username)), textParam("password", secret("password", "password"))}
	case projectfile.AuthBearer:
		out.Params = []pmAuthParam{textParam("token", secret("token", "token"))}
		if a.BearerPrefix() != projectfile.DefaultBearerPrefix {
			changed = append(changed, "Postman sends bearer tokens with the prefix Bearer, not "+prefixName(a.BearerPrefix())+".")
		}
	case projectfile.AuthAPIKey:
		in := "header"
		if a.In == projectfile.APIKeyInQuery {
			in = "query"
		}
		out.Params = []pmAuthParam{textParam("key", plain(a.Key)), textParam("value", secret("value", "API key")), textParam("in", in)}
	case projectfile.AuthJWT:
		out.Params, changed = jwtParams(a, secret, plain)
	case projectfile.AuthOAuth1:
		out.Params, changed = oauth1Params(a, secret, plain, ctx)
	case projectfile.AuthAWSV4:
		out.Params = []pmAuthParam{
			textParam("accessKey", plain(a.AccessKey)), textParam("secretKey", secret("secretKey", "secret key")),
			textParam("region", plain(a.Region)), textParam("service", plain(a.Service)),
			textParam("sessionToken", secret("sessionToken", "session token")),
			flagParam("addAuthDataToQuery", a.In == projectfile.APIKeyInQuery),
		}
		if a.UnsignedPayload {
			changed = append(changed, "Postman signs the body's hash, not UNSIGNED-PAYLOAD.")
		}
		if a.In == projectfile.APIKeyInQuery && a.Expires != 0 && a.Expires != projectfile.DefaultAWSExpires {
			changed = append(changed, "The presigned URL's validity ("+strconv.FormatInt(a.Expires, 10)+" seconds) is not exported.")
		}
	case projectfile.AuthHawk:
		alg := strings.ToLower(strings.TrimSpace(a.HawkAlgorithm))
		if alg == "" {
			alg = "sha256"
		}
		out.Params = []pmAuthParam{
			textParam("authId", plain(a.HawkID)), textParam("authKey", secret("hawkKey", "Hawk key")), textParam("algorithm", alg),
			textParam("extraData", plain(a.Ext)), textParam("app", plain(a.App)), textParam("delegation", plain(a.Dlg)),
			flagParam("includePayloadHash", a.PayloadHash),
		}
	case projectfile.AuthOAuth2:
		out.Params, changed = oauth2Params(a, secret, plain)
	default:
		name := a.Type
		if a.Type == projectfile.AuthNetrc {
			name = ".netrc"
		}
		add(CodeExportAuth, importer.SeverityWarning, "Postman has no "+clip(name)+" auth: the "+ctx.what+" is exported with no auth. Set up the credentials in Postman.")
		return &pmAuth{Type: "noauth"}
	}
	if len(emptied) > 0 {
		add(CodeExportSecret, importer.SeverityInfo, "The auth's "+joinAnd(emptied)+" "+pick(len(emptied) == 1, "is", "are")+
			" kept on this computer and exported empty. Fill "+pick(len(emptied) == 1, "it", "them")+" in Postman, or use a {{variable}}.")
	}
	if len(changed) > 0 {
		add(CodeExportAuth, importer.SeverityWarning, "The "+ctx.what+"'s auth was exported with a change. "+strings.Join(changed, " "))
	}
	return out
}

func prefixName(p string) string {
	if p == "" {
		return "none"
	}
	return strconv.Quote(clip(p))
}

// jwtParams writes a JWT auth as Postman's "JWT Bearer".
func jwtParams(a projectfile.Auth, secret func(string, string) string, plain func(string) string) ([]pmAuthParam, []string) {
	var changed []string
	alg := a.Algorithm
	if alg == "" {
		alg = "HS256"
	}
	header := a.JWTHeader
	if strings.TrimSpace(header) == "" {
		header = "{}"
	}
	key := secret("secret", "signing key")
	ps := []pmAuthParam{textParam("algorithm", alg)}
	if strings.HasPrefix(alg, "HS") {
		ps = append(ps, textParam("secret", key), flagParam("isSecretBase64Encoded", a.SecretBase64))
	} else {
		ps = append(ps, textParam("privateKey", key))
	}
	ps = append(ps, textParam("payload", plain(a.Payload)), textParam("header", plain(header)))
	if a.In == projectfile.APIKeyInQuery {
		param := a.Key
		if param == "" {
			param = projectfile.DefaultJWTQueryParam
		}
		ps = append(ps, textParam("addTokenTo", "queryParam"), textParam("queryParamKey", param))
	} else {
		ps = append(ps, textParam("addTokenTo", "header"), textParam("headerPrefix", a.BearerPrefix()))
		if k := strings.TrimSpace(a.Key); k != "" && !strings.EqualFold(k, "Authorization") {
			changed = append(changed, "Postman sends the token in the Authorization header, not "+strconv.Quote(clip(k))+".")
		}
	}
	if a.KeyFile != "" {
		changed = append(changed, "The key file is not exported: paste the key in Postman.")
	}
	var claims []string
	if a.IssuedAt {
		claims = append(claims, "iat")
	}
	if a.NotBefore {
		claims = append(claims, "nbf")
	}
	if a.JWTID {
		claims = append(claims, "jti")
	}
	if a.ExpiresIn > 0 {
		claims = append(claims, "exp")
	}
	if len(claims) > 0 {
		changed = append(changed, "PostSonde adds the "+joinAnd(claims)+" "+pick(len(claims) == 1, "claim", "claims")+" at send time; Postman does not: add "+pick(len(claims) == 1, "it", "them")+" to the payload, e.g. with {{$timestamp}}.")
	}
	return ps, changed
}

// oauth1Params writes an OAuth 1.0 auth.
func oauth1Params(a projectfile.Auth, secret func(string, string) string, plain func(string) string, ctx authContext) ([]pmAuthParam, []string) {
	var changed []string
	method := a.SignatureMethod
	if method == "" {
		method = "HMAC-SHA1"
	}
	ps := []pmAuthParam{textParam("signatureMethod", method), textParam("consumerKey", plain(a.ConsumerKey))}
	if strings.HasPrefix(strings.ToUpper(method), "RSA-") {
		ps = append(ps, textParam("privateKey", secret("consumerSecret", "private key")))
	} else {
		ps = append(ps, textParam("consumerSecret", secret("consumerSecret", "consumer secret")))
	}
	ps = append(ps, textParam("token", plain(a.AccessToken)))
	if !strings.HasPrefix(strings.ToUpper(method), "RSA-") {
		ps = append(ps, textParam("tokenSecret", secret("tokenSecret", "token secret")))
	}
	version := "1.0"
	if a.OmitVersion {
		version = ""
	}
	ps = append(ps,
		textParam("timestamp", plain(a.Timestamp)), textParam("nonce", plain(a.Nonce)), textParam("version", version),
		textParam("realm", plain(a.Realm)), textParam("callback", plain(a.Callback)), textParam("verifier", plain(a.Verifier)),
		flagParam("includeBodyHash", a.BodyHash), flagParam("addParamsToHeader", a.In == "" || a.In == projectfile.APIKeyInHeader),
		flagParam("addEmptyParamsToSign", false))
	// Postman puts the parameters it does not send in the header in the
	// body of a POST or PUT with a URL-encoded body, else in the URL.
	formBody := (ctx.method == "POST" || ctx.method == "PUT") && ctx.bodyMode == projectfile.BodyURLEncoded
	switch {
	case a.In == projectfile.AuthInBody && ctx.what != "request":
		changed = append(changed, "Postman puts the parameters in the body only for POST and PUT requests with a URL-encoded body, and in the URL otherwise.")
	case a.In == projectfile.AuthInBody && !formBody:
		changed = append(changed, "Postman puts the parameters in the URL: the request is not a POST or PUT with a URL-encoded body.")
	case a.In == projectfile.APIKeyInQuery && ctx.what == "request" && formBody:
		changed = append(changed, "Postman puts the parameters in the body, not the URL, of a POST or PUT with a URL-encoded body.")
	}
	return ps, changed
}

// behaviorOf maps request settings to Postman's protocolProfileBehavior;
// nil when none is set. Settings Postman lacks are reported.
func behaviorOf(s projectfile.RequestSettings, graphql bool, add func(string, importer.Severity, string)) *pmBehavior {
	b := &pmBehavior{
		FollowRedirects: s.FollowRedirects, MaxRedirects: s.MaxRedirects, FollowOriginalHTTPMethod: s.FollowOriginalMethod,
		FollowAuthorizationHeader: s.ForwardAuthorization, StrictSSL: s.VerifyTLS,
	}
	var lost []string
	switch s.URLEncoding {
	case projectfile.URLEncodingNone:
		on := true
		b.DisableURLEncoding = &on
	case projectfile.URLEncodingRFC3986:
		lost = append(lost, "strict URL encoding (RFC 3986)")
	}
	switch s.HTTPVersion {
	case projectfile.HTTPVersion1, projectfile.HTTPVersion2:
		b.ProtocolVersion = s.HTTPVersion
	}
	sendOff := s.SendCookies != nil && !*s.SendCookies
	storeOff := s.StoreCookies != nil && !*s.StoreCookies
	switch {
	case sendOff && storeOff:
		on := true
		b.DisableCookies = &on
	case sendOff:
		lost = append(lost, "not sending cookies")
	case storeOff:
		lost = append(lost, "not storing cookies")
	}
	if s.TimeoutMs != nil {
		lost = append(lost, "the timeout")
	}
	if len(s.OmitHeaders) > 0 {
		lost = append(lost, "the automatic headers left out ("+strings.Join(s.OmitHeaders, ", ")+")")
	}
	if graphql && s.GraphQLTransport != nil && (s.GraphQLTransport.Query != "" || s.GraphQLTransport.Mutation != "" || s.GraphQLTransport.Subscription != "") {
		lost = append(lost, "the GraphQL transports")
	}
	if graphql && s.GraphQLSchemaRefresh != nil && (s.GraphQLSchemaRefresh.OnSend || s.GraphQLSchemaRefresh.IntervalMinutes > 0) {
		lost = append(lost, "the schema auto-refresh")
	}
	if len(lost) > 0 {
		add(CodeExportSetting, importer.SeverityWarning, "Postman has no setting for "+joinAnd(lost)+"; "+pick(len(lost) == 1, "it is", "they are")+" not exported.")
	}
	if *b == (pmBehavior{}) {
		return nil
	}
	return b
}

// urlObject writes a URL as Postman's URL object: raw as typed, and its
// parts. The query list is the params table (disabled rows included), a
// row without "=" in the URL having a null value; path variables are the
// path parameter rows.
func urlObject(raw string, params, pathParams []projectfile.KeyValue, t *scan, add func(string, importer.Severity, string)) pmURL {
	// A relative URL is sent to {{baseUrl}} (COL-08, request.ExpandURL);
	// Postman has no such rule, so the export writes the variable out.
	if trimmed := strings.TrimSpace(raw); strings.HasPrefix(trimmed, "/") && !strings.HasPrefix(trimmed, "//") {
		raw = "{{baseUrl}}" + trimmed
	}
	t.check(raw, "the URL")
	u := pmURL{Raw: raw}
	rest := raw
	if i := indexOutside(rest, '#'); i >= 0 {
		u.Hash, rest = rest[i+1:], rest[:i]
	}
	qs, hasQuery := "", false
	if i := indexOutside(rest, '?'); i >= 0 {
		qs, rest, hasQuery = rest[i+1:], rest[:i], true
	}
	if m := reProtocol.FindStringSubmatch(rest); m != nil {
		u.Protocol, rest = m[1], rest[len(m[0]):]
	}
	hostPort, pathPart, hasPath := rest, "", false
	if i := indexOutside(rest, '/'); i >= 0 {
		hostPort, pathPart, hasPath = rest[:i], rest[i+1:], true
	}
	if i := lastIndexOutside(hostPort, '@'); i >= 0 {
		hostPort = hostPort[i+1:]
	}
	if i := lastIndexOutside(hostPort, ':'); i >= 0 && !strings.Contains(hostPort[i:], "]") {
		u.Port, hostPort = hostPort[i+1:], hostPort[:i]
	}
	if hostPort != "" {
		u.Host = splitOutside(hostPort, '.')
	}
	if hasPath {
		u.Path = splitOutside(pathPart, '/')
	}
	if hasQuery || len(params) > 0 {
		u.Query = queryList(qs, params, t)
	}
	for _, p := range pathParams {
		t.check(p.Value, "a path parameter")
		u.Variable = append(u.Variable, pmVariable{Key: p.Name, Value: p.Value, Description: p.Description})
	}
	var braces []string
	for _, p := range request.PathParams(raw) {
		if strings.HasPrefix(raw[p.Start:p.End], "{") && !slices.Contains(braces, p.Name) {
			braces = append(braces, p.Name)
		}
	}
	if len(braces) > 0 {
		add(CodeExportChanged, importer.SeverityWarning, "Postman fills only :name path parameters, not {"+braces[0]+"}: write the "+
			pick(len(braces) == 1, "parameter", "parameters")+" as :name in Postman.")
	}
	return u
}

var reProtocol = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9+.\-]*)://`)

type queryEntry struct {
	key, value string
	eq         bool
}

func queryList(qs string, params []projectfile.KeyValue, t *scan) []pmQuery {
	var entries []queryEntry
	for entry := range strings.SplitSeq(qs, "&") {
		if entry == "" {
			continue
		}
		k, v, eq := strings.Cut(entry, "=")
		entries = append(entries, queryEntry{k, v, eq})
	}
	out := []pmQuery{}
	if len(params) == 0 {
		for _, e := range entries {
			q := pmQuery{Key: e.key}
			if e.eq {
				v := e.value
				q.Value = &v
			}
			out = append(out, q)
		}
		return out
	}
	j := 0
	for _, p := range params {
		t.check(p.Name, "a query parameter")
		t.check(p.Value, "a query parameter")
		v := p.Value
		q := pmQuery{Key: p.Name, Value: &v, Description: p.Description, Disabled: p.Disabled}
		if !p.Disabled && j < len(entries) {
			e := entries[j]
			j++
			if e.key == p.Name && !e.eq && p.Value == "" {
				q.Value = nil
			}
		}
		out = append(out, q)
	}
	// Entries of the URL the table does not have.
	for ; j < len(entries); j++ {
		e := entries[j]
		q := pmQuery{Key: e.key}
		if e.eq {
			v := e.value
			q.Value = &v
		}
		out = append(out, q)
	}
	return out
}

// inVariable marks the bytes of s inside {{variables}}.
func inVariable(s string) []bool {
	in := make([]bool, len(s))
	for _, seg := range template.Parse(s) {
		if seg.Kind == template.Var {
			for i := seg.Start; i < seg.End; i++ {
				in[i] = true
			}
		}
	}
	return in
}

func indexOutside(s string, c byte) int {
	in := inVariable(s)
	for i := 0; i < len(s); i++ {
		if s[i] == c && !in[i] {
			return i
		}
	}
	return -1
}

func lastIndexOutside(s string, c byte) int {
	in := inVariable(s)
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == c && !in[i] {
			return i
		}
	}
	return -1
}

func splitOutside(s string, c byte) []string {
	in := inVariable(s)
	out := []string{}
	start := 0
	for i := 0; i < len(s); i++ {
		if s[i] == c && !in[i] {
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// scan finds the template features Postman lacks in the texts of one
// item, for one warning per item.
type scan struct {
	dynamic map[string][]string
	pipes   []string
	prompts []string
}

// check looks at text; where says where it is ("the URL").
func (t *scan) check(text, where string) {
	if !strings.Contains(text, "{{") {
		return
	}
	for _, seg := range template.Parse(text) {
		if seg.Kind != template.Var {
			continue
		}
		name := seg.Name
		switch {
		case !strings.HasPrefix(name, "$") && !strings.Contains(name, "|"):
		case sameDynamic[name] || otherDynamic[name] != "":
		case isPrompt(name):
			t.prompts = addPlace(t.prompts, where)
		case template.IsDynamic(name):
			if t.dynamic == nil {
				t.dynamic = map[string][]string{}
			}
			t.dynamic[name] = addPlace(t.dynamic[name], where)
		case template.IsPipe(name):
			t.pipes = addPlace(t.pipes, where)
		}
	}
}

func isPrompt(name string) bool {
	_, ok, _ := template.ParsePrompt(name)
	return ok
}

func (t *scan) headers(rows []projectfile.KeyValue) {
	for _, h := range rows {
		t.check(h.Name, "a header")
		t.check(h.Value, "a header")
	}
}

// report adds t's findings as warnings.
func (t *scan) report(add func(string, importer.Severity, string)) {
	for _, name := range sortedKeys(t.dynamic) {
		add(CodeExportDynamic, importer.SeverityWarning, "The dynamic value {{"+clip(name)+"}} in "+strings.Join(t.dynamic[name], ", ")+
			" is PostSonde's own and is exported as written: Postman has no such variable.")
	}
	if len(t.pipes) > 0 {
		add(CodeExportDynamic, importer.SeverityWarning, "A filter pipe ({{name | filter}}) in "+strings.Join(t.pipes, ", ")+
			" is exported as written: Postman has no filters. Compute the value in a pre-request script.")
	}
	if len(t.prompts) > 0 {
		add(CodeExportDynamic, importer.SeverityWarning, "A {{$prompt}} in "+strings.Join(t.prompts, ", ")+
			" is exported as written: Postman does not ask for values at send time.")
	}
}

func joinAnd(list []string) string {
	switch len(list) {
	case 0:
		return ""
	case 1:
		return list[0]
	}
	return strings.Join(list[:len(list)-1], ", ") + " and " + list[len(list)-1]
}

func quoteList(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = strconv.Quote(clip(n))
	}
	return joinAnd(q)
}

func pick(one bool, a, b string) string {
	if one {
		return a
	}
	return b
}

// The collection's JSON, in the member order Postman writes.

type pmCollection struct {
	Info     pmInfo       `json:"info"`
	Item     []pmItem     `json:"item"`
	Auth     *pmAuth      `json:"auth,omitempty"`
	Variable []pmVariable `json:"variable,omitempty"`
}

type pmInfo struct {
	ID          string `json:"_postman_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Schema      string `json:"schema"`
}

// pmItem is a folder (Item set) or a request (Request set).
type pmItem struct {
	Name                    string       `json:"name"`
	Description             string       `json:"description,omitempty"`
	Item                    *[]pmItem    `json:"item,omitempty"`
	Auth                    *pmAuth      `json:"auth,omitempty"`
	Variable                []pmVariable `json:"variable,omitempty"`
	ProtocolProfileBehavior *pmBehavior  `json:"protocolProfileBehavior,omitempty"`
	Request                 *pmRequest   `json:"request,omitempty"`
	Response                *[]any       `json:"response,omitempty"`
}

type pmVariable struct {
	Key         string `json:"key"`
	Value       string `json:"value"`
	Type        string `json:"type,omitempty"`
	Description string `json:"description,omitempty"`
	Disabled    bool   `json:"disabled,omitempty"`
}

type pmBehavior struct {
	DisableURLEncoding        *bool  `json:"disableUrlEncoding,omitempty"`
	FollowRedirects           *bool  `json:"followRedirects,omitempty"`
	FollowOriginalHTTPMethod  *bool  `json:"followOriginalHttpMethod,omitempty"`
	FollowAuthorizationHeader *bool  `json:"followAuthorizationHeader,omitempty"`
	MaxRedirects              *int   `json:"maxRedirects,omitempty"`
	StrictSSL                 *bool  `json:"strictSSL,omitempty"`
	DisableCookies            *bool  `json:"disableCookies,omitempty"`
	ProtocolVersion           string `json:"protocolVersion,omitempty"`
}

type pmRequest struct {
	Auth        *pmAuth    `json:"auth,omitempty"`
	Method      string     `json:"method"`
	Header      []pmHeader `json:"header"`
	Body        *pmBody    `json:"body,omitempty"`
	URL         pmURL      `json:"url"`
	Description string     `json:"description,omitempty"`
}

type pmHeader struct {
	Key         string `json:"key"`
	Value       string `json:"value"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type"`
	Disabled    bool   `json:"disabled,omitempty"`
}

type pmURL struct {
	Raw      string       `json:"raw"`
	Protocol string       `json:"protocol,omitempty"`
	Host     []string     `json:"host,omitempty"`
	Port     string       `json:"port,omitempty"`
	Path     []string     `json:"path,omitempty"`
	Query    []pmQuery    `json:"query,omitempty"`
	Hash     string       `json:"hash,omitempty"`
	Variable []pmVariable `json:"variable,omitempty"`
}

type pmQuery struct {
	Key         string  `json:"key"`
	Value       *string `json:"value"`
	Description string  `json:"description,omitempty"`
	Disabled    bool    `json:"disabled,omitempty"`
}

type pmBody struct {
	Mode       string         `json:"mode"`
	Raw        *string        `json:"raw,omitempty"`
	URLEncoded []pmFormField  `json:"urlencoded,omitempty"`
	FormData   []pmFormField  `json:"formdata,omitempty"`
	File       *pmFile        `json:"file,omitempty"`
	GraphQL    *pmGraphQL     `json:"graphql,omitempty"`
	Options    *pmBodyOptions `json:"options,omitempty"`
}

type pmFormField struct {
	Key         string  `json:"key"`
	Value       *string `json:"value,omitempty"`
	Src         string  `json:"src,omitempty"`
	Type        string  `json:"type"`
	ContentType string  `json:"contentType,omitempty"`
	Description string  `json:"description,omitempty"`
	Disabled    bool    `json:"disabled,omitempty"`
}

type pmFile struct {
	Src string `json:"src"`
}

type pmGraphQL struct {
	Query     string `json:"query"`
	Variables string `json:"variables"`
}

type pmBodyOptions struct {
	Raw *pmRawOptions `json:"raw,omitempty"`
}

type pmRawOptions struct {
	Language string `json:"language"`
}

// pmAuth is written {"type": t, t: [params]}.
type pmAuth struct {
	Type   string
	Params []pmAuthParam
}

type pmAuthParam struct {
	Key   string `json:"key"`
	Value any    `json:"value"`
	Type  string `json:"type"`
}

func textParam(key, value string) pmAuthParam {
	return pmAuthParam{Key: key, Value: value, Type: "string"}
}
func flagParam(key string, value bool) pmAuthParam {
	return pmAuthParam{Key: key, Value: value, Type: "boolean"}
}

func (a pmAuth) MarshalJSON() ([]byte, error) {
	typ, err := json.Marshal(a.Type)
	if err != nil {
		return nil, err
	}
	var b bytes.Buffer
	b.WriteString(`{"type":`)
	b.Write(typ)
	if a.Type != "noauth" {
		params := a.Params
		if params == nil {
			params = []pmAuthParam{}
		}
		var p bytes.Buffer
		enc := json.NewEncoder(&p)
		enc.SetEscapeHTML(false)
		if err := enc.Encode(params); err != nil {
			return nil, err
		}
		b.WriteString(",")
		b.Write(typ)
		b.WriteString(":")
		b.Write(bytes.TrimSpace(p.Bytes()))
	}
	b.WriteString("}")
	return b.Bytes(), nil
}
