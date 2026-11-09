package curl

import "strings"

// form parses a -F or --form-string value the way curl does:
//
//	name=value[;type=...][;headers=...]
//	name=@file[;type=...][;filename=...][;headers=...]
//	name=<file[;type=...]
//
// A value or parameter may be quoted with "..." (\" and \\ escaped) to hold
// a ";". Unknown parameters are skipped with a warning, as curl skips them.
// --form-string takes everything after the first "=" literally.
func (p *parser) form(u use, literal bool) {
	name, rest, ok := strings.Cut(u.value, "=")
	if !ok {
		p.invalid(u, "A form field must be “name=value”; this one is ignored.")
		return
	}
	f := FormPart{Kind: FormText, Name: name, Start: u.start, End: u.end}
	if literal {
		f.Value = rest
		p.c.Form = append(p.c.Form, f)
		return
	}
	switch {
	case strings.HasPrefix(rest, "@"):
		f.Kind = FormFile
		rest = rest[1:]
	case strings.HasPrefix(rest, "<"):
		f.Kind = FormFileContent
		rest = rest[1:]
	}
	word, rest := formWord(rest)
	if f.Kind == FormText {
		f.Value = word
	} else {
		f.File = word
		sev := SeverityInfo
		if f.Kind == FormFileContent || word == "-" {
			sev = SeverityWarning
		}
		p.fileRef(u, sev, word)
	}
	for strings.HasPrefix(rest, ";") {
		rest = strings.TrimLeft(rest[1:], " \t")
		key, after, hasEq := strings.Cut(rest, "=")
		if !hasEq || strings.ContainsRune(key, ';') {
			key = ""
		}
		switch key {
		case "type", "filename", "headers", "encoder":
			var v string
			v, rest = formWord(after)
			switch key {
			case "type":
				f.ContentType = v
			case "filename":
				f.Filename = v
			case "headers":
				if strings.HasPrefix(v, "@") {
					p.fileRef(u, SeverityWarning, v[1:])
				} else {
					f.Headers = append(f.Headers, v)
				}
			case "encoder":
				p.invalid(u, "The form “encoder” parameter is not imported.")
			}
		default:
			var skipped string
			skipped, rest = formWord(rest)
			if skipped != "" {
				p.invalid(u, "curl skips the unknown form parameter "+quoteText(skipped)+"; quote the value if it contains “;”.")
			}
		}
	}
	p.c.Form = append(p.c.Form, f)
}

// formWord reads a word up to ";" or a "..." quoted word, and returns it
// and the rest starting at the ";" (or "").
func formWord(s string) (word, rest string) {
	if strings.HasPrefix(s, `"`) {
		var b strings.Builder
		for i := 1; i < len(s); i++ {
			c := s[i]
			if c == '\\' && i+1 < len(s) && (s[i+1] == '\\' || s[i+1] == '"') {
				b.WriteByte(s[i+1])
				i++
				continue
			}
			if c == '"' {
				rest = s[i+1:]
				if k := strings.IndexByte(rest, ';'); k >= 0 {
					rest = rest[k:]
				} else {
					rest = ""
				}
				return b.String(), rest
			}
			b.WriteByte(c)
		}
		// No closing quote: read it unquoted, like curl.
	}
	if k := strings.IndexByte(s, ';'); k >= 0 {
		return s[:k], s[k:]
	}
	return s, ""
}
