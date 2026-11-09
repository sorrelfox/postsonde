// Package linker loads .proto files with their imports through import
// paths (protoc's --proto_path) and links them into the descriptor model of
// package desc: type names resolved across files and packages, extensions
// checked against their extendees, options resolved and type-checked,
// syntax and features applied to fields and enums.
//
// Loading never stops at the first problem. Syntax errors, parser.Check
// diagnostics and linker diagnostics are returned together, and every file
// that could be read is linked as far as possible (see package desc for
// what partial descriptors look like). Load returns an error only for
// invalid options or a cancelled context.
//
// See docs/proto.md.
package linker

import (
	"cmp"
	"context"
	"errors"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/fileaccess"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/ast"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/parser"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/wellknown"
)

// Options configure Load.
type Options struct {
	// Files are the files to load, in order. Each is either an import path
	// found under ImportPaths ("api/v1/orders.proto") or a file path. A file
	// path under an import path gets the import path relative to the first
	// such root; a file under none of them gets its base name, and its
	// directory is added as an import path after the configured ones (with
	// a CodeImplicitImportPath warning).
	Files []string
	// ImportPaths are the import roots, searched in order. The embedded
	// well-known types (package wellknown) are searched after all of them.
	ImportPaths []string
	// FS, if set, is read instead of the operating system's file system;
	// paths are then slash-separated and relative to its root ("." is the
	// root). Otherwise relative paths are relative to the working directory.
	FS fs.FS
	// Overlay maps file paths (in the same form as Files; they are cleaned,
	// and made absolute without FS) to contents that replace the file on
	// disk, such as unsaved editor buffers. Overlay files need not exist.
	Overlay map[string]string
	// NoWellKnown disables the embedded well-known types.
	NoWellKnown bool
	// Access, if set, limits the files of the operating system's file
	// system that are found and read (PRV-10): a file it refuses does not
	// exist for imports, and an input file it refuses is reported with
	// its error's code (fileaccess.not_allowed). fileaccess.Policy
	// implements it. It is not used with FS.
	Access FileAccess
}

// FileAccess decides which files of the operating system's file system
// Load may find and read: fileaccess.Policy, the project folder and the
// allowed directories of the settings.
type FileAccess interface {
	ReadFile(path string) ([]byte, error)
	Stat(path string) (fs.FileInfo, error)
}

// Result is the outcome of Load.
type Result struct {
	// Files are the linked input files, in the order of Options.Files,
	// without inputs that could not be read and without duplicates.
	Files []*desc.File
	// Pool holds every loaded file, dependencies first: the inputs, their
	// imports, and google/protobuf/descriptor.proto, which is always loaded
	// to resolve options.
	Pool *desc.Pool
	// Diagnostics lists syntax errors, Check diagnostics and linker
	// diagnostics, by file in Pool order (inputs that could not be loaded
	// first), then by position.
	Diagnostics []Diagnostic
	// ImportPaths are the import roots used, the implicit ones included.
	ImportPaths []string
}

// HasErrors reports whether any diagnostic is an error.
func (r *Result) HasErrors() bool {
	for _, d := range r.Diagnostics {
		if d.Severity == parser.SeverityError {
			return true
		}
	}
	return false
}

// Err returns nil if no diagnostic is an error, and otherwise a parse error
// whose message is the first error diagnostic, with the error count as the
// "errors" detail.
func (r *Result) Err() error {
	n := 0
	var first *Diagnostic
	for i := range r.Diagnostics {
		if r.Diagnostics[i].Severity == parser.SeverityError {
			if first == nil {
				first = &r.Diagnostics[i]
			}
			n++
		}
	}
	if first == nil {
		return nil
	}
	return errs.New(errs.Parse, first.Error()).WithCode("proto.link_failed").
		WithDetail("errors", strconv.Itoa(n))
}

