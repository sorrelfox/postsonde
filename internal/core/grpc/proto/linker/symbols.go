package linker

import (
	"strconv"
	"strings"
	"sync"

	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/ast"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/parser"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/wellknown"
)

type symKind uint8

const (
	symPackage symKind = iota + 1
	symMessage
	symEnum
	symEnumValue
	symService
	symMethod
	symField
	symOneof
)

var symKindNames = [...]string{
	symPackage: "a package", symMessage: "a message", symEnum: "an enum", symEnumValue: "an enum value",
	symService: "a service", symMethod: "a method", symField: "a field", symOneof: "a oneof",
}

// symbol is an entry of the global symbol table.
type symbol struct {
	kind  symKind
	d     desc.Descriptor // nil for packages
	file  *fileState      // defining file; the first one for a package
	files []*fileState    // packages: every file declaring it or a subpackage
}

func (s *symbol) isType() bool { return s.kind == symMessage || s.kind == symEnum }

func (s *symbol) isAggregate() bool {
	return s.kind == symPackage || s.kind == symMessage || s.kind == symEnum || s.kind == symService
}

func (s *symbol) describe() string {
	if s.d != nil {
		if x, ok := s.d.(*desc.Field); ok && x.IsExtension {
			return "an extension"
		}
	}
	return symKindNames[s.kind]
}

func join(scope, name string) string {
	if scope == "" {
		return name
	}
	return scope + "." + name
}

// definePackage registers pkg and its parent packages for f.
func (l *linker) definePackage(f *fileState, pkg string, n ast.Node) {
	if pkg == "" || strings.Contains("."+pkg+".", "..") {
		return
	}
	for i := 0; i <= len(pkg); i++ {
		if i < len(pkg) && pkg[i] != '.' {
			continue
		}
		name := pkg[:i]
		s := l.syms[name]
		switch {
		case s == nil:
			l.syms[name] = &symbol{kind: symPackage, file: f, files: []*fileState{f}}
		case s.kind == symPackage:
			if s.files[len(s.files)-1] != f {
				s.files = append(s.files, f)
			}
		default:
			l.errorAt(f, CodeDuplicateSymbol, n, "package "+strconv.Quote(pkg)+" conflicts with "+
				strconv.Quote(name)+", which is "+s.describe()+" defined in "+strconv.Quote(s.file.path))
			return
		}
	}
}

// define registers d under its full name. Duplicates within one file are
// left to parser.Check.
func (l *linker) define(f *fileState, kind symKind, d desc.Descriptor) {
	b := d.Desc()
	name := b.FullName
	s := l.syms[name]
	if s == nil {
		l.syms[name] = &symbol{kind: kind, d: d, file: f}
		if kind == symMessage || kind == symEnum {
			l.typeNames = append(l.typeNames, name)
		}
		return
	}
	if s.file == f && s.kind != symPackage {
		return
	}
	var msg string
	if s.kind == symPackage {
		msg = strconv.Quote(name) + " is already defined as a package in " + strconv.Quote(s.file.path)
	} else {
		msg = strconv.Quote(name) + " is already defined in " + strconv.Quote(s.file.path)
		if s.file.diskPath != "" && s.file.diskPath == f.diskPath {
			msg += "; the same file is loaded as both " + strconv.Quote(s.file.path) + " and " +
				strconv.Quote(f.path) + " through different import paths"
		} else if kind == symEnumValue || s.kind == symEnumValue {
			msg += "; enum values are siblings of their enum type, not children of it (C++ scoping)"
		}
	}
	l.report(f, CodeDuplicateSymbol, parser.SeverityError, b.NameLoc, msg)
}

