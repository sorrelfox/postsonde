// Package netrc reads credentials from a .netrc file (AUTH-19), the way
// curl does: the file named by the request, else the NETRC environment
// variable, else ~/.netrc (~/_netrc on Windows). See
// docs/auth.md#netrc-auth-19.
//
// The format is a list of whitespace-separated tokens:
//
//	machine <host>     starts an entry for a host
//	default            starts the entry for every other host
//	login <name>       the entry's user name
//	password <secret>  the entry's password
//	account <text>     an extra password; read, not used
//	macdef <name>      a macro: its body, from the next line up to an
//	                   empty line, is skipped
//
// A line whose first non-blank character is "#" is a comment; a "#"
// elsewhere is part of a token, so a password may contain one. A token may
// be written in double quotes, with \" \\ \n \r \t escapes (curl 7.84 and
// later), to hold spaces or start with "#"; a quoted token is never a
// keyword. Unknown words are skipped, as curl skips them.
package netrc

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"strings"
)

// MaxSize is the largest file read: a netrc file is a few lines.
const MaxSize = 1 << 20

// Entry is one machine or default entry of a file.
type Entry struct {
	// Machine is the host name of a machine entry; "" for default.
	Machine string
	// Default is set for the default entry.
	Default bool
	// Login, Password and Account are the entry's values; "" when the
	// entry does not set them.
	Login    string
	Password string
	Account  string
	// HasPassword is set when the entry has a password token, even an
	// empty one.
	HasPassword bool
	// Line is the 1-based line of the machine or default token.
	Line int
}

// Name is the entry as the Auth tab names it: the machine's host, or
// "default".
func (e Entry) Name() string {
	if e.Default {
		return "default"
	}
	return e.Machine
}

// File is a parsed netrc file.
type File struct {
	// Entries are the machine and default entries in file order.
	Entries []Entry
}

// SyntaxError is a file that cannot be read as netrc. The message never
// holds a value from the file.
type SyntaxError struct {
	Line int
	Msg  string
}

func (e *SyntaxError) Error() string { return fmt.Sprintf("line %d: %s", e.Line, e.Msg) }

// Parse reads a netrc file.
func Parse(data []byte) (*File, error) {
	p := parser{data: data, line: 1, lineStart: true}
	f := &File{}
	var cur *Entry
	for {
		tok, ok, err := p.next()
		if err != nil {
			return nil, err
		}
		if !ok {
			return f, nil
		}
		if tok.quoted {
			// A value where a keyword is expected: skipped, as curl does.
			continue
		}
		switch tok.text {
		case "machine":
			name, err := p.value(tok)
			if err != nil {
				return nil, err
			}
			f.Entries = append(f.Entries, Entry{Machine: name, Line: tok.line})
			cur = &f.Entries[len(f.Entries)-1]
		case "default":
			f.Entries = append(f.Entries, Entry{Default: true, Line: tok.line})
			cur = &f.Entries[len(f.Entries)-1]
		case "login", "password", "account":
			v, err := p.value(tok)
			if err != nil {
				return nil, err
			}
			if cur == nil {
				return nil, &SyntaxError{Line: tok.line, Msg: fmt.Sprintf("%q comes before any machine or default entry", tok.text)}
			}
			switch tok.text {
			case "login":
				cur.Login = v
			case "password":
				cur.Password, cur.HasPassword = v, true
			default:
				cur.Account = v
			}
		case "macdef":
			if _, err := p.value(tok); err != nil {
				return nil, err
			}
			p.skipMacro()
		}
	}
}

// Lookup returns the entry for host: the first machine entry whose name
// is host (ignoring case), else the first default entry. With login set
// (the user name of the URL), only entries with that login count, as in
// curl.
func (f *File) Lookup(host, login string) (Entry, bool) {
	fits := func(e Entry) bool { return login == "" || e.Login == login }
	for _, e := range f.Entries {
		if !e.Default && strings.EqualFold(e.Machine, host) && fits(e) {
			return e, true
		}
	}
	for _, e := range f.Entries {
		if e.Default && fits(e) {
			return e, true
		}
	}
	return Entry{}, false
}

type token struct {
	text   string
	quoted bool
	line   int
}

type parser struct {
	data []byte
	pos  int
	line int
	// lineStart is set while only blanks were read on the current line,
	// so a "#" starts a comment.
	lineStart bool
}

