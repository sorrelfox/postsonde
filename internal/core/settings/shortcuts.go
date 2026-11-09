package settings

import (
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Custom keyboard shortcuts (UI-07), section "shortcuts": the chords the
// user chose instead of the default ones, by shortcut id:
//
//	"shortcuts": { "request.send": ["Mod+Shift+Enter"] }
//
// Go does not know the shortcut registry (frontend/src/lib/shortcuts); it
// only checks the shape: ids and chords written as the registry writes
// them. The UI ignores ids it does not know, so a file from another
// version keeps its overrides.

// Bounds of the "shortcuts" section.
const (
	// MaxShortcutOverrides bounds the number of overridden shortcuts.
	MaxShortcutOverrides = 200
	// MaxShortcutChords bounds the chords of one shortcut.
	MaxShortcutChords = 4
	// maxShortcutIDLen bounds a shortcut id.
	maxShortcutIDLen = 64
)

var (
	// shortcutIDPattern: dot-separated camelCase words, "request.send".
	shortcutIDPattern = regexp.MustCompile(`^[a-z][A-Za-z0-9]*(\.[a-z][A-Za-z0-9]*)+$`)
	// shortcutNamedKey: a named key, "Enter", "PageUp", "F6", "Space".
	shortcutNamedKey = regexp.MustCompile(`^[A-Z][A-Za-z0-9]{1,19}$`)
)

var shortcutModifiers = []string{"Mod", "Ctrl", "Alt", "Shift"}

var shortcutsExpected = fmt.Sprintf(`an object of at most %d shortcut ids ("request.send"), each with a list of 1 to %d different chords such as "Mod+Shift+Enter" (modifiers Mod, Ctrl, Alt, Shift, then one key)`,
	MaxShortcutOverrides, MaxShortcutChords)

// validShortcuts accepts at most MaxShortcutOverrides ids, each with 1 to
// MaxShortcutChords different valid chords.
func validShortcuts(v map[string][]string) bool {
	if len(v) > MaxShortcutOverrides {
		return false
	}
	for id, chords := range v {
		if len(id) > maxShortcutIDLen || !shortcutIDPattern.MatchString(id) {
			return false
		}
		if len(chords) == 0 || len(chords) > MaxShortcutChords {
			return false
		}
		for i, c := range chords {
			if !ValidChord(c) || slices.Contains(chords[:i], c) {
				return false
			}
		}
	}
	return true
}

// ValidChord reports whether c is a chord as the shortcut registry writes
// it: modifiers from Mod, Ctrl, Alt and Shift, each at most once, then one
// key: a printable ASCII character other than "+" (a letter, digit or
// symbol) or a named key ("Enter", "ArrowUp", "F6").
func ValidChord(c string) bool {
	parts := strings.Split(c, "+")
	key := parts[len(parts)-1]
	seen := map[string]bool{}
	for _, m := range parts[:len(parts)-1] {
		if !slices.Contains(shortcutModifiers, m) || seen[m] {
			return false
		}
		seen[m] = true
	}
	if len(key) == 1 {
		return key[0] > ' ' && key[0] < 0x7f
	}
	return shortcutNamedKey.MatchString(key) && !slices.Contains(shortcutModifiers, key)
}

// cloneShortcuts copies v deeply, nil when it is empty (so the section is
// left out of the file).
func cloneShortcuts(v map[string][]string) map[string][]string {
	if len(v) == 0 {
		return nil
	}
	out := make(map[string][]string, len(v))
	for id, chords := range v {
		out[id] = slices.Clone(chords)
	}
	return out
}

// shortcutExpected describes one entry of the section.
var shortcutExpected = fmt.Sprintf(`a list of 1 to %d different chords such as "Mod+Shift+Enter" (modifiers Mod, Ctrl, Alt, Shift, then one key)`, MaxShortcutChords)

// decodeShortcuts reads the "shortcuts" section entry by entry: an invalid
// entry is reported and dropped (that shortcut keeps its default chords),
// the others are kept. A section that is not an object, or has more than
// MaxShortcutOverrides entries, is reported and ignored as a whole.
func decodeShortcuts(problems *[]*errs.Error, raw json.RawMessage) map[string][]string {
	if isAbsent(raw) {
		return nil
	}
	var entries map[string]json.RawMessage
	if err := json.Unmarshal(raw, &entries); err != nil || entries == nil || len(entries) > MaxShortcutOverrides {
		*problems = append(*problems, invalid("shortcuts", compactJSON(raw), shortcutsExpected, "the default shortcuts"))
		return nil
	}
	out := map[string][]string{}
	for _, id := range slices.Sorted(maps.Keys(entries)) {
		var chords []string
		if err := json.Unmarshal(entries[id], &chords); err != nil || !validShortcuts(map[string][]string{id: chords}) {
			*problems = append(*problems, invalid("shortcuts."+id, compactJSON(entries[id]), shortcutExpected, "the default chords"))
			continue
		}
		out[id] = chords
	}
	return cloneShortcuts(out)
}

// equalShortcuts reports whether a and b hold the same overrides (nil and
// empty are equal).
func equalShortcuts(a, b map[string][]string) bool {
	return maps.EqualFunc(a, b, slices.Equal[[]string])
}
