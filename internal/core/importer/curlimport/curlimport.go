// Package curlimport is the import framework's cURL format (IMP-01,
// docs/import.md#curl-files): a text file of curl commands, one request
// per command, read by internal/core/curl. A shell script, a .txt of
// commands copied from API docs, or a single "Copy as cURL" saved to a
// file. Standard library only.
package curlimport

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/curl"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/importer"
)

// CodeNotCommand: lines before the first curl command that are neither
// blank nor comments were ignored (info, at the first of them).
const CodeNotCommand = "curl.not_command"

// maxName is the longest request name taken from a comment, in characters.
const maxName = 200

// Importer reads cURL command files.
type Importer struct{}

var _ importer.Importer = Importer{}

// Format describes the cURL format.
func (Importer) Format() importer.Format {
	return importer.Format{
		ID:         "curl",
		Name:       "cURL commands",
		Extensions: []string{".sh", ".curl", ".txt", ".bat", ".cmd", ".ps1"},
	}
}

// Detect is Likely when the first line that is not blank or a comment
// starts a curl command, Maybe when a later one of the first lines does
// (a script that sets variables first); a usual extension adds a little.
// No otherwise.
func (im Importer) Detect(src importer.Source) int {
	text := string(head(src.Data, 64<<10))
	seen := 0
	for _, l := range splitLines(text) {
		line := strings.TrimSpace(l.text)
		if line == "" || isComment(line) {
			continue
		}
		if strings.HasPrefix(line, "{") || strings.HasPrefix(line, "[") || strings.HasPrefix(line, "<") {
			return importer.No
		}
		if curl.StartsCommand(line) {
			score := importer.Maybe
			if seen == 0 {
				score = importer.Likely
			}
			if slices.Contains(im.Format().Extensions, src.Ext()) {
				score += 10
			}
			return score
		}
		if seen++; seen >= 20 {
			break
		}
	}
	return importer.No
}

// Parse splits the file into commands and maps each to an http request.
// A command with an error diagnostic is skipped with its first error as
// the reason. A comment line right above a command names its request;
// otherwise the name is curl's suggestion (the URL's last path segment,
// or its host).
func (im Importer) Parse(ctx context.Context, src importer.Source) (*importer.Model, error) {
	if !utf8.Valid(src.Data) {
		return nil, importer.InvalidFile(im.Format(), "The file is not UTF-8 text.", importer.Location{})
	}
	text := strings.TrimPrefix(string(src.Data), "\uFEFF")
	lines := splitLines(text)
	pos := newPositions(text)
	m := &importer.Model{Name: src.BaseName()}

	var cmds []command
	var junk = -1 // first ignored line before the first command
	var q quotes
	for i, l := range lines {
		trimmed := strings.TrimSpace(l.text)
		if q.state == outside && !q.cont && curl.StartsCommand(trimmed) {
			cmds = append(cmds, command{start: l.start, end: l.end, line: i, name: commentName(lines, i)})
		} else if len(cmds) > 0 {
			cmds[len(cmds)-1].end = l.end
		} else if trimmed != "" && !isComment(trimmed) && junk < 0 {
			junk = i
		}
		q.scan(l.text)
	}
	if junk >= 0 {
		m.Diagnostics = append(m.Diagnostics, importer.Diagnostic{
			Code: CodeNotCommand, Severity: importer.SeverityInfo,
			Message: "Lines before the first curl command were ignored.",
			Source:  importer.Location{Line: junk + 1, Column: 1},
		})
	}
	if len(cmds) == 0 {
		return nil, importer.InvalidFile(im.Format(), "The file has no curl command.", importer.Location{})
	}

	for n, c := range cmds {
		if err := ctx.Err(); err != nil {
			return nil, errs.Wrap(err, errs.KindOf(err), "Reading the file was interrupted.")
		}
		chunk := trimTrailing(text[c.start:c.end])
		loc := importer.Location{Line: c.line + 1, Column: 1, Path: "command " + strconv.Itoa(n+1)}
		imported := curl.Import(chunk)
		diags := make([]importer.Diagnostic, 0, len(imported.Diagnostics))
		for _, d := range imported.Diagnostics {
			line, col := pos.at(c.start + d.Start)
			diags = append(diags, importer.Diagnostic{
				Code: d.Code, Severity: importer.Severity(d.Severity), Message: d.Message,
				Source: importer.Location{Line: line, Column: col, Path: loc.Path},
			})
		}
		name := c.name
		if name == "" {
			name = imported.Name
		}
		if curl.HasErrors(imported.Diagnostics) {
			first := firstError(diags)
			m.Root.Items = append(m.Root.Items, importer.SkippedItem(&importer.Skipped{
				Kind: "curl command", Name: name, Code: first.Code, Reason: first.Message, Source: first.Source,
			}))
			continue
		}
		req := imported.Request
		m.Root.Items = append(m.Root.Items, importer.RequestItem(&importer.Request{
			Name: name, File: &req, Source: loc, Diagnostics: diags,
		}))
	}
	return m, nil
}

// Command maps one curl command, as pasted, to a model with its request
// in the collection's top level (the Import cURL dialog, through
// importer.Write with IntoParent). name overrides the suggested name. It
// fails with curl.invalid_command for a command with an error.
func Command(text, name string) (*importer.Model, error) {
	c := curl.Parse(text)
	if err := c.Err(); err != nil {
		return nil, err
	}
	imported := c.Import()
	if strings.TrimSpace(name) == "" {
		name = imported.Name
	}
	req := imported.Request
	m := &importer.Model{Name: name}
	m.Root.Items = []importer.Item{importer.RequestItem(&importer.Request{Name: strings.TrimSpace(name), File: &req})}
	return m, nil
}

