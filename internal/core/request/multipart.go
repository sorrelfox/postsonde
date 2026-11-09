package request

import (
	"io"
	"mime/multipart"
	"net/textproto"
	"os"
	"path"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// CodeInvalidPartContentType: a multipart part's Content-Type (or a file
// body's) contains a line break or another control character (kind
// invalid_argument).
const CodeInvalidPartContentType = "request.invalid_part_content_type"

// DetailPart is the detail of the multipart errors that holds the part's
// name as typed (never its value).
const DetailPart = "part"

// MultipartBody is a multipart/form-data body (HTTP-14). Text parts are
// held in memory (they are typed text); file parts are streamed from disk
// part by part through an io.Pipe each time the body is opened, so a large
// file is never loaded. Its length is computed up front from the part
// headers and the file sizes, so it is sent with a Content-Length.
type MultipartBody struct {
	boundary string
	parts    []multipartPart
	length   int64
	// omitType sends no Content-Type (settings.omitHeaders, HTTP-09).
	omitType bool
}

var _ httpclient.Body = (*MultipartBody)(nil)

type multipartPart struct {
	header textproto.MIMEHeader
	// name and filename are the part's name and file name as rendered,
	// before escaping; filename is "" for a text part.
	name, filename string
	value          string // a text part's value
	// A file part: path on disk, path as stored and its size when mapped.
	path   string
	stored string
	what   string
	size   int64
}

// MultipartPart is one part of a MultipartBody as mapped, for the code
// generators (internal/core/snippet): what is sent, not how.
type MultipartPart struct {
	// Name is the part's name, rendered, not escaped.
	Name string
	// Value is a text part's value, rendered.
	Value string
	// File is a file part's absolute path on disk; "" for a text part.
	File string
	// Filename is the file name sent for a file part: the base name of
	// the path as stored.
	Filename string
	// ContentType is the part's Content-Type: always set for a file part
	// (FileMediaType), "" for a text part without one.
	ContentType string
}

// Parts returns the body's parts in the order they are sent.
func (b *MultipartBody) Parts() []MultipartPart {
	out := make([]MultipartPart, len(b.parts))
	for i, p := range b.parts {
		out[i] = MultipartPart{Name: p.name, Value: p.value, File: p.path, Filename: p.filename, ContentType: p.header.Get("Content-Type")}
	}
	return out
}

// Boundary returns the body's boundary, generated when it was mapped.
func (b *MultipartBody) Boundary() string { return b.boundary }

// ContentType returns "multipart/form-data; boundary=…", or "" when the
// request's settings turn the automatic Content-Type off.
func (b *MultipartBody) ContentType() string {
	if b.omitType {
		return ""
	}
	return MediaTypeMultipart + "; boundary=" + b.boundary
}

// Open returns a reader that writes the body as it is read, and its
// length. Closing the reader early stops the writer and closes its file.
func (b *MultipartBody) Open() (io.ReadCloser, int64, error) {
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(b.write(pw, nil))
	}()
	return pr, b.length, nil
}

// write writes the body to w. With count set, file contents are not read:
// their sizes are added to count instead, which is how the length is
// computed with the very code that writes the body.
func (b *MultipartBody) write(w io.Writer, count *countingWriter) error {
	mw := multipart.NewWriter(w)
	if err := mw.SetBoundary(b.boundary); err != nil {
		return err
	}
	for _, p := range b.parts {
		pw, err := mw.CreatePart(p.header)
		if err != nil {
			return err
		}
		switch {
		case p.path == "":
			_, err = io.WriteString(pw, p.value)
		case count != nil:
			count.n += p.size
		default:
			err = copyFile(pw, p)
		}
		if err != nil {
			return err
		}
	}
	return mw.Close()
}

// copyFile writes the file of p, which must still have the size it had
// when the body was mapped.
func copyFile(w io.Writer, p multipartPart) error {
	f, err := os.Open(p.path)
	if err != nil {
		return bodyFileError(err, p.stored, p.what)
	}
	defer f.Close()
	_, err = io.Copy(w, &sizedFile{f: f, remaining: p.size, stored: p.stored, what: p.what})
	return err
}

type countingWriter struct{ n int64 }

func (c *countingWriter) Write(p []byte) (int, error) {
	c.n += int64(len(p))
	return len(p), nil
}

