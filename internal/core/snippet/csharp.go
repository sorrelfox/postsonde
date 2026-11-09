package snippet

// csharp.go: the request as a C# HttpClient program (GEN-09).
// docs/snippets.md#c-httpclient.

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/sorrelfox/postsonde/internal/core/httpclient"
)

// csContentHeaders are the headers .NET keeps on HttpContent rather than
// on the request.
var csContentHeaders = []string{"allow", "content-disposition", "content-encoding", "content-language",
	"content-location", "content-md5", "content-range", "content-type", "expires", "last-modified"}

// CSharp returns r as a C# program with top-level statements (.NET 6 or
// later) that sends it with HttpClient over a SocketsHttpHandler
// (GEN-09).
//
// Header rows are TryAddWithoutValidation, on the content for content
// headers (Content-Type…); Content-Length is left out (.NET writes it), as
// is an empty User-Agent row (.NET sends none of its own). A Cookie row
// turns the handler's cookies off (UseCookies = false), and a value that
// is not ASCII sets RequestHeaderEncodingSelector to UTF-8. Basic
// credentials are an Authorization header, Digest ones the handler's
// Credentials (answered to the challenge). A text body is a
// ByteArrayContent of its UTF-8 bytes (no Content-Type of its own), a
// file body a StreamContent, a multipart body a MultipartContent of
// form-data whose parts carry their Content-Disposition as PostSonde
// writes it (MultipartFormDataContent would rewrite it). The URL is a
// Uri that keeps its path and query as written when .NET would change
// them (csURLKept). Redirects (AllowAutoRedirect,
// MaxAutomaticRedirections), TLS verification off, the client's Timeout
// and the request's Version (exact) follow the settings. It fails with
// CodeUnsupportedChar for text that is not UTF-8.
func CSharp(r Request) (string, error) {
	if err := textFields(r, func(s, field string) error { return validText(s, field, "C#") }); err != nil {
		return "", err
	}
	usings := map[string]bool{"System": true, "System.Net.Http": true}
	var handler, pre, req []string

	headers := sentHeaders(r)
	if hasHeader(headers, "Cookie") {
		handler = append(handler, "UseCookies = false,")
	}
	if slices.ContainsFunc(headers, func(h Header) bool { return !ascii(h.Value) }) {
		usings["System.Text"] = true
		handler = append(handler, "RequestHeaderEncodingSelector = (_, _) => Encoding.UTF8,")
	}
	if r.Insecure {
		usings["System.Net.Security"] = true
		handler = append(handler, "SslOptions = new SslClientAuthenticationOptions",
			"{",
			"    RemoteCertificateValidationCallback = (_, _, _, _) => true,",
			"},")
	}
	if r.FollowRedirects {
		handler = append(handler, "MaxAutomaticRedirections = "+strconv.Itoa(r.MaxRedirects)+",")
	} else {
		handler = append(handler, "AllowAutoRedirect = false,")
	}
	if c := r.Digest; c != nil {
		usings["System.Net"] = true
		handler = append(handler, "Credentials = new NetworkCredential("+csString(c.Username)+", "+csString(c.Password)+"),")
	}

	switch r.HTTPVersion {
	case httpclient.HTTPVersion1, httpclient.HTTPVersion2:
		usings["System.Net"] = true
		v := "Version11"
		if r.HTTPVersion == httpclient.HTTPVersion2 {
			v = "Version20"
		}
		req = append(req, "request.Version = HttpVersion."+v+";",
			"request.VersionPolicy = HttpVersionPolicy.RequestVersionExact;")
	}
	var content []string
	for _, h := range headers {
		name := strings.ToLower(h.Name)
		switch {
		case noUserAgent(h), name == "content-length":
		case slices.Contains(csContentHeaders, name):
			content = append(content, "request.Content.Headers.TryAddWithoutValidation("+csString(h.Name)+", "+csString(h.Value)+");")
		default:
			req = append(req, "request.Headers.TryAddWithoutValidation("+csString(h.Name)+", "+csString(h.Value)+");")
		}
	}
	if c := r.Basic; c != nil {
		usings["System.Net.Http.Headers"], usings["System.Text"] = true, true
		req = append(req, `request.Headers.Authorization = new AuthenticationHeaderValue("Basic",`,
			"    Convert.ToBase64String(Encoding.UTF8.GetBytes("+csString(c.Username+":"+c.Password)+")));")
	}

	switch r.Body.Kind {
	case BodyData:
		usings["System.Text"] = true
		data := string(r.Body.Data)
		if lines := splitLines(data); len(lines) > 1 {
			pre = append(pre, "var body =")
			for i, l := range lines {
				end := " +"
				if i == len(lines)-1 {
					end = ";"
				}
				pre = append(pre, "    "+csString(l)+end)
			}
			data = "body"
		} else {
			data = csString(data)
		}
		req = append(req, "request.Content = new ByteArrayContent(Encoding.UTF8.GetBytes("+data+"));")
	case BodyFile:
		usings["System.IO"] = true
		req = append(req, "request.Content = new StreamContent(File.OpenRead("+csString(r.Body.File)+"));")
	case BodyMultipart:
		// MultipartFormDataContent.Add would write the name unquoted when
		// it is a token, add a filename*, and throw on a quote in it: the
		// parts carry their own Content-Disposition instead.
		pre = append(pre, "var form = new MultipartContent(\"form-data\");")
		if slices.ContainsFunc(r.Body.Parts, func(p Part) bool { return !ascii(partDisposition(p)) || !ascii(p.ContentType) }) {
			usings["System.Text"] = true
			pre = append(pre, "form.HeaderEncodingSelector = (_, _) => Encoding.UTF8;")
		}
		for i, p := range r.Body.Parts {
			v := "part" + strconv.Itoa(i+1)
			if p.File != "" {
				usings["System.IO"] = true
				pre = append(pre, "var "+v+" = new StreamContent(File.OpenRead("+csString(p.File)+"));")
			} else {
				usings["System.Text"] = true
				pre = append(pre, "var "+v+" = new ByteArrayContent(Encoding.UTF8.GetBytes("+csString(p.Value)+"));")
			}
			pre = append(pre, v+".Headers.TryAddWithoutValidation(\"Content-Disposition\", "+csString(partDisposition(p))+");")
			if p.ContentType != "" {
				pre = append(pre, v+".Headers.TryAddWithoutValidation(\"Content-Type\", "+csString(p.ContentType)+");")
			}
			pre = append(pre, "form.Add("+v+");")
		}
		req = append(req, "request.Content = form;")
	}
	if len(content) > 0 && r.Body.Kind == BodyNone {
		req = append(req, "request.Content = new ByteArrayContent(Array.Empty<byte>());")
	}
	req = append(req, content...)

	var b strings.Builder
	names := make([]string, 0, len(usings))
	for name := range usings {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		b.WriteString("using " + name + ";\n")
	}
	b.WriteString("\n")
	if c := r.Digest; c != nil {
		b.WriteString("// Digest auth: the handler answers the server's challenge.\n")
	}
	b.WriteString("var handler = new SocketsHttpHandler\n{\n")
	for _, l := range handler {
		b.WriteString("    " + l + "\n")
	}
	b.WriteString("};\nusing var client = new HttpClient(handler);\n")
	if r.Timeout > 0 {
		b.WriteString("client.Timeout = TimeSpan.FromMilliseconds(" + strconv.FormatInt(r.Timeout.Milliseconds(), 10) + ");\n")
	}
	b.WriteString("\n")
	for _, l := range pre {
		b.WriteString(l + "\n")
	}
	uri := csString(r.URL)
	if !csURLKept(r.URL) {
		b.WriteString("// Sent as written: Uri would remove dot segments and percent-encode or decode characters.\n")
		uri = "new Uri(" + uri + ", new UriCreationOptions { DangerousDisablePathAndQueryCanonicalization = true })"
	}
	b.WriteString("using var request = new HttpRequestMessage(new HttpMethod(" + csString(r.Method) + "), " + uri + ");\n")
	for _, l := range req {
		b.WriteString(l + "\n")
	}
	b.WriteString("\nusing var response = await client.SendAsync(request);\n")
	b.WriteString("Console.WriteLine((int)response.StatusCode);\n")
	b.WriteString("Console.WriteLine(await response.Content.ReadAsStringAsync());")
	return b.String(), nil
}

