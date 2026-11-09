package projectzip

import (
	"archive/zip"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/collection"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Importer reads PostSonde folder zips.
type Importer struct{}

var _ importer.Importer = Importer{}

// Format describes the folder zip.
func (Importer) Format() importer.Format {
	return importer.Format{ID: FormatID, Name: "PostSonde folder (.zip)", Extensions: []string{Ext}}
}

// Detect is Sure for a zip whose central directory has a top-level
// directory's manifest, Maybe for any other zip (so Parse can say what is
// missing), No for anything else.
func (Importer) Detect(src importer.Source) int {
	if !isZip(src.Data) {
		return importer.No
	}
	r, err := zip.NewReader(bytes.NewReader(src.Data), int64(len(src.Data)))
	if err != nil {
		return importer.Maybe
	}
	for i, f := range r.File {
		if i >= MaxEntries {
			break
		}
		if top, rest, ok := strings.Cut(f.Name, "/"); ok && top != "" && rest == ManifestName {
			return importer.Sure
		}
	}
	return importer.Maybe
}

func isZip(data []byte) bool {
	return bytes.HasPrefix(data, []byte("PK\x03\x04")) || bytes.HasPrefix(data, []byte("PK\x05\x06"))
}

// entry is a checked file of the zip.
type entry struct {
	f    *zip.File
	name string
}

// dirInfo is a directory of the zip: its files and subdirectories by
// entry name.
type dirInfo struct {
	files map[string]*entry
	dirs  map[string]*dirInfo
}

func newDir() *dirInfo { return &dirInfo{files: map[string]*entry{}, dirs: map[string]*dirInfo{}} }

// Parse checks every entry of the zip, then reads the folder tree of its
// top-level directory into a model: the directory's _folder.json is the
// model's root, its subdirectories with a _folder.json are folders, its
// request files requests (in the folder's order). Nothing is extracted.
// It fails, importing nothing, for a zip that is not a folder export, has
// an unsafe entry (CodeUnsafeEntry), is too large (CodeTooLarge) or comes
// from a newer PostSonde (CodeNewerFormat).
func (im Importer) Parse(ctx context.Context, src importer.Source) (*importer.Model, error) {
	format := im.Format()
	if !isZip(src.Data) {
		return nil, importer.InvalidFile(format, "The file is not a zip.", importer.Location{})
	}
	r, err := zip.NewReader(bytes.NewReader(src.Data), int64(len(src.Data)))
	if err != nil {
		return nil, importer.InvalidFile(format, "The zip is damaged and cannot be read.", importer.Location{})
	}
	if len(r.File) > MaxEntries {
		return nil, tooLargeZip("The zip has more than "+strconv.Itoa(MaxEntries)+" entries.", MaxEntries, "")
	}
	root, top, err := checkEntries(format, r.File)
	if err != nil {
		return nil, err
	}
	p := &parser{ctx: ctx, format: format, m: &importer.Model{}}
	man, ok := root.files[ManifestName]
	if !ok {
		return nil, importer.InvalidFile(format, "The zip is not a PostSonde folder export: it has no "+ManifestName+".", importer.Location{})
	}
	if err := p.manifest(man); err != nil {
		return nil, err
	}
	if _, ok := root.files[projectfile.FolderFileName]; !ok {
		return nil, importer.InvalidFile(format, "The zip's folder has no "+projectfile.FolderFileName+".", importer.Location{Path: top + "/"})
	}
	folder, err := p.folder(root, top, top, true)
	if err != nil {
		return nil, err
	}
	p.m.Root = *folder
	p.m.Name = folder.Name
	if p.ignored > 0 {
		msg := "1 file in the zip is not part of the folder tree and was not imported."
		if p.ignored > 1 {
			msg = strconv.Itoa(p.ignored) + " files in the zip are not part of the folder tree and were not imported."
		}
		p.m.Diagnostics = append(p.m.Diagnostics, importer.Diagnostic{Code: CodeIgnored, Severity: importer.SeverityInfo, Message: msg})
	}
	return p.m, nil
}

// checkEntries refuses a zip with an unsafe entry and returns the tree of
// its top-level directory and that directory's name. Directory entries
// only need to be safe; the tree is made from the files' paths.
func checkEntries(format importer.Format, files []*zip.File) (*dirInfo, string, error) {
	root := newDir()
	top := ""
	seen := map[string]bool{}
	var declared uint64
	for _, f := range files {
		name := f.Name
		mode := f.Mode()
		isDir := strings.HasSuffix(name, "/")
		clean := strings.TrimSuffix(name, "/")
		if why := checkName(clean); why != "" {
			return nil, "", unsafeEntry(name, why)
		}
		switch {
		case mode.Type()&fs.ModeSymlink != 0 || mode.Type() != 0 && mode.Type() != fs.ModeDir:
			// Symbolic links, devices, pipes, sockets.
			return nil, "", unsafeEntry(name, "a symbolic link or another special file")
		case isDir != mode.IsDir():
			return nil, "", unsafeEntry(name, "a directory that is not marked as one")
		case f.Flags&0x1 != 0:
			return nil, "", unsafeEntry(name, "encryption")
		}
		if seen[clean] {
			return nil, "", unsafeEntry(name, "a name that is in the zip twice")
		}
		seen[clean] = true
		parts := strings.Split(clean, "/")
		if top == "" {
			top = parts[0]
		} else if parts[0] != top {
			return nil, "", importer.InvalidFile(format, "The zip is not a PostSonde folder export: it has more than one top-level entry.", importer.Location{Path: name})
		}
		if isDir {
			continue
		}
		if len(parts) == 1 {
			return nil, "", importer.InvalidFile(format, "The zip is not a PostSonde folder export: it has a file outside its folder.", importer.Location{Path: name})
		}
		if f.UncompressedSize64 > MaxEntrySize {
			return nil, "", tooLargeZip("A file in the zip is larger than "+strconv.Itoa(MaxEntrySize>>20)+" MiB.", MaxEntrySize, name)
		}
		declared += f.UncompressedSize64
		if declared > MaxTotalSize {
			return nil, "", tooLargeZip("The zip's files are larger than "+strconv.Itoa(MaxTotalSize>>20)+" MiB together.", MaxTotalSize, "")
		}
		d := root
		for _, p := range parts[1 : len(parts)-1] {
			sub, ok := d.dirs[p]
			if !ok {
				if _, clash := d.files[p]; clash {
					return nil, "", unsafeEntry(name, "a name that is both a file and a directory")
				}
				sub = newDir()
				d.dirs[p] = sub
			}
			d = sub
		}
		base := parts[len(parts)-1]
		if _, clash := d.dirs[base]; clash {
			return nil, "", unsafeEntry(name, "a name that is both a file and a directory")
		}
		d.files[base] = &entry{f: f, name: name}
	}
	if top == "" {
		return nil, "", importer.InvalidFile(format, "The zip is empty.", importer.Location{})
	}
	return root, top, nil
}

type parser struct {
	ctx     context.Context
	format  importer.Format
	m       *importer.Model
	total   int64
	ignored int
}

// read returns the decompressed content of e, counting it against
// MaxTotalSize. The sizes the zip declares are not trusted: reading stops
// at the limits.
func (p *parser) read(e *entry) ([]byte, error) {
	if err := p.ctx.Err(); err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "Reading the zip was interrupted.")
	}
	rc, err := e.f.Open()
	if err != nil {
		return nil, p.damaged(e, err)
	}
	defer rc.Close()
	data, err := io.ReadAll(io.LimitReader(rc, MaxEntrySize+1))
	if err != nil {
		return nil, p.damaged(e, err)
	}
	if len(data) > MaxEntrySize {
		return nil, tooLargeZip("A file in the zip is larger than "+strconv.Itoa(MaxEntrySize>>20)+" MiB.", MaxEntrySize, e.name)
	}
	p.total += int64(len(data))
	if p.total > MaxTotalSize {
		return nil, tooLargeZip("The zip's files are larger than "+strconv.Itoa(MaxTotalSize>>20)+" MiB together.", MaxTotalSize, "")
	}
	return data, nil
}

