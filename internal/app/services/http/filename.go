package http

import (
	"mime"
	"net/url"
	"path"
	"strings"
	"unicode"
	"unicode/utf8"
)

// defaultFileName is the suggested name when neither Content-Disposition
// nor the URL gives one.
const defaultFileName = "response"

// maxFileNameBytes caps a suggested file name.
const maxFileNameBytes = 200

// suggestFileName is the file name SaveBody offers for a response
// (HTTP-34): the Content-Disposition filename (filename* first, RFC 6266),
// else the last segment of the URL path, else "response"; made safe for
// every OS, with an extension from the Content-Type when it has none.
func suggestFileName(contentDisposition, rawURL, contentType string) string {
	name := dispositionFileName(contentDisposition)
	if name == "" {
		name = urlFileName(rawURL)
	}
	name = sanitizeFileName(name)
	if name == "" {
		name = defaultFileName
	}
	if path.Ext(name) == "" {
		name += extensionFor(contentType)
	}
	return name
}

// dispositionFileName returns the file name of a Content-Disposition
// value, or "". mime.ParseMediaType decodes filename* (RFC 5987) into
// "filename"; a value it rejects (an unquoted name with spaces, say) is
// scanned for filename= by hand.
func dispositionFileName(v string) string {
	if strings.TrimSpace(v) == "" {
		return ""
	}
	if _, params, err := mime.ParseMediaType(v); err == nil {
		return params["filename"]
	}
	for part := range strings.SplitSeq(v, ";") {
		name, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok || !strings.EqualFold(strings.TrimSpace(name), "filename") {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"`)
	}
	return ""
}

// urlFileName returns the last segment of the URL's path, unescaped, or
// "".
func urlFileName(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return ""
	}
	p := u.EscapedPath()
	seg := p[strings.LastIndex(p, "/")+1:]
	if s, err := url.PathUnescape(seg); err == nil {
		seg = s
	}
	return seg
}

// windowsReserved are device names Windows does not allow as file names,
// with or without an extension.
var windowsReserved = map[string]bool{
	"CON": true, "PRN": true, "AUX": true, "NUL": true,
	"COM1": true, "COM2": true, "COM3": true, "COM4": true, "COM5": true, "COM6": true, "COM7": true, "COM8": true, "COM9": true,
	"LPT1": true, "LPT2": true, "LPT3": true, "LPT4": true, "LPT5": true, "LPT6": true, "LPT7": true, "LPT8": true, "LPT9": true,
}

// sanitizeFileName keeps the last path element of name and drops what no
// OS accepts in a file name: control characters, / \ : * ? " < > |,
// leading and trailing dots and spaces, invalid UTF-8. It is cut to
// maxFileNameBytes, keeping the extension. "" when nothing is left.
func sanitizeFileName(name string) string {
	name = strings.ReplaceAll(name, `\`, "/")
	name = name[strings.LastIndex(name, "/")+1:]
	var b strings.Builder
	for _, r := range strings.ToValidUTF8(name, "") {
		switch {
		case unicode.IsControl(r), strings.ContainsRune(`:*?"<>|`, r):
			b.WriteRune('_')
		default:
			b.WriteRune(r)
		}
	}
	name = strings.Trim(b.String(), ". ")
	if name == "" {
		return ""
	}
	base := strings.ToUpper(strings.TrimSuffix(name, path.Ext(name)))
	if windowsReserved[base] {
		name = "_" + name
	}
	if len(name) > maxFileNameBytes {
		ext := path.Ext(name)
		if len(ext) > 16 {
			ext = ""
		}
		stem := name[:maxFileNameBytes-len(ext)]
		for !utf8.ValidString(stem) {
			stem = stem[:len(stem)-1]
		}
		name = strings.TrimRight(stem, ". ") + ext
	}
	return name
}

// extensions maps media types to the extension of a saved body.
var extensions = map[string]string{
	"application/json":         ".json",
	"application/xml":          ".xml",
	"text/xml":                 ".xml",
	"text/html":                ".html",
	"application/xhtml+xml":    ".html",
	"text/plain":               ".txt",
	"text/csv":                 ".csv",
	"text/css":                 ".css",
	"text/markdown":            ".md",
	"text/javascript":          ".js",
	"application/javascript":   ".js",
	"application/x-ndjson":     ".ndjson",
	"application/yaml":         ".yaml",
	"application/x-yaml":       ".yaml",
	"text/yaml":                ".yaml",
	"application/graphql":      ".graphql",
	"application/pdf":          ".pdf",
	"application/zip":          ".zip",
	"application/gzip":         ".gz",
	"application/x-tar":        ".tar",
	"application/wasm":         ".wasm",
	"application/octet-stream": ".bin",
	"application/x-protobuf":   ".bin",
	"application/protobuf":     ".bin",
	"image/png":                ".png",
	"image/jpeg":               ".jpg",
	"image/gif":                ".gif",
	"image/webp":               ".webp",
	"image/avif":               ".avif",
	"image/svg+xml":            ".svg",
	"image/x-icon":             ".ico",
	"image/vnd.microsoft.icon": ".ico",
	"audio/mpeg":               ".mp3",
	"audio/wav":                ".wav",
	"video/mp4":                ".mp4",
	"video/webm":               ".webm",
	"font/woff2":               ".woff2",
	"font/woff":                ".woff",
}

// extensionFor returns the extension for a Content-Type value, "" when
// none is known. Structured suffixes (+json, +xml) count.
func extensionFor(contentType string) string {
	mt, _, err := mime.ParseMediaType(contentType)
	if err != nil {
		return ""
	}
	if ext, ok := extensions[mt]; ok {
		return ext
	}
	switch {
	case strings.HasSuffix(mt, "+json"):
		return ".json"
	case strings.HasSuffix(mt, "+xml"):
		return ".xml"
	case strings.HasPrefix(mt, "text/"):
		return ".txt"
	}
	return ""
}