// csURLKept reports whether .NET's Uri keeps the path and query of u as
// they are, so that HttpClient sends them as written. It removes dot
// segments (also percent-encoded), turns \ into /, decodes percent-encoded
// letters, digits and - . _ ~, and percent-encodes the other characters
// outside RFC 3986 and a % that starts no escape. Anything else is kept.
// A masked value (Mask) and a template reference ({{name}}) stand for text
// the user puts in, so they do not count.
func csURLKept(u string) bool {
	u = strings.ReplaceAll(u, Mask, "")
	for {
		i := strings.Index(u, "{{")
		j := strings.Index(u[max(i, 0):], "}}")
		if i < 0 || j < 0 {
			break
		}
		u = u[:i] + u[i+j+2:]
	}
	if i := strings.Index(u, "://"); i >= 0 {
		u = u[i+3:]
	}
	i := strings.IndexAny(u, "/?")
	if i < 0 {
		return true
	}
	rest := u[i:]
	for i := 0; i < len(rest); i++ {
		c := rest[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case strings.IndexByte("-._~!$&'()*+,;=:@/?[]", c) >= 0:
		case c == '%':
			hi, ok1 := hexDigit(rest, i+1)
			lo, ok2 := hexDigit(rest, i+2)
			if !ok1 || !ok2 {
				return false
			}
			if d := hi<<4 | lo; 'a' <= d && d <= 'z' || 'A' <= d && d <= 'Z' || '0' <= d && d <= '9' || strings.IndexByte("-._~", d) >= 0 {
				return false
			}
			i += 2
		default:
			return false
		}
	}
	path, _, _ := strings.Cut(rest, "?")
	for seg := range strings.SplitSeq(path, "/") {
		if seg == "." || seg == ".." {
			return false
		}
	}
	return true
}

