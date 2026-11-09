// Package har writes HTTP Archive (HAR 1.2) files (EXP-04,
// docs/har.md#export): the types of the format, the conversion of request
// history entries (internal/core/history) and of requests as PostSonde
// would send them (internal/core/snippet) into HAR entries, encoding, a
// check of the members HAR 1.2 requires, and an atomic file write.
// Reading HAR files is the importer's (internal/core/importer/harimport).
// Standard library only.
package har

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
)

// Version is the HAR version written.
const Version = "1.2"

// CreatorName names the application in log.creator.
const CreatorName = "PostSonde"

// File is a HAR document: {"log": …}.
type File struct {
	Log Log `json:"log"`
}

// Log is the root of the archive.
type Log struct {
	Version string  `json:"version"`
	Creator Creator `json:"creator"`
	Pages   []Page  `json:"pages,omitempty"`
	Entries []Entry `json:"entries"`
	Comment string  `json:"comment,omitempty"`
}

// Creator names the application that wrote the file.
type Creator struct {
	Name    string `json:"name"`
	Version string `json:"version"`
	Comment string `json:"comment,omitempty"`
}

// Page groups entries, as a browser groups them under the page that made
// them.
type Page struct {
	StartedDateTime string      `json:"startedDateTime"`
	ID              string      `json:"id"`
	Title           string      `json:"title"`
	PageTimings     PageTimings `json:"pageTimings"`
	Comment         string      `json:"comment,omitempty"`
}

// PageTimings are a page's load events; -1 when not applicable, as for
// every PostSonde page.
type PageTimings struct {
	OnContentLoad float64 `json:"onContentLoad"`
	OnLoad        float64 `json:"onLoad"`
}

// Entry is one exchange.
type Entry struct {
	Pageref         string   `json:"pageref,omitempty"`
	StartedDateTime string   `json:"startedDateTime"`
	Time            float64  `json:"time"`
	Request         Request  `json:"request"`
	Response        Response `json:"response"`
	Cache           Cache    `json:"cache"`
	Timings         Timings  `json:"timings"`
	ServerIPAddress string   `json:"serverIPAddress,omitempty"`
	Connection      string   `json:"connection,omitempty"`
	Comment         string   `json:"comment,omitempty"`
	// Error is why no response arrived, as Chrome writes it.
	Error string `json:"_error,omitempty"`
}

// Request is the request of an entry.
type Request struct {
	Method      string      `json:"method"`
	URL         string      `json:"url"`
	HTTPVersion string      `json:"httpVersion"`
	Cookies     []Cookie    `json:"cookies"`
	Headers     []NameValue `json:"headers"`
	QueryString []NameValue `json:"queryString"`
	PostData    *PostData   `json:"postData,omitempty"`
	HeadersSize int64       `json:"headersSize"`
	BodySize    int64       `json:"bodySize"`
	Comment     string      `json:"comment,omitempty"`
}

// Response is the response of an entry; status 0 when none arrived.
type Response struct {
	Status      int         `json:"status"`
	StatusText  string      `json:"statusText"`
	HTTPVersion string      `json:"httpVersion"`
	Cookies     []Cookie    `json:"cookies"`
	Headers     []NameValue `json:"headers"`
	Content     Content     `json:"content"`
	RedirectURL string      `json:"redirectURL"`
	HeadersSize int64       `json:"headersSize"`
	BodySize    int64       `json:"bodySize"`
	Comment     string      `json:"comment,omitempty"`
}

// Cookie is a cookie sent (Cookie) or set (Set-Cookie).
type Cookie struct {
	Name     string `json:"name"`
	Value    string `json:"value"`
	Path     string `json:"path,omitempty"`
	Domain   string `json:"domain,omitempty"`
	Expires  string `json:"expires,omitempty"`
	HTTPOnly bool   `json:"httpOnly,omitempty"`
	Secure   bool   `json:"secure,omitempty"`
	Comment  string `json:"comment,omitempty"`
}

// NameValue is a header field or a query string entry.
type NameValue struct {
	Name    string `json:"name"`
	Value   string `json:"value"`
	Comment string `json:"comment,omitempty"`
}

// PostData is a request body.
type PostData struct {
	MimeType string  `json:"mimeType"`
	Params   []Param `json:"params,omitempty"`
	Text     string  `json:"text"`
	Comment  string  `json:"comment,omitempty"`
}

// Param is a posted form field or multipart part.
type Param struct {
	Name        string `json:"name"`
	Value       string `json:"value,omitempty"`
	FileName    string `json:"fileName,omitempty"`
	ContentType string `json:"contentType,omitempty"`
	Comment     string `json:"comment,omitempty"`
}

// Content is a response body.
type Content struct {
	Size     int64  `json:"size"`
	MimeType string `json:"mimeType"`
	Text     string `json:"text,omitempty"`
	Encoding string `json:"encoding,omitempty"`
	Comment  string `json:"comment,omitempty"`
}

// Cache says nothing: PostSonde has no cache.
type Cache struct{}

// Timings are the phases of an entry in milliseconds; -1 for blocked,
// dns, connect and ssl when they did not happen. ssl is also counted in
// connect, as HAR 1.2 has it.
type Timings struct {
	Blocked float64 `json:"blocked"`
	DNS     float64 `json:"dns"`
	Connect float64 `json:"connect"`
	Send    float64 `json:"send"`
	Wait    float64 `json:"wait"`
	Receive float64 `json:"receive"`
	SSL     float64 `json:"ssl"`
	Comment string  `json:"comment,omitempty"`
}

// NewFile returns an empty archive written by PostSonde version (the
// app's version; "" writes "dev").
func NewFile(version string) *File {
	if version == "" {
		version = "dev"
	}
	return &File{Log: Log{Version: Version, Creator: Creator{Name: CreatorName, Version: version}, Entries: []Entry{}}}
}

// Encode returns the archive as indented JSON, HTML characters not
// escaped, ending with a newline.
func Encode(f *File) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(f); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// WriteFile writes data to path through a temporary file in the same
// directory, renamed over path once complete, so a failed write never
// leaves a partial file under the chosen name. The file is 0644: the user
// chose where it goes.
func WriteFile(path string, data []byte) error {
	f, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".*.part")
	if err != nil {
		return err
	}
	tmp := f.Name()
	_, err = f.Write(data)
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Chmod(tmp, 0o644)
	}
	if err == nil {
		err = os.Rename(tmp, path)
	}
	if err != nil {
		_ = os.Remove(tmp)
	}
	return err
}

// FileName returns a file name for an archive titled title: the title
// without characters file systems refuse, with ".har" ("requests.har" for
// a blank title).
func FileName(title string) string {
	var b strings.Builder
	for _, r := range strings.TrimSpace(title) {
		switch {
		case r < 0x20, strings.ContainsRune(`/\:*?"<>|`, r):
			b.WriteByte('-')
		default:
			b.WriteRune(r)
		}
	}
	name := strings.Trim(b.String(), " .-")
	if name == "" {
		name = "requests"
	}
	if r := []rune(name); len(r) > 100 {
		name = string(r[:100])
	}
	return name + ".har"
}
