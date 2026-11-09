package request

import (
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Error codes of Headers.
const (
	// CodeInvalidHeaderName: an enabled row's name is not an RFC 9110
	// token (after expansion).
	CodeInvalidHeaderName = "request.invalid_header_name"
	// CodeInvalidHeaderValue: an enabled row's value contains a line
	// break, NUL or another control character (after expansion).
	CodeInvalidHeaderValue = "request.invalid_header_value"
)

// Details of the errors Headers returns.
const (
	// DetailRow is the row's index in the table, from 0, disabled and
	// unnamed rows included, so the editor can point at it.
	DetailRow = "row"
	// DetailField is "name" or "value".
	DetailField = "field"
	// DetailHeader is the row's name as typed (never the value).
	DetailHeader = "header"
)

// Expand renders the {{variables}} of a header name or value. It returns
// an error when the text cannot be sent (an undefined variable, a cycle).
type Expand func(text string) (string, error)

// Headers maps a request's header rows onto the header list httpclient
// sends:
//
//   - disabled rows are skipped, and so are rows whose name is empty as
//     typed (a row being filled in, whatever its value);
//   - the other rows keep their order, duplicates and the spelling typed;
//     values are not trimmed or otherwise changed;
//   - expand, when not nil, renders the name and then the value, and the
//     rendered text is what is validated and sent;
//   - a name must be an RFC 9110 token (letters, digits and
//     !#$%&'*+-.^_`|~), and a value must not contain CR, LF, NUL or any
//     other control character except the horizontal tab.
//
// Every error is an *errs.Error, of kind InvalidArgument or the kind of
// expand's error, with the DetailRow, DetailField and DetailHeader details.
// Values never appear in errors: they may be secrets. The result has
// Enabled set on every header and is empty, not nil, when no row is sent.
func Headers(rows []projectfile.KeyValue, expand Expand) ([]httpclient.Header, error) {
	out := make([]httpclient.Header, 0, len(rows))
	for i, row := range rows {
		if row.Disabled || row.Name == "" {
			continue
		}
		name, value := row.Name, row.Value
		if expand != nil {
			var err error
			if name, err = expand(row.Name); err != nil {
				return nil, expandError(err, i, "name", row.Name)
			}
			if value, err = expand(row.Value); err != nil {
				return nil, expandError(err, i, "value", row.Name)
			}
		}
		if err := checkName(i, row.Name, name); err != nil {
			return nil, err
		}
		if err := checkValue(i, row.Name, value); err != nil {
			return nil, err
		}
		out = append(out, httpclient.Header{Name: name, Value: value, Enabled: true})
	}
	return out, nil
}

func withRow(e *errs.Error, row int, field, header string) *errs.Error {
	return e.WithDetail(DetailRow, strconv.Itoa(row)).
		WithDetail(DetailField, field).
		WithDetail(DetailHeader, header)
}

// expandError keeps an *errs.Error from expand (its kind, code and hint)
// and adds the row; any other error is wrapped as InvalidArgument.
func expandError(err error, row int, field, header string) error {
	e, ok := err.(*errs.Error)
	if !ok {
		e = errs.Wrap(err, errs.InvalidArgument, "The "+field+" of header "+strconv.Itoa(row+1)+" could not be rendered.").
			WithHint("Check the variables used in the header.")
	}
	return withRow(e, row, field, header)
}

func checkName(row int, typed, name string) error {
	if isToken(name) {
		return nil
	}
	msg := "Header " + strconv.Itoa(row+1) + " has an invalid name " + strconv.Quote(name) + "."
	hint := "Header names may contain only letters, digits and !#$%&'*+-.^_`|~, with no spaces."
	if name == "" {
		msg = "The name of header " + strconv.Itoa(row+1) + " is empty once its variables are filled in."
		hint = "Check the variables used in the header name."
	}
	e := errs.New(errs.InvalidArgument, msg).WithCode(CodeInvalidHeaderName).WithHint(hint)
	return withRow(e, row, "name", typed)
}

func checkValue(row int, typed, value string) error {
	for j := 0; j < len(value); j++ {
		c := value[j]
		if (c >= ' ' || c == '\t') && c != 0x7f {
			continue
		}
		what := "a control character"
		switch c {
		case '\r', '\n':
			what = "a line break"
		case 0:
			what = "a NUL character"
		}
		e := errs.New(errs.InvalidArgument, "The value of header "+strconv.Itoa(row+1)+" ("+strconv.Quote(typed)+") contains "+what+".").
			WithCode(CodeInvalidHeaderValue).
			WithHint("Remove line breaks and control characters from the value, or from the variables it uses.")
		return withRow(e, row, "value", typed)
	}
	return nil
}

// isToken reports whether s is a non-empty RFC 9110 token.
func isToken(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9':
		case strings.IndexByte("!#$%&'*+-.^_`|~", c) >= 0:
		default:
			return false
		}
	}
	return true
}

// DetailFolder is the ID of the folder whose header row an error is about
// (InheritHeaders); absent for the request's own rows.
const DetailFolder = "folder"

// FolderHeaders are the header rows of one folder above a request, from
// its _folder.json (COL-03).
type FolderHeaders struct {
	// FolderID and FolderName identify the folder, for errors.
	FolderID   string
	FolderName string
	Rows       []projectfile.KeyValue
}

// InheritHeaders adds the headers of a request's folders to its own
// headers own (from Headers) (COL-03). folders run from the top-level
// folder down to the request's own folder, as the variable scopes do.
//
// Each folder's rows are mapped by Headers, with the same rules and
// errors plus DetailFolder (the row index is the folder's). A folder
// header is left out when a nearer level sends a header of the same name,
// compared ignoring case: the request's own enabled rows win over every
// folder, and a folder wins over the folders above it. Duplicates within
// one level are kept, as typed. Disabled and unnamed rows do not
// override anything.
//
// The result lists the kept folder headers first, the top-level folder's
// first, then own. It is empty, not nil, when nothing is sent.
func InheritHeaders(own []httpclient.Header, folders []FolderHeaders, expand Expand) ([]httpclient.Header, error) {
	if len(folders) == 0 {
		return append(make([]httpclient.Header, 0, len(own)), own...), nil
	}
	taken := make(map[string]bool, len(own))
	for _, h := range own {
		taken[strings.ToLower(h.Name)] = true
	}
	levels := make([][]httpclient.Header, len(folders))
	for i := len(folders) - 1; i >= 0; i-- {
		f := folders[i]
		hs, err := Headers(f.Rows, expand)
		if err != nil {
			return nil, folderHeaderError(err, f)
		}
		kept := hs[:0]
		for _, h := range hs {
			if !taken[strings.ToLower(h.Name)] {
				kept = append(kept, h)
			}
		}
		for _, h := range kept {
			taken[strings.ToLower(h.Name)] = true
		}
		levels[i] = kept
	}
	var out []httpclient.Header
	for _, l := range levels {
		out = append(out, l...)
	}
	out = append(out, own...)
	if out == nil {
		out = make([]httpclient.Header, 0)
	}
	return out, nil
}

// folderHeaderError names folder f in an error of Headers.
func folderHeaderError(err error, f FolderHeaders) error {
	e, ok := err.(*errs.Error)
	if !ok {
		return err
	}
	e = e.WithDetail(DetailFolder, f.FolderID)
	name := f.FolderName
	if name == "" {
		name = f.FolderID
	}
	e.Message = "In the headers of folder " + strconv.Quote(name) + ": " + e.Message
	return e
}
