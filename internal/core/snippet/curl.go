package snippet

import (
	"path"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/httpclient"
)

// formContentType is the Content-Type curl sends with -d and
// --data-binary unless a header sets one.
const formContentType = "application/x-www-form-urlencoded"

// Curl returns r as a curl command for a POSIX shell (GEN-01): every
// argument quoted so the shell passes it unchanged, the URL on the first
// line and one option per continued line. docs/curl.md#copy-as-curl lists
// the mapping. It fails with CodeNulByte when a value holds a NUL byte,
// which no command-line argument can carry.
func Curl(r Request) (string, error) { return curlCommand(r, posixShell) }

// curlShell is a shell a curl command is written for.
type curlShell struct {
	// command is the program's name ("curl", "curl.exe").
	command string
	// quote writes s as one word of the shell; field names s in errors.
	quote func(s, field string) (string, error)
	// cont ends a line of the command and starts the next.
	cont string
}

// posixShell is a POSIX shell (bash, zsh, dash, ksh).
var posixShell = curlShell{command: "curl", quote: quoteArg, cont: " \\\n  "}

// curlCommand writes r as a curl command for sh: the URL on the first
// line and one option per continued line (Curl, CurlCmd, CurlPowerShell).
func curlCommand(r Request, sh curlShell) (string, error) {
	var lines [][]string // the first holds curl, the method and the URL
	add := func(words ...string) { lines = append(lines, words) }
	quoteArg := sh.quote

	first := []string{sh.command}
	hasBody := r.Body.Kind != BodyNone
	switch {
	case !hasBody && r.Method == "HEAD":
		first = append(first, "-I")
	case !hasBody && r.Method == "GET", hasBody && r.Method == "POST":
	default:
		m, err := quoteArg(r.Method, "method")
		if err != nil {
			return "", err
		}
		first = append(first, "-X", m)
	}
	u, err := quoteArg(r.URL, "URL")
	if err != nil {
		return "", err
	}
	lines = append(lines, append(first, u))
	if strings.ContainsAny(r.URL, "[]{}") {
		// curl would expand these as URL globs.
		add("-g")
	}
	if hasDotSegment(r.URL) {
		// curl would remove "." and ".." segments; Send keeps them.
		add("--path-as-is")
	}

	hasContentType := false
	for _, h := range r.Headers {
		if strings.EqualFold(h.Name, "Content-Type") {
			hasContentType = true
		}
		v := h.Name + ": " + h.Value
		switch {
		case h.Value == "" && strings.EqualFold(h.Name, "User-Agent"):
			// Send sends no User-Agent for an empty row; "Name:" makes
			// curl leave out its own.
			v = h.Name + ":"
		case h.Value == "":
			// "Name:" would remove the header; "Name;" sends it empty.
			v = h.Name + ";"
		}
		q, err := quoteArg(v, "header "+h.Name)
		if err != nil {
			return "", err
		}
		add("-H", q)
	}
	if (r.Body.Kind == BodyData || r.Body.Kind == BodyFile) && !hasContentType {
		switch ct := r.Body.ContentType; ct {
		case formContentType:
			// curl's own.
		case "":
			q, err := quoteArg("Content-Type:", "Content-Type")
			if err != nil {
				return "", err
			}
			add("-H", q)
		default:
			q, err := quoteArg("Content-Type: "+ct, "Content-Type")
			if err != nil {
				return "", err
			}
			add("-H", q)
		}
	}

	switch {
	case r.Basic != nil:
		q, err := quoteArg(r.Basic.Username+":"+r.Basic.Password, "credentials")
		if err != nil {
			return "", err
		}
		add("-u", q)
	case r.Digest != nil:
		q, err := quoteArg(r.Digest.Username+":"+r.Digest.Password, "credentials")
		if err != nil {
			return "", err
		}
		add("--digest", "-u", q)
	}
	if r.Cookies != "" {
		// A -b value without "=" names a cookie file.
		opt, v := "-b", r.Cookies
		if !strings.Contains(v, "=") {
			opt, v = "-H", "Cookie: "+v
		}
		q, err := quoteArg(v, "cookies")
		if err != nil {
			return "", err
		}
		add(opt, q)
	}

	switch r.Body.Kind {
	case BodyData:
		q, err := quoteArg(string(r.Body.Data), "body")
		if err != nil {
			return "", err
		}
		add("--data-raw", q)
	case BodyFile:
		q, err := quoteArg("@"+r.Body.File, "body file path")
		if err != nil {
			return "", err
		}
		add("--data-binary", q)
	case BodyMultipart:
		for _, p := range r.Body.Parts {
			opt, v := formPart(p)
			q, err := quoteArg(v, "multipart part "+p.Name)
			if err != nil {
				return "", err
			}
			add(opt, q)
		}
	}

	if r.Insecure {
		add("-k")
	}
	if r.FollowRedirects {
		follow := "-L"
		if r.ForwardAuthorization {
			follow = "--location-trusted"
		}
		add(follow, "--max-redirs", strconv.Itoa(r.MaxRedirects))
		if r.KeepMethod && hasBody && r.Method == "POST" {
			// Without -X curl turns a POST into a GET on these, as Send
			// does unless the method is kept.
			add("--post301", "--post302", "--post303")
		}
	}
	if r.Timeout > 0 {
		add("-m", strconv.FormatFloat(r.Timeout.Seconds(), 'f', -1, 64))
	}
	switch r.HTTPVersion {
	case httpclient.HTTPVersion1:
		add("--http1.1")
	case httpclient.HTTPVersion2:
		// HTTP/2 only, as Send: h2c with prior knowledge on http, only
		// h2 offered on https.
		add("--http2-prior-knowledge")
	}
	// The request's own proxy: -x takes every scheme PostSonde does
	// (socks5h://, socks5://, socks4a://, socks4://, http://, https://).
	// No proxy of its own leaves curl to its environment, as the app's
	// proxy is not written either.
	switch {
	case r.Proxy != "":
		q, err := quoteArg(r.Proxy, "proxy")
		if err != nil {
			return "", err
		}
		add("-x", q)
		if r.ProxyUser != nil {
			q, err := quoteArg(r.ProxyUser.Username+":"+r.ProxyUser.Password, "proxy credentials")
			if err != nil {
				return "", err
			}
			add("--proxy-user", q)
		}
	case r.NoProxy:
		q, err := quoteArg("*", "proxy")
		if err != nil {
			return "", err
		}
		add("--noproxy", q)
	}

	var b strings.Builder
	for i, line := range lines {
		if i > 0 {
			b.WriteString(sh.cont)
		}
		b.WriteString(strings.Join(line, " "))
	}
	return b.String(), nil
}