type command struct {
	start, end int // byte range of the command's lines in the text
	line       int // 0-based line of its first line
	name       string
}

type line struct {
	text       string // without the line break
	start, end int    // the line's range, end including the line break
}

func splitLines(text string) []line {
	var out []line
	for start := 0; start < len(text); {
		nl := strings.IndexByte(text[start:], '\n')
		end := len(text)
		if nl >= 0 {
			end = start + nl + 1
		}
		out = append(out, line{text: strings.TrimRight(text[start:end], "\r\n"), start: start, end: end})
		start = end
	}
	return out
}

// quotes follows the shell quoting across lines, so a line inside a
// quoted, multi-line body is never taken for the start of a command. It
// knows POSIX quoting ('...', "...", $'...', backslash escapes, comments)
// and the line continuations of POSIX shells (\), cmd (^) and PowerShell
// (`), which is close enough for cmd and PowerShell text too.
type quotes struct {
	state quoteState
	// cont is set when the last line ended with a line continuation.
	cont bool
}

type quoteState int

const (
	outside quoteState = iota
	single
	double
	ansi // $'...'
)

func (q *quotes) scan(l string) {
	wordStart := true
	for i := 0; i < len(l); i++ {
		c := l[i]
		switch q.state {
		case outside:
			switch {
			case c == '\\':
				i++
			case c == '\'':
				q.state = single
			case c == '"':
				q.state = double
			case c == '$' && i+1 < len(l) && l[i+1] == '\'':
				q.state = ansi
				i++
			case c == '#' && wordStart:
				i = len(l)
				continue
			}
			wordStart = c == ' ' || c == '\t'
		case single:
			if c == '\'' {
				q.state = outside
			}
		case double, ansi:
			switch {
			case c == '\\':
				i++
			case (c == '"' && q.state == double) || (c == '\'' && q.state == ansi):
				q.state = outside
			}
		}
	}
	t := strings.TrimRight(l, " \t")
	q.cont = q.state == outside && (strings.HasSuffix(t, `\`) || strings.HasSuffix(t, "^") || strings.HasSuffix(t, "`"))
}

// isComment reports whether a trimmed line is a comment in a shell, a
// batch file or a notes file.
func isComment(l string) bool {
	upper := strings.ToUpper(l)
	return strings.HasPrefix(l, "#") || strings.HasPrefix(l, "//") || strings.HasPrefix(l, "::") ||
		upper == "REM" || strings.HasPrefix(upper, "REM ") || strings.HasPrefix(l, "@echo") || strings.HasPrefix(upper, "@ECHO")
}

// commentName returns the text of the comment line right above line i,
// if there is one and it is not a shebang or a batch directive.
func commentName(lines []line, i int) string {
	if i == 0 {
		return ""
	}
	l := strings.TrimSpace(lines[i-1].text)
	if strings.HasPrefix(l, "#!") || strings.HasPrefix(strings.ToLower(l), "@echo") {
		return ""
	}
	var name string
	switch {
	case strings.HasPrefix(l, "#"):
		name = strings.TrimLeft(l, "#")
	case strings.HasPrefix(l, "//"):
		name = strings.TrimLeft(l, "/")
	case strings.HasPrefix(l, "::"):
		name = l[2:]
	case strings.HasPrefix(strings.ToUpper(l), "REM "):
		name = l[4:]
	default:
		return ""
	}
	name = strings.TrimSpace(name)
	if utf8.RuneCountInString(name) > maxName {
		name = strings.TrimSpace(string([]rune(name)[:maxName]))
	}
	return name
}

// trimTrailing leaves out the blank and comment lines at the end of a
// command's lines: they come before the next command.
func trimTrailing(chunk string) string {
	lines := splitLines(chunk)
	end := len(lines)
	for end > 1 {
		t := strings.TrimSpace(lines[end-1].text)
		if t != "" && !isComment(t) {
			break
		}
		end--
	}
	if end == len(lines) {
		return chunk
	}
	return chunk[:lines[end-1].end]
}

func firstError(ds []importer.Diagnostic) importer.Diagnostic {
	for _, d := range ds {
		if d.Severity == importer.SeverityError {
			return d
		}
	}
	return importer.Diagnostic{Code: "curl.invalid_command", Message: "The command cannot be imported."}
}

func head(b []byte, n int) []byte {
	if len(b) <= n {
		return b
	}
	return b[:n]
}

// positions converts byte offsets of a text to 1-based lines and columns
// (columns in characters).
type positions struct {
	text   string
	starts []int
}

func newPositions(text string) positions {
	starts := []int{0}
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			starts = append(starts, i+1)
		}
	}
	return positions{text: text, starts: starts}
}

func (p positions) at(off int) (int, int) {
	off = max(0, min(off, len(p.text)))
	lo, hi := 0, len(p.starts)-1
	for lo < hi {
		mid := (lo + hi + 1) / 2
		if p.starts[mid] <= off {
			lo = mid
		} else {
			hi = mid - 1
		}
	}
	return lo + 1, utf8.RuneCountInString(p.text[p.starts[lo]:off]) + 1
}