// visibleFiles returns the files whose symbols f can use: f, its direct
// imports and, transitively, their public imports. With options, edition
// 2024 option imports count too.
func (l *linker) visibleFiles(f *fileState, options bool) map[*fileState]bool {
	cache := &f.visible
	if options {
		cache = &f.optVisible
	}
	if *cache != nil {
		return *cache
	}
	set := map[*fileState]bool{f: true}
	var addPublic func(t *fileState)
	addPublic = func(t *fileState) {
		if set[t] {
			return
		}
		set[t] = true
		for _, imp := range t.imports {
			if imp.target != nil && imp.decl.Kind == ast.ImportPublic {
				addPublic(imp.target)
			}
		}
	}
	for _, imp := range f.imports {
		if imp.target == nil || imp.decl.Kind == ast.ImportOption && !options {
			continue
		}
		addPublic(imp.target)
	}
	*cache = set
	return set
}

func (l *linker) isVisible(f *fileState, s *symbol, options bool) bool {
	set := l.visibleFiles(f, options)
	if s.kind == symPackage {
		for _, pf := range s.files {
			if set[pf] {
				return true
			}
		}
		return false
	}
	return set[s.file]
}

// lookupResult is the outcome of a name lookup.
type lookupResult struct {
	sym *symbol
	// hidden is a matching symbol in a file f cannot see.
	hidden *symbol
	// partial is set when the first part of a compound name resolved to an
	// aggregate but the rest did not: the full name that was tried.
	partial string
}

// nameString returns the name as written, or "" if a part is missing.
func nameString(n *ast.FullName) string {
	if n == nil || len(n.Parts) == 0 {
		return ""
	}
	for _, p := range n.Parts {
		if p.Missing() {
			return ""
		}
	}
	return n.String()
}

// lookup resolves name from scope (a full name) the way protoc does:
// fully qualified names are looked up directly; otherwise the first
// component is searched from the innermost scope outwards, and the rest of
// the name must resolve inside what it found. With typesOnly, single-part
// matches that are not messages or enums are skipped.
func (l *linker) lookup(f *fileState, scope, name string, typesOnly, options bool) lookupResult {
	res := l.lookupVisible(f, scope, name, typesOnly, options)
	if res.sym != nil || res.hidden != nil || res.partial != "" {
		return res
	}
	// Nothing visible matched, possibly because a package on the way is
	// declared only in files f does not import: look for the name itself
	// in every enclosing scope to explain the failure.
	for sc := scope; ; {
		full := join(sc, name)
		if strings.HasPrefix(name, ".") {
			full = name[1:]
		}
		if s := l.syms[full]; s != nil && s.kind != symPackage && (!typesOnly || s.isType()) {
			res.hidden = s
			return res
		}
		if sc == "" || strings.HasPrefix(name, ".") {
			return res
		}
		if i := strings.LastIndexByte(sc, '.'); i >= 0 {
			sc = sc[:i]
		} else {
			sc = ""
		}
	}
}

func (l *linker) lookupVisible(f *fileState, scope, name string, typesOnly, options bool) lookupResult {
	var res lookupResult
	find := func(n string) *symbol {
		s := l.syms[n]
		if s == nil {
			return nil
		}
		if !l.isVisible(f, s, options) {
			if res.hidden == nil && s.kind != symPackage {
				res.hidden = s
			}
			return nil
		}
		return s
	}
	if full, ok := strings.CutPrefix(name, "."); ok {
		res.sym = find(full)
		return res
	}
	first, _, compound := strings.Cut(name, ".")
	for {
		if s := find(join(scope, first)); s != nil {
			if compound {
				if s.isAggregate() {
					full := join(scope, name)
					if res.sym = find(full); res.sym == nil && l.syms[full] == nil {
						res.partial = full
					}
					return res
				}
			} else if !typesOnly || s.isType() {
				res.sym = s
				return res
			}
		}
		if scope == "" {
			return res
		}
		if i := strings.LastIndexByte(scope, '.'); i >= 0 {
			scope = scope[:i]
		} else {
			scope = ""
		}
	}
}

