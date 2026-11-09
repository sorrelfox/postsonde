package graphqlmock

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"hash/fnv"
	"slices"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/graphql/schema"
)

// Generated data is a function of the configuration's seed and of where a
// value is in the response: each value's seed mixes its parent's seed with
// the field's name and arguments (or a list item's index), so the same
// query gets the same response on every request and after a restart, two
// fields with different arguments get different data, and an alias does
// not change anything. Values look like what their field holds where the
// name says so (email, url, name, createdAt, …).

// mix hashes seed and parts (FNV-1a, then the SplitMix64 finalizer).
func mix(seed uint64, parts ...string) uint64 {
	h := fnv.New64a()
	var b [8]byte
	binary.LittleEndian.PutUint64(b[:], seed)
	_, _ = h.Write(b[:])
	for _, p := range parts {
		_, _ = h.Write([]byte(p))
		_, _ = h.Write([]byte{0})
	}
	x := h.Sum64()
	x ^= x >> 30
	x *= 0xbf58476d1ce4e5b9
	x ^= x >> 27
	x *= 0x94d049bb133111eb
	x ^= x >> 31
	return x
}

// rootSeed is the seed of an operation's root value.
func rootSeed(seed int64, op string) uint64 {
	return mix(uint64(seed), "root", op)
}

// isNull says whether a nullable value at seed is generated as null.
func isNull(seed uint64, percent int) bool {
	return percent > 0 && int(mix(seed, "null")%100) < percent
}

var (
	firstNames = []string{"Ada", "Alan", "Grace", "Linus", "Margaret", "Dennis", "Barbara", "Ken", "Frances", "Edsger", "Radia", "Tim"}
	lastNames  = []string{"Lovelace", "Turing", "Hopper", "Torvalds", "Hamilton", "Ritchie", "Liskov", "Thompson", "Allen", "Dijkstra", "Perlman", "Berners-Lee"}
	words      = []string{"alpha", "bravo", "charlie", "delta", "echo", "foxtrot", "golf", "hotel", "india", "juliet", "kilo", "lima", "mike", "november", "oscar", "papa"}
	sentences  = []string{
		"A short example text made by the PostSonde mock.",
		"Generated from the schema, the same on every request.",
		"Replace it with an override to answer something specific.",
		"Every field gets a value of its type.",
	}
)

func pick[T any](list []T, h uint64) T { return list[h%uint64(len(list))] }

// generateLeaf makes a value of the scalar or enum t for the field named
// field ("email"), from seed.
func generateLeaf(t *schema.Type, field string, seed uint64) any {
	if t.Kind == schema.KindEnum {
		values := make([]*schema.EnumValue, 0, len(t.EnumValues))
		for _, v := range t.EnumValues {
			if !v.IsDeprecated {
				values = append(values, v)
			}
		}
		if len(values) == 0 {
			values = t.EnumValues
		}
		if len(values) == 0 {
			return nil
		}
		return pick(values, mix(seed, "enum")).Name
	}
	h := mix(seed, "scalar")
	n := h % 1000
	switch t.Name {
	case "Int":
		return json.Number(strconv.FormatUint(n, 10))
	case "Float":
		return json.Number(strconv.FormatFloat(float64(h%100000)/100, 'f', -1, 64))
	case "Boolean":
		return h&1 == 1
	case "ID":
		return strconv.FormatUint(h%100000+1, 10)
	case "String":
		return generateString(field, h)
	}
	return generateCustom(t.Name, field, h)
}

