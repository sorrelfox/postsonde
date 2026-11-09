package curl

import (
	"math"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"
)

// Command is what a curl command line asks for, before it is mapped to a
// PostSonde request. Values are kept as curl would use them; nothing is
// read from files and no shell expansion is done.
type Command struct {
	// Dialect is the shell syntax the command was read as.
	Dialect Dialect `json:"dialect"`

	// Method is the request method: -X as given, otherwise inferred the way
	// curl does it (-I: HEAD, -G: GET, -d/-F/--json: POST, -T: PUT, else
	// GET). MethodExplicit is true when it came from -X.
	Method         string `json:"method"`
	MethodExplicit bool   `json:"methodExplicit"`

	// URL is the first URL (positional or --url), as written: it may lack
	// a scheme, which curl then guesses. URLs has every URL; only the first
	// is used.
	URL  string `json:"url"`
	URLs []Arg  `json:"urls"`
	// URLQuery holds --url-query parts, in order.
	URLQuery []DataPart `json:"urlQuery"`

	// Headers from -H, in order, duplicates and case kept, followed by the
	// ones --json implies.
	Headers []Header `json:"headers"`

	// Data holds the -d/--data-*/--json parts in order. Body joins them.
	Data []DataPart `json:"data"`
	// Get is -G: the data goes into the URL query instead of the body.
	Get bool `json:"get"`
	// Head is -I: a HEAD request.
	Head bool `json:"head"`
	// Form holds the -F/--form-string parts in order (multipart/form-data).
	Form []FormPart `json:"form"`
	// UploadFile is -T: a file sent as the body with PUT. Not read.
	UploadFile string `json:"uploadFile"`

	// User is -u. AuthScheme is "basic" (the default with -u), "digest",
	// "ntlm", "negotiate" or "any"; empty without -u or an auth option.
	User       *Credentials `json:"user"`
	AuthScheme string       `json:"authScheme"`
	// OAuth2Bearer is --oauth2-bearer.
	OAuth2Bearer string `json:"oauth2Bearer"`

	// UserAgent is -A and Referer is -e (without ";auto"); nil when not
	// given. An empty -A removes the header.
	UserAgent *string `json:"userAgent"`
	Referer   *string `json:"referer"`
	// Cookies are -b values that are cookie strings ("a=1; b=2"), in order;
	// curl sends them joined with "; ". -b values without "=" are cookie
	// files, which are reported and not read.
	Cookies []string `json:"cookies"`

	Insecure        bool `json:"insecure"`        // -k
	Compressed      bool `json:"compressed"`      // --compressed
	FollowRedirects bool `json:"followRedirects"` // -L
	// MaxRedirects is --max-redirs; nil when not given, -1 for no limit.
	MaxRedirects *int `json:"maxRedirects"`
	// HTTPVersion is "1.0", "1.1", "2", "2-prior-knowledge", "3" or
	// "3-only" from --http*; empty when not given.
	HTTPVersion string `json:"httpVersion"`
	// MaxTime is -m and ConnectTimeout --connect-timeout; 0 when not given.
	MaxTime        time.Duration `json:"maxTime"`
	ConnectTimeout time.Duration `json:"connectTimeout"`

	// Proxy is -x, ProxyUser -U and NoProxy --noproxy.
	Proxy     string       `json:"proxy"`
	ProxyUser *Credentials `json:"proxyUser"`
	NoProxy   string       `json:"noProxy"`

	// Diagnostics are sorted by Start. Err turns the first error into an
	// error value.
	Diagnostics []Diagnostic `json:"diagnostics"`

	// options has the range of the last use of each option, by long name,
	// for the diagnostics of Import.
	options map[string]Arg
	// trusted is set when the last -L was --location-trusted: credentials
	// go to other hosts on a redirect.
	trusted bool
}

