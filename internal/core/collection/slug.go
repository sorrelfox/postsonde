package collection

import (
	"errors"
	"io/fs"
	"os"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// MaxSlugLen is the longest slug Slug returns, in bytes. With a collision
// suffix and ".json" a file name stays far below the 255-byte limit of
// every common file system, leaving room for deep folders on Windows.
const MaxSlugLen = 64

// FallbackSlug is the slug of a name that has no letters or digits Slug
// can keep ("", "???", "日本語").
const FallbackSlug = "untitled"

// maxNumberedCandidates bounds the "-2", "-3", … suffixes UniqueName tries
// before it falls back to a random suffix.
const maxNumberedCandidates = 9999

// Slug returns the base of a file or directory name for an item called
// name: lowercase ASCII letters, digits and single hyphens, never starting
// or ending with a hyphen, at most MaxSlugLen bytes.
//
//   - Letters are lowercased. Combining marks are dropped, and common
//     precomposed Latin and Cyrillic letters are transliterated ("Zoë" →
//     "zoe", "Straße" → "strasse", "Пользователи" → "polzovateli"), so a
//     name typed in NFC and the same name in NFD give the same slug.
//   - Apostrophes and the Cyrillic hard and soft signs are dropped
//     ("User's" → "users"); every other run of
//     characters that are not kept becomes one hyphen.
//   - A long slug is cut at the last hyphen within MaxSlugLen when there is
//     one, else at MaxSlugLen.
//   - A name with nothing to keep gives FallbackSlug.
//
// Because slugs are plain ASCII, they cannot start with the reserved "_"
// or ".", end with a dot or space, or differ only in Unicode normalization.
// A slug can still be a reserved device name on Windows ("con") or a
// reserved project name ("environments"), and it can collide with an
// existing entry: UniqueName takes care of both.
//
// The UI previews file names with a TypeScript port of Slug
// (frontend/src/components/tree/newItem.ts); change both together.
func Slug(name string) string {
	var b strings.Builder
	pendingHyphen := false
	for _, r := range name {
		r = unicode.ToLower(r)
		var keep string
		switch {
		case r == '\'' || r == '\u2019' || r == 'ъ' || r == 'ь' || isMark(r):
			continue
		case r < utf8.RuneSelf && (r >= 'a' && r <= 'z' || r >= '0' && r <= '9'):
			keep = string(r)
		default:
			keep = translit[r]
		}
		if keep == "" {
			pendingHyphen = b.Len() > 0
			continue
		}
		if pendingHyphen {
			b.WriteByte('-')
			pendingHyphen = false
		}
		b.WriteString(keep)
	}
	s := b.String()
	if len(s) > MaxSlugLen {
		cut := s[:MaxSlugLen]
		if i := strings.LastIndexByte(cut, '-'); s[MaxSlugLen] != '-' && i > 0 {
			cut = cut[:i]
		}
		s = strings.TrimRight(cut, "-")
	}
	if s == "" {
		return FallbackSlug
	}
	return s
}

// UniqueName returns a name for a new entry in the directory dir: slug
// followed by ext (".json" for a request, "" for a folder), or, when that
// is taken, slug with "-2", "-3", … inserted before ext. root says dir is
// the project root, where postsonde.json and environments are reserved.
//
// A candidate is taken when an entry of dir matches it ignoring case and
// Unicode normalization (see FoldName), whatever the entry is (a file, a
// folder, a non-project file), so the name is free on case-insensitive and
// normalization-insensitive file systems too (macOS, Windows). Windows
// device names (CON, PRN, AUX, NUL, COM0–9, LPT0–9, with any extension) and
// the reserved project names are always taken. A missing dir has no
// entries. slug should come from Slug; an empty slug is FallbackSlug.
func UniqueName(dir, slug, ext string, root bool) (string, error) {
	entries, err := os.ReadDir(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return "", errs.Wrap(err, errs.KindOf(err), "The folder could not be read to choose a file name.").
			WithCode(CodeReadDirFailed).
			WithHint("Check that the project folder is readable.").
			WithDetail("path", dir)
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	return uniqueName(names, slug, ext, root), nil
}

// uniqueName is UniqueName over a list of existing entry names.
func uniqueName(existing []string, slug, ext string, root bool) string {
	if slug == "" {
		slug = FallbackSlug
	}
	taken := make(map[string]bool, len(existing))
	for _, name := range existing {
		taken[FoldName(name)] = true
	}
	free := func(candidate string) bool {
		if taken[FoldName(candidate)] || IsWindowsReserved(candidate) {
			return false
		}
		return !root || !isReservedAtRoot(candidate)
	}
	if c := slug + ext; free(c) {
		return c
	}
	for n := 2; n <= maxNumberedCandidates; n++ {
		if c := slug + "-" + strconv.Itoa(n) + ext; free(c) {
			return c
		}
	}
	for {
		// A random suffix; 16 hex digits make a clash vanishingly unlikely.
		id := projectfile.NewID(projectfile.KindHTTP)
		if c := slug + "-" + id[len(projectfile.RequestIDPrefix):] + ext; free(c) {
			return c
		}
	}
}

// windowsDevices are the names Windows reserves in every directory, with
// or without an extension.
var windowsDevices = map[string]bool{
	"con": true, "prn": true, "aux": true, "nul": true,
	"conin$": true, "conout$": true,
}

// IsWindowsReserved reports whether name cannot be used for a file or
// directory on Windows because it is a device name (CON, PRN, AUX, NUL,
// COM0–COM9, LPT0–LPT9, also with superscript digits), ignoring case and
// any extension ("nul.json"), or because it ends with a dot or a space.
func IsWindowsReserved(name string) bool {
	if name == "" || strings.HasSuffix(name, ".") || strings.HasSuffix(name, " ") {
		return true
	}
	base := name
	if i := strings.IndexByte(base, '.'); i >= 0 {
		base = base[:i]
	}
	base = strings.ToLower(strings.TrimRight(base, " "))
	if windowsDevices[base] {
		return true
	}
	if len(base) >= 4 && (strings.HasPrefix(base, "com") || strings.HasPrefix(base, "lpt")) {
		switch rest := base[3:]; rest {
		case "0", "1", "2", "3", "4", "5", "6", "7", "8", "9", "¹", "²", "³":
			return true
		}
	}
	return false
}

// isReservedAtRoot reports whether name is reserved at the project root.
func isReservedAtRoot(name string) bool {
	f := FoldName(name)
	return f == FoldName(projectfile.WorkspaceFileName) || f == FoldName(projectfile.EnvironmentsDir)
}

// FoldName returns the key two entry names share when a case-insensitive,
// normalization-insensitive file system (APFS and HFS+ on macOS, NTFS)
// could treat them as the same name: letters lowercased, combining marks
// dropped, and the precomposed letters Slug transliterates replaced by
// their transliteration. Other characters are kept as they are.
//
// The standard library has no Unicode normalization, so this is
// deliberately conservative: it may call two different names the same
// ("Café" and "cafe"), which only makes UniqueName skip a name it could
// have used, but it matches NFC and NFD spellings of the Latin and
// Cyrillic letters in its table. Names PostSonde creates are ASCII, so
// they never depend on it.
func FoldName(name string) string {
	var b strings.Builder
	b.Grow(len(name))
	for _, r := range name {
		r = unicode.ToLower(r)
		if isMark(r) {
			continue
		}
		if t, ok := translit[r]; ok {
			b.WriteString(t)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// isMark reports whether r is a combining mark, which NFD puts after a
// base letter (U+0301 in "e\u0301").
func isMark(r rune) bool {
	return r >= 0x300 && unicode.Is(unicode.M, r)
}

// translit maps lowercase letters that are not ASCII to ASCII. A letter
// with a canonical decomposition maps to the transliteration of its base
// letter, so dropping the combining marks of the NFD form gives the same
// result ("é" and "e\u0301" are both "e"; "й" and "и\u0306" are both "i").
var translit = buildTranslit()

func buildTranslit() map[rune]string {
	groups := []struct{ to, from string }{
		// Latin-1 Supplement and Latin Extended-A.
		{"a", "àáâãäåāăąǎ"},
		{"c", "çćĉċč"},
		{"d", "ďđð"},
		{"e", "èéêëēĕėęěẽ"},
		{"g", "ĝğġģ"},
		{"h", "ĥħ"},
		{"i", "ìíîïĩīĭįıǐ"},
		{"j", "ĵ"},
		{"k", "ķ"},
		{"l", "ĺļľŀł"},
		{"n", "ñńņňŉ"},
		{"o", "òóôõöøōŏőǒ"},
		{"r", "ŕŗř"},
		{"s", "śŝşšș"},
		{"t", "ţťŧț"},
		{"u", "ùúûüũūŭůűųǔ"},
		{"w", "ŵ"},
		{"y", "ýÿŷ"},
		{"z", "źżž"},
		{"ae", "æ"},
		{"oe", "œ"},
		{"ss", "ß"},
		{"th", "þ"},
		{"ij", "ĳ"},
		// Cyrillic (Russian, Ukrainian, Belarusian). Letters with a
		// decomposition (й, ё, ї, ў) follow their base letter.
		{"a", "а"}, {"b", "б"}, {"v", "в"}, {"g", "гґ"}, {"d", "д"},
		{"e", "еёэ"}, {"ye", "є"}, {"zh", "ж"}, {"z", "з"}, {"i", "иійї"},
		{"k", "к"}, {"l", "л"}, {"m", "м"}, {"n", "н"}, {"o", "о"},
		{"p", "п"}, {"r", "р"}, {"s", "с"}, {"t", "т"}, {"u", "уў"},
		{"f", "ф"}, {"kh", "х"}, {"ts", "ц"}, {"ch", "ч"}, {"sh", "ш"},
		{"shch", "щ"}, {"y", "ы"}, {"yu", "ю"}, {"ya", "я"},
	}
	m := map[rune]string{}
	for _, g := range groups {
		for _, r := range g.from {
			m[r] = g.to
		}
	}
	return m
}
