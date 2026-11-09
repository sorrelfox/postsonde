package template

import (
	"bytes"
	"errors"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// File contents (DYN-08): {{$file PATH}} is the text of a local file, read
// when the request is sent. The resolver does not decide which files may
// be read: Resolver.Files does (the app gives it the allowlist of PRV-10,
// internal/core/fileaccess). Binary content is refused unless the
// reference is the input of a filter pipe ({{$file logo.png | base64}}),
// which turns it into text (docs/templates.md#file-contents).

// FileName is the dynamic value name of file contents.
const FileName = "$file"

// FileScope is Ref.Scope of a reference read from a file.
const FileScope = "File"

// Codes of the errors about {{$file}} references that Result.Err returns
// itself; a Resolver.Files error (outside the allowlist, missing, too
// large) is returned as it is, with its own code.
const (
	// CodeFileBinary: the file is not UTF-8 text and the reference is not
	// piped through a filter (kind invalid_argument). Details: path.
	CodeFileBinary = "template.file_binary"
	// CodeFileUnavailable: this kind of request does not read files
	// (Resolver.Files is nil; kind unsupported).
	CodeFileUnavailable = "template.file_unavailable"
)

// ProblemFile: a {{$file}} reference whose file cannot be inserted.
// Problem.Message says why and Problem.Cause is the error, which
// Result.Err returns. The reference is left as written.
const ProblemFile ProblemKind = "file"

// FileReader reads the file of a {{$file PATH}} reference, with PATH as
// written. It decides which files may be read; its error is shown as it
// is, so it should be an *errs.Error with a message for the user.
type FileReader interface {
	ReadFile(path string) ([]byte, error)
}

// ErrFilesUnavailable is the Problem.Cause of a {{$file}} reference
// rendered without a Resolver.Files. Previews that leave unresolved
// references as written can recognise it with errors.Is.
var ErrFilesUnavailable = errs.New(errs.Unsupported, "File contents are inserted only when an HTTP or GraphQL request is sent.").
	WithCode(CodeFileUnavailable).
	WithHint("Use a variable here instead of {{$file}}.")

func init() {
	// Listed with the dynamic values (completion, the editor's chips), but
	// resolved by resolveFile: IsDynamic and the renderer check ParseFile
	// first.
	dynamicFuncs[FileName] = func(*dynamicGen, []string) (string, error) {
		return "", errors.New("it is read when the request is sent")
	}
	dynamicDescriptions[FileName] = "The text of a local file in an allowed directory, read when the request is sent: {{$file PATH}}"
}

// fileArgsErr says how a file reference is written.
var fileArgsErr = errors.New(`write {{$file PATH}} with one path, in double quotes if it has spaces or a |: {{$file "data/my file.json"}}`)

// ParseFile reads a {{$file PATH}} reference, as written between the
// braces (Segment.Name): ok is false when name is not a file reference at
// all; err says why its argument is not valid. The one argument is the
// path, in double quotes when it has spaces. An unquoted "|" is not part
// of a path, so {{$file a.bin|base64}} is a pipe.
func ParseFile(name string) (path string, ok bool, err error) {
	rest, found := strings.CutPrefix(name, FileName)
	if !found || (rest != "" && strings.IndexByte(spaces, rest[0]) < 0) {
		return "", false, nil
	}
	toks, valid := splitTokens(name)
	if !valid || len(toks) != 2 || (!toks[1].quoted && strings.Contains(toks[1].text, "|")) {
		return "", true, fileArgsErr
	}
	if strings.TrimSpace(toks[1].text) == "" {
		return "", true, errors.New("the path is empty")
	}
	return toks[1].text, true, nil
}

// resolveFile resolves a {{$file}} reference with Resolver.Files. binary
// allows content that is not UTF-8 text (the input of a pipe).
func (st *renderer) resolveFile(seg Segment, ref Ref, path string, err error, binary bool) Ref {
	if err != nil {
		st.reportMessage(ProblemInvalidDynamic, seg.Name, err.Error(), seg.Start, seg.End)
		return ref
	}
	if st.files == nil {
		st.reportCause(ProblemFile, seg.Name, ErrFilesUnavailable, seg.Start, seg.End)
		return ref
	}
	data, err := st.files.ReadFile(path)
	if err != nil {
		st.reportCause(ProblemFile, seg.Name, err, seg.Start, seg.End)
		return ref
	}
	if !binary && !IsText(data) {
		cause := errs.New(errs.InvalidArgument, "The file "+strconv.Quote(path)+" is not UTF-8 text.").
			WithCode(CodeFileBinary).
			WithHint("Encode it with a filter, as in {{$file \""+path+"\" | base64}}, or send it as a file body.").
			WithDetail("path", path)
		st.reportCause(ProblemFile, seg.Name, cause, seg.Start, seg.End)
		return ref
	}
	// Used as is: never parsed for variables, like a generated value.
	ref.Resolved, ref.Scope, ref.Value = true, FileScope, string(data)
	return ref
}

// IsText reports whether data is inserted as text by {{$file}}: valid
// UTF-8 without NUL bytes.
func IsText(data []byte) bool {
	return utf8.Valid(data) && bytes.IndexByte(data, 0) < 0
}

// fileErr is the error of Result.Err for a ProblemFile.
func fileErr(p *Problem) *errs.Error {
	var e *errs.Error
	if !errors.As(p.Cause, &e) {
		e = errs.Wrap(p.Cause, errs.KindOf(p.Cause), "The file of "+braces(p.Name)+" cannot be inserted.")
	}
	return e.WithDetail("reference", p.Name)
}