// value returns the token after keyword kw.
func (p *parser) value(kw token) (string, error) {
	tok, ok, err := p.next()
	if err != nil {
		return "", err
	}
	if !ok {
		return "", &SyntaxError{Line: kw.line, Msg: fmt.Sprintf("%q has no value", kw.text)}
	}
	return tok.text, nil
}

// next returns the next token; ok is false at the end of the file.
func (p *parser) next() (tok token, ok bool, err error) {
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		switch {
		case c == '\n':
			p.pos++
			p.line++
			p.lineStart = true
		case c == ' ' || c == '\t' || c == '\r' || c == '\f' || c == '\v':
			p.pos++
		case c == '#' && p.lineStart:
			for p.pos < len(p.data) && p.data[p.pos] != '\n' {
				p.pos++
			}
		case c == '"':
			p.lineStart = false
			return p.quoted()
		default:
			p.lineStart = false
			start := p.pos
			for p.pos < len(p.data) && !isSpace(p.data[p.pos]) {
				p.pos++
			}
			return token{text: string(p.data[start:p.pos]), line: p.line}, true, nil
		}
	}
	return token{}, false, nil
}

// quoted reads a token in double quotes, p.pos at the opening quote.
func (p *parser) quoted() (token, bool, error) {
	line := p.line
	p.pos++
	var b strings.Builder
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		switch c {
		case '"':
			p.pos++
			return token{text: b.String(), quoted: true, line: line}, true, nil
		case '\n':
			return token{}, false, &SyntaxError{Line: line, Msg: "a quoted token is not closed on its line"}
		case '\\':
			p.pos++
			if p.pos >= len(p.data) {
				continue
			}
			switch e := p.data[p.pos]; e {
			case 'n':
				b.WriteByte('\n')
			case 'r':
				b.WriteByte('\r')
			case 't':
				b.WriteByte('\t')
			case '\n':
				return token{}, false, &SyntaxError{Line: line, Msg: "a quoted token is not closed on its line"}
			default:
				b.WriteByte(e)
			}
			p.pos++
		default:
			b.WriteByte(c)
			p.pos++
		}
	}
	return token{}, false, &SyntaxError{Line: line, Msg: "a quoted token is not closed at the end of the file"}
}

// skipMacro skips the rest of the macdef line and the macro body: the
// lines up to and including the first empty (or blank) line.
func (p *parser) skipMacro() {
	p.skipLine()
	for p.pos < len(p.data) {
		start := p.pos
		p.skipLine()
		if strings.TrimSpace(string(p.data[start:p.pos])) == "" {
			return
		}
	}
}

// skipLine moves past the next line break.
func (p *parser) skipLine() {
	for p.pos < len(p.data) {
		c := p.data[p.pos]
		p.pos++
		if c == '\n' {
			p.line++
			p.lineStart = true
			return
		}
	}
}

func isSpace(c byte) bool {
	return c == ' ' || c == '\t' || c == '\n' || c == '\r' || c == '\f' || c == '\v'
}

// Where a Location's path comes from.
const (
	// SourceRequest: the file named in the request's auth.
	SourceRequest = "request"
	// SourceEnv: the NETRC environment variable.
	SourceEnv = "env"
	// SourceHome: .netrc (_netrc on Windows) in the home directory.
	SourceHome = "home"
)

// Location is the file a netrc auth reads.
type Location struct {
	// Path is the file read first.
	Path string
	// Fallback is read when Path does not exist: ~/.netrc on Windows,
	// after ~/_netrc. "" for none.
	Fallback string
	// Source is SourceRequest, SourceEnv or SourceHome.
	Source string
}

// Env is where the netrc file is looked for and how it is read. The zero
// value is the real one: the process environment, the user's home
// directory and the file system. Tests replace the functions so that the
// user's own file is never read.
type Env struct {
	// Getenv reads an environment variable; nil is os.Getenv.
	Getenv func(string) string
	// HomeDir returns the home directory; nil is os.UserHomeDir.
	HomeDir func() (string, error)
	// GOOS is the operating system; "" is runtime.GOOS.
	GOOS string
	// ReadFile reads the file at path, at most MaxSize bytes, and returns
	// its permission bits; nil reads the file system (ReadOSFile).
	ReadFile func(path string) ([]byte, fs.FileMode, error)
}

func (e Env) goos() string {
	if e.GOOS == "" {
		return runtime.GOOS
	}
	return e.GOOS
}

func (e Env) getenv(name string) string {
	if e.Getenv == nil {
		return os.Getenv(name)
	}
	return e.Getenv(name)
}

