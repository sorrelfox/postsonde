package projectzip

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"errors"
	"io/fs"
	"strconv"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/requestsecrets"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// ExportOptions say how Export writes the zip.
type ExportOptions struct {
	// Modified is the time stamp of every entry; the zero time uses
	// 1980-01-01, the earliest a zip can hold.
	Modified time.Time
}

// ExportNote is a line of the export's report.
type ExportNote struct {
	// Path names the item from the exported folder down; empty for the
	// folder itself.
	Path []string
	// Kind is "folder", "request" or, for a skipped item, what it was.
	Kind     string
	Code     string
	Severity importer.Severity
	// Message never contains a secret or any other value of the file.
	Message string
	// Skipped is set when the item is not in the zip.
	Skipped bool
}

// ExportResult is what Export made.
type ExportResult struct {
	// Data is the zip.
	Data []byte
	// Name is the exported folder's display name.
	Name string
	// Folders counts the folders in the zip, the exported one included;
	// Requests the request files; Skipped the items left out.
	Folders  int
	Requests int
	Skipped  int
	// SecretsRemoved counts the secret values typed into project files
	// by hand that were left out. Secrets kept on this computer are never
	// read, so they are not counted.
	SecretsRemoved int
	Notes          []ExportNote
}

// Export writes folder n of a loaded tree, with everything inside, as a
// zip of project files. The files are taken as the tree read them from
// disk: secrets kept on this computer are not in them, and a secret typed
// into a file by hand is left out (requestsecrets.SplitFile) with a note.
// Requests of a kind this version does not know, files from a newer
// PostSonde and files that did not load are left out with a warning, as
// their secrets cannot be checked. Directories and files that are not
// part of the tree (fixtures) are not included; file paths in requests
// are kept as written.
//
// It fails with CodeTooLarge when the zip would be too large to import.
func Export(n *collection.Node, opts ExportOptions) (*ExportResult, error) {
	if n == nil || n.Kind != collection.KindFolder {
		return nil, errs.New(errs.InvalidArgument, "Only a folder can be exported as a zip.").
			WithHint("Choose a folder in the tree.")
	}
	e := &exporter{modified: opts.Modified, res: &ExportResult{Name: n.DisplayName()}}
	if e.modified.IsZero() {
		e.modified = time.Date(1980, 1, 1, 0, 0, 0, 0, time.UTC)
	}
	var buf bytes.Buffer
	e.zw = zip.NewWriter(&buf)
	top := n.Entry
	if !validEntry(top) {
		top = collection.Slug(n.DisplayName())
	}
	if err := e.dir(top); err != nil {
		return nil, err
	}
	m, err := json.MarshalIndent(manifest{
		Format: FormatID, FormatVersion: FormatVersion, SchemaVersion: projectfile.CurrentVersion, Name: n.DisplayName(),
	}, "", "  ")
	if err != nil {
		return nil, errs.Wrap(err, errs.Internal, "The zip could not be made.")
	}
	if err := e.file(top+"/"+ManifestName, append(m, '\n')); err != nil {
		return nil, err
	}
	if err := e.folder(n, top, nil); err != nil {
		return nil, err
	}
	if err := e.zw.Close(); err != nil {
		return nil, errs.Wrap(err, errs.Internal, "The zip could not be made.")
	}
	e.res.Data = buf.Bytes()
	return e.res, nil
}

type exporter struct {
	zw       *zip.Writer
	modified time.Time
	res      *ExportResult
	entries  int
	total    int64
}

func (e *exporter) note(path []string, kind, code string, sev importer.Severity, msg string, skipped bool) {
	e.res.Notes = append(e.res.Notes, ExportNote{
		Path: append([]string(nil), path...), Kind: kind, Code: code, Severity: sev, Message: msg, Skipped: skipped,
	})
	if skipped {
		e.res.Skipped++
	}
}

