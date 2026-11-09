package snippet

// java.go: the request as a Java java.net.http program (GEN-08).
// docs/snippets.md#java-httpclient.

import (
	"fmt"
	"slices"
	"strconv"
	"strings"
	"unicode"

	"github.com/sorrelfox/postsonde/internal/core/httpclient"
)

// javaRestricted are the headers java.net.http.HttpClient does not let a
// request set (it throws): it writes them itself.
var javaRestricted = []string{"connection", "content-length", "expect", "host", "upgrade"}

// Java returns r as a Java 17 program (class Main, run with java
// Main.java) that sends it with java.net.http.HttpClient (GEN-08).
//
// Header rows are .header (an empty User-Agent row left out, since
// HttpClient sends its own; a header HttpClient does not let a request
// set, like Host, a comment; HttpClient sends a character that is not
// ASCII as ?); Basic credentials are an Authorization header encoded
// with Base64; a text body is BodyPublishers.ofString, several lines as a
// text block (when they are printable); a file body ofFile; a multipart
// body is written by hand (fixed parts, the
// files read with Files.readAllBytes, a random boundary). Redirects
// (Redirect.NORMAL; HttpClient follows up to 5), the HTTP version, the
// request timeout and, for TLS verification off, an SSLContext that
// trusts every certificate follow the settings. Digest auth, which
// HttpClient does not have, is a comment. It fails with
// CodeUnsupportedChar for text that is not UTF-8.
func Java(r Request) (string, error) {
	if err := textFields(r, func(s, field string) error { return validText(s, field, "Java") }); err != nil {
		return "", err
	}
	imports := map[string]bool{
		"java.net.URI": true, "java.net.http.HttpClient": true,
		"java.net.http.HttpRequest": true, "java.net.http.HttpResponse": true,
	}
	const in = "        " // a statement in main
	var pre []string
	utf8 := func() string { imports["java.nio.charset.StandardCharsets"] = true; return "StandardCharsets.UTF_8" }

	if r.Insecure {
		imports["java.security.SecureRandom"] = true
		imports["java.security.cert.X509Certificate"] = true
		imports["javax.net.ssl.SSLContext"] = true
		imports["javax.net.ssl.TrustManager"] = true
		imports["javax.net.ssl.X509TrustManager"] = true
		pre = append(pre,
			`// TLS verification is off: trust every certificate and host name.`,
			`System.setProperty("jdk.internal.httpclient.disableHostnameVerification", "true");`,
			`TrustManager[] trustAll = { new X509TrustManager() {`,
			`    public void checkClientTrusted(X509Certificate[] chain, String authType) {}`,
			`    public void checkServerTrusted(X509Certificate[] chain, String authType) {}`,
			`    public X509Certificate[] getAcceptedIssuers() { return new X509Certificate[0]; }`,
			`} };`,
			`SSLContext ssl = SSLContext.getInstance("TLS");`,
			`ssl.init(null, trustAll, new SecureRandom());`)
	}

	publisher := "HttpRequest.BodyPublishers.noBody()"
	switch r.Body.Kind {
	case BodyData:
		data := string(r.Body.Data)
		lines := splitLines(data)
		switch {
		case len(lines) <= 1:
			pre = append(pre, "String body = "+javaString(data)+";")
		case !strings.ContainsFunc(data, func(c rune) bool { return c > 0x7f && !unicode.IsPrint(c) }):
			pre = append(pre, "String body = "+javaTextBlock(data, in+"        ")+";")
		default:
			// A \u escape in a text block can make javac warn about
			// white space before it (U+2028): one literal per line.
			pre = append(pre, "String body = "+javaString(lines[0]))
			for _, l := range lines[1:] {
				pre = append(pre, "        + "+javaString(l))
			}
			pre[len(pre)-1] += ";"
		}
		publisher = "HttpRequest.BodyPublishers.ofString(body)"
	case BodyFile:
		imports["java.nio.file.Path"] = true
		publisher = "HttpRequest.BodyPublishers.ofFile(Path.of(" + javaString(r.Body.File) + "))"
	case BodyMultipart:
		imports["java.nio.file.Files"], imports["java.nio.file.Path"] = true, true
		imports["java.util.ArrayList"], imports["java.util.List"], imports["java.util.UUID"] = true, true, true
		pre = append(pre, `String boundary = "PostSonde" + UUID.randomUUID();`, "List<byte[]> form = new ArrayList<>();")
		text := func(lines ...string) {
			pre = append(pre, `form.add(("--" + boundary + "\r\n"`)
			for _, l := range lines {
				pre = append(pre, "        + "+javaString(l+"\r\n"))
			}
			pre[len(pre)-1] += ").getBytes(" + utf8() + "));"
		}
		for _, p := range r.Body.Parts {
			head := []string{"Content-Disposition: " + partDisposition(p)}
			if p.ContentType != "" {
				head = append(head, "Content-Type: "+p.ContentType)
			}
			if p.File == "" {
				text(append(head, "", p.Value)...)
				continue
			}
			text(append(head, "")...)
			pre = append(pre, "form.add(Files.readAllBytes(Path.of("+javaString(p.File)+")));",
				`form.add("\r\n".getBytes(`+utf8()+"));")
		}
		pre = append(pre, `form.add(("--" + boundary + "--\r\n").getBytes(`+utf8()+"));")
		publisher = "HttpRequest.BodyPublishers.ofByteArrays(form)"
	}

	var builder []string
	hasBody := r.Body.Kind != BodyNone
	switch {
	case r.Method == "GET" && !hasBody:
		builder = append(builder, ".GET()")
	case r.Method == "DELETE" && !hasBody:
		builder = append(builder, ".DELETE()")
	case r.Method == "POST" || r.Method == "PUT":
		builder = append(builder, "."+r.Method+"("+publisher+")")
	default:
		builder = append(builder, ".method("+javaString(r.Method)+", "+publisher+")")
	}
	for _, h := range sentHeaders(r) {
		switch {
		case noUserAgent(h):
		case slices.Contains(javaRestricted, strings.ToLower(h.Name)):
			pre = append(pre, "// HttpClient sets this header itself: "+javaString(h.Name+": "+h.Value))
		default:
			builder = append(builder, ".header("+javaString(h.Name)+", "+javaString(h.Value)+")")
		}
	}
	if r.Body.Kind == BodyMultipart && !hasHeader(r.Headers, "Content-Type") {
		builder = append(builder, `.header("Content-Type", "multipart/form-data; boundary=" + boundary)`)
	}
	if c := r.Basic; c != nil {
		imports["java.util.Base64"] = true
		builder = append(builder, `.header("Authorization", "Basic " + Base64.getEncoder().encodeToString(`+
			javaString(c.Username+":"+c.Password)+".getBytes("+utf8()+")))")
	}
	if c := r.Digest; c != nil {
		pre = append(pre, "// HttpClient has no Digest auth: answer the server's challenge for user "+javaString(c.Username)+".")
	}
	if r.Timeout > 0 {
		imports["java.time.Duration"] = true
		builder = append(builder, ".timeout(Duration.ofMillis("+strconv.FormatInt(r.Timeout.Milliseconds(), 10)+"))")
	}

	var clientOpts []string
	if r.FollowRedirects {
		clientOpts = append(clientOpts, ".followRedirects(HttpClient.Redirect.NORMAL)")
	}
	switch r.HTTPVersion {
	case httpclient.HTTPVersion1:
		clientOpts = append(clientOpts, ".version(HttpClient.Version.HTTP_1_1)")
	case httpclient.HTTPVersion2:
		clientOpts = append(clientOpts, ".version(HttpClient.Version.HTTP_2)")
	}
	if r.Insecure {
		clientOpts = append(clientOpts, ".sslContext(ssl)")
	}

	var b strings.Builder
	names := make([]string, 0, len(imports))
	for name := range imports {
		names = append(names, name)
	}
	slices.Sort(names) // java.* before javax.*
	for _, name := range names {
		b.WriteString("import " + name + ";\n")
	}
	b.WriteString("\npublic class Main {\n    public static void main(String[] args) throws Exception {\n")
	for _, l := range pre {
		b.WriteString(in + l + "\n")
	}
	if len(clientOpts) == 0 {
		b.WriteString(in + "HttpClient client = HttpClient.newHttpClient();\n")
	} else {
		b.WriteString(in + "HttpClient client = HttpClient.newBuilder()\n")
		for _, o := range clientOpts {
			b.WriteString(in + "        " + o + "\n")
		}
		b.WriteString(in + "        .build();\n")
	}
	b.WriteString(in + "HttpRequest request = HttpRequest.newBuilder(URI.create(" + javaString(r.URL) + "))\n")
	for _, o := range builder {
		b.WriteString(in + "        " + o + "\n")
	}
	b.WriteString(in + "        .build();\n")
	b.WriteString(in + "HttpResponse<String> response = client.send(request, HttpResponse.BodyHandlers.ofString());\n")
	b.WriteString(in + "System.out.println(response.statusCode());\n")
	b.WriteString(in + "System.out.println(response.body());\n")
	b.WriteString("    }\n}")
	return b.String(), nil
}