func (p *parser) damaged(e *entry, err error) error {
	if errors.Is(err, zip.ErrAlgorithm) {
		return importer.InvalidFile(p.format, "A file in the zip is compressed with a method PostSonde does not read.", importer.Location{Path: e.name})
	}
	return importer.InvalidFile(p.format, "A file in the zip is damaged and cannot be read.", importer.Location{Path: e.name})
}

func (p *parser) manifest(e *entry) error {
	data, err := p.read(e)
	if err != nil {
		return err
	}
	var m manifest
	if json.Unmarshal(data, &m) != nil || m.Format != FormatID {
		return importer.InvalidFile(p.format, "The zip's "+ManifestName+" is not the manifest of a PostSonde folder export.", importer.Location{Path: e.name})
	}
	if m.FormatVersion > FormatVersion {
		return newerFormat(m.FormatVersion, FormatVersion)
	}
	if m.SchemaVersion > projectfile.CurrentVersion {
		return newerFormat(m.SchemaVersion, projectfile.CurrentVersion)
	}
	if m.FormatVersion < 1 {
		return importer.InvalidFile(p.format, "The zip's "+ManifestName+" has no valid formatVersion.", importer.Location{Path: e.name})
	}
	return nil
}

// folder reads directory d, at zip path dir with entry name entryName,
// as a folder. d has a _folder.json; isTop is set for the top-level
// directory, which holds the manifest.
func (p *parser) folder(d *dirInfo, dir, entryName string, isTop bool) (*importer.Folder, error) {
	out := &importer.Folder{Name: entryName, Auth: projectfile.Auth{Type: projectfile.AuthInherit}, Source: importer.Location{Path: dir + "/"}}
	fe := d.files[projectfile.FolderFileName]
	data, err := p.read(fe)
	if err != nil {
		return nil, err
	}
	var order []string
	f, derr := projectfile.DecodeFolder(data)
	switch {
	case derr != nil:
		out.Diagnostics = append(out.Diagnostics, importer.Diagnostic{Code: CodeSettingsSkipped, Severity: importer.SeverityWarning,
			Message: "The folder's settings (_folder.json) could not be read, so it is imported without them.", Source: importer.Location{Path: fe.name}})
	case f.Newer():
		out.Diagnostics = append(out.Diagnostics, importer.Diagnostic{Code: CodeSettingsSkipped, Severity: importer.SeverityWarning,
			Message: "The folder's settings (_folder.json) come from a newer PostSonde, so it is imported without them.", Source: importer.Location{Path: fe.name}})
		if strings.TrimSpace(f.Name) != "" {
			out.Name = f.Name
		}
	default:
		if strings.TrimSpace(f.Name) != "" {
			out.Name = f.Name
		}
		out.Description, out.Variables, out.Headers = f.Description, f.Variables, f.Headers
		if f.Auth.Type != "" {
			out.Auth = f.Auth
		}
		out.GRPC, out.Server, out.Unknown = f.GRPC, f.Server, f.Unknown
		order = f.Order
	}

	type child struct {
		entry string
		item  importer.Item
	}
	var children []child
	for name, sub := range d.dirs {
		if _, ok := sub.files[projectfile.FolderFileName]; !ok || strings.HasPrefix(name, "_") || strings.HasPrefix(name, ".") {
			p.ignored += countFiles(sub)
			continue
		}
		sf, err := p.folder(sub, dir+"/"+name, name, false)
		if err != nil {
			return nil, err
		}
		children = append(children, child{name, importer.FolderItem(sf)})
	}
	for name, e := range d.files {
		if name == projectfile.FolderFileName || isTop && name == ManifestName {
			continue
		}
		if strings.HasPrefix(name, "_") || strings.HasPrefix(name, ".") || !strings.HasSuffix(name, projectfile.Ext) {
			p.ignored++
			continue
		}
		it, ok, err := p.request(e, name)
		if err != nil {
			return nil, err
		}
		if !ok {
			p.ignored++
			continue
		}
		children = append(children, child{name, it})
	}

	// The tree's display order (collection.applyOrder): the entries the
	// order names first, then the others by name ignoring case.
	rank := map[string]int{}
	for i, name := range order {
		key := collection.FoldName(name)
		if _, ok := rank[key]; !ok {
			rank[key] = i
		}
	}
	slices.SortFunc(children, func(a, b child) int {
		ra, oka := rank[collection.FoldName(a.entry)]
		rb, okb := rank[collection.FoldName(b.entry)]
		switch {
		case oka && okb:
			if ra != rb {
				return cmp.Compare(ra, rb)
			}
		case oka:
			return -1
		case okb:
			return 1
		}
		return cmp.Or(cmp.Compare(collection.FoldName(a.entry), collection.FoldName(b.entry)), cmp.Compare(a.entry, b.entry))
	})
	for _, c := range children {
		out.Items = append(out.Items, c.item)
	}
	return out, nil
}