// Multipart maps the parts of a multipart form body:
//
//   - disabled parts are skipped, and so are parts whose name, value and
//     file are all empty (a row being filled in); the others keep their
//     order and duplicates;
//   - expand, when not nil, renders the name and a text part's value;
//     file contents and paths are never expanded;
//   - a text part has no Content-Type unless ContentType is set; a file
//     part has FileMediaType(file, ContentType);
//   - a file part's filename is the base name of its path; names and
//     filenames are written in quotes with `"`, CR and LF percent-encoded
//     (%22, %0D, %0A) and other text as UTF-8, as browsers do (the HTML
//     standard's multipart/form-data encoding; RFC 7578 section 4.2 rules
//     out filename*);
//   - relative file paths are relative to root, the project root.
//
// Every file is checked (it must exist and be a regular file) and its size
// read now; the body's length assumes these sizes. Errors are
// *errs.Error with the DetailRow and DetailPart details (and DetailField
// for rendering errors, DetailPath for files).
func Multipart(parts []projectfile.Part, expand Expand, root string) (*MultipartBody, error) {
	return multipartOf(parts, expand, root, func(i int, part projectfile.Part) (int, string, string) {
		return i, part.Name, "The file of multipart part " + strconv.Itoa(i+1) + " (" + strconv.Quote(part.Name) + ")"
	})
}

// partLabel names part i in the errors about its file: the row and part
// name of the DetailRow and DetailPart details, and how messages name the
// file.
type partLabel func(i int, part projectfile.Part) (row int, name, what string)

// multipartOf is Multipart with the errors about files labelled by label.
func multipartOf(parts []projectfile.Part, expand Expand, root string, label partLabel) (*MultipartBody, error) {
	b := &MultipartBody{boundary: multipart.NewWriter(io.Discard).Boundary()}
	for i, part := range parts {
		if part.Disabled || (part.Name == "" && part.Value == "" && part.File == "") {
			continue
		}
		name, value := part.Name, part.Value
		if expand != nil {
			var err error
			if name, err = expand(part.Name); err != nil {
				return nil, partExpandError(err, i, "name", part.Name)
			}
			if part.File == "" {
				if value, err = expand(part.Value); err != nil {
					return nil, partExpandError(err, i, "value", part.Name)
				}
			}
		}
		if hasHeaderBreak(part.ContentType) {
			return nil, withPart(errs.New(errs.InvalidArgument, "The Content-Type of multipart part "+strconv.Itoa(i+1)+" ("+strconv.Quote(part.Name)+") contains a line break or control character.").
				WithCode(CodeInvalidPartContentType).
				WithHint("Remove line breaks and control characters from the part's Content-Type."), i, part.Name)
		}

		p := multipartPart{header: textproto.MIMEHeader{}, name: name}
		disposition := `form-data; name="` + escapeFormName(name) + `"`
		if part.File == "" {
			p.value = value
			if part.ContentType != "" {
				p.header.Set("Content-Type", part.ContentType)
			}
		} else {
			row, labelName, what := label(i, part)
			p.stored = part.File
			p.what = what
			abs, size, err := bodyFile(root, part.File, p.what)
			if err != nil {
				return nil, withPart(err.(*errs.Error), row, labelName)
			}
			p.path, p.size = abs, size
			base := path.Base(strings.ReplaceAll(part.File, `\`, "/"))
			p.filename = base
			disposition += `; filename="` + escapeFormName(base) + `"`
			p.header.Set("Content-Type", FileMediaType(base, part.ContentType))
		}
		p.header.Set("Content-Disposition", disposition)
		b.parts = append(b.parts, p)
	}

	var count countingWriter
	if err := b.write(&count, &count); err != nil {
		return nil, errs.Wrap(err, errs.Internal, "The multipart body could not be built.")
	}
	b.length = count.n
	return b, nil
}

// escapeFormName escapes a field name or filename for a quoted
// Content-Disposition parameter the way the HTML standard's
// multipart/form-data encoding does: `"` as %22, CR as %0D and LF as %0A.
func escapeFormName(s string) string {
	if !strings.ContainsAny(s, "\"\r\n") {
		return s
	}
	return strings.NewReplacer(`"`, "%22", "\r", "%0D", "\n", "%0A").Replace(s)
}

func withPart(e *errs.Error, row int, name string) *errs.Error {
	return e.WithDetail(DetailRow, strconv.Itoa(row)).WithDetail(DetailPart, name)
}

// partExpandError keeps an *errs.Error from expand and adds the part; any
// other error is wrapped as InvalidArgument.
func partExpandError(err error, row int, field, name string) error {
	e, ok := err.(*errs.Error)
	if !ok {
		e = errs.Wrap(err, errs.InvalidArgument, "The "+field+" of multipart part "+strconv.Itoa(row+1)+" could not be rendered.").
			WithHint("Check the variables used in the part.")
	}
	return withPart(e, row, name).WithDetail(DetailField, field)
}