// unresolved reports a failed lookup of n (written as name) in f.
func (l *linker) unresolved(f *fileState, n ast.Node, name, what string, res lookupResult, types bool) {
	switch {
	case res.partial != "" && res.partial != strings.TrimPrefix(name, "."):
		l.errorAt(f, CodeUnknownType, n, strconv.Quote(name)+" resolved to "+strconv.Quote(res.partial)+
			", which is not defined; names are searched from the innermost scope outwards, so use "+
			strconv.Quote("."+strings.TrimPrefix(name, "."))+" to start from the outermost scope")
	case res.hidden != nil:
		l.errorAt(f, CodeNotImported, n, strconv.Quote(res.hidden.nameOr(name))+" is defined in "+
			strconv.Quote(res.hidden.file.path)+", which is not imported by "+strconv.Quote(f.path)+
			"; add import "+strconv.Quote(res.hidden.file.path)+";")
	default:
		msg := "unknown " + what + " " + strconv.Quote(name)
		full := strings.TrimPrefix(name, ".")
		if p, ok := wellKnownIndex()[full]; ok && l.files[p] == nil {
			msg += "; add import " + strconv.Quote(p) + ";"
		} else if s := l.suggest(f, name, types); s != "" {
			msg += "; did you mean " + strconv.Quote(s) + "?"
		}
		l.errorAt(f, CodeUnknownType, n, msg)
	}
}

func (s *symbol) nameOr(name string) string {
	if s.d != nil {
		return s.d.Desc().FullName
	}
	return strings.TrimPrefix(name, ".")
}

const maxSuggestions = 50

// suggest returns the full name of a visible type (or, without types, any
// symbol) whose simple name is close to the last part of name, or "".
func (l *linker) suggest(f *fileState, name string, types bool) string {
	last := name[strings.LastIndexByte(name, '.')+1:]
	// Suggestions scan the symbol table; a file full of unknown names
	// gets them for the first few only.
	if len(last) < 3 || l.suggestions >= maxSuggestions {
		return ""
	}
	l.suggestions++
	candidates := l.typeNames
	if !types {
		candidates = make([]string, 0, len(l.syms))
		for full := range l.syms {
			candidates = append(candidates, full)
		}
	}
	limit := 1
	if len(last) >= 8 {
		limit = 2
	}
	best, bestDist := "", limit+1
	trimmed := strings.TrimPrefix(name, ".")
	for _, full := range candidates {
		s := l.syms[full]
		if s.kind == symPackage || types && !s.isType() || full == trimmed || !l.isVisible(f, s, false) {
			continue
		}
		simple := full[strings.LastIndexByte(full, '.')+1:]
		if abs(len(simple)-len(last)) > limit {
			continue
		}
		d := distance(strings.ToLower(simple), strings.ToLower(last))
		if simple != last && d == 0 {
			d = 1 // case differs only: ranked as one edit
		}
		if d < bestDist || d == bestDist && (len(full) < len(best) || len(full) == len(best) && full < best) {
			best, bestDist = full, d
		}
	}
	return best
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}

// distance is the Levenshtein distance between two short strings.
func distance(a, b string) int {
	prev := make([]int, len(b)+1)
	cur := make([]int, len(b)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(a); i++ {
		cur[0] = i
		for j := 1; j <= len(b); j++ {
			cost := 1
			if a[i-1] == b[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(b)]
}

// wellKnownIndex maps the full names of the messages and enums in the
// embedded well-known type files to their import paths.
var wellKnownIndex = sync.OnceValue(func() map[string]string {
	index := map[string]string{}
	for _, p := range wellknown.Paths() {
		src, _ := wellknown.Source(p)
		a, _ := parser.Parse(p, src)
		var walk func(scope string, decls []ast.Decl)
		walk = func(scope string, decls []ast.Decl) {
			for _, d := range decls {
				switch d := d.(type) {
				case *ast.Message:
					if d.Name != nil && !d.Name.Missing() {
						index[join(scope, d.Name.Value)] = p
						walk(join(scope, d.Name.Value), d.Body)
					}
				case *ast.Enum:
					if d.Name != nil && !d.Name.Missing() {
						index[join(scope, d.Name.Value)] = p
					}
				}
			}
		}
		walk(a.Package(), a.Decls)
	}
	return index
})
