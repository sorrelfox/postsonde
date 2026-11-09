package curl

import (
	"encoding/json"
	"math"
	"net/url"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/request"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Diagnostic codes of Import, besides those of Parse.
const (
	// CodeNotImported: an option was read but the request has no place for
	// it (--connect-timeout, --http2, a proxy, NTLM or Negotiate auth, a form
	// part's ;filename=, --compressed). Warning; info where the request
	// behaves the same without it. Range: the option and its value.
	CodeNotImported = "curl.not_imported"
	// CodeFileNotRead: a file curl would send is imported as its path, or
	// its content is missing from the body. Warning. Range: the option and
	// its value.
	CodeFileNotRead = "curl.file_not_read"
	// CodeSchemeAdded: the URL had no scheme; "http://" is added, as curl
	// guesses it. Info. Range: the URL.
	CodeSchemeAdded = "curl.scheme_added"
	// CodeBodyType: the body's type was chosen differently from what curl
	// would send (a JSON body sent by -d without a Content-Type header).
	// Info. Range: the first data option.
	CodeBodyType = "curl.body_type"
)

// Imported is a Command mapped to an HTTP request file (IMP-01).
type Imported struct {
	// Request is the request, without ID; Name is empty (see Name).
	Request projectfile.HTTPRequest
	// Name is a name for the request: the URL's last path segment, else
	// its host.
	Name string
	// Diagnostics are the Command's followed by Import's own, sorted by
	// Start.
	Diagnostics []Diagnostic
}

// Import parses src (dialect detected) and maps it to a request.
func Import(src string) Imported {
	return Parse(src).Import()
}

// Import maps the command to an HTTP request file. It never fails; what
// cannot be imported is reported in Diagnostics (docs/curl.md). The
// request of a command with error diagnostics is only a best effort.
func (c *Command) Import() Imported {
	m := &mapper{c: c}
	r := projectfile.HTTPRequest{
		Method: c.Method,
		Body:   projectfile.Body{Mode: projectfile.BodyNone},
		Auth:   projectfile.Auth{Type: projectfile.AuthInherit},
	}
	if r.Method == "" {
		r.Method = "GET"
	}
	r.URL = m.url()
	r.Params = queryRows(r.URL)
	for _, name := range request.PathParamNames(r.URL) {
		r.PathParams = append(r.PathParams, projectfile.KeyValue{Name: name})
	}
	r.Headers = m.headers()
	r.Body = m.body()
	if m.dropContentType {
		r.Headers = slices.DeleteFunc(r.Headers, func(h projectfile.KeyValue) bool {
			return strings.EqualFold(h.Name, "Content-Type")
		})
	}
	if m.contentType != "" {
		r.Headers = append(r.Headers, projectfile.KeyValue{Name: "Content-Type", Value: m.contentType})
	}
	r.Auth = m.auth()
	r.Settings = m.settings()

	diags := append(append([]Diagnostic{}, c.Diagnostics...), m.diags...)
	sortDiagnostics(diags)
	return Imported{Request: r, Name: requestName(r.URL), Diagnostics: diags}
}

type mapper struct {
	c     *Command
	diags []Diagnostic
	// contentType is a Content-Type header to add after the -H headers:
	// the one curl sends by default when the imported body would imply
	// another.
	contentType string
	// dropContentType removes -H Content-Type: multipart/form-data, whose
	// boundary is generated when a multipart body is sent.
	dropContentType bool
}

func (m *mapper) diag(code string, sev Severity, start, end int, msg string) {
	m.diags = append(m.diags, Diagnostic{Code: code, Severity: sev, Start: start, End: end, Message: msg})
}

// option reports on the last use of the option long; when it was not
// given (an auth scheme implied by -u), the report has an empty range at 0.
func (m *mapper) option(long, code string, sev Severity, msg string) {
	a := m.c.options[long]
	m.diag(code, sev, a.Start, a.End, strings.ReplaceAll(msg, "%o", quoteText(optionName(a, long))))
}

func optionName(a Arg, long string) string {
	if a.Value != "" {
		return a.Value
	}
	return "--" + long
}

// url is the first URL with a scheme, the --url-query parts and, with -G,
// the data added to its query.
func (m *mapper) url() string {
	c := m.c
	u := strings.TrimSpace(c.URL)
	if u != "" && !strings.Contains(u, "://") {
		u = "http://" + u
		if len(c.URLs) > 0 {
			m.diag(CodeSchemeAdded, SeverityInfo, c.URLs[0].Start, c.URLs[0].End,
				"The URL has no scheme; “http://” is added, as curl does.")
		}
	}
	var query []string
	for _, q := range c.URLQuery {
		switch {
		case q.File != "":
		case q.Kind == DataRawQuery:
			query = append(query, q.Value)
		default:
			query = append(query, q.Encoded())
		}
	}
	if c.Get {
		for _, d := range c.Data {
			if d.File == "" {
				query = append(query, d.Encoded())
			}
		}
	}
	return addQuery(u, strings.Join(query, "&"))
}

// addQuery appends q to the query of u, before a fragment.
func addQuery(u, q string) string {
	if q == "" {
		return u
	}
	frag := ""
	if i := strings.IndexByte(u, '#'); i >= 0 {
		u, frag = u[:i], u[i:]
	}
	switch {
	case !strings.Contains(u, "?"):
		u += "?"
	case !strings.HasSuffix(u, "?") && !strings.HasSuffix(u, "&"):
		u += "&"
	}
	return u + q + frag
}

// queryRows returns the query-parameter rows of u: its query split at "&",
// each entry at the first "=", as written (nothing is decoded), empty
// entries left out. The editor's rule (frontend lib/urlParams.ts).
func queryRows(u string) []projectfile.KeyValue {
	if i := strings.IndexByte(u, '#'); i >= 0 {
		u = u[:i]
	}
	_, q, ok := strings.Cut(u, "?")
	if !ok {
		return nil
	}
	var rows []projectfile.KeyValue
	for entry := range strings.SplitSeq(q, "&") {
		if entry == "" {
			continue
		}
		k, v, _ := strings.Cut(entry, "=")
		rows = append(rows, projectfile.KeyValue{Name: k, Value: v})
	}
	return rows
}

func (m *mapper) headers() []projectfile.KeyValue {
	c := m.c
	var rows []projectfile.KeyValue
	for _, h := range c.Headers {
		name := strings.TrimSpace(h.Name)
		switch {
		case h.Implied:
			// Content-Type from --json comes with the body (raw JSON or the
			// file's content type); Accept is sent as curl sends it.
			if strings.EqualFold(name, "Content-Type") {
				continue
			}
			rows = append(rows, projectfile.KeyValue{Name: name, Value: h.Value})
		case strings.EqualFold(name, "Content-Length"):
			m.diag(CodeNotImported, SeverityInfo, h.Start, h.End,
				"The Content-Length header is not imported: PostSonde sends the length of the body it sends.")
		case h.Kind == HeaderRemove:
			m.diag(CodeNotImported, SeverityInfo, h.Start, h.End,
				"“"+name+":” stops curl from sending a header of its own; PostSonde sends no such header unless the body or auth needs it, so it is not imported.")
		default:
			rows = append(rows, projectfile.KeyValue{Name: name, Value: h.Value})
		}
	}
	if c.UserAgent != nil && *c.UserAgent != "" {
		rows = append(rows, projectfile.KeyValue{Name: "User-Agent", Value: *c.UserAgent})
	}
	if c.Referer != nil && *c.Referer != "" {
		rows = append(rows, projectfile.KeyValue{Name: "Referer", Value: *c.Referer})
	}
	if len(c.Cookies) > 0 {
		rows = append(rows, projectfile.KeyValue{Name: "Cookie", Value: strings.Join(c.Cookies, "; ")})
	}
	if c.Compressed {
		// curl decompresses the response; PostSonde shows it as received,
		// so it asks for none.
		n := len(rows)
		rows = slices.DeleteFunc(rows, func(h projectfile.KeyValue) bool { return strings.EqualFold(h.Name, "Accept-Encoding") })
		msg := "PostSonde shows the response as received and does not decompress it, so %o is not imported."
		if len(rows) < n {
			msg = "PostSonde shows the response as received and does not decompress it, so %o and the Accept-Encoding header are not imported."
		}
		m.option("compressed", CodeNotImported, SeverityInfo, msg)
	}
	return rows
}

// headerValue returns the value of the last -H header called name that is
// set, and whether there is one.
func (m *mapper) headerValue(name string) (string, bool) {
	value, found := "", false
	for _, h := range m.c.Headers {
		if !h.Implied && h.Kind != HeaderRemove && strings.EqualFold(strings.TrimSpace(h.Name), name) {
			value, found = h.Value, true
		}
	}
	return value, found
}

func (m *mapper) body() projectfile.Body {
	c := m.c
	switch {
	case c.Head:
		return projectfile.Body{Mode: projectfile.BodyNone}
	case len(c.Form) > 0:
		return m.multipart()
	case len(c.Data) > 0 && !c.Get:
		return m.data()
	case c.UploadFile != "" && c.UploadFile != "-":
		m.filePath(c.options["upload-file"], c.UploadFile)
		return projectfile.Body{Mode: projectfile.BodyFile, Path: c.UploadFile}
	}
	return projectfile.Body{Mode: projectfile.BodyNone}
}

// filePath warns that the file is sent from its path, which for a
// relative path is relative to the project, not where curl ran.
func (m *mapper) filePath(a Arg, file string) {
	if path.IsAbs(file) || isWindowsAbs(file) {
		return
	}
	m.diag(CodeFileNotRead, SeverityWarning, a.Start, a.End,
		"The file "+quoteText(file)+" is sent from its path, which PostSonde reads relative to the project folder, not to where curl ran.")
}

func isWindowsAbs(p string) bool {
	return len(p) >= 3 && p[1] == ':' && (p[2] == '\\' || p[2] == '/') ||
		strings.HasPrefix(p, `\\`)
}

func (m *mapper) multipart() projectfile.Body {
	b := projectfile.Body{Mode: projectfile.BodyMultipart}
	for _, f := range m.c.Form {
		at := Arg{Start: f.Start, End: f.End}
		part := projectfile.Part{Name: f.Name, ContentType: f.ContentType}
		switch f.Kind {
		case FormText:
			part.Value = f.Value
		case FormFile, FormFileContent:
			if f.File == "-" {
				continue // reported by Parse
			}
			part.File = f.File
			if f.Kind == FormFileContent {
				m.diag(CodeFileNotRead, SeverityWarning, f.Start, f.End,
					"curl sends the content of "+quoteText(f.File)+" as a text field; PostSonde sends the file as an upload.")
			} else {
				m.filePath(at, f.File)
			}
		}
		if f.Filename != "" {
			m.diag(CodeNotImported, SeverityWarning, f.Start, f.End,
				"The part’s “filename=” is not imported; the file’s own name is sent.")
		}
		if len(f.Headers) > 0 {
			m.diag(CodeNotImported, SeverityWarning, f.Start, f.End,
				"The part’s “headers=” are not imported.")
		}
		b.Parts = append(b.Parts, part)
	}
	if ct, ok := m.headerValue("Content-Type"); ok && mediaType(ct) == request.MediaTypeMultipart && !strings.Contains(strings.ToLower(ct), "boundary=") {
		m.dropContentType = true
		for _, h := range m.c.Headers {
			if !h.Implied && strings.EqualFold(strings.TrimSpace(h.Name), "Content-Type") {
				m.diag(CodeNotImported, SeverityInfo, h.Start, h.End,
					"The Content-Type header is not imported: PostSonde sends multipart/form-data with the boundary it generates.")
			}
		}
	}
	return b
}

// data maps -d, --data-*, --json parts (without -G) to a body.
func (m *mapper) data() projectfile.Body {
	c := m.c
	ct, hasCT := m.headerValue("Content-Type")
	isJSON := false
	for _, d := range c.Data {
		isJSON = isJSON || d.Kind == DataJSON
	}
	defaultCT := request.MediaTypeFormURLEncoded
	if isJSON {
		defaultCT = "application/json"
	}

	// One file: a file body.
	if d := c.Data[0]; len(c.Data) == 1 && d.File != "" {
		if d.File == "-" {
			return projectfile.Body{Mode: projectfile.BodyNone} // reported by Parse
		}
		at := Arg{Start: d.Start, End: d.End}
		if d.Kind == DataASCII || d.Kind == DataURLEncode {
			m.diag(CodeFileNotRead, SeverityWarning, d.Start, d.End,
				"curl changes the file "+quoteText(d.File)+" as it reads it (line breaks removed or content encoded); PostSonde sends the file as it is.")
		} else {
			m.filePath(at, d.File)
		}
		b := projectfile.Body{Mode: projectfile.BodyFile, Path: d.File}
		if !hasCT {
			b.ContentType = defaultCT
		}
		return b
	}

	var parts []string
	for i, d := range c.Data {
		if d.File != "" {
			m.diag(CodeFileNotRead, SeverityWarning, d.Start, d.End,
				"The content of "+quoteText(d.File)+" is missing from the imported body; curl reads it when it runs.")
			continue
		}
		if i > 0 && d.Kind != DataJSON && len(parts) > 0 {
			parts = append(parts, "&")
		}
		parts = append(parts, d.Encoded())
	}
	text := strings.Join(parts, "")
	first := c.Data[0]
	media := mediaType(ct)

	switch {
	case isJSON && !hasCT:
		return rawBody(projectfile.RawJSON, text)
	case !hasCT && looksLikeJSON(text):
		m.diag(CodeBodyType, SeverityInfo, first.Start, first.End,
			"The body is JSON but has no Content-Type header; it is imported as JSON, where curl would send it as a form.")
		return rawBody(projectfile.RawJSON, text)
	case !hasCT || media == request.MediaTypeFormURLEncoded:
		if fields, ok := formFields(text); ok {
			return projectfile.Body{Mode: projectfile.BodyURLEncoded, Fields: fields}
		}
		if !hasCT {
			m.contentType = defaultCT
		}
		return rawBody(projectfile.RawText, text)
	}
	return rawBody(rawType(media), text)
}

func rawBody(typ, text string) projectfile.Body {
	return projectfile.Body{Mode: projectfile.BodyRaw, Type: typ, Text: text}
}

// mediaType is the lower-case media type of a Content-Type value, without
// parameters.
func mediaType(ct string) string {
	mt, _, _ := strings.Cut(ct, ";")
	return strings.ToLower(strings.TrimSpace(mt))
}

// rawType is the raw body type for a media type.
func rawType(media string) string {
	switch {
	case media == "application/json" || strings.HasSuffix(media, "+json"):
		return projectfile.RawJSON
	case media == "application/xml" || media == "text/xml" || strings.HasSuffix(media, "+xml"):
		return projectfile.RawXML
	case media == "text/html":
		return projectfile.RawHTML
	case strings.Contains(media, "yaml"):
		return projectfile.RawYAML
	}
	return projectfile.RawText
}

func looksLikeJSON(s string) bool {
	t := strings.TrimSpace(s)
	return (strings.HasPrefix(t, "{") || strings.HasPrefix(t, "[")) && json.Valid([]byte(t))
}

// formFields splits a URL-encoded body into decoded fields. It fails for a
// body that is not one: an entry without "=", invalid percent-encoding, or
// text that would not be sent the same way (a {{variable}}).
func formFields(text string) ([]projectfile.KeyValue, bool) {
	if text == "" || strings.Contains(text, "{{") {
		return nil, false
	}
	var fields []projectfile.KeyValue
	for entry := range strings.SplitSeq(text, "&") {
		if entry == "" {
			continue
		}
		k, v, ok := strings.Cut(entry, "=")
		if !ok || k == "" || strings.ContainsAny(entry, " \t\r\n") {
			return nil, false
		}
		name, err1 := url.QueryUnescape(k)
		value, err2 := url.QueryUnescape(v)
		if err1 != nil || err2 != nil {
			return nil, false
		}
		fields = append(fields, projectfile.KeyValue{Name: name, Value: value})
	}
	return fields, len(fields) > 0
}

func (m *mapper) auth() projectfile.Auth {
	c := m.c
	inherit := projectfile.Auth{Type: projectfile.AuthInherit}
	if c.User != nil {
		user := projectfile.Auth{Type: projectfile.AuthBasic, Username: c.User.User, Password: c.User.Password}
		imported := true
		switch c.AuthScheme {
		case "", "basic":
		case "digest":
			// PostSonde answers the server's Digest challenge, as curl
			// does (AUTH-06).
			user.Type = projectfile.AuthDigest
		case "any":
			m.option("anyauth", CodeNotImported, SeverityInfo,
				"PostSonde does not negotiate the auth scheme; %o is imported as Basic auth.")
		default:
			imported = false
			m.option(c.AuthScheme, CodeNotImported, SeverityWarning,
				"PostSonde has no "+authLabel(c.AuthScheme)+" auth; %o and the user are not imported.")
		}
		if imported {
			if c.OAuth2Bearer != "" {
				m.option("oauth2-bearer", CodeNotImported, SeverityWarning,
					"A request has one auth; %o is not imported because -u is.")
			}
			return user
		}
	}
	if c.OAuth2Bearer != "" {
		return projectfile.Auth{Type: projectfile.AuthBearer, Token: c.OAuth2Bearer}
	}
	return inherit
}

func authLabel(scheme string) string {
	switch scheme {
	case "ntlm":
		return "NTLM"
	case "negotiate":
		return "Negotiate (Kerberos)"
	}
	return scheme
}

func (m *mapper) settings() projectfile.RequestSettings {
	c := m.c
	var s projectfile.RequestSettings
	if c.Insecure {
		off := false
		s.VerifyTLS = &off
	}
	if c.FollowRedirects {
		on := true
		s.FollowRedirects = &on
		if c.trusted {
			s.ForwardAuthorization = &on
		}
		if c.MaxRedirects != nil {
			if n := *c.MaxRedirects; n >= 0 {
				s.MaxRedirects = &n
			} else {
				m.option("max-redirs", CodeNotImported, SeverityInfo,
					"PostSonde always limits redirects; with %o -1 the app’s limit applies.")
			}
		}
	} else if c.MaxRedirects != nil {
		m.option("max-redirs", CodeNotImported, SeverityInfo,
			"%o only matters with -L, which the command does not use; it is not imported.")
	}
	if c.MaxTime > 0 {
		ms := int64(math.Ceil(float64(c.MaxTime) / float64(time.Millisecond)))
		s.TimeoutMs = &ms
	}
	if c.ConnectTimeout > 0 {
		m.option("connect-timeout", CodeNotImported, SeverityWarning,
			"HTTP requests have no connect timeout of their own; %o is not imported.")
	}
	switch c.HTTPVersion {
	case "":
	case "1.1":
		s.HTTPVersion = projectfile.HTTPVersion1
	case "2-prior-knowledge":
		s.HTTPVersion = projectfile.HTTPVersion2
	default:
		m.option("http"+c.HTTPVersion, CodeNotImported, SeverityWarning,
			"A request’s HTTP version is automatic (HTTP/2 when a TLS server offers it), HTTP/1.1 or HTTP/2 only; %o is not imported.")
	}
	s.Proxy = m.proxy()
	return s
}

// proxy maps -x, -U and --noproxy onto the request's own proxy
// (settings.proxy, docs/network.md#a-requests-own-proxy): -x as a custom
// proxy (http:// when it has no scheme, as curl reads it), -U as its user
// name and password, and --noproxy '*' (or an empty -x) as no proxy.
// Other --noproxy lists are not imported: a request has no bypass list.
func (m *mapper) proxy() projectfile.RequestProxy {
	c := m.c
	_, hasProxy := c.options["proxy"]
	noProxy := strings.TrimSpace(c.NoProxy)
	switch {
	case noProxy == "*":
		if hasProxy {
			m.option("proxy", CodeNotImported, SeverityInfo, "%o is not used with “--noproxy *”, which connects directly; it is not imported.")
		}
		if c.ProxyUser != nil {
			m.option("proxy-user", CodeNotImported, SeverityInfo, "%o is not used with “--noproxy *”, which connects directly; it is not imported.")
		}
		return projectfile.RequestProxy{Mode: projectfile.ProxyNone}
	case noProxy != "":
		m.option("noproxy", CodeNotImported, SeverityWarning,
			"A request’s own proxy has no bypass list; %o is imported only as “*”, which connects directly.")
	}
	if !hasProxy {
		if c.ProxyUser != nil {
			m.option("proxy-user", CodeNotImported, SeverityWarning, "%o only matters with -x, which the command does not use; it is not imported.")
		}
		return projectfile.RequestProxy{}
	}
	raw := strings.TrimSpace(c.Proxy)
	if raw == "" {
		// curl: an empty proxy turns every proxy off.
		return projectfile.RequestProxy{Mode: projectfile.ProxyNone}
	}
	if !strings.Contains(raw, "://") {
		raw = "http://" + raw
	}
	if !strings.Contains(raw, "{{") {
		if _, err := netx.ParseProxyURL(raw); err != nil {
			m.option("proxy", CodeNotImported, SeverityWarning,
				"PostSonde cannot use this proxy (it takes socks5h, socks5, socks4a, socks4, http and https proxies); %o is not imported.")
			return projectfile.RequestProxy{}
		}
	}
	p := projectfile.RequestProxy{Mode: projectfile.ProxyCustom, URL: raw}
	if u := c.ProxyUser; u != nil && u.User != "" {
		// The user goes into the URL, replacing one there; the password
		// is kept apart, and moved to local storage when the request is
		// saved.
		scheme, rest, _ := strings.Cut(raw, "://")
		if at := strings.LastIndex(strings.SplitN(rest, "/", 2)[0], "@"); at >= 0 {
			rest = rest[at+1:]
		}
		user := u.User
		if !strings.Contains(user, "{{") {
			user = url.User(user).String()
		}
		p.URL = scheme + "://" + user + "@" + rest
		p.Password = u.Password
	}
	return p
}

// requestName is the URL's last path segment, else its host, else
// "Imported request".
func requestName(u string) string {
	rest := u
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	if i := strings.IndexAny(rest, "?#"); i >= 0 {
		rest = rest[:i]
	}
	host, p, _ := strings.Cut(rest, "/")
	segs := strings.FieldsFunc(p, func(r rune) bool { return r == '/' })
	name := host
	if len(segs) > 0 {
		name = segs[len(segs)-1]
		if dec, err := url.PathUnescape(name); err == nil {
			name = dec
		}
	}
	if at := strings.LastIndexByte(name, '@'); at >= 0 && name == host {
		name = name[at+1:] // no user:password in the name
	}
	name = strings.TrimSpace(name)
	if name == "" {
		return "Imported request"
	}
	return name
}