// folder writes folder n into the zip directory dir: its items, then its
// _folder.json with their order.
func (e *exporter) folder(n *collection.Node, dir string, at []string) error {
	e.res.Folders++
	var file *projectfile.Folder
	if f, ok := n.File.(*projectfile.Folder); ok && n.ReadOnly != collection.ReadOnlyNewerVersion && n.ReadOnly != collection.ReadOnlyInvalid {
		c := *f
		file = &c
	} else {
		msg := "The folder's settings (_folder.json) could not be read, so the zip has the folder's name only."
		if n.ReadOnly == collection.ReadOnlyNewerVersion {
			msg = "The folder's settings (_folder.json) come from a newer PostSonde, so the zip has the folder's name only; export it with that version to keep them."
		}
		e.note(at, "folder", CodeExportSettings, importer.SeverityWarning, msg, false)
		file = &projectfile.Folder{Name: n.DisplayName(), Auth: projectfile.Auth{Type: projectfile.AuthInherit}}
	}
	file.ID = n.ID
	hadOrder := len(file.Order) > 0

	used := map[string]bool{projectfile.FolderFileName: true, ManifestName: true}
	var order []string
	for _, c := range n.Children {
		path := append(append([]string(nil), at...), c.DisplayName())
		switch c.Kind {
		case collection.KindFolder:
			entry := uniqueEntry(used, c.Entry, collection.Slug(c.DisplayName()), "")
			if err := e.dir(dir + "/" + entry); err != nil {
				return err
			}
			if err := e.folder(c, dir+"/"+entry, path); err != nil {
				return err
			}
			order = append(order, entry)
		case collection.KindRequest:
			if c.File == nil || c.ReadOnly == collection.ReadOnlyNewerVersion {
				e.note(path, "request of a newer PostSonde", CodeExportSkipped, importer.SeverityWarning,
					"The request comes from a newer PostSonde, so it is not exported; export it with that version.", true)
				continue
			}
			f, secrets := requestsecrets.SplitFile(c.File)
			data, err := projectfile.Marshal(f)
			if err != nil {
				e.note(path, "file", CodeExportSkipped, importer.SeverityWarning,
					"The request could not be written: "+message(err), true)
				continue
			}
			entry := uniqueEntry(used, c.Entry, collection.Slug(c.DisplayName()), projectfile.Ext)
			if err := e.file(dir+"/"+entry, data); err != nil {
				return err
			}
			e.res.Requests++
			e.secrets(path, "request", len(secrets))
			order = append(order, entry)
		case collection.KindUnknown:
			e.note(path, "request of a newer PostSonde", CodeExportSkipped, importer.SeverityWarning,
				"The request is of a kind this version of PostSonde does not know, so it is not exported.", true)
		case collection.KindInvalid:
			e.note(append(append([]string(nil), at...), c.Entry), "file", CodeExportSkipped, importer.SeverityWarning,
				"The file could not be loaded, so it is not exported; fix it and export again.", true)
		}
	}
	if hadOrder {
		file.Order = order
	}
	f, secrets := requestsecrets.SplitFile(file)
	data, err := projectfile.Marshal(f)
	if err != nil {
		return errs.Wrap(err, errs.KindOf(err), "The folder's settings could not be written to the zip.")
	}
	e.secrets(at, "folder", len(secrets))
	return e.file(dir+"/"+projectfile.FolderFileName, data)
}

func (e *exporter) secrets(at []string, kind string, n int) {
	if n == 0 {
		return
	}
	e.res.SecretsRemoved += n
	what := "A secret typed into the file was"
	if n > 1 {
		what = strconv.Itoa(n) + " secrets typed into the file were"
	}
	e.note(at, kind, CodeExportSecret, importer.SeverityInfo, what+" left out of the zip.", false)
}

func (e *exporter) dir(name string) error {
	if err := e.count(0); err != nil {
		return err
	}
	h := &zip.FileHeader{Name: name + "/", Method: zip.Store, Modified: e.modified}
	h.SetMode(fs.ModeDir | 0o755)
	_, err := e.zw.CreateHeader(h)
	return wrapWrite(err)
}

func (e *exporter) file(name string, data []byte) error {
	if len(data) > MaxEntrySize {
		return tooLarge("A file is too large for a zip PostSonde can import.", MaxEntrySize).WithDetail("entry", name)
	}
	if err := e.count(int64(len(data))); err != nil {
		return err
	}
	h := &zip.FileHeader{Name: name, Method: zip.Deflate, Modified: e.modified}
	h.SetMode(0o644)
	w, err := e.zw.CreateHeader(h)
	if err != nil {
		return wrapWrite(err)
	}
	_, err = w.Write(data)
	return wrapWrite(err)
}

func (e *exporter) count(size int64) error {
	e.entries++
	e.total += size
	if e.entries > MaxEntries {
		return tooLarge("The folder has too many items for a zip PostSonde can import.", MaxEntries)
	}
	if e.total > MaxTotalSize {
		return tooLarge("The folder is too large for a zip PostSonde can import.", MaxTotalSize)
	}
	return nil
}

func wrapWrite(err error) error {
	if err == nil {
		return nil
	}
	return errs.Wrap(err, errs.Internal, "The zip could not be made.")
}

// uniqueEntry returns entry when it can go into the zip as it is and is
// not used yet, else slug+ext, or slug-2+ext… It marks the name used.
func uniqueEntry(used map[string]bool, entry, slug, ext string) string {
	if validEntry(entry) && !used[entry] && (ext == "" || strings.HasSuffix(entry, ext)) {
		used[entry] = true
		return entry
	}
	name := slug + ext
	for i := 2; used[name]; i++ {
		name = slug + "-" + strconv.Itoa(i) + ext
	}
	used[name] = true
	return name
}

// message is err's message without values: an errs.Error's own message.
func message(err error) string {
	var e *errs.Error
	if errors.As(err, &e) {
		return e.Message
	}
	return "an unexpected error."
}

func tooLarge(msg string, limit int) *errs.Error {
	return errs.New(errs.InvalidArgument, msg).
		WithCode(CodeTooLarge).
		WithHint("Export a smaller folder.").
		WithDetail("limit", strconv.Itoa(limit))
}