// ErrNoHome is returned by Locate when there is no home directory to look
// in.
var ErrNoHome = errors.New("netrc: no home directory")

// ErrTooLarge is returned by ReadOSFile for a file over MaxSize.
var ErrTooLarge = errors.New("netrc: file too large")

// ErrNotFile is returned by ReadOSFile for a directory or another file
// that is not a regular file.
var ErrNotFile = errors.New("netrc: not a regular file")

// Home returns the home directory: on Windows the HOME variable when set
// (as curl does), then the system's.
func (e Env) Home() (string, error) {
	if e.goos() == "windows" {
		if h := e.getenv("HOME"); h != "" {
			return h, nil
		}
	}
	home := os.UserHomeDir
	if e.HomeDir != nil {
		home = e.HomeDir
	}
	h, err := home()
	if err != nil || h == "" {
		return "", ErrNoHome
	}
	return h, nil
}

// Locate returns the file to read. override is the path the request
// names ("" for none): "~/" at its start is the home directory, and a
// relative path is relative to root (an error when root is ""). Without
// one, the NETRC environment variable names it, else .netrc in the home
// directory; on Windows _netrc, with .netrc as the fallback. Nothing is
// read.
func (e Env) Locate(override, root string) (Location, error) {
	if override != "" {
		p := override
		if p == "~" || strings.HasPrefix(p, "~/") || (e.goos() == "windows" && strings.HasPrefix(p, `~\`)) {
			h, err := e.Home()
			if err != nil {
				return Location{}, err
			}
			p = filepath.Join(h, p[1:])
		} else if !filepath.IsAbs(p) {
			if root == "" {
				return Location{}, fmt.Errorf("netrc: relative path %q without a project", p)
			}
			p = filepath.Join(root, p)
		}
		return Location{Path: p, Source: SourceRequest}, nil
	}
	if p := e.getenv("NETRC"); p != "" {
		return Location{Path: p, Source: SourceEnv}, nil
	}
	h, err := e.Home()
	if err != nil {
		return Location{}, err
	}
	if e.goos() == "windows" {
		return Location{Path: filepath.Join(h, "_netrc"), Fallback: filepath.Join(h, ".netrc"), Source: SourceHome}, nil
	}
	return Location{Path: filepath.Join(h, ".netrc"), Source: SourceHome}, nil
}

// Loaded is a netrc file read and parsed.
type Loaded struct {
	// Path is the file that was read.
	Path string
	File *File
	// Insecure is set on Unix when the group or others may read or write
	// the file. curl does not refuse such a file either; it is a warning.
	Insecure bool
}

// Load reads and parses the file at loc. Errors: an fs error of reading
// (fs.ErrNotExist, fs.ErrPermission), ErrTooLarge, ErrNotFile, or a
// *SyntaxError, wrapped in a *PathError naming the path.
func (e Env) Load(loc Location) (Loaded, error) {
	read := ReadOSFile
	if e.ReadFile != nil {
		read = e.ReadFile
	}
	path := loc.Path
	data, mode, err := read(path)
	if err != nil && loc.Fallback != "" && errors.Is(err, fs.ErrNotExist) {
		path = loc.Fallback
		data, mode, err = read(path)
		if err != nil && errors.Is(err, fs.ErrNotExist) {
			// Name the preferred file in the error.
			path = loc.Path
		}
	}
	if err != nil {
		return Loaded{}, &PathError{Path: path, Err: err}
	}
	f, err := Parse(data)
	if err != nil {
		return Loaded{}, &PathError{Path: path, Err: err}
	}
	return Loaded{Path: path, File: f, Insecure: e.goos() != "windows" && mode.Perm()&0o077 != 0}, nil
}

// PathError is an error of reading or parsing the file at Path.
type PathError struct {
	Path string
	Err  error
}

func (e *PathError) Error() string { return "netrc " + e.Path + ": " + e.Err.Error() }
func (e *PathError) Unwrap() error { return e.Err }

// ReadOSFile reads a regular file of at most MaxSize bytes and returns its
// permission bits.
func ReadOSFile(path string) ([]byte, fs.FileMode, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, 0, err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return nil, 0, err
	}
	if !info.Mode().IsRegular() {
		return nil, 0, ErrNotFile
	}
	data, err := io.ReadAll(io.LimitReader(f, MaxSize+1))
	if err != nil {
		return nil, 0, err
	}
	if len(data) > MaxSize {
		return nil, 0, ErrTooLarge
	}
	return data, info.Mode(), nil
}
