package snippet

// golang.go: the request as a Go net/http program (GEN-07).
// docs/snippets.md#go-nethttp.

import (
	"go/format"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/httpclient"
)

// Go returns r as a Go program that sends it with net/http (GEN-07),
// formatted as gofmt does.
//
// Header rows are req.Header.Add (a Host row is req.Host; an empty
// User-Agent row sends none, as in net/http); Basic credentials are
// req.SetBasicAuth; a text body is a strings.Reader, a file body the open
// file with its size as ContentLength, a multipart body written with
// mime/multipart (each part's Content-Disposition as PostSonde writes
// it). The client's Timeout, CheckRedirect (net/http stops after 10
// redirects on its own), a transport with InsecureSkipVerify and
// Protocols (HTTP/1.1 only, or HTTP/2 only: h2c on http) follow the
// settings. Digest auth, which net/http does not have, is a comment. Any
// text can be written: a string that is not UTF-8 is written with \x
// escapes.
func Go(r Request) (string, error) {
	imports := map[string]bool{"fmt": true, "io": true, "net/http": true}
	var pre, after []string
	line := func(ls *[]string, s ...string) { *ls = append(*ls, s...) }
	check := []string{"if err != nil {", "\tpanic(err)", "}"}

	body := "nil"
	switch r.Body.Kind {
	case BodyData:
		imports["strings"] = true
		body = "strings.NewReader(" + goString(string(r.Body.Data)) + ")"
	case BodyFile:
		imports["os"] = true
		line(&pre, "file, err := os.Open("+goString(r.Body.File)+")")
		line(&pre, check...)
		line(&pre, "defer file.Close()", "info, err := file.Stat()")
		line(&pre, check...)
		body = "file"
		line(&after, "req.ContentLength = info.Size()")
	case BodyMultipart:
		imports["bytes"], imports["mime/multipart"], imports["net/textproto"] = true, true, true
		line(&pre,
			"var body bytes.Buffer",
			"form := multipart.NewWriter(&body)",
			"part := func(disposition, contentType string) io.Writer {",
			"\theader := textproto.MIMEHeader{\"Content-Disposition\": {disposition}}",
			"\tif contentType != \"\" {",
			"\t\theader.Set(\"Content-Type\", contentType)",
			"\t}",
			"\tw, err := form.CreatePart(header)",
			"\tif err != nil {",
			"\t\tpanic(err)",
			"\t}",
			"\treturn w",
			"}")
		if slices.ContainsFunc(r.Body.Parts, func(p Part) bool { return p.File != "" }) {
			imports["os"] = true
			line(&pre,
				"readFile := func(path string) []byte {",
				"\tdata, err := os.ReadFile(path)",
				"\tif err != nil {",
				"\t\tpanic(err)",
				"\t}",
				"\treturn data",
				"}")
		}
		for _, p := range r.Body.Parts {
			w := "part(" + goString(partDisposition(p)) + ", " + goString(p.ContentType) + ")"
			if p.File != "" {
				line(&pre, w+".Write(readFile("+goString(p.File)+"))")
			} else {
				line(&pre, "io.WriteString("+w+", "+goString(p.Value)+")")
			}
		}
		line(&pre, "if err := form.Close(); err != nil {", "\tpanic(err)", "}")
		body = "&body"
		if !hasHeader(r.Headers, "Content-Type") {
			line(&after, `req.Header.Set("Content-Type", form.FormDataContentType())`)
		}
	}

	var headers []string
	for _, h := range sentHeaders(r) {
		if strings.EqualFold(h.Name, "Host") {
			line(&headers, "req.Host = "+goString(h.Value))
			continue
		}
		line(&headers, "req.Header.Add("+goString(h.Name)+", "+goString(h.Value)+")")
	}
	if c := r.Basic; c != nil {
		line(&headers, "req.SetBasicAuth("+goString(c.Username)+", "+goString(c.Password)+")")
	}
	if c := r.Digest; c != nil {
		line(&headers, "// net/http has no Digest auth: answer the server's challenge for user "+goString(c.Username)+".")
	}

	var transport, client []string
	if r.Insecure || r.HTTPVersion != httpclient.HTTPVersionAuto {
		line(&transport, "transport := http.DefaultTransport.(*http.Transport).Clone()")
		client = append(client, "Transport: transport,")
	}
	if r.Insecure {
		imports["crypto/tls"] = true
		line(&transport, "transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}")
	}
	switch r.HTTPVersion {
	case httpclient.HTTPVersion1:
		line(&transport, "transport.Protocols = new(http.Protocols)", "transport.Protocols.SetHTTP1(true)")
	case httpclient.HTTPVersion2:
		line(&transport, "transport.Protocols = new(http.Protocols)", "transport.Protocols.SetHTTP2(true)")
		if urlScheme(r.URL) != "https" {
			line(&transport, "transport.Protocols.SetUnencryptedHTTP2(true)")
		}
	}
	if r.Timeout > 0 {
		imports["time"] = true
		client = append(client, "Timeout: "+goDuration(r.Timeout)+",")
	}
	switch {
	case !r.FollowRedirects:
		client = append(client,
			"CheckRedirect: func(req *http.Request, via []*http.Request) error {",
			"\treturn http.ErrUseLastResponse",
			"},")
	case r.MaxRedirects != 10:
		client = append(client,
			"CheckRedirect: func(req *http.Request, via []*http.Request) error {",
			"\tif len(via) >= "+strconv.Itoa(r.MaxRedirects)+" {",
			"\t\treturn fmt.Errorf(\"stopped after %d redirects\", len(via))",
			"\t}",
			"\treturn nil",
			"},")
	}

	var b strings.Builder
	b.WriteString("package main\n\nimport (\n")
	names := make([]string, 0, len(imports))
	for name := range imports {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		b.WriteString("\t" + strconv.Quote(name) + "\n")
	}
	b.WriteString(")\n\nfunc main() {\n")
	body1 := func(ls []string) {
		for _, l := range ls {
			b.WriteString("\t" + l + "\n")
		}
	}
	body1(pre)
	body1([]string{"req, err := http.NewRequest(" + goString(r.Method) + ", " + goString(r.URL) + ", " + body + ")"})
	body1(check)
	body1(headers)
	body1(after)
	b.WriteString("\n")
	body1(transport)
	if len(client) == 0 {
		body1([]string{"client := &http.Client{}"})
	} else {
		body1([]string{"client := &http.Client{"})
		for _, l := range client {
			b.WriteString("\t\t" + l + "\n")
		}
		body1([]string{"}"})
	}
	body1([]string{"resp, err := client.Do(req)"})
	body1(check)
	body1([]string{"defer resp.Body.Close()", "data, err := io.ReadAll(resp.Body)"})
	body1(check)
	body1([]string{"fmt.Println(resp.Status)", "fmt.Println(string(data))"})
	b.WriteString("}\n")

	src, err := format.Source([]byte(b.String()))
	if err != nil {
		// Not expected: every piece is written as Go. The code as it is.
		return strings.TrimSuffix(b.String(), "\n"), nil
	}
	return strings.TrimSuffix(string(src), "\n"), nil
}

// goDuration is d as a Go expression: 2 * time.Second, 1500 *
// time.Millisecond, or time.Duration(n).
func goDuration(d time.Duration) string {
	switch {
	case d%time.Second == 0:
		return strconv.FormatInt(int64(d/time.Second), 10) + " * time.Second"
	case d%time.Millisecond == 0:
		return strconv.FormatInt(int64(d/time.Millisecond), 10) + " * time.Millisecond"
	}
	return "time.Duration(" + strconv.FormatInt(int64(d), 10) + ")"
}

// goString is s as a Go string literal: a raw string when s has a line
// break, " or \ and nothing a raw string cannot hold (`, a carriage
// return, another control character but tab, a character that is not
// printable, bytes that are not UTF-8), else strconv.Quote.
func goString(s string) string {
	if !strings.ContainsAny(s, "\n\"\\") || !utf8.ValidString(s) || strings.ContainsRune(s, '`') {
		return strconv.Quote(s)
	}
	for _, c := range s {
		if c != '\n' && c != '\t' && !unicode.IsPrint(c) {
			return strconv.Quote(s)
		}
	}
	return "`" + s + "`"
}
