package postman

import (
	"slices"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/template"
	"github.com/sorrelfox/postsonde/internal/core/yaml"
)

// Postman texts use PostSonde's {{name}} syntax already. Only the dynamic
// variables ({{$name}}) differ: those PostSonde has under the same name
// stay, a few become another spelling, the others are left as written and
// reported.

// sameDynamic are the Postman dynamic variables PostSonde has under the
// same name and with the same meaning: the UUIDs, timestamps and random
// values, and the fake data PostSonde generates from its built-in word
// lists (docs/templates.md). Each is a template.IsDynamic name.
var sameDynamic = map[string]bool{
	"$guid": true, "$randomUUID": true, "$timestamp": true, "$isoTimestamp": true,
	"$randomInt": true, "$randomBoolean": true,
	// Names.
	"$randomFirstName": true, "$randomLastName": true, "$randomFullName": true,
	"$randomNamePrefix": true, "$randomNameSuffix": true, "$randomUserName": true,
	"$randomJobTitle": true,
	// Internet.
	"$randomEmail": true, "$randomExampleEmail": true, "$randomDomainWord": true,
	"$randomDomainSuffix": true, "$randomDomainName": true, "$randomUrl": true,
	"$randomProtocol": true, "$randomIP": true, "$randomIPV6": true,
	"$randomMACAddress": true, "$randomPassword": true, "$randomSemver": true,
	// Places and phone.
	"$randomCity": true, "$randomStreetName": true, "$randomStreetAddress": true,
	"$randomCountry": true, "$randomCountryCode": true, "$randomLatitude": true,
	"$randomLongitude": true, "$randomPhoneNumber": true, "$randomPhoneNumberExt": true,
	// Business, colours and dates.
	"$randomCompanyName": true, "$randomCompanySuffix": true, "$randomPrice": true,
	"$randomCurrencyCode": true, "$randomLocale": true, "$randomAbbreviation": true,
	"$randomColor": true, "$randomHexColor": true, "$randomWeekday": true,
	"$randomMonth": true, "$randomDatePast": true, "$randomDateFuture": true,
	"$randomDateRecent": true,
	// Words and lorem ipsum.
	"$randomWord": true, "$randomWords": true, "$randomLoremWord": true,
	"$randomLoremWords": true, "$randomLoremSlug": true, "$randomLoremSentence": true,
	"$randomLoremSentences": true, "$randomLoremLines": true, "$randomLoremParagraph": true,
	"$randomLoremParagraphs": true, "$randomLoremText": true,
}

// otherDynamic are the Postman dynamic variables PostSonde spells
// differently.
var otherDynamic = map[string]string{
	"$randomAlphaNumeric": "{{$randomString 1}}",
	"$randomHexadecimal":  "{{$randomHex 1}}",
}

// texts converts the texts of one item and remembers what it could not
// convert, for the item's diagnostics.
type texts struct {
	// unsupported are the dynamic variables left as written, with where
	// they are.
	unsupported map[string][]string
	// converted are the dynamic variables that became another spelling.
	converted map[string]string
	// vault are the places of {{vault:…}} references.
	vault []string
}

// conv converts text; where says where the text is ("the URL", "a
// header") for the diagnostics.
func (t *texts) conv(text, where string) string {
	if !strings.Contains(text, "{{") {
		return text
	}
	var b strings.Builder
	for _, seg := range template.Parse(text) {
		raw := text[seg.Start:seg.End]
		if seg.Kind != template.Var {
			b.WriteString(raw)
			continue
		}
		name := seg.Name
		switch {
		case strings.HasPrefix(name, "vault:"):
			t.vault = addPlace(t.vault, where)
		case !strings.HasPrefix(name, "$") || sameDynamic[name]:
		case otherDynamic[name] != "":
			raw = otherDynamic[name]
			if t.converted == nil {
				t.converted = map[string]string{}
			}
			t.converted[name] = raw
		default:
			if t.unsupported == nil {
				t.unsupported = map[string][]string{}
			}
			t.unsupported[name] = addPlace(t.unsupported[name], where)
		}
		b.WriteString(raw)
	}
	return b.String()
}

func addPlace(list []string, where string) []string {
	if slices.Contains(list, where) {
		return list
	}
	return append(list, where)
}