// request reads file e as an item of its folder. A .json file without a
// kind is not a project file (ok false); other files that cannot be
// imported are skipped items.
func (p *parser) request(e *entry, name string) (importer.Item, bool, error) {
	data, err := p.read(e)
	if err != nil {
		return importer.Item{}, false, err
	}
	at := importer.Location{Path: e.name}
	display := strings.TrimSuffix(name, projectfile.Ext)
	skipped := func(kind, reason string) (importer.Item, bool, error) {
		return importer.SkippedItem(&importer.Skipped{Kind: kind, Name: display, Code: CodeSkipped, Reason: reason, Source: at}), true, nil
	}
	f, derr := projectfile.Decode(data)
	if derr != nil {
		var e *errs.Error
		if errors.As(derr, &e) && e.Code == projectfile.CodeUnknownKind {
			return skipped("request of a newer PostSonde", "The request is of a kind this version of PostSonde does not know.")
		}
		var members map[string]json.RawMessage
		if json.Unmarshal(data, &members) == nil {
			if _, hasKind := members["kind"]; !hasKind {
				return importer.Item{}, false, nil
			}
		}
		return skipped("file", "The file could not be loaded: "+message(derr))
	}
	if !f.FileKind().IsRequest() {
		return skipped(string(f.FileKind())+" file", "Only folders and requests are imported from a folder zip.")
	}
	if newer(f) {
		return skipped("request of a newer PostSonde", "The request comes from a newer PostSonde; import it with that version.")
	}
	if n := requestName(f); strings.TrimSpace(n) != "" {
		display = n
	}
	return importer.RequestItem(&importer.Request{Name: display, File: f, Source: at}), true, nil
}