// javaString is s as a Java string literal: \ and " escaped, control
// characters as \n \r \t or octal escapes (\001), characters that are not
// printable as \uHHHH (one per UTF-16 unit). s must be valid UTF-8.
func javaString(s string) string {
	var b strings.Builder
	b.WriteByte('"')
	for _, c := range s {
		switch c {
		case '\\', '"':
			b.WriteRune('\\')
			b.WriteRune(c)
		default:
			javaChar(&b, c)
		}
	}
	b.WriteByte('"')
	return b.String()
}

// javaChar writes c into a string literal or text block: its escape when
// it is a control character or not printable. Never a \u escape for a
// character below U+0080: Java reads \u escapes before anything else, so
// \u000a would end the line.
func javaChar(b *strings.Builder, c rune) {
	switch {
	case c == '\n':
		b.WriteString(`\n`)
	case c == '\r':
		b.WriteString(`\r`)
	case c == '\t':
		b.WriteString(`\t`)
	case c < ' ' || c == 0x7f:
		fmt.Fprintf(b, `\%03o`, c)
	case c > 0x7f && !unicode.IsPrint(c):
		for _, u := range utf16Units(c) {
			fmt.Fprintf(b, `\u%04x`, u)
		}
	default:
		b.WriteRune(c)
	}
}

