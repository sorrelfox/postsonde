package request

import (
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// Path parameters (HTTP-05): the :name and {name} placeholders in the path
// of a URL, and their values from the request's pathParams rows. The
// frontend mirrors PathParams (frontend/src/lib/pathParams.ts); both are
// tested against testdata/path_params.json. docs/http-client.md lists the
// rules.

// CodePathParamMissing is the errs code of the error SubstitutePath returns
// when a placeholder has no value.
const CodePathParamMissing = "request.path_param_missing"

// Details of the errors SubstitutePath returns.
const (
	// DetailPathParams lists the names without a value, comma-separated,
	// in URL order.
	DetailPathParams = "path_params"
	// DetailPathParam is the name whose value could not be rendered.
	DetailPathParam = "path_param"
)

// PathParam is one placeholder in a URL.
type PathParam struct {
	// Name is the name without its ":" or braces.
	Name string
	// Start and End are byte offsets: url[Start:End] is the placeholder
	// as typed (":id" or "{id}").
	Start, End int
}

// PathParams returns the placeholders in the path of rawURL, the URL as
// typed (with its {{variables}} unrendered), in order, duplicates included.
//
//   - Only the path is searched: it starts at the first "/" after the
//     authority ("scheme://host:port", or the text before the first "/"
//     when there is no "://") and ends at the first "?" or "#". So a port
//     (":8080"), an IPv6 literal ("[::1]"), userinfo, the query and the
//     fragment never hold placeholders.
//   - {{variables}} are skipped, and a "?", "#" or "/" inside one does not
//     split the URL.
//   - ":name" starts right after a "/" and takes the letters, digits and
//     underscores that follow (at least one): "/users/:id.json" has "id",
//     "/a:b", "/::" and "/:" have none.
//   - "{name}" is a single pair of braces around letters, digits, "_", "-"
//     and "."; it may sit inside a segment ("report-{year}.csv"). "{{",
//     "}}", "{}" and "{ id }" are not placeholders.
func PathParams(rawURL string) []PathParam {
	// varEnd[i] is the end of the {{variable}} holding byte i, or 0.
	varEnd := make([]int, len(rawURL))
	for _, span := range varSpans(rawURL) {
		for i := span[0]; i < span[1]; i++ {
			varEnd[i] = span[1]
		}
	}

	end := len(rawURL)
	scheme := -1
	for i := 0; i < len(rawURL); i++ {
		if varEnd[i] > 0 {
			i = varEnd[i] - 1
			continue
		}
		if c := rawURL[i]; c == '?' || c == '#' {
			end = i
			break
		}
		if scheme < 0 && strings.HasPrefix(rawURL[i:], "://") {
			scheme = i
		}
	}

	start := -1
	from := 0
	if scheme >= 0 {
		from = scheme + 3
	}
	for i := from; i < end; i++ {
		if varEnd[i] > 0 {
			i = varEnd[i] - 1
			continue
		}
		if rawURL[i] == '/' {
			start = i
			break
		}
	}
	if start < 0 {
		return nil
	}

	var out []PathParam
	for i := start; i < end; i++ {
		if varEnd[i] > 0 {
			i = varEnd[i] - 1
			continue
		}
		switch rawURL[i] {
		case ':':
			if rawURL[i-1] != '/' {
				continue
			}
			j := i + 1
			for j < end && isIdentByte(rawURL[j]) {
				j++
			}
			if j > i+1 {
				out = append(out, PathParam{Name: rawURL[i+1 : j], Start: i, End: j})
				i = j - 1
			}
		case '{':
			if rawURL[i-1] == '{' || i+1 >= end || rawURL[i+1] == '{' {
				continue
			}
			j := i + 1
			for j < end && isBraceNameByte(rawURL[j]) {
				j++
			}
			if j == i+1 || j >= end || rawURL[j] != '}' || (j+1 < len(rawURL) && rawURL[j+1] == '}') {
				continue
			}
			out = append(out, PathParam{Name: rawURL[i+1 : j], Start: i, End: j + 1})
			i = j
		}
	}
	return out
}

// PathParamNames returns the distinct names of PathParams(rawURL), in the
// order of their first use: the rows of the editor's path table.
func PathParamNames(rawURL string) []string {
	var names []string
	seen := map[string]bool{}
	for _, p := range PathParams(rawURL) {
		if !seen[p.Name] {
			seen[p.Name] = true
			names = append(names, p.Name)
		}
	}
	return names
}

// SubstitutePath replaces the placeholders of rawURL, the URL as typed,
// with the values of rows (the request's pathParams), before the URL's own
// {{variables}} are rendered:
//
//   - a placeholder takes the value of the first row with its name (names
//     are case-sensitive); every use of a name gets the same value. The
//     rows' disabled flag is ignored: the editor has no toggle, since each
//     placeholder needs a value. Rows whose name is not in the URL are
//     ignored.
//   - expand, when not nil, renders the value's {{variables}}; its error is
//     returned with DetailPathParam (and DetailField "value") added.
//   - the value is plain text, percent-encoded as one path segment: every
//     byte but the RFC 3986 pchar characters (letters, digits,
//     -._~!$&'()*+,;=:@) becomes %XX with upper-case hex, including "/",
//     "?", "#", "%", space, braces and non-ASCII. Text that is already
//     percent-encoded is encoded again ("a%2Fb" is sent as "a%252Fb"), as
//     for form fields: the value is never shown inside the URL.
//   - a name without a row, or whose value is empty (after expansion), keeps
//     its placeholder, and an InvalidArgument error with
//     CodePathParamMissing and DetailPathParams is returned along with that
//     URL, so a caller can still show it. Sending must stop.
func SubstitutePath(rawURL string, rows []projectfile.KeyValue, expand Expand) (string, error) {
	params := PathParams(rawURL)
	if len(params) == 0 {
		return rawURL, nil
	}
	values := map[string]string{}
	var missing []string
	for _, name := range PathParamNames(rawURL) {
		value := ""
		for _, row := range rows {
			if row.Name == name {
				value = row.Value
				break
			}
		}
		if value != "" && expand != nil {
			rendered, err := expand(value)
			if err != nil {
				return "", pathExpandError(err, name)
			}
			value = rendered
		}
		if value == "" {
			missing = append(missing, name)
			continue
		}
		values[name] = encodePathSegment(value)
	}

	var b strings.Builder
	last := 0
	for _, p := range params {
		value, ok := values[p.Name]
		if !ok {
			continue
		}
		b.WriteString(rawURL[last:p.Start])
		b.WriteString(value)
		last = p.End
	}
	b.WriteString(rawURL[last:])
	out := b.String()

	if len(missing) > 0 {
		return out, missingPathParams(missing)
	}
	return out, nil
}

func missingPathParams(names []string) error {
	msg := "The path variable " + names[0] + " has no value."
	if len(names) > 1 {
		msg = "The path variables " + strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1] + " have no value."
	}
	return errs.New(errs.InvalidArgument, msg).
		WithCode(CodePathParamMissing).
		WithHint("Enter the values in the Path variables table of the Params tab.").
		WithDetail(DetailPathParams, strings.Join(names, ","))
}

