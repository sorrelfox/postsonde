package snippet

// httpie.go: the request as HTTPie and wget commands (GEN-03), for
// a POSIX shell. docs/snippets.md#httpie-and-wget.

import (
	"strconv"
	"strings"
)

// HTTPie returns r as an HTTPie 3 command (http) for a POSIX shell
// (GEN-03): the options first, then the method and URL, then one request
// item per line. Each word is quoted with Quote.
//
// Header rows are items Name:value (an empty value Name;, an empty
// User-Agent row User-Agent: which sends none); a body in memory is
// --raw; a file body is the command's standard input (< path); a
// multipart body is --multipart with name=value and name@path;type=…
// items (a text part's Content-Type and a file part's own file name
// cannot be set and are left out). Basic and Digest credentials are
// --auth (--auth-type=digest), and --verify=no, --follow
// --max-redirects=N, --timeout and --path-as-is follow the settings. It
// fails with CodeNulByte for a NUL byte.
func HTTPie(r Request) (string, error) {
	var first, lines [][]string
	opt := func(words ...string) { first = append(first, words) }
	var err error
	q := func(s, field string) string {
		if err != nil {
			return ""
		}
		var w string
		w, err = quoteArg(s, field)
		return w
	}

	if r.Body.Kind == BodyMultipart {
		opt("--multipart")
	}
	switch {
	case r.Basic != nil:
		opt(q("--auth="+r.Basic.Username+":"+r.Basic.Password, "credentials"))
	case r.Digest != nil:
		opt("--auth-type=digest", q("--auth="+r.Digest.Username+":"+r.Digest.Password, "credentials"))
	}
	if r.Insecure {
		opt("--verify=no")
	}
	if r.FollowRedirects {
		opt("--follow", "--max-redirects="+strconv.Itoa(r.MaxRedirects))
	}
	if r.Timeout > 0 {
		opt("--timeout=" + strconv.FormatFloat(r.Timeout.Seconds(), 'f', -1, 64))
	}
	if hasDotSegment(r.URL) {
		opt("--path-as-is")
	}
	if r.Body.Kind == BodyData {
		data := string(r.Body.Data)
		if strings.HasPrefix(data, "-") {
			// A word starting with - would be read as an option.
			lines = append(lines, []string{q("--raw="+data, "body")})
		} else {
			lines = append(lines, []string{"--raw", q(data, "body")})
		}
	}
	lines = append(lines, []string{q(r.Method, "method"), q(r.URL, "URL")})

	for _, h := range sentHeaders(r) {
		item := h.Name + ":" + httpieValue(h.Value, "=")
		switch {
		case noUserAgent(h):
			item = h.Name + ":"
		case h.Value == "":
			item = h.Name + ";"
		}
		lines = append(lines, []string{q(item, "header "+h.Name)})
	}
	for _, p := range r.Body.Parts {
		name := httpieName(p.Name)
		item := name + "=" + httpieValue(p.Value, "=@")
		if p.File != "" {
			item = name + "@" + p.File
			if p.ContentType != "" {
				item += ";type=" + p.ContentType
			}
		}
		lines = append(lines, []string{q(item, "multipart part "+p.Name)})
	}
	if r.Body.Kind == BodyFile {
		lines = append(lines, []string{"<", q(r.Body.File, "body file path")})
	}
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("http")
	if len(first) == 0 {
		// --raw, or the method and URL, on the first line.
		b.WriteString(" " + strings.Join(lines[0], " "))
		lines = lines[1:]
	}
	for _, words := range first {
		b.WriteString(" " + strings.Join(words, " "))
	}
	for _, words := range lines {
		b.WriteString(" \\\n  " + strings.Join(words, " "))
	}
	return b.String(), nil
}

// httpieName escapes the characters HTTPie reads as item separators in a
// field name with a backslash.
func httpieName(s string) string {
	var b strings.Builder
	for _, c := range s {
		if strings.ContainsRune(`:=@;`, c) {
			b.WriteByte('\\')
		}
		b.WriteRune(c)
	}
	return b.String()
}

// httpieValue escapes the first character of a value when it would make
// the separator before it a longer one (":" and "=" make ":=", "=" and
// "@" make "==" and "=@"); longer lists those characters.
func httpieValue(s, longer string) string {
	if s != "" && strings.ContainsRune(longer, rune(s[0])) {
		return `\` + s
	}
	return s
}

// Wget returns r as a GNU Wget command for a POSIX shell (GEN-03): the URL
// on the first line, one option per continued line, the response body
// printed (-O -). Each word is quoted with Quote.
//
// The method is --method unless it is a GET without a body; header rows
// are --header 'Name: value', a User-Agent row --user-agent (empty: none
// sent); a body in memory is --body-data, a file body --body-file, with
// the Content-Type the body implies as a header unless it is Wget's own
// (application/x-www-form-urlencoded). Basic credentials are --user and
// --password with --auth-no-challenge (sent at once, as PostSonde does),
// Digest ones the same without it; --no-check-certificate, --max-redirect
// N (0 when redirects are not followed) and --timeout follow the
// settings. It fails with CodeBodyUnsupported for a multipart body, which
// Wget cannot send, and CodeNulByte for a NUL byte.
func Wget(r Request) (string, error) {
	if r.Body.Kind == BodyMultipart {
		return "", unsupportedBody("wget", "a multipart form")
	}
	var lines [][]string
	var err error
	add := func(opt, value, field string) {
		if err != nil {
			return
		}
		var w string
		if w, err = quoteArg(value, field); err == nil {
			lines = append(lines, []string{opt, w})
		}
	}
	if r.Method != "GET" || r.Body.Kind != BodyNone {
		add("--method", r.Method, "method")
	}
	for _, h := range sentHeaders(r) {
		switch {
		case strings.EqualFold(h.Name, "User-Agent"):
			add("--user-agent", h.Value, "header "+h.Name)
		case h.Value == "":
			add("--header", h.Name+":", "header "+h.Name)
		case strings.EqualFold(h.Name, "Content-Type") && h.Value == formContentType && !hasHeader(r.Headers, h.Name):
			// Wget's own.
		default:
			add("--header", h.Name+": "+h.Value, "header "+h.Name)
		}
	}
	for _, c := range []*Credentials{r.Basic, r.Digest} {
		if c != nil {
			add("--user", c.Username, "credentials")
			add("--password", c.Password, "credentials")
		}
	}
	if r.Basic != nil {
		lines = append(lines, []string{"--auth-no-challenge"})
	}
	switch r.Body.Kind {
	case BodyData:
		add("--body-data", string(r.Body.Data), "body")
	case BodyFile:
		add("--body-file", r.Body.File, "body file path")
	}
	if r.Insecure {
		lines = append(lines, []string{"--no-check-certificate"})
	}
	lines = append(lines, []string{"--max-redirect", strconv.Itoa(r.MaxRedirects)})
	if r.Timeout > 0 {
		lines = append(lines, []string{"--timeout", strconv.FormatFloat(r.Timeout.Seconds(), 'f', -1, 64)})
	}
	lines = append(lines, []string{"-O", "-"})
	u, uerr := quoteArg(r.URL, "URL")
	if err == nil {
		err = uerr
	}
	if err != nil {
		return "", err
	}

	var b strings.Builder
	b.WriteString("wget " + u)
	for _, words := range lines {
		b.WriteString(" \\\n  " + strings.Join(words, " "))
	}
	return b.String(), nil
}