// Load reads, parses, checks and links opts.Files and everything they
// import.
func Load(ctx context.Context, opts Options) (*Result, error) {
	if len(opts.Files) == 0 {
		return nil, errs.New(errs.InvalidArgument, "No .proto files to load.").WithCode("proto.no_files")
	}
	l := &linker{
		ctx:        ctx,
		overlay:    map[string]string{},
		files:      map[string]*fileState{},
		wellKnown:  !opts.NoWellKnown,
		syms:       map[string]*symbol{},
		nodes:      map[*desc.Field]ast.Node{},
		optionSeen: map[*[]*desc.Option]*setOptions{},
	}
	if opts.FS != nil {
		l.src = fsSource{opts.FS}
	} else {
		l.src = osSource{access: opts.Access}
	}
	for _, r := range opts.ImportPaths {
		p, err := l.src.clean(r)
		if err != nil {
			return nil, errs.Wrap(err, errs.InvalidArgument, "Invalid import path "+strconv.Quote(r)+".").
				WithCode("proto.invalid_import_root")
		}
		if !slices.Contains(l.roots, p) {
			l.roots = append(l.roots, p)
		}
	}
	for name, content := range opts.Overlay {
		p, err := l.src.clean(name)
		if err != nil {
			return nil, errs.Wrap(err, errs.InvalidArgument, "Invalid overlay path "+strconv.Quote(name)+".").
				WithCode("proto.invalid_overlay_path")
		}
		l.overlay[p] = content
	}

	var inputs []*fileState
	for _, name := range opts.Files {
		if err := ctx.Err(); err != nil {
			return nil, cancelled(err)
		}
		f := l.openInput(name)
		if f == nil {
			continue
		}
		if f.state == stateNew {
			if err := l.visit(f); err != nil {
				return nil, err
			}
		}
		if !slices.Contains(inputs, f) {
			inputs = append(inputs, f)
		}
	}
	// Options are interpreted against descriptor.proto whether or not a
	// file imports it, as protoc does with its built-in copy.
	const descriptorPath = "google/protobuf/descriptor.proto"
	if l.files[descriptorPath] == nil && len(l.order) > 0 {
		if f := l.openImport(descriptorPath, nil, nil); f != nil {
			if err := l.visit(f); err != nil {
				return nil, err
			}
		}
	}
	l.descriptorFile = l.files[descriptorPath]

	if err := l.link(); err != nil {
		return nil, err
	}

	res := &Result{ImportPaths: slices.Clone(l.roots)}
	files := make([]*desc.File, len(l.order))
	index := map[string]int{}
	for i, f := range l.order {
		files[i] = f.d
		index[f.path] = i
	}
	for _, f := range inputs {
		res.Files = append(res.Files, f.d)
	}
	res.Pool = desc.NewPool(files)
	slices.SortStableFunc(l.diags, func(a, b Diagnostic) int {
		ia, oka := index[a.File]
		ib, okb := index[b.File]
		if !oka {
			ia = -1
		}
		if !okb {
			ib = -1
		}
		return cmp.Or(cmp.Compare(ia, ib), cmp.Compare(a.Start, b.Start))
	})
	res.Diagnostics = l.diags
	return res, nil
}

func cancelled(err error) error {
	return errs.Wrap(err, errs.KindOf(err), "Loading .proto files was cancelled.").WithCode("proto.cancelled")
}

type loadState uint8

const (
	stateNew loadState = iota
	stateLoading
	stateDone
)

// fileState is a loaded file during linking.
type fileState struct {
	path     string // import path
	diskPath string
	root     string
	builtin  bool
	ast      *ast.File
	d        *desc.File
	imports  []*importState
	state    loadState

	visible, optVisible map[*fileState]bool
}

type importState struct {
	decl   *ast.Import
	target *fileState
}

type linker struct {
	ctx       context.Context
	src       source
	overlay   map[string]string
	roots     []string
	wellKnown bool

	files          map[string]*fileState
	order          []*fileState // dependencies first
	stack          []*fileState
	descriptorFile *fileState
	diags          []Diagnostic
	implicitWarned map[string]bool

	syms        map[string]*symbol
	typeNames   []string // messages and enums, in definition order
	suggestions int

	pendingFields  []pendingField
	pendingExtends []pendingExtend
	pendingMethods []pendingMethod
	pendingOptions []pendingOptions
	features       map[any]*featureSet
	packed         map[*desc.Field]bool
	nodes          map[*desc.Field]ast.Node
	optionSeen     map[*[]*desc.Option]*setOptions
}

// report adds a diagnostic at l in f.
func (l *linker) report(f *fileState, code string, sev parser.Severity, loc ast.Loc, msg string) {
	pos := f.ast.Position(loc.Start)
	l.diags = append(l.diags, Diagnostic{Code: code, Severity: sev, File: f.path, DiskPath: f.diskPath,
		Start: loc.Start, End: loc.End, Line: pos.Line, Column: pos.Column, Message: msg})
}

func (l *linker) errorAt(f *fileState, code string, n ast.Node, msg string) {
	l.report(f, code, parser.SeverityError, n.Pos(), msg)
}