func countFiles(d *dirInfo) int {
	n := len(d.files)
	for _, s := range d.dirs {
		n += countFiles(s)
	}
	return n
}

// newer reports whether request file f comes from a newer schema version.
func newer(f projectfile.File) bool {
	switch r := f.(type) {
	case *projectfile.HTTPRequest:
		return r.Newer()
	case *projectfile.GraphQLRequest:
		return r.Newer()
	case *projectfile.TCPRequest:
		return r.Newer()
	case *projectfile.UDPRequest:
		return r.Newer()
	case *projectfile.WebSocketRequest:
		return r.Newer()
	case *projectfile.GRPCRequest:
		return r.Newer()
	case *projectfile.SSERequest:
		return r.Newer()
	case *projectfile.SocketIORequest:
		return r.Newer()
	}
	return true
}

// requestName returns the name member of request file f.
func requestName(f projectfile.File) string {
	switch r := f.(type) {
	case *projectfile.HTTPRequest:
		return r.Name
	case *projectfile.GraphQLRequest:
		return r.Name
	case *projectfile.TCPRequest:
		return r.Name
	case *projectfile.UDPRequest:
		return r.Name
	case *projectfile.WebSocketRequest:
		return r.Name
	case *projectfile.GRPCRequest:
		return r.Name
	case *projectfile.SSERequest:
		return r.Name
	case *projectfile.SocketIORequest:
		return r.Name
	}
	return ""
}

func unsafeEntry(name, why string) *errs.Error {
	if len(name) > 200 || !utf8.ValidString(name) {
		name = "(not shown)"
	}
	return errs.New(errs.InvalidArgument, "The zip has an entry PostSonde does not import: "+why+".").
		WithCode(CodeUnsafeEntry).
		WithHint("The zip was not made by PostSonde, or it was changed. Nothing was imported.").
		WithDetail("entry", name).
		WithDetail("reason", why)
}

func tooLargeZip(msg string, limit int, name string) *errs.Error {
	e := errs.New(errs.InvalidArgument, msg+" Nothing was imported.").
		WithCode(CodeTooLarge).
		WithHint("Export a smaller folder, or check that the zip is a PostSonde folder export.").
		WithDetail("limit", strconv.Itoa(limit))
	if name != "" {
		e = e.WithDetail("entry", name)
	}
	return e
}

func newerFormat(version, supported int) *errs.Error {
	return errs.New(errs.Unsupported, "The zip was exported by a newer version of PostSonde.").
		WithCode(CodeNewerFormat).
		WithHint("Update PostSonde to import it.").
		WithDetail("version", strconv.Itoa(version)).
		WithDetail("supported", strconv.Itoa(supported))
}