// Arg is a value with the byte range of the word(s) it came from.
type Arg struct {
	Value string `json:"value"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

// Credentials is a user[:password] value from -u or -U.
type Credentials struct {
	User     string `json:"user"`
	Password string `json:"password"`
	// HasPassword is false when there was no ":"; curl would prompt.
	HasPassword bool `json:"hasPassword"`
}

// HeaderKind is what a -H value does.
type HeaderKind string

// Header kinds.
const (
	// HeaderSet: "Name: value" sends the header.
	HeaderSet HeaderKind = "set"
	// HeaderEmpty: "Name;" sends the header with an empty value.
	HeaderEmpty HeaderKind = "empty"
	// HeaderRemove: "Name:" with no value stops curl sending a header it
	// would add itself (Accept, User-Agent, Content-Type, ...).
	HeaderRemove HeaderKind = "remove"
)

// Header is one -H value.
type Header struct {
	Name  string     `json:"name"`
	Value string     `json:"value"`
	Kind  HeaderKind `json:"kind"`
	// Implied is true for Content-Type and Accept added by --json.
	Implied bool `json:"implied"`
	Start   int  `json:"start"`
	End     int  `json:"end"`
}

// DataKind is the option a data part came from.
type DataKind string

// Data kinds.
const (
	// DataASCII: -d, --data, --data-ascii. "@file" reads the file and strips
	// CR and LF from it.
	DataASCII DataKind = "data"
	// DataRaw: --data-raw. "@" has no special meaning.
	DataRaw DataKind = "raw"
	// DataBinary: --data-binary. "@file" reads the file as is.
	DataBinary DataKind = "binary"
	// DataURLEncode: --data-urlencode, and --url-query without "+".
	DataURLEncode DataKind = "urlencode"
	// DataJSON: --json, like --data-binary, and parts are joined without "&".
	DataJSON DataKind = "json"
	// DataRawQuery: --url-query "+text", added to the query as is.
	DataRawQuery DataKind = "rawquery"
)

// DataPart is one data or query value.
type DataPart struct {
	Kind DataKind `json:"kind"`
	// Option is the option as written ("-d", "--data-raw").
	Option string `json:"option"`
	// Value is the literal content: for DataURLEncode the content before
	// encoding. Empty when File is set.
	Value string `json:"value"`
	// Name is the "name" of a DataURLEncode "name=content" or "name@file".
	Name string `json:"name"`
	// File is the file the content is read from ("-" for stdin); empty for
	// literal content. The file is not read.
	File  string `json:"file"`
	Start int    `json:"start"`
	End   int    `json:"end"`
}

// Encoded returns the part as curl sends it, or "" for a file part.
func (d DataPart) Encoded() string {
	if d.File != "" {
		return ""
	}
	if d.Kind != DataURLEncode {
		return d.Value
	}
	if d.Name != "" {
		return d.Name + "=" + Escape(d.Value)
	}
	return Escape(d.Value)
}

// FormKind is the kind of a form part.
type FormKind string

// Form part kinds.
const (
	// FormText: name=value, or --form-string.
	FormText FormKind = "text"
	// FormFile: name=@file, a file upload.
	FormFile FormKind = "file"
	// FormFileContent: name=<file, a text field with the file's content.
	FormFileContent FormKind = "file_content"
)

// FormPart is one -F or --form-string value.
type FormPart struct {
	Kind  FormKind `json:"kind"`
	Name  string   `json:"name"`
	Value string   `json:"value"`
	// File is the path for FormFile and FormFileContent; not read.
	File string `json:"file"`
	// ContentType is ";type=", Filename ";filename=" and Headers
	// ";headers=" (in order).
	ContentType string   `json:"contentType"`
	Filename    string   `json:"filename"`
	Headers     []string `json:"headers"`
	Start       int      `json:"start"`
	End         int      `json:"end"`
}

// Body returns the request body curl builds from the data parts: parts
// joined with "&" (no separator before a --json part). complete is false
// when a part comes from a file, whose content is then missing.
func (c *Command) Body() (body string, complete bool) {
	var b strings.Builder
	complete = true
	for i, d := range c.Data {
		if i > 0 && d.Kind != DataJSON {
			b.WriteByte('&')
		}
		if d.File != "" {
			complete = false
			continue
		}
		b.WriteString(d.Encoded())
	}
	return b.String(), complete
}

// Escape percent-encodes s like curl_easy_escape: every byte except
// A-Z a-z 0-9 - . _ ~ becomes %XX.
func Escape(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isNameChar(c) || strings.IndexByte("-.~", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}

// Parse reads a curl command line, detecting its dialect. It always returns
// a Command; problems are in its Diagnostics and Err.
func Parse(src string) *Command {
	return ParseDialect(src, DialectAuto)
}

// ParseDialect is Parse with a given dialect.
func ParseDialect(src string, d Dialect) *Command {
	toks := Tokenize(src, d)
	c := &Command{Dialect: toks.Dialect, Diagnostics: toks.Diagnostics}
	p := &parser{c: c, src: src, end: toks.End}
	p.run(toks.Words)
	sortDiagnostics(c.Diagnostics)
	return c
}

type parser struct {
	c   *Command
	src string
	end int

	cmdStart int
	json     bool
	next     bool // stopped at --next
}

func (p *parser) diag(code string, sev Severity, start, end int, msg string) {
	p.c.Diagnostics = append(p.c.Diagnostics, Diagnostic{Code: code, Severity: sev, Start: start, End: end, Message: msg})
}

// StartsCommand reports whether line starts a curl command: an optional
// shell prompt, then curl or curl.exe (bare or with a path), then a blank
// or the end of the line. The cURL file importer splits a file into
// commands with it (docs/import.md).
func StartsCommand(line string) bool {
	rest := line[skipPrompt(line):]
	end := strings.IndexAny(rest, " \t\r\n")
	if end < 0 {
		end = len(rest)
	}
	w := rest[:end]
	if strings.ContainsAny(w, `"'^`+"`") {
		return false
	}
	return isCurl(w)
}

// isCurl reports whether a word names the curl program.
func isCurl(w string) bool {
	base := strings.ToLower(path.Base(strings.ReplaceAll(w, `\`, "/")))
	return base == "curl" || base == "curl.exe"
}

// use is one option occurrence.
type use struct {
	name       string // as written: "-d", "--data"
	long       string
	value      string
	start, end int // the option and its value
}

func (p *parser) run(words []Token) {
	if len(words) == 0 {
		p.diag(CodeEmpty, SeverityError, 0, len(p.src), "There is no command to import.")
		return
	}
	if !isCurl(words[0].Value) {
		p.diag(CodeNotCurl, SeverityError, words[0].Start, words[0].End,
			"This is not a curl command; it must start with “curl”.")
		return
	}
	p.cmdStart = words[0].Start
	args := words[1:]
	options := true
	for i := 0; i < len(args) && !p.next; i++ {
		a := args[i]
		v := a.Value
		switch {
		case !options || len(v) < 2 || v[0] != '-':
			p.url(a.Value, a.Start, a.End)
		case v == "--":
			options = false
		case strings.HasPrefix(v, "--"):
			i = p.long(args, i)
		default:
			i = p.short(args, i)
		}
	}
	p.finish()
}

func (p *parser) url(v string, start, end int) {
	c := p.c
	if len(c.URLs) > 0 {
		p.diag(CodeMultipleURLs, SeverityWarning, start, end,
			"Only the first URL is imported; this one is ignored.")
	} else {
		c.URL = v
	}
	c.URLs = append(c.URLs, Arg{Value: v, Start: start, End: end})
}

// long handles --name, --name=value and --no-name at args[i] and returns
// the index of the last word used.
func (p *parser) long(args []Token, i int) int {
	a := args[i]
	name, value, hasValue := strings.Cut(a.Value[2:], "=")
	spec, negated := lookupLong(name)
	if spec == nil {
		return p.unknown(args, i, "--"+name, hasValue)
	}
	u := use{name: "--" + name, long: spec.long, start: a.Start, end: a.End}
	switch {
	case spec.arg && hasValue:
		u.value = value
	case spec.arg && i+1 < len(args):
		i++
		u.value, u.end = args[i].Value, args[i].End
	case spec.arg:
		p.missing(u)
		return i
	case hasValue:
		p.diag(CodeUnexpectedValue, SeverityWarning, a.Start, a.End,
			"The option "+quoteText(u.name)+" takes no value; the value is ignored.")
	}
	p.apply(u, negated)
	return i
}

// short handles a cluster of short options at args[i] (-sSL, -XPOST,
// -HX:y) and returns the index of the last word used.
func (p *parser) short(args []Token, i int) int {
	a := args[i]
	cluster := a.Value[1:]
	for j := 0; j < len(cluster); {
		spec := shortOptions[cluster[j]]
		if spec == nil {
			_, size := utf8.DecodeRuneInString(cluster[j:])
			name := "-" + cluster[j:j+size]
			if len(cluster) == size {
				return p.unknown(args, i, name, false)
			}
			p.diag(CodeUnknownOption, SeverityWarning, a.Start, a.End,
				"curl has no option "+quoteText(name)+"; it is ignored.")
			j += size
			continue
		}
		u := use{name: "-" + string(cluster[j]), long: spec.long, start: a.Start, end: a.End}
		if !spec.arg {
			p.apply(u, false)
			j++
			continue
		}
		switch rest := cluster[j+1:]; {
		case rest != "":
			u.value = rest
		case i+1 < len(args):
			i++
			u.value, u.end = args[i].Value, args[i].End
		default:
			p.missing(u)
			return i
		}
		p.apply(u, false)
		break
	}
	return i
}

func (p *parser) missing(u use) {
	p.diag(CodeMissingValue, SeverityError, u.start, u.end,
		"The option "+quoteText(u.name)+" needs a value.")
}

// unknown reports an unknown option. Whether it takes a value cannot be
// known; the next word is taken as its value unless there is none, the
// option was written --name=value, or the word starts with "-" or looks
// like a URL.
func (p *parser) unknown(args []Token, i int, name string, hasValue bool) int {
	a := args[i]
	if !hasValue && i+1 < len(args) {
		n := args[i+1].Value
		if n != "" && n[0] != '-' && !looksLikeURL(n) {
			p.diag(CodeUnknownOption, SeverityWarning, a.Start, args[i+1].End,
				"curl has no option "+quoteText(name)+"; it is ignored, with the word after it taken as its value.")
			return i + 1
		}
	}
	p.diag(CodeUnknownOption, SeverityWarning, a.Start, a.End,
		"curl has no option "+quoteText(name)+"; it is ignored.")
	return i
}

func (p *parser) unsupported(u use) {
	p.diag(CodeUnsupportedOption, SeverityWarning, u.start, u.end,
		"The option "+quoteText(u.name)+" is not imported.")
}

func (p *parser) fileRef(u use, sev Severity, file string) {
	msg := "curl reads this value from the file " + quoteText(file) + "; PostSonde does not read it."
	if sev == SeverityInfo {
		msg = "curl sends the file " + quoteText(file) + "; it is kept as a path and not read here."
	}
	if file == "-" {
		msg = "curl reads this value from standard input; it is not imported."
	}
	p.diag(CodeFileReference, sev, u.start, u.end, msg)
}

func (p *parser) invalid(u use, msg string) {
	p.diag(CodeInvalidValue, SeverityWarning, u.start, u.end, msg)
}

// apply records one option.
func (p *parser) apply(u use, negated bool) {
	c := p.c
	on := !negated
	if c.options == nil {
		c.options = map[string]Arg{}
	}
	c.options[u.long] = Arg{Value: u.name, Start: u.start, End: u.end}
	switch u.long {
	case "request":
		c.Method, c.MethodExplicit = u.value, true
	case "header":
		p.header(u)
	case "data", "data-ascii":
		p.data(u, DataASCII)
	case "data-raw":
		p.data(u, DataRaw)
	case "data-binary":
		p.data(u, DataBinary)
	case "json":
		p.json = true
		p.data(u, DataJSON)
	case "data-urlencode":
		p.urlencode(u, &c.Data)
	case "url-query":
		if raw, ok := strings.CutPrefix(u.value, "+"); ok {
			c.URLQuery = append(c.URLQuery, DataPart{Kind: DataRawQuery, Option: u.name, Value: raw, Start: u.start, End: u.end})
		} else {
			p.urlencode(u, &c.URLQuery)
		}
	case "form":
		p.form(u, false)
	case "form-string":
		p.form(u, true)
	case "get":
		c.Get = on
	case "head":
		c.Head = on
	case "upload-file":
		c.UploadFile = u.value
		p.fileRef(u, SeverityInfo, u.value)
	case "url":
		p.url(u.value, u.start, u.end)
	case "user":
		c.User = p.credentials(u)
		if c.AuthScheme == "" {
			c.AuthScheme = "basic"
		}
	case "proxy-user":
		c.ProxyUser = p.credentials(u)
	case "basic", "digest", "ntlm", "negotiate":
		if on {
			c.AuthScheme = u.long
		}
	case "anyauth":
		if on {
			c.AuthScheme = "any"
		}
	case "oauth2-bearer":
		c.OAuth2Bearer = u.value
	case "user-agent":
		v := u.value
		c.UserAgent = &v
	case "referer":
		v, _ := strings.CutSuffix(u.value, ";auto")
		c.Referer = &v
	case "cookie":
		if strings.Contains(u.value, "=") {
			c.Cookies = append(c.Cookies, u.value)
		} else {
			p.fileRef(u, SeverityWarning, u.value)
		}
	case "insecure":
		c.Insecure = on
	case "compressed":
		c.Compressed = on
	case "location", "location-trusted", "follow":
		c.FollowRedirects = on
		c.trusted = on && u.long == "location-trusted"
	case "max-redirs":
		n, err := strconv.Atoi(u.value)
		if err != nil || n < -1 {
			p.invalid(u, "The number of redirects must be a whole number of -1 or more; the option is ignored.")
			return
		}
		c.MaxRedirects = &n
	case "http1.0", "http1.1", "http2", "http2-prior-knowledge", "http3", "http3-only":
		if on {
			c.HTTPVersion = strings.TrimPrefix(u.long, "http")
		}
	case "max-time":
		c.MaxTime = p.seconds(u)
	case "connect-timeout":
		c.ConnectTimeout = p.seconds(u)
	case "proxy":
		c.Proxy = u.value
	case "noproxy":
		c.NoProxy = u.value
	case "next":
		p.next = true
		p.diag(CodeUnsupportedOption, SeverityWarning, u.start, p.end,
			"Only the options before “--next” are imported; the rest is ignored.")
	case "config":
		p.unsupported(u)
		p.fileRef(u, SeverityWarning, u.value)
	default:
		if ignoredOptions[u.long] {
			p.diag(CodeIgnoredOption, SeverityInfo, u.start, u.end,
				"The option "+quoteText(u.name)+" does not change the request and is ignored.")
			return
		}
		p.unsupported(u)
	}
}

func (p *parser) header(u use) {
	v := strings.TrimRight(u.value, "\r\n")
	if file, ok := strings.CutPrefix(v, "@"); ok {
		p.fileRef(u, SeverityWarning, file)
		return
	}
	h := Header{Start: u.start, End: u.end}
	name, value, colon := strings.Cut(v, ":")
	semiName, rest, semi := strings.Cut(v, ";")
	switch {
	case colon && name != "":
		h.Name, h.Value, h.Kind = name, strings.Trim(value, " \t"), HeaderSet
		if h.Value == "" {
			h.Kind = HeaderRemove
		}
	case !colon && semi && semiName != "" && strings.Trim(rest, " \t") == "":
		h.Name, h.Kind = semiName, HeaderEmpty
	default:
		p.invalid(u, "A header must be “Name: value”, “Name:” or “Name;”; this one is ignored.")
		return
	}
	p.c.Headers = append(p.c.Headers, h)
}

func (p *parser) data(u use, kind DataKind) {
	d := DataPart{Kind: kind, Option: u.name, Value: u.value, Start: u.start, End: u.end}
	if kind != DataRaw {
		if file, ok := strings.CutPrefix(u.value, "@"); ok {
			d.Value, d.File = "", file
			sev := SeverityWarning
			if kind == DataBinary || kind == DataJSON {
				sev = SeverityInfo
			}
			p.fileRef(u, sev, file)
		}
	}
	p.c.Data = append(p.c.Data, d)
}

// urlencode parses a --data-urlencode or --url-query value: "content",
// "=content", "name=content", "@file" or "name@file". The first "=" wins;
// "@" counts only when there is no "=".
func (p *parser) urlencode(u use, to *[]DataPart) {
	d := DataPart{Kind: DataURLEncode, Option: u.name, Start: u.start, End: u.end}
	v := u.value
	k := strings.IndexByte(v, '=')
	if k < 0 {
		k = strings.IndexByte(v, '@')
	}
	switch {
	case k < 0:
		d.Value = v
	case v[k] == '=':
		d.Name, d.Value = v[:k], v[k+1:]
	default:
		d.Name, d.File = v[:k], v[k+1:]
		p.fileRef(u, SeverityWarning, d.File)
	}
	*to = append(*to, d)
}

func (p *parser) credentials(u use) *Credentials {
	user, pass, ok := strings.Cut(u.value, ":")
	if !ok {
		p.diag(CodePasswordMissing, SeverityWarning, u.start, u.end,
			"There is no password after the user name; curl would ask for it.")
	}
	return &Credentials{User: user, Password: pass, HasPassword: ok}
}

// seconds parses a timeout in seconds, fractions allowed.
func (p *parser) seconds(u use) time.Duration {
	f, err := strconv.ParseFloat(u.value, 64)
	if err != nil || f < 0 || math.IsNaN(f) || f*1e9 > math.MaxInt64 {
		p.invalid(u, "The time for "+quoteText(u.name)+" must be a number of seconds; the option is ignored.")
		return 0
	}
	return time.Duration(f * float64(time.Second))
}

// finish checks the command as a whole and infers the method.
func (p *parser) finish() {
	c := p.c
	whole := func() (int, int) { return p.cmdStart, max(p.cmdStart, p.end) }
	if len(c.URLs) == 0 {
		s, e := whole()
		p.diag(CodeMissingURL, SeverityError, s, e, "The command has no URL.")
	}
	if p.json {
		for _, name := range []string{"Content-Type", "Accept"} {
			if !p.hasHeader(name) {
				c.Headers = append(c.Headers, Header{Name: name, Value: "application/json", Kind: HeaderSet, Implied: true})
			}
		}
	}
	bodies := 0
	for _, has := range []bool{len(c.Data) > 0 && !c.Get, len(c.Form) > 0, c.UploadFile != "", c.Head} {
		if has {
			bodies++
		}
	}
	if bodies > 1 {
		s, e := whole()
		p.diag(CodeConflictingBody, SeverityWarning, s, e,
			"curl does not allow -d, -F, -T and -I together; the method and body may not be what was intended.")
	}
	if !c.MethodExplicit {
		switch {
		case c.Head:
			c.Method = "HEAD"
		case c.Get:
			c.Method = "GET"
		case len(c.Data) > 0 || len(c.Form) > 0:
			c.Method = "POST"
		case c.UploadFile != "":
			c.Method = "PUT"
		default:
			c.Method = "GET"
		}
	}
}

func (p *parser) hasHeader(name string) bool {
	for _, h := range p.c.Headers {
		if strings.EqualFold(strings.TrimSpace(h.Name), name) {
			return true
		}
	}
	return false
}