// generateString is a string that fits the field's name, read as words
// ("createdAt" is created and at, "avatar_url" avatar and url).
func generateString(field string, h uint64) string {
	w := nameWords(field)
	has := func(names ...string) bool {
		for _, x := range w {
			if slices.Contains(names, x) {
				return true
			}
		}
		return false
	}
	last := ""
	if len(w) > 0 {
		last = w[len(w)-1]
	}
	n := h % 1000
	switch {
	case has("email", "mail"):
		return fmt.Sprintf("%s.%s@example.com", strings.ToLower(pick(firstNames, h)), strings.ToLower(pick(lastNames, h>>8)))
	case has("url", "uri", "link", "href", "website", "homepage"):
		return fmt.Sprintf("https://example.com/%s/%d", strings.ToLower(field), n)
	case has("phone", "mobile", "tel"):
		return fmt.Sprintf("+1-555-01%02d", n%100)
	case has("color", "colour"):
		return fmt.Sprintf("#%06x", h&0xffffff)
	case has("first", "given") && last == "name":
		return pick(firstNames, h)
	case has("last", "family") && last == "name" || last == "surname":
		return pick(lastNames, h)
	case has("username", "login", "handle", "slug", "nickname"):
		return fmt.Sprintf("%s%d", strings.ToLower(pick(firstNames, h)), n)
	case last == "name" && (len(w) == 1 || has("full", "display", "author", "user", "person", "contact")):
		return pick(firstNames, h) + " " + pick(lastNames, h>>8)
	case last == "name" || has("title", "label"):
		return capitalize(pick(words, h)) + " " + pick(words, h>>8)
	case last == "at" || has("date", "time", "timestamp", "birthday"):
		return dateTime(h)
	case has("description", "text", "body", "content", "bio", "summary", "message", "comment", "note", "notes"):
		return pick(sentences, h)
	case field == "":
		return pick(words, h) + " " + strconv.FormatUint(n, 10)
	}
	return field + " " + strconv.FormatUint(n, 10)
}

// nameWords splits a camelCase or snake_case name into lower-case words.
func nameWords(s string) []string {
	var out []string
	var cur []byte
	flush := func() {
		if len(cur) > 0 {
			out = append(out, strings.ToLower(string(cur)))
			cur = cur[:0]
		}
	}
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '_' || c == '-':
			flush()
		case 'A' <= c && c <= 'Z':
			// A capital starts a word, except inside a run of capitals
			// ("URL" in "avatarURL") that a lower case does not follow.
			prevUpper := i > 0 && 'A' <= s[i-1] && s[i-1] <= 'Z'
			nextLower := i+1 < len(s) && 'a' <= s[i+1] && s[i+1] <= 'z'
			if !prevUpper || nextLower {
				flush()
			}
			cur = append(cur, c)
		default:
			cur = append(cur, c)
		}
	}
	flush()
	return out
}

func capitalize(s string) string {
	if s == "" || s[0] < 'a' || s[0] > 'z' {
		return s
	}
	return string(s[0]-'a'+'A') + s[1:]
}

// generateCustom is a value of a custom scalar that fits its name
// (DateTime, UUID, URL, JSON, …): a string unless the name says it is a
// number, a Boolean or an object.
func generateCustom(scalar, field string, h uint64) any {
	w := nameWords(scalar)
	has := func(names ...string) bool {
		for _, x := range w {
			if slices.Contains(names, x) {
				return true
			}
		}
		return false
	}
	switch {
	case has("datetime", "timestamp", "instant", "timestamptz") || has("date") && has("time"):
		return dateTime(h)
	case has("date", "localdate"):
		return dateTime(h)[:10]
	case has("time", "localtime"):
		return dateTime(h)[11:19]
	case has("uuid", "guid"):
		g := mix(h, "uuid")
		return fmt.Sprintf("%08x-%04x-4%03x-%04x-%012x", uint32(h>>32), uint16(h>>16), uint16(h)&0xfff, uint16(g>>48)&0x3fff|0x8000, g&0xffffffffffff)
	case has("url", "uri"):
		return fmt.Sprintf("https://example.com/%s/%d", strings.ToLower(field), h%1000)
	case has("email"):
		return generateString("email", h)
	case has("json", "object", "map"):
		return map[string]any{"key": pick(words, h), "value": json.Number(strconv.FormatUint(h%1000, 10))}
	case has("long", "bigint", "int64", "int", "integer", "uint", "unsigned"):
		return json.Number(strconv.FormatUint(h%1_000_000, 10))
	case has("decimal", "bigdecimal", "float", "double", "money", "number", "currency"):
		return json.Number(strconv.FormatFloat(float64(h%100000)/100, 'f', -1, 64))
	case has("bool", "boolean"):
		return h&1 == 1
	}
	return scalar + " " + strconv.FormatUint(h%1000, 10)
}

// dateTime is a UTC timestamp in RFC 3339, in 2020 to 2029.
func dateTime(h uint64) string {
	year := 2020 + h%10
	month := 1 + (h>>8)%12
	day := 1 + (h>>16)%28
	hour := (h >> 24) % 24
	minute := (h >> 32) % 60
	second := (h >> 40) % 60
	return fmt.Sprintf("%04d-%02d-%02dT%02d:%02d:%02dZ", year, month, day, hour, minute, second)
}
