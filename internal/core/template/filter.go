package template

import (
	"crypto/hmac"
	"crypto/md5" // for APIs that ask for it, not for security
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"hash"
	"net/url"
	"slices"
	"strings"
)

// Filters (DYN-04): a reference can pass its value through encoding and
// hashing functions, left to right:
//
//	{{ password | base64 }}
//	{{ body | hmacSha256 signingKey base64 }}
//	{{ "user:pass" | base64 }}
//	{{ $uuid | sha256 }}
//
// The part before the first "|" is the input: a variable name (with dot
// notation, or a dynamic value), resolved like any reference, or a text in
// double quotes. Each part after a "|" is a filter name and its arguments,
// separated by whitespace; a "|" inside double quotes is not a separator.
// A pipe is only read when no scope defines the whole name and it is not a
// valid dynamic value, so every template that rendered before renders the
// same.
//
// The result is secret when the input or an HMAC key is: a hash of a
// secret counts as secret.

// filterFunc applies one filter to in. keys are the resolved key
// arguments (HMAC); args are the other arguments.
type filterFunc func(in string, keys []string, args []string) (string, error)

type filterSpec struct {
	fn filterFunc
	// keys is the number of leading arguments that are keys: a variable
	// name, or a text in double quotes.
	keys int
	// maxArgs bounds the arguments after the keys.
	maxArgs int
	desc    string
}

var filters = map[string]filterSpec{
	"base64":       {fn: noArgFilter(func(s string) (string, error) { return base64.StdEncoding.EncodeToString([]byte(s)), nil }), desc: "Base64 (standard alphabet, padded)"},
	"base64url":    {fn: noArgFilter(func(s string) (string, error) { return base64.RawURLEncoding.EncodeToString([]byte(s)), nil }), desc: "Base64 with the URL alphabet, unpadded (as in JWTs)"},
	"base64Decode": {fn: noArgFilter(base64Decode), desc: "Decodes Base64, standard or URL alphabet, padded or not"},
	"urlEncode":    {fn: noArgFilter(func(s string) (string, error) { return urlEncode(s), nil }), desc: "Percent-encodes everything but letters, digits and - . _ ~"},
	"urlDecode":    {fn: noArgFilter(urlDecode), desc: "Decodes %XX sequences (a + stays a +)"},
	"hex":          {fn: noArgFilter(func(s string) (string, error) { return hex.EncodeToString([]byte(s)), nil }), desc: "Lowercase hex of the bytes"},
	"hexDecode":    {fn: noArgFilter(hexDecode), desc: "Decodes hex digits to bytes"},
	"md5":          {fn: hashFilter(md5.New), maxArgs: 1, desc: "MD5 digest, hex (or base64, base64url)"},
	"sha1":         {fn: hashFilter(sha1.New), maxArgs: 1, desc: "SHA-1 digest, hex (or base64, base64url)"},
	"sha256":       {fn: hashFilter(sha256.New), maxArgs: 1, desc: "SHA-256 digest, hex (or base64, base64url)"},
	"sha512":       {fn: hashFilter(sha512.New), maxArgs: 1, desc: "SHA-512 digest, hex (or base64, base64url)"},
	"hmacMd5":      {fn: hmacFilter(md5.New), keys: 1, maxArgs: 1, desc: "HMAC-MD5 with a key, hex (or base64, base64url)"},
	"hmacSha1":     {fn: hmacFilter(sha1.New), keys: 1, maxArgs: 1, desc: "HMAC-SHA1 with a key, hex (or base64, base64url)"},
	"hmacSha256":   {fn: hmacFilter(sha256.New), keys: 1, maxArgs: 1, desc: "HMAC-SHA256 with a key, hex (or base64, base64url)"},
	"hmacSha512":   {fn: hmacFilter(sha512.New), keys: 1, maxArgs: 1, desc: "HMAC-SHA512 with a key, hex (or base64, base64url)"},
}

// FilterNames returns the names of the filters, sorted, for completion
// lists.
func FilterNames() []string {
	names := make([]string, 0, len(filters))
	for n := range filters {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// FilterDescription says in a few words what the filter does; "" for an
// unknown name.
func FilterDescription(name string) string { return filters[name].desc }

func noArgFilter(fn func(string) (string, error)) filterFunc {
	return func(in string, _, _ []string) (string, error) { return fn(in) }
}

func base64Decode(s string) (string, error) {
	s = strings.TrimSpace(s)
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return string(b), nil
		}
	}
	return "", errors.New("the value is not valid Base64")
}