// diagnostics returns what t converted or could not, as diagnostics at
// loc. Messages name the variables and places, never values.
func (t *texts) diagnostics(loc importer.Location) []importer.Diagnostic {
	var out []importer.Diagnostic
	for _, name := range sortedKeys(t.unsupported) {
		out = append(out, importer.Diagnostic{
			Code: CodeDynamicUnsupported, Severity: importer.SeverityWarning, Source: loc,
			Message: "The Postman dynamic variable {{" + name + "}} in " + strings.Join(t.unsupported[name], ", ") +
				" is left as written: PostSonde has no such dynamic value. Replace it with a variable or a dynamic value such as {{$randomString}}.",
		})
	}
	if len(t.vault) > 0 {
		out = append(out, importer.Diagnostic{
			Code: CodeVault, Severity: importer.SeverityWarning, Source: loc,
			Message: "A Postman Vault reference in " + strings.Join(t.vault, ", ") +
				" is left as written: PostSonde has no vault. Use a secret variable instead.",
		})
	}
	for _, name := range sortedKeys(t.converted) {
		out = append(out, importer.Diagnostic{
			Code: CodeDynamicConverted, Severity: importer.SeverityInfo, Source: loc,
			Message: "The Postman dynamic variable {{" + name + "}} became the dynamic value " + t.converted[name] + ".",
		})
	}
	return out
}

func sortedKeys[V any](m map[string]V) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}

// description returns a Postman description: a string, or an object with
// its text in "content".
func description(n *yaml.Node) string {
	if isMap(n) {
		return strings.TrimSpace(sget(n, "content"))
	}
	return strings.TrimSpace(str(n))
}

// appendNote adds a section to a description, after a blank line.
func appendNote(desc, note string) string {
	if desc == "" {
		return note
	}
	return desc + "\n\n" + note
}

// fenced returns text in a Markdown code block of language lang, with a
// fence longer than any run of backticks in text.
func fenced(lang, text string) string {
	longest, run := 0, 0
	for i := 0; i < len(text); i++ {
		if text[i] == '`' {
			run++
			longest = max(longest, run)
		} else {
			run = 0
		}
	}
	fence := strings.Repeat("`", max(3, longest+1))
	return fence + lang + "\n" + strings.TrimRight(text, "\n") + "\n" + fence
}

// scriptNames name Postman's script events.
var scriptNames = map[string]string{"prerequest": "pre-request", "test": "test"}

// withScripts returns desc with the item's scripts (its "event" list)
// appended as code blocks, and a warning per script. what is "request",
// "folder" or "collection".
func withScripts(desc string, events *yaml.Node, what string, loc importer.Location) (string, []importer.Diagnostic) {
	var diags []importer.Diagnostic
	for _, ev := range items(events) {
		listen := sget(ev, "listen")
		script := get(ev, "script")
		var lines []string
		if exec := get(script, "exec"); exec != nil && exec.Kind == yaml.SequenceNode {
			for _, l := range exec.Items {
				lines = append(lines, str(l))
			}
		} else {
			lines = []string{str(exec)}
		}
		text := strings.Join(lines, "\n")
		if strings.TrimSpace(text) == "" {
			continue
		}
		name := scriptNames[listen]
		if name == "" {
			name = "“" + clip(listen) + "”"
		}
		state := "not run by PostSonde"
		if isTrue(get(ev, "disabled")) {
			state = "turned off in Postman; not run by PostSonde"
		}
		desc = appendNote(desc, "Postman "+name+" script ("+state+"):\n\n"+fenced("js", text))
		diags = append(diags, importer.Diagnostic{
			Code: CodeScript, Severity: importer.SeverityWarning, Source: loc,
			Message: "The " + what + "'s " + name + " script is kept in its description: PostSonde does not run Postman scripts.",
		})
	}
	return desc, diags
}

// truncate cuts s to at most n bytes, at a character boundary.
func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && n < len(s) && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}

// clip shortens a name from the file for a message.
func clip(s string) string {
	if len(s) > 40 {
		return truncate(s, 40) + "…"
	}
	return s
}

func plural(n int, one, many string) string {
	if n == 1 {
		return "1 " + one
	}
	return strconv.Itoa(n) + " " + many
}