// reportFile adds a diagnostic without a position.
func (l *linker) reportFile(file, disk, code string, sev parser.Severity, msg string) {
	l.diags = append(l.diags, Diagnostic{Code: code, Severity: sev, File: file, DiskPath: disk, Message: msg})
}

// ---- file systems ----

// source abstracts the OS file system and an fs.FS.
type source interface {
	clean(p string) (string, error)
	join(dir, rel string) string
	// rel returns p relative to dir with forward slashes, if p is inside.
	rel(dir, p string) (string, bool)
	split(p string) (dir, base string)
	isFile(p string) bool
	read(p string) (string, error)
	display(p string) string
}

// osSource is the operating system's file system, limited to what access
// allows when it is set.
type osSource struct{ access FileAccess }

func (osSource) clean(p string) (string, error) { return filepath.Abs(p) }
func (osSource) join(dir, rel string) string    { return filepath.Join(dir, filepath.FromSlash(rel)) }
func (osSource) rel(dir, p string) (string, bool) {
	r, err := filepath.Rel(dir, p)
	if err != nil || r == "." || r == ".." || strings.HasPrefix(r, ".."+string(filepath.Separator)) {
		return "", false
	}
	return filepath.ToSlash(r), true
}
func (osSource) split(p string) (string, string) { return filepath.Dir(p), filepath.Base(p) }
func (s osSource) isFile(p string) bool {
	stat := os.Stat
	if s.access != nil {
		stat = s.access.Stat
	}
	st, err := stat(p)
	return err == nil && st.Mode().IsRegular()
}
func (s osSource) read(p string) (string, error) {
	read := os.ReadFile
	if s.access != nil {
		read = s.access.ReadFile
	}
	b, err := read(p)
	return string(b), err
}

// refused returns the error of a file that access does not allow, nil
// for one it allows or that is missing.
func (s osSource) refused(p string) error {
	if s.access == nil {
		return nil
	}
	if _, err := s.access.Stat(p); fileaccess.IsNotAllowed(err) {
		return err
	}
	return nil
}
func (osSource) display(p string) string { return p }

type fsSource struct{ fsys fs.FS }

func (fsSource) clean(p string) (string, error) {
	p = path.Clean(filepath.ToSlash(p))
	if !fs.ValidPath(p) {
		return "", errors.New("not a valid path in the file system: " + strconv.Quote(p))
	}
	return p, nil
}
func (fsSource) join(dir, rel string) string { return path.Join(dir, rel) }
func (fsSource) rel(dir, p string) (string, bool) {
	if dir == "." {
		return p, p != "."
	}
	if r, ok := strings.CutPrefix(p, dir+"/"); ok {
		return r, true
	}
	return "", false
}
func (fsSource) split(p string) (string, string) { return path.Dir(p), path.Base(p) }
func (s fsSource) isFile(p string) bool {
	st, err := fs.Stat(s.fsys, p)
	return err == nil && st.Mode().IsRegular()
}
func (s fsSource) read(p string) (string, error) {
	b, err := fs.ReadFile(s.fsys, p)
	return string(b), err
}
func (fsSource) display(p string) string { return p }