// urlEncode escapes every byte but the RFC 3986 unreserved characters, so
// the result fits anywhere in a URL (like encodeURIComponent, but also
// escaping ! ' ( ) *).
func urlEncode(s string) string {
	const upper = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'a' <= c && c <= 'z' || 'A' <= c && c <= 'Z' || '0' <= c && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(upper[c>>4])
		b.WriteByte(upper[c&15])
	}
	return b.String()
}

func urlDecode(s string) (string, error) {
	v, err := url.PathUnescape(s)
	if err != nil {
		return "", errors.New("the value has a % not followed by two hex digits")
	}
	return v, nil
}

func hexDecode(s string) (string, error) {
	b, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil {
		return "", errors.New("the value is not an even number of hex digits")
	}
	return string(b), nil
}

// digestEncoding encodes a digest as its optional argument says.
func digestEncoding(sum []byte, args []string) (string, error) {
	enc := "hex"
	if len(args) > 0 {
		enc = args[0]
	}
	switch enc {
	case "hex":
		return hex.EncodeToString(sum), nil
	case "base64":
		return base64.StdEncoding.EncodeToString(sum), nil
	case "base64url":
		return base64.RawURLEncoding.EncodeToString(sum), nil
	}
	return "", fmt.Errorf("the output encoding %q is not hex, base64 or base64url", enc)
}

func hashFilter(h func() hash.Hash) filterFunc {
	return func(in string, _, args []string) (string, error) {
		d := h()
		d.Write([]byte(in))
		return digestEncoding(d.Sum(nil), args)
	}
}

func hmacFilter(h func() hash.Hash) filterFunc {
	return func(in string, keys, args []string) (string, error) {
		m := hmac.New(h, []byte(keys[0]))
		m.Write([]byte(in))
		return digestEncoding(m.Sum(nil), args)
	}
}

// token is one argument of a pipe: a bare word or a text in double quotes
// (without them).
type token struct {
	text   string
	quoted bool
}

// splitTokens splits s on whitespace; a double-quoted token may contain
// spaces and "|". An unclosed quote, or a quote followed by other text, is
// not valid.
func splitTokens(s string) ([]token, bool) {
	var out []token
	i := 0
	for {
		for i < len(s) && strings.IndexByte(spaces, s[i]) >= 0 {
			i++
		}
		if i == len(s) {
			return out, true
		}
		if s[i] == '"' {
			end := strings.IndexByte(s[i+1:], '"')
			if end < 0 {
				return nil, false
			}
			out = append(out, token{text: s[i+1 : i+1+end], quoted: true})
			i += end + 2
			if i < len(s) && strings.IndexByte(spaces, s[i]) < 0 {
				return nil, false
			}
			continue
		}
		j := i
		for j < len(s) && strings.IndexByte(spaces, s[j]) < 0 {
			j++
		}
		out = append(out, token{text: s[i:j]})
		i = j
	}
}

// splitPipe splits a reference name on the "|" outside double quotes. ok
// is false when there is no such "|" (or a quote is not closed): the name
// is not a pipe. Stages are trimmed of whitespace.
func splitPipe(name string) (stages []string, ok bool) {
	inQuote, start := false, 0
	for i := 0; i < len(name); i++ {
		switch name[i] {
		case '"':
			inQuote = !inQuote
		case '|':
			if !inQuote {
				stages = append(stages, strings.Trim(name[start:i], spaces))
				start = i + 1
			}
		}
	}
	if inQuote || len(stages) == 0 {
		return nil, false
	}
	return append(stages, strings.Trim(name[start:], spaces)), true
}

// pipeStep is one parsed filter of a pipe.
type pipeStep struct {
	name string
	spec filterSpec
	keys []token
	args []string
}

// pipe is a parsed pipe: its input and filters.
type pipe struct {
	input token
	steps []pipeStep
}