// formPart returns the option and value that send p: --form-string for a
// text part without a Content-Type (its value is literal), else -F with
// the value or the file path in curl's "..." quoting.
func formPart(p Part) (string, string) {
	var v string
	switch {
	case p.File != "":
		v = p.Name + "=@" + formParam(p.File)
		if p.Filename != "" && p.Filename != path.Base(strings.ReplaceAll(p.File, `\`, "/")) {
			v += ";filename=" + formParam(p.Filename)
		}
	case p.ContentType == "":
		return "--form-string", p.Name + "=" + p.Value
	default:
		v = p.Name + "=" + formQuote(p.Value)
	}
	switch {
	case p.ContentType == "":
	case simpleMediaType(p.ContentType):
		v += ";type=" + p.ContentType
	default:
		// curl's type= stops at ";" and ",": a type with parameters is
		// sent as the part's own header, which replaces curl's.
		v += ";headers=" + formQuote("Content-Type: "+p.ContentType)
	}
	return "-F", v
}

// simpleMediaType reports whether curl's -F type= takes t as it is: a
// "major/minor" type without parameters or blanks.
func simpleMediaType(t string) bool {
	major, minor, ok := strings.Cut(t, "/")
	return ok && major != "" && minor != "" && !strings.ContainsAny(t, ";,\"\\ \t\r\n") && !strings.Contains(minor, "/")
}

// formParam quotes a -F word when curl would otherwise end it early (at
// ";" or ","), strip or unescape it.
func formParam(s string) string {
	if s != "" && !strings.ContainsAny(s, ";,\"\\ \t") {
		return s
	}
	return formQuote(s)
}

// formQuote writes s in -F's "..." quoting: \ and " escaped.
func formQuote(s string) string {
	return `"` + strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(s) + `"`
}

// hasDotSegment reports whether the path of u, a URL as sent, has a "."
// or ".." segment.
func hasDotSegment(u string) bool {
	rest := u
	if i := strings.Index(rest, "://"); i >= 0 {
		rest = rest[i+3:]
	}
	i := strings.IndexByte(rest, '/')
	if i < 0 {
		return false
	}
	p := rest[i:]
	if j := strings.IndexByte(p, '?'); j >= 0 {
		p = p[:j]
	}
	for seg := range strings.SplitSeq(p, "/") {
		if seg == "." || seg == ".." {
			return true
		}
	}
	return false
}

// quoteArg is Quote, failing with CodeNulByte for a value with a NUL
// byte; field names the value in the error.
func quoteArg(s, field string) (string, error) {
	if strings.IndexByte(s, 0) >= 0 {
		return "", nulError(field)
	}
	return Quote(s), nil
}

// Quote returns s as one POSIX shell word that the shell reads back as s:
//
//   - s itself when it is not empty and only has letters, digits and
//   - _ . / : = @ % + , (never special to the shell);
//   - '...' otherwise, with ' written as '\” (line breaks and tabs are
//     kept as they are);
//   - $'...' (POSIX.1-2024 dollar-single-quotes, in bash, zsh and ksh)
//     when s has other control characters or is not valid UTF-8, which a
//     terminal paste could change: \\ \' \n \t \r and \xHH escapes.
//
// A NUL byte cannot be passed and is written as \x00, which ends the
// argument; quoteArg refuses it.
func Quote(s string) string {
	if s == "" {
		return "''"
	}
	plain, literal := true, true
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case strings.IndexByte("-_./:=@%+,", c) >= 0:
		case c == '\n' || c == '\t':
			plain = false
		case c < ' ' || c == 0x7f:
			plain, literal = false, false
		default:
			plain = false
		}
	}
	if plain && s[0] != '-' {
		return s
	}
	if literal && utf8.ValidString(s) {
		return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
	}
	const hex = "0123456789abcdef"
	var b strings.Builder
	b.WriteString("$'")
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		switch {
		case r == utf8.RuneError && size <= 1:
			c := s[i]
			b.WriteString(`\x` + string(hex[c>>4]) + string(hex[c&15]))
		case r == '\\':
			b.WriteString(`\\`)
		case r == '\'':
			b.WriteString(`\'`)
		case r == '\n':
			b.WriteString(`\n`)
		case r == '\t':
			b.WriteString(`\t`)
		case r == '\r':
			b.WriteString(`\r`)
		case r < ' ' || r == 0x7f:
			b.WriteString(`\x` + string(hex[r>>4]) + string(hex[r&15]))
		default:
			b.WriteString(s[i : i+size])
		}
		i += size
	}
	b.WriteString("'")
	return b.String()
}
