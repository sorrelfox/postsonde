package importer

import (
	"context"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Error codes of the pipeline (docs/import.md), in addition to those of
// the formats, the collection and the environments.
const (
	// CodeUnknownFormat: no importer recognises the file, or the format
	// asked for is not known (kind unsupported). Details: name, format.
	CodeUnknownFormat = "import.unknown_format"
	// CodeInvalidFile: the file is not valid in its format at all (kind
	// parse). Importers wrap their fatal errors with it. Details: format.
	CodeInvalidFile = "import.invalid_file"
	// CodeEmpty: the file has nothing to import (a warning diagnostic).
	CodeEmpty = "import.empty"
	// CodeRenamed: an item was given another name because the target
	// already has one with its name (info).
	CodeRenamed = "import.renamed"
	// CodeSettingsSkipped: the collection's own settings were not applied,
	// because its items went straight into an existing folder (warning).
	CodeSettingsSkipped = "import.settings_skipped"
	// CodeEnvironmentsSkipped: the environments were left out on request
	// (info).
	CodeEnvironmentsSkipped = "import.environments_skipped"
	// CodeInvalidItem: an item the importer produced cannot be written, for
	// example a request file of an unknown kind (error).
	CodeInvalidItem = "import.invalid_item"
	// CodeWriteFailed: an item could not be written (error); the message
	// says why.
	CodeWriteFailed = "import.write_failed"
)

// Format describes a file format an Importer reads.
type Format struct {
	// ID is stable: "curl", "postman", "insomnia", "openapi"…
	ID string
	// Name is shown to the user: "cURL commands", "Postman Collection v2.1".
	Name string
	// Extensions are the usual file extensions, lower case with the dot.
	// They only help detection; content decides.
	Extensions []string
}

// Source is a file to import.
type Source struct {
	// Name is the file's base name ("shop.postman_collection.json"); it may
	// be empty.
	Name string
	Data []byte
}

// Ext returns the lower-case extension of the source's name, with the dot.
func (s Source) Ext() string { return strings.ToLower(filepath.Ext(s.Name)) }

// BaseName returns the source's name without its directory and its last
// extension ("shop.postman_collection" for
// "shop.postman_collection.json"; importers that know a double extension
// trim the rest).
func (s Source) BaseName() string {
	base := filepath.Base(s.Name)
	if base == "." || base == string(filepath.Separator) {
		return ""
	}
	return strings.TrimSuffix(base, filepath.Ext(base))
}

// Detection confidences returned by Importer.Detect.
const (
	// No: the source is not in the format.
	No = 0
	// Maybe: the name or a loose look at the content fits.
	Maybe = 25
	// Likely: the content has the format's usual shape.
	Likely = 60
	// Sure: the content declares the format (a schema URL, a version
	// member).
	Sure = 100
)

// Importer reads one file format. Implementations must be safe for
// concurrent use; they read only src and never touch the file system or
// the network.
type Importer interface {
	// Format describes the format.
	Format() Format
	// Detect says how sure the importer is that src is in its format: No,
	// Maybe, Likely, Sure or anything between. It must be cheap: look at
	// the name and the start of the data.
	Detect(src Source) int
	// Parse reads src into a model. Problems of single items are their
	// diagnostics, never errors: Parse fails (with an errs.Error of kind
	// parse, code CodeInvalidFile or the format's own) only when the file
	// cannot be read in the format at all, or when ctx is done.
	Parse(ctx context.Context, src Source) (*Model, error)
}

// Registry is the set of importers the app knows, in order of preference
// for ties in detection.
type Registry struct {
	list []Importer
}

// NewRegistry returns a registry of importers.
func NewRegistry(importers ...Importer) *Registry {
	return &Registry{list: append([]Importer(nil), importers...)}
}

// Formats lists the formats, in registration order.
func (r *Registry) Formats() []Format {
	out := make([]Format, 0, len(r.list))
	for _, im := range r.list {
		out = append(out, im.Format())
	}
	return out
}

// Get returns the importer of format id, or nil.
func (r *Registry) Get(id string) Importer {
	for _, im := range r.list {
		if im.Format().ID == id {
			return im
		}
	}
	return nil
}

// Detect returns the importer most sure that src is in its format; the
// first registered wins a tie. It fails with CodeUnknownFormat when none
// is.
func (r *Registry) Detect(src Source) (Importer, error) {
	var best Importer
	score := No
	for _, im := range r.list {
		if s := im.Detect(src); s > score {
			best, score = im, s
		}
	}
	if best == nil {
		return nil, errs.New(errs.Unsupported, "PostSonde does not recognise the format of this file.").
			WithCode(CodeUnknownFormat).
			WithHint("Choose the format in the list, or check that the file is an export PostSonde can import.").
			WithDetail("name", src.Name)
	}
	return best, nil
}

// Parse reads src with the importer of format id, or with the one Detect
// picks when id is "". It returns the importer used. The model is
// normalised: an empty Name becomes the file's base name, or "Imported";
// an empty file gets a CodeEmpty warning.
func (r *Registry) Parse(ctx context.Context, src Source, id string) (*Model, Importer, error) {
	var im Importer
	if id == "" {
		var err error
		if im, err = r.Detect(src); err != nil {
			return nil, nil, err
		}
	} else if im = r.Get(id); im == nil {
		return nil, nil, errs.New(errs.Unsupported, "PostSonde cannot import this format.").
			WithCode(CodeUnknownFormat).
			WithHint("This is a bug in PostSonde.").
			WithDetail("format", id)
	}
	if err := ctx.Err(); err != nil {
		return nil, im, errs.Wrap(err, errs.KindOf(err), "Reading the file was interrupted.")
	}
	m, err := im.Parse(ctx, src)
	if err != nil {
		return nil, im, err
	}
	if m == nil {
		m = &Model{}
	}
	if strings.TrimSpace(m.Name) == "" {
		m.Name = src.BaseName()
	}
	m.Name = strings.TrimSpace(m.Name)
	if m.Name == "" {
		m.Name = "Imported"
	}
	if len(m.Root.Items) == 0 && len(m.Environments) == 0 {
		m.Diagnostics = append(m.Diagnostics, Diagnostic{
			Code: CodeEmpty, Severity: SeverityWarning,
			Message: "The file has no requests or environments to import.",
		})
	}
	return m, im, nil
}

// InvalidFile returns the error an importer fails with when src cannot be
// read in its format at all: kind parse, code CodeInvalidFile, with the
// format and the place, if known, in the details. msg says what is wrong
// without quoting values.
func InvalidFile(format Format, msg string, at Location) *errs.Error {
	e := errs.New(errs.Parse, msg).
		WithCode(CodeInvalidFile).
		WithHint("Check that the file is a complete "+format.Name+" export, or choose another format.").
		WithDetail("format", format.ID)
	if at.Line > 0 {
		e = e.WithDetail("line", strconv.Itoa(at.Line))
	}
	if at.Path != "" {
		e = e.WithDetail("path", at.Path)
	}
	return e
}