// parsePipe checks the stages of a pipe: the input, then known filters
// with valid arguments.
func parsePipe(stages []string) (pipe, error) {
	var p pipe
	head := stages[0]
	switch {
	case head == "":
		return p, errors.New("a variable or a quoted text must come before the first |")
	case strings.HasPrefix(head, `"`):
		toks, ok := splitTokens(head)
		if !ok || len(toks) != 1 {
			return p, errors.New("the quoted text before the first | is not closed or has text after it")
		}
		p.input = toks[0]
	default:
		p.input = token{text: head}
	}
	for _, st := range stages[1:] {
		toks, ok := splitTokens(st)
		if !ok {
			return p, fmt.Errorf("the filter %q has a quote that is not closed or text right after a quote", st)
		}
		if len(toks) == 0 {
			return p, errors.New("a filter name is missing after a |")
		}
		name := toks[0].text
		spec, known := filters[name]
		if toks[0].quoted || !known {
			return p, fmt.Errorf("%q is not a filter", name)
		}
		args := toks[1:]
		if len(args) < spec.keys {
			return p, fmt.Errorf("%s needs a key: a variable name or a quoted text", name)
		}
		step := pipeStep{name: name, spec: spec, keys: args[:spec.keys]}
		for _, a := range args[spec.keys:] {
			step.args = append(step.args, a.text)
		}
		if len(step.args) > spec.maxArgs {
			switch {
			case spec.maxArgs == 0:
				return p, fmt.Errorf("%s takes no arguments", name)
			case spec.keys > 0:
				return p, fmt.Errorf("%s takes a key and at most an output encoding", name)
			default:
				return p, fmt.Errorf("%s takes at most an output encoding", name)
			}
		}
		if spec.maxArgs > 0 {
			// Check the output encoding now, before anything resolves.
			if _, err := digestEncoding(nil, step.args); err != nil {
				return p, fmt.Errorf("%s: %w", name, err)
			}
		}
		p.steps = append(p.steps, step)
	}
	return p, nil
}

// IsPipe reports whether name, as written between the braces, is a filter
// pipe ({{token | base64}}) with known filters and valid arguments. The
// input and keys may still not resolve. Like IsDynamic, a scope that
// defines the whole name wins.
func IsPipe(name string) bool {
	if IsDynamic(name) {
		return false
	}
	stages, ok := splitPipe(name)
	if !ok {
		return false
	}
	_, err := parsePipe(stages)
	return err == nil
}

// resolvePipe resolves a pipe reference: its input and keys are resolved
// as references of their own (with their nesting, cycle and limit checks),
// then each filter applies in turn. Ref.Scope is the input's (DynamicScope
// for a quoted text) and Ref.Nested holds the input and key references.
func (st *renderer) resolvePipe(seg Segment, ref Ref, stages []string) Ref {
	p, err := parsePipe(stages)
	if err != nil {
		st.reportMessage(ProblemInvalidFilter, seg.Name, err.Error(), seg.Start, seg.End)
		return ref
	}
	// A {{$file}} input may be binary: the filters make text of it.
	st.pipeInput = !p.input.quoted
	value, ok := st.pipeOperand(seg, &ref, p.input)
	if !ok {
		return ref
	}
	if ref.Scope == "" {
		ref.Scope = DynamicScope
	}
	for _, step := range p.steps {
		keys := make([]string, len(step.keys))
		for i, k := range step.keys {
			scope := ref.Scope
			if keys[i], ok = st.pipeOperand(seg, &ref, k); !ok {
				return ref
			}
			ref.Scope = scope
		}
		value, err = step.spec.fn(value, keys, step.args)
		if err != nil {
			st.reportMessage(ProblemInvalidFilter, seg.Name, step.name+": "+err.Error(), seg.Start, seg.End)
			return ref
		}
	}
	ref.Resolved, ref.Value = true, value
	return ref
}

// pipeOperand is the value of a pipe's input or key: a quoted text as is,
// or a reference resolved like any other. ok is false when it does not
// resolve (the problem is reported). The resolved reference is added to
// ref.Nested, and its secrecy and scope to ref's.
func (st *renderer) pipeOperand(seg Segment, ref *Ref, tok token) (string, bool) {
	if tok.quoted {
		return tok.text, true
	}
	sub := seg
	sub.Name = tok.text
	r := st.resolve(sub)
	ref.Nested = append(ref.Nested, r)
	if !r.Resolved {
		return "", false
	}
	ref.Secret = ref.Secret || r.Secret
	ref.Scope = r.Scope
	return r.Value, true
}
