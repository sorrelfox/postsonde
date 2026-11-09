package settings

import (
	"slices"
	"strings"
	"testing"
)

// TestAllowedDirs: privacy.allowedDirs (PRV-10) is read, validated,
// patched, compared and written, round trip included; an empty list
// leaves the whole section out of the file.
func TestAllowedDirs(t *testing.T) {
	a, b := abs(t, "fixtures"), abs(t, "keys")

	// Load: a valid list.
	s, r, err := Load(writeSettings(t, `{"privacy":{"allowedDirs":`+jsonString(t, []string{a, b})+`}}`))
	if err != nil || len(r.Problems) != 0 || !slices.Equal(s.Privacy.AllowedDirs, []string{a, b}) {
		t.Fatalf("Load = %+v, %+v, %v", s, r.Problems, err)
	}

	// Load: invalid lists fall back to none with a problem; an invalid
	// section too.
	for _, bad := range []string{`"dir"`, `["relative/dir"]`, `[""]`, jsonString(t, []string{a, a}), `[1]`} {
		s, r, err := Load(writeSettings(t, `{"privacy":{"allowedDirs":`+bad+`}}`))
		if err != nil || s.Privacy.AllowedDirs != nil || len(r.Problems) != 1 || r.Problems[0].Details["field"] != "privacy.allowedDirs" {
			t.Errorf("%s: Load = %+v, %+v, %v", bad, s.Privacy, r.Problems, err)
		}
	}
	if s, r, err := Load(writeSettings(t, `{"privacy":[]}`)); err != nil || len(r.Problems) != 1 || s.Privacy.AllowedDirs != nil {
		t.Errorf("privacy not an object: %+v, %+v, %v", s.Privacy, r.Problems, err)
	}

	// Apply replaces the list, copying it; an empty list clears it.
	list := []string{a}
	next, err := Defaults().Apply(Patch{Privacy: PrivacyPatch{AllowedDirs: &list}})
	if err != nil || !slices.Equal(next.Privacy.AllowedDirs, list) {
		t.Fatalf("Apply = %+v, %v", next.Privacy, err)
	}
	list[0] = b
	if next.Privacy.AllowedDirs[0] != a {
		t.Error("Apply kept the caller's slice")
	}
	if next.Equal(Defaults()) || !next.Equal(with(func(s *Settings) { s.Privacy.AllowedDirs = []string{a} })) {
		t.Error("Equal ignores allowedDirs")
	}
	empty := []string{}
	cleared, err := next.Apply(Patch{Privacy: PrivacyPatch{AllowedDirs: &empty}})
	if err != nil || cleared.Privacy.AllowedDirs != nil || !cleared.Equal(Defaults()) {
		t.Errorf("clearing = %+v, %v", cleared.Privacy, err)
	}
	for _, bad := range [][]string{{"relative"}, {a, a}, {" "}} {
		if _, err := Defaults().Apply(Patch{Privacy: PrivacyPatch{AllowedDirs: &bad}}); err == nil {
			t.Errorf("Apply(%q) accepted", bad)
		}
	}

	// Marshal: written when set, the whole section left out when empty.
	data, err := Marshal(next, nil)
	if err != nil || !strings.Contains(string(data), `"privacy"`) || !strings.Contains(string(data), `"allowedDirs"`) {
		t.Errorf("Marshal = %s, %v", data, err)
	}
	data, err = Marshal(Defaults(), nil)
	if err != nil || strings.Contains(string(data), "privacy") {
		t.Errorf("Marshal defaults = %s, %v", data, err)
	}

	// Round trip through the file, with an unknown member of the section
	// kept.
	path := writeSettings(t, `{"privacy":{"allowedDirs":`+jsonString(t, []string{a})+`,"later":true}}`)
	s, r, err = Load(path)
	if err != nil || len(r.Problems) != 0 {
		t.Fatalf("Load = %+v, %v", r.Problems, err)
	}
	if err := Save(path, s, r.Unknown); err != nil {
		t.Fatal(err)
	}
	again, r2, err := Load(path)
	if err != nil || !again.Equal(s) || len(r2.Unknown["privacy"]) != 1 {
		t.Errorf("round trip = %+v, unknown %v, %v", again.Privacy, r2.Unknown, err)
	}
}