func pathExpandError(err error, name string) error {
	e, ok := err.(*errs.Error)
	if !ok {
		e = errs.Wrap(err, errs.InvalidArgument, "The value of the path variable "+name+" could not be rendered.").
			WithHint("Check the variables used in the value.")
	}
	return e.WithDetail(DetailPathParam, name).WithDetail(DetailField, "value")
}

// varSpans returns the [start, end) byte ranges of the {{variables}} in s,
// as the template parser reads them.
func varSpans(s string) [][2]int {
	if !strings.Contains(s, "{{") {
		return nil
	}
	var spans [][2]int
	for _, seg := range template.Parse(s) {
		if seg.Kind == template.Var {
			spans = append(spans, [2]int{seg.Start, seg.End})
		}
	}
	return spans
}

func isIdentByte(c byte) bool {
	return 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '_'
}

func isBraceNameByte(c byte) bool {
	return isIdentByte(c) || c == '-' || c == '.'
}

// encodePathSegment percent-encodes s as one path segment: all but the
// RFC 3986 pchar characters, with upper-case hex digits.
func encodePathSegment(s string) string {
	const hex = "0123456789ABCDEF"
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		c := s[i]
		if isIdentByte(c) || strings.IndexByte("-.~!$&'()*+,;=:@", c) >= 0 {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hex[c>>4])
		b.WriteByte(hex[c&15])
	}
	return b.String()
}