// validImportPath reports whether p is a clean relative path with forward
// slashes, as protoc requires for virtual paths.
func validImportPath(p string) bool {
	return fs.ValidPath(p) && p != "." && !strings.Contains(p, `\`)
}

// exists reports whether p is in the overlay or is a regular file.
func (l *linker) exists(p string) bool {
	if _, ok := l.overlay[p]; ok {
		return true
	}
	return l.src.isFile(p)
}

func (l *linker) readFile(p string) (string, error) {
	if s, ok := l.overlay[p]; ok {
		return s, nil
	}
	return l.src.read(p)
}

// ---- loading ----

// hit is a place an import path was found.
type hit struct {
	disk, root string
	builtin    bool
}

// locate returns every place importPath exists, in search order.
func (l *linker) locate(importPath string) []hit {
	var hits []hit
	for _, r := range l.roots {
		if p := l.src.join(r, importPath); l.exists(p) {
			hits = append(hits, hit{disk: p, root: r})
		}
	}
	if l.wellKnown {
		if _, ok := wellknown.Source(importPath); ok {
			hits = append(hits, hit{builtin: true})
		}
	}
	return hits
}

func (l *linker) searched() string {
	parts := make([]string, 0, len(l.roots)+1)
	for _, r := range l.roots {
		parts = append(parts, l.src.display(r))
	}
	if l.wellKnown {
		parts = append(parts, "the built-in well-known types")
	}
	if len(parts) == 0 {
		return "no import paths are configured"
	}
	return "searched " + strings.Join(parts, ", ")
}

// newFile parses and checks src and registers it under importPath.
func (l *linker) newFile(importPath string, h hit, src string) *fileState {
	var p *parsed
	if h.builtin {
		p = parseBuiltin(importPath, src)
	} else {
		p = parse(importPath, h.disk, src)
	}
	f := &fileState{path: importPath, diskPath: h.disk, root: h.root, builtin: h.builtin, ast: p.ast}
	f.d = &desc.File{Path: importPath, DiskPath: h.disk, Root: h.root, Builtin: h.builtin, AST: p.ast}
	l.files[importPath] = f
	l.diags = append(l.diags, p.diags...)
	return f
}

// parsed is a parsed and checked file.
type parsed struct {
	ast   *ast.File
	diags []Diagnostic
}

func parse(importPath, disk, src string) *parsed {
	a, perrs := parser.Parse(importPath, src)
	p := &parsed{ast: a}
	for _, e := range perrs {
		p.diags = append(p.diags, Diagnostic{Code: CodeSyntax, Severity: parser.SeverityError, File: importPath,
			DiskPath: disk, Start: e.Start, End: e.End, Line: e.Line, Column: e.Column, Message: e.Message})
	}
	for _, d := range parser.Check(a) {
		p.diags = append(p.diags, Diagnostic{Code: checkCode(d.Code), Severity: d.Severity, File: importPath,
			DiskPath: disk, Start: d.Start, End: d.End, Line: d.Line, Column: d.Column, Message: d.Message})
	}
	return p
}

// builtinCache holds the parsed well-known type files, which never change;
// syntax trees are read-only, so results may share them.
var builtinCache sync.Map // import path -> *parsed

func parseBuiltin(importPath, src string) *parsed {
	if p, ok := builtinCache.Load(importPath); ok {
		return p.(*parsed)
	}
	p, _ := builtinCache.LoadOrStore(importPath, parse(importPath, "", src))
	return p.(*parsed)
}

// openHit reads the file at h. from and decl locate a read error.
func (l *linker) openHit(importPath string, h hit, from *fileState, decl *ast.Import) *fileState {
	var src string
	if h.builtin {
		src, _ = wellknown.Source(importPath)
	} else {
		var err error
		if src, err = l.readFile(h.disk); err != nil {
			msg := "cannot read " + strconv.Quote(l.src.display(h.disk)) + ": " + err.Error()
			code := CodeReadFailed
			if fileaccess.IsNotAllowed(err) {
				code = fileaccess.CodeNotAllowed // a link changed after it was found
			}
			if from != nil && decl != nil && decl.Path != nil {
				l.errorAt(from, code, decl.Path, msg)
			} else {
				l.reportFile(importPath, h.disk, code, parser.SeverityError, msg)
			}
			return nil
		}
	}
	return l.newFile(importPath, h, src)
}

// openImport locates and reads importPath for an import statement (from
// and decl are nil for descriptor.proto loaded for options). Missing files
// are reported at the import.
func (l *linker) openImport(importPath string, from *fileState, decl *ast.Import) *fileState {
	hits := l.locate(importPath)
	if len(hits) == 0 {
		if from == nil {
			return nil
		}
		msg := strconv.Quote(importPath) + " not found: " + l.searched()
		if decl.Kind == ast.ImportWeak {
			l.report(from, CodeWeakImportNotFound, parser.SeverityWarning, decl.Path.Loc, msg+" (weak import, ignored)")
		} else {
			l.errorAt(from, CodeImportNotFound, decl.Path, msg)
		}
		return nil
	}
	if from != nil {
		l.warnShadowed(importPath, hits, from, decl)
	}
	return l.openHit(importPath, hits[0], from, decl)
}

// warnShadowed warns when importPath exists under several import roots
// (a built-in copy shadowed by an import root is expected and not reported).
func (l *linker) warnShadowed(importPath string, hits []hit, from *fileState, decl *ast.Import) {
	var others []string
	for _, h := range hits[1:] {
		if !h.builtin {
			others = append(others, strconv.Quote(l.src.display(h.disk)))
		}
	}
	if len(others) == 0 {
		return
	}
	l.report(from, CodeImportShadowed, parser.SeverityWarning, decl.Path.Loc,
		strconv.Quote(importPath)+" exists under more than one import path; using "+
			strconv.Quote(l.src.display(hits[0].disk))+", not "+strings.Join(others, ", "))
}

// openInput resolves one of Options.Files.
func (l *linker) openInput(name string) *fileState {
	asImport := name
	if _, ok := l.src.(osSource); ok {
		if filepath.IsAbs(name) {
			asImport = ""
		} else {
			asImport = filepath.ToSlash(name)
		}
	}
	if validImportPath(asImport) {
		if f := l.files[asImport]; f != nil {
			return f
		}
		if hits := l.locate(asImport); len(hits) > 0 {
			return l.openHit(asImport, hits[0], nil, nil)
		}
	}

	p, err := l.src.clean(name)
	if osrc, ok := l.src.(osSource); ok && err == nil {
		if rerr := osrc.refused(p); rerr != nil {
			l.reportFile(name, p, fileaccess.CodeNotAllowed, parser.SeverityError, rerr.Error())
			return nil
		}
	}
	if err != nil || !l.exists(p) {
		msg := "file not found"
		if validImportPath(asImport) {
			msg += "; also looked for it as an import path: " + l.searched()
		}
		l.reportFile(name, "", CodeFileNotFound, parser.SeverityError, msg)
		return nil
	}
	rootIdx, importPath := -1, ""
	for i, r := range l.roots {
		if rel, ok := l.src.rel(r, p); ok && validImportPath(rel) {
			rootIdx, importPath = i, rel
			break
		}
	}
	if rootIdx < 0 {
		dir, base := l.src.split(p)
		l.roots = append(l.roots, dir)
		rootIdx, importPath = len(l.roots)-1, base
		if l.implicitWarned == nil {
			l.implicitWarned = map[string]bool{}
		}
		if !l.implicitWarned[dir] {
			l.implicitWarned[dir] = true
			l.reportFile(importPath, p, CodeImplicitImportPath, parser.SeverityWarning,
				strconv.Quote(l.src.display(p))+" is not under any import path; added its directory "+
					strconv.Quote(l.src.display(dir))+" as an import path")
		}
	}
	root := l.roots[rootIdx]
	if f := l.files[importPath]; f != nil {
		if f.diskPath == p {
			return f
		}
		l.reportFile(importPath, p, CodeInputShadowed, parser.SeverityError,
			strconv.Quote(l.src.display(p))+" has import path "+strconv.Quote(importPath)+
				", which is already loaded from "+describeOrigin(l, f)+"; remove one of them or reorder the import paths")
		return nil
	}
	for _, r := range l.roots[:rootIdx] {
		if q := l.src.join(r, importPath); l.exists(q) {
			l.reportFile(importPath, p, CodeInputShadowed, parser.SeverityError,
				strconv.Quote(l.src.display(p))+" is shadowed by "+strconv.Quote(l.src.display(q))+
					", which has the same import path "+strconv.Quote(importPath)+
					" in an earlier import path; imports of it resolve to that file. Load that file instead, or reorder the import paths")
			break
		}
	}
	return l.openHit(importPath, hit{disk: p, root: root}, nil, nil)
}

func describeOrigin(l *linker, f *fileState) string {
	if f.builtin {
		return "the built-in well-known types"
	}
	return strconv.Quote(l.src.display(f.diskPath))
}

// visit loads f's imports depth-first and appends f to l.order after them.
func (l *linker) visit(f *fileState) error {
	f.state = stateLoading
	l.stack = append(l.stack, f)
	for _, d := range f.ast.Decls {
		decl, ok := d.(*ast.Import)
		if !ok || decl.Path == nil {
			continue
		}
		imp := &importState{decl: decl}
		f.imports = append(f.imports, imp)
		p := decl.Path.Value
		if !validImportPath(p) {
			l.errorAt(f, CodeInvalidImportPath, decl.Path, "import path "+strconv.Quote(p)+
				" must be relative to an import path, with forward slashes and without \".\" or \"..\" elements")
			continue
		}
		t := l.files[p]
		if t == nil {
			if err := l.ctx.Err(); err != nil {
				return cancelled(err)
			}
			if t = l.openImport(p, f, decl); t == nil {
				continue
			}
			if err := l.visit(t); err != nil {
				return err
			}
		} else if t.state == stateLoading {
			i := slices.Index(l.stack, t)
			chain := make([]string, 0, len(l.stack)-i+1)
			for _, s := range l.stack[i:] {
				chain = append(chain, strconv.Quote(s.path))
			}
			chain = append(chain, strconv.Quote(t.path))
			l.errorAt(f, CodeImportCycle, decl.Path, "import cycle: "+strings.Join(chain, " -> "))
			continue
		}
		imp.target = t
	}
	l.stack = l.stack[:len(l.stack)-1]
	f.state = stateDone
	l.order = append(l.order, f)
	return nil
}
