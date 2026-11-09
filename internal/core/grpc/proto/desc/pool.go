package desc

import (
	"cmp"
	"slices"
	"strings"
)

// Pool indexes a set of linked files by path and full name.
type Pool struct {
	files   []*File
	byPath  map[string]*File
	symbols map[string]Descriptor
	exts    map[*Message][]*Field
}

// NewPool indexes files, which should list dependencies before dependents.
// If two files have the same path or define the same full name, the first
// one wins; the linker reports such conflicts.
func NewPool(files []*File) *Pool {
	p := &Pool{
		files:   slices.Clone(files),
		byPath:  make(map[string]*File, len(files)),
		symbols: map[string]Descriptor{},
		exts:    map[*Message][]*Field{},
	}
	for _, f := range files {
		if _, dup := p.byPath[f.Path]; dup {
			continue
		}
		p.byPath[f.Path] = f
		Walk(f, func(d Descriptor) bool {
			name := d.Desc().FullName
			if _, dup := p.symbols[name]; !dup && name != "" {
				p.symbols[name] = d
			}
			if x, ok := d.(*Field); ok && x.IsExtension && x.Extendee != nil {
				p.exts[x.Extendee] = append(p.exts[x.Extendee], x)
			}
			return true
		})
	}
	for _, xs := range p.exts {
		slices.SortStableFunc(xs, func(a, b *Field) int { return cmp.Compare(a.Number, b.Number) })
	}
	return p
}

// Files returns the files in dependency order.
func (p *Pool) Files() []*File { return p.files }

// FindFile returns the file with import path path, or nil.
func (p *Pool) FindFile(path string) *File { return p.byPath[path] }

// Find returns the element with the given full name (a leading dot is
// allowed), or nil.
func (p *Pool) Find(fullName string) Descriptor {
	return p.symbols[strings.TrimPrefix(fullName, ".")]
}

// FindMessage returns the message with the given full name, or nil.
func (p *Pool) FindMessage(fullName string) *Message {
	m, _ := p.Find(fullName).(*Message)
	return m
}

// FindEnum returns the enum with the given full name, or nil.
func (p *Pool) FindEnum(fullName string) *Enum {
	e, _ := p.Find(fullName).(*Enum)
	return e
}

// FindService returns the service with the given full name, or nil.
func (p *Pool) FindService(fullName string) *Service {
	s, _ := p.Find(fullName).(*Service)
	return s
}

// FindExtension returns the extension with the given full name, or nil.
func (p *Pool) FindExtension(fullName string) *Field {
	f, _ := p.Find(fullName).(*Field)
	if f == nil || !f.IsExtension {
		return nil
	}
	return f
}

// Extensions returns the extensions of m in all files, by number.
func (p *Pool) Extensions(m *Message) []*Field { return p.exts[m] }

// FindExtensionByNumber returns the extension of m with number n, or nil.
func (p *Pool) FindExtensionByNumber(m *Message, n int32) *Field {
	for _, x := range p.exts[m] {
		if x.Number == n {
			return x
		}
	}
	return nil
}

// Services returns every service in file order.
func (p *Pool) Services() []*Service {
	var out []*Service
	for _, f := range p.files {
		out = append(out, f.Services...)
	}
	return out
}

// Walk calls fn for every element of f in declaration order, parents before
// children: messages (then their fields, oneofs, nested messages, enums and
// extensions), enums (then their values), services (then their methods) and
// top-level extensions. If fn returns false for an element, its children
// are skipped.
func Walk(f *File, fn func(Descriptor) bool) {
	for _, m := range f.Messages {
		walkMessage(m, fn)
	}
	for _, e := range f.Enums {
		walkEnum(e, fn)
	}
	for _, s := range f.Services {
		if fn(s) {
			for _, m := range s.Methods {
				fn(m)
			}
		}
	}
	for _, x := range f.Extensions {
		fn(x)
	}
}

func walkMessage(m *Message, fn func(Descriptor) bool) {
	if !fn(m) {
		return
	}
	for _, x := range m.Fields {
		fn(x)
	}
	for _, o := range m.Oneofs {
		fn(o)
	}
	for _, n := range m.Messages {
		walkMessage(n, fn)
	}
	for _, e := range m.Enums {
		walkEnum(e, fn)
	}
	for _, x := range m.Extensions {
		fn(x)
	}
}

func walkEnum(e *Enum, fn func(Descriptor) bool) {
	if fn(e) {
		for _, v := range e.Values {
			fn(v)
		}
	}
}