// hexDigit is the value of the hex digit at s[i].
func hexDigit(s string, i int) (byte, bool) {
	if i >= len(s) {
		return 0, false
	}
	switch c := s[i]; {
	case '0' <= c && c <= '9':
		return c - '0', true
	case 'a' <= c && c <= 'f':
		return c - 'a' + 10, true
	case 'A' <= c && c <= 'F':
		return c - 'A' + 10, true
	}
	return 0, false
}

// csString is s as a C# string literal: a verbatim string (@"…", " as
// "") when s has a backslash and only printable characters, else a
// regular one with \\ \" and escapes for control characters and
// characters that are not printable (U+0085, U+2028 and U+2029, which end
// a line in C#, included): \0 \a \b \f \n \r \t \v \uHHHH \UHHHHHHHH.
// s must be valid UTF-8.
func csString(s string) string {
	printable := true
	for _, c := range s {
		if c < ' ' || c == 0x7f || c > 0x7f && !unicode.IsPrint(c) {
			printable = false
			break
		}
	}
	if printable && strings.ContainsRune(s, '\\') {
		return `@"` + strings.ReplaceAll(s, `"`, `""`) + `"`
	}
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range s {
		switch c {
		case '\\', '"':
			b.WriteRune('\\')
			b.WriteRune(c)
		case 0:
			b.WriteString(`\0`)
		case '\a':
			b.WriteString(`\a`)
		case '\b':
			b.WriteString(`\b`)
		case '\f':
			b.WriteString(`\f`)
		case '\n':
			b.WriteString(`\n`)
		case '\r':
			b.WriteString(`\r`)
		case '\t':
			b.WriteString(`\t`)
		case '\v':
			b.WriteString(`\v`)
		default:
			switch {
			case c < ' ' || c == 0x7f || c > 0x7f && c <= 0xffff && !unicode.IsPrint(c):
				fmt.Fprintf(&b, `\u%04x`, c)
			case c > 0xffff && !unicode.IsPrint(c):
				fmt.Fprintf(&b, `\U%08x`, c)
			default:
				b.WriteRune(c)
			}
		}
	}
	b.WriteByte('"')
	return b.String()
}
