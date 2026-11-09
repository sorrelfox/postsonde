// Package harimport is the import framework's HAR format (IMP-07,
// docs/har.md#import): HTTP Archive 1.1 and 1.2 files, as the network
// panels of Chrome, Edge, Firefox and Safari export them. Each recorded
// exchange becomes a request (http, graphql or websocket), grouped in a
// folder per page, or per host when the file has no pages. Static
// resources, CORS preflights and repeated requests are left out and
// counted. Browser credentials never reach the project: cookies are
// dropped, an Authorization header becomes the request's typed auth, whose
// secret the writer keeps in the local side files. Standard library only.
package harimport

import (
	"bytes"
	"context"
	"regexp"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/yaml"
)

// Diagnostic codes of the format (docs/har.md#import).
const (
	// CodeVersion: the file names no HAR version, or one other than 1.1
	// and 1.2; it is read as 1.2 (info, file level).
	CodeVersion = "har.version"
	// CodeFiltered: static resources, CORS preflights and repeated
	// requests were left out (info, file level, with the counts).
	CodeFiltered = "har.filtered"
	// CodeCookies: the request's cookies (Cookie header, cookies list)
	// were not imported (warning).
	CodeCookies = "har.cookies_dropped"
	// CodeAuth: an Authorization or API key header became the request's
	// typed auth, its secret kept on this machine only (info).
	CodeAuth = "har.auth"
	// CodeAuthDropped: a credential header that could not become typed
	// auth (Digest, Proxy-Authorization, a second key, a WebSocket
	// request's) was not imported (warning).
	CodeAuthDropped = "har.auth_dropped"
	// CodeFilePart: a multipart part uploaded a file; only its name is in
	// the recording (info).
	CodeFilePart = "har.file_part"
	// CodeBody: a body that could not be imported: binary, or a
	// multipart body that does not parse (warning).
	CodeBody = "har.body_skipped"
	// CodeWebSocket: a WebSocket's recorded messages: those sent became
	// saved messages, those received are not imported (info).
	CodeWebSocket = "har.websocket_messages"
	// CodeUnsupported: an entry whose URL PostSonde cannot send (data:,
	// blob:, chrome-extension:…) or that is not a request, skipped
	// (warning).
	CodeUnsupported = "har.unsupported"
)

// bom is the UTF-8 byte order mark, ignored at the start of a file.
var bom = []byte("\xef\xbb\xbf")

// Importer reads HAR files.
type Importer struct{}

var _ importer.Importer = Importer{}

var format = importer.Format{
	ID:         "har",
	Name:       "HAR (HTTP Archive from browser DevTools)",
	Extensions: []string{".har"},
}

// Format describes the HAR format.
func (Importer) Format() importer.Format { return format }

var (
	reLog     = regexp.MustCompile(`"log"\s*:\s*\{`)
	reEntries = regexp.MustCompile(`"entries"\s*:\s*\[`)
	reCreator = regexp.MustCompile(`"creator"\s*:\s*\{`)
)

// Detect is Sure for JSON with a log object holding a creator or entries
// (every browser writes both), Maybe for other JSON named *.har, No
// otherwise.
func (Importer) Detect(src importer.Source) int {
	head := bytes.TrimPrefix(src.Data, bom)
	if len(head) > 64<<10 {
		head = head[:64<<10]
	}
	head = bytes.TrimLeft(head, " \t\r\n")
	if len(head) == 0 || head[0] != '{' {
		return importer.No
	}
	if reLog.Match(head) && (reCreator.Match(head) || reEntries.Match(head)) {
		return importer.Sure
	}
	if strings.HasSuffix(strings.ToLower(src.Name), ".har") {
		return importer.Maybe
	}
	return importer.No
}

// Parse reads a HAR file into the model. It fails only when the file is
// not JSON or has no log with a list of entries.
func (Importer) Parse(ctx context.Context, src importer.Source) (*importer.Model, error) {
	if !utf8.Valid(src.Data) {
		return nil, importer.InvalidFile(format, "The file is not UTF-8 text.", importer.Location{})
	}
	root, err := parseJSON(bytes.TrimPrefix(src.Data, bom))
	if err != nil {
		at := importer.Location{}
		if je, ok := err.(*jsonError); ok {
			at.Line = je.line
		}
		return nil, importer.InvalidFile(format, "The file is not valid JSON: "+err.Error()+".", at)
	}
	log := get(root, "log")
	if !isMap(log) {
		return nil, importer.InvalidFile(format, "The file is not a HAR file: it has no log object.", importer.Location{Line: root.Line})
	}
	entries := get(log, "entries")
	if entries == nil || entries.Kind != yaml.SequenceNode {
		return nil, importer.InvalidFile(format, "The HAR file's log has no list of entries.", importer.Location{Line: log.Line, Path: "log.entries"})
	}
	if err := ctx.Err(); err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "Reading the file was interrupted.")
	}
	m := &importer.Model{}
	if d := version(log); d != nil {
		m.Diagnostics = append(m.Diagnostics, *d)
	}
	r := &reader{ctx: ctx, seen: map[string]bool{}}
	if err := r.read(log, entries, m); err != nil {
		return nil, err
	}
	return m, nil
}

// version returns an info unless the file is HAR 1.1 or 1.2.
func version(log *yaml.Node) *importer.Diagnostic {
	v := strings.TrimSpace(sget(log, "version"))
	if v == "1.2" || v == "1.1" {
		return nil
	}
	at := importer.Location{Line: log.Line, Column: log.Column, Path: "log"}
	if n := get(log, "version"); n != nil {
		at = importer.Location{Line: n.Line, Column: n.Column, Path: "log.version"}
	}
	msg := "The file names no HAR version; it is read as HAR 1.2."
	if v != "" {
		msg = "The file is HAR " + clip(v) + "; it is read as HAR 1.2."
	}
	return &importer.Diagnostic{Code: CodeVersion, Severity: importer.SeverityInfo, Source: at, Message: msg}
}

// clip shortens a value quoted in a message.
func clip(s string) string {
	if utf8.RuneCountInString(s) <= 40 {
		return s
	}
	r := []rune(s)
	return string(r[:40]) + "…"
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
