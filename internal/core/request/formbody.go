package request

import (
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// DetailFormField is the detail of FormURLEncoded's errors that holds the
// field's name as typed (never the value).
const DetailFormField = "form_field"

// FormURLEncoded encodes the fields of a URL-encoded form body (HTTP-13)
// as application/x-www-form-urlencoded:
//
//   - disabled rows are skipped, and so are rows whose name and value are
//     both empty as typed (a row being filled in); a row with only a value
//     is sent as "=value", as the URL params table does;
//   - the other rows keep their order and duplicates, joined with "&" as
//     name=value, with "=" even when the value is empty;
//   - rows hold plain text, not pre-encoded text: expand, when not nil,
//     renders the name and then the value, and the rendered text is
//     encoded with the WHATWG URL standard's urlencoded serializer, so a
//     typed "%20" is sent as "%2520" and a typed "a&b" as "a%26b".
//
// Errors come only from expand; they are *errs.Error with the DetailRow,
// DetailField and DetailFormField details. Values never appear in errors.
func FormURLEncoded(rows []projectfile.KeyValue, expand Expand) (string, error) {
	var sb strings.Builder
	first := true
	for i, row := range rows {
		if row.Disabled || (row.Name == "" && row.Value == "") {
			continue
		}
		name, value := row.Name, row.Value
		if expand != nil {
			var err error
			if name, err = expand(row.Name); err != nil {
				return "", formExpandError(err, i, "name", row.Name)
			}
			if value, err = expand(row.Value); err != nil {
				return "", formExpandError(err, i, "value", row.Name)
			}
		}
		if !first {
			sb.WriteByte('&')
		}
		first = false
		formEscape(&sb, name)
		sb.WriteByte('=')
		formEscape(&sb, value)
	}
	return sb.String(), nil
}

// formEscape writes s percent-encoded with the
// application/x-www-form-urlencoded percent-encode set of the WHATWG URL
// standard: ASCII letters, digits and *-._ stay, a space becomes "+", and
// every other byte of the UTF-8 text is written as %XX (upper-case hex).
func formEscape(sb *strings.Builder, s string) {
	const hex = "0123456789ABCDEF"
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case 'a' <= c && c <= 'z', 'A' <= c && c <= 'Z', '0' <= c && c <= '9',
			c == '*', c == '-', c == '.', c == '_':
			sb.WriteByte(c)
		case c == ' ':
			sb.WriteByte('+')
		default:
			sb.WriteByte('%')
			sb.WriteByte(hex[c>>4])
			sb.WriteByte(hex[c&0x0f])
		}
	}
}

// formExpandError keeps an *errs.Error from expand (its kind, code and
// hint) and adds the row; any other error is wrapped as InvalidArgument.
func formExpandError(err error, row int, field, name string) error {
	e, ok := err.(*errs.Error)
	if !ok {
		e = errs.Wrap(err, errs.InvalidArgument, "The "+field+" of form field "+strconv.Itoa(row+1)+" could not be rendered.").
			WithHint("Check the variables used in the form field.")
	}
	return e.WithDetail(DetailRow, strconv.Itoa(row)).
		WithDetail(DetailField, field).
		WithDetail(DetailFormField, name)
}