// javaTextBlock is s as a Java text block ("""), its lines indented with
// indent and the closing delimiter on a line of its own at that indent,
// so the indentation Java strips is exactly indent: \ and the " of """
// escaped, the last line ended with \ (no line break of its own), a
// trailing blank escaped (\s, \t) so Java keeps it, control characters
// and characters that are not printable as escapes.
func javaTextBlock(s, indent string) string {
	var b strings.Builder
	b.WriteString("\"\"\"\n")
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		if l != "" {
			b.WriteString(indent)
		}
		quotes := 0
		for j, c := range l {
			switch {
			case c == '\\':
				b.WriteString(`\\`)
			case c == '"':
				quotes++
				if quotes == 3 {
					b.WriteString(`\"`)
					quotes = 0
					continue
				}
				b.WriteRune(c)
				continue
			case (c == ' ' || c == '\t') && j == len(l)-1:
				// Java strips trailing blanks; an escape is kept.
				if c == ' ' {
					b.WriteString(`\s`)
				} else {
					b.WriteString(`\t`)
				}
			case c == '\t':
				b.WriteRune(c)
			default:
				javaChar(&b, c)
			}
			quotes = 0
		}
		if i == len(lines)-1 {
			if l == "" {
				b.WriteString(indent)
			}
			b.WriteString("\\\n")
		} else {
			b.WriteString("\n")
		}
	}
	b.WriteString(indent + `"""`)
	return b.String()
}

// utf16Units returns c in UTF-16.
func utf16Units(c rune) []rune {
	if c < 0x10000 {
		return []rune{c}
	}
	c -= 0x10000
	return []rune{0xd800 + c>>10, 0xdc00 + c&0x3ff}
}
