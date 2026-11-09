package collection

import (
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

var slugPattern = regexp.MustCompile(`^[a-z0-9]+(-[a-z0-9]+)*$`)

func TestSlug(t *testing.T) {
	t.Parallel()
	long := strings.Repeat("word ", 20)
	tests := []struct {
		name, want string
	}{
		{"Create user", "create-user"},
		{"  GET /users/{id}  ", "get-users-id"},
		{"User's list", "users-list"},
		{"User’s list", "users-list"},
		{"already-a-slug", "already-a-slug"},
		{"snake_case_name", "snake-case-name"},
		{"v2.1 Health", "v2-1-health"},
		{"--- Leading and trailing ---", "leading-and-trailing"},
		{"ÀÉÎÕÜ çñ", "aeiou-cn"},
		{"Zoë Straße Øresund Łódź", "zoe-strasse-oresund-lodz"},
		{"Ærø œuvre", "aero-oeuvre"},
		{"Получить пользователя", "poluchit-polzovatelya"},
		{"Щука і їжак, ёж", "shchuka-i-izhak-ezh"},
		{"日本語 API", "api"},
		{"", FallbackSlug},
		{"???", FallbackSlug},
		{"日本語", FallbackSlug},
		{"...", FallbackSlug},
		{"_folder", "folder"},
		{".hidden", "hidden"},
		{"CON", "con"},
		{"emoji 🚀 launch", "emoji-launch"},
		// Cut at a word boundary: 13 words are exactly MaxSlugLen bytes.
		{long, strings.Repeat("word-", 12) + "word"},
		{strings.Repeat("abc ", 30), strings.TrimSuffix(strings.Repeat("abc-", 16), "-")},
		{strings.Repeat("a", 100), strings.Repeat("a", MaxSlugLen)},
	}
	for _, tt := range tests {
		got := Slug(tt.name)
		if got != tt.want {
			t.Errorf("Slug(%q) = %q, want %q", tt.name, got, tt.want)
		}
		if !slugPattern.MatchString(got) || len(got) > MaxSlugLen {
			t.Errorf("Slug(%q) = %q is not a valid slug", tt.name, got)
		}
	}
}

// A name typed on macOS may arrive in NFD (base letter + combining mark);
// it must give the same slug and fold to the same key as NFC.
func TestSlugNormalizationInsensitive(t *testing.T) {
	t.Parallel()
	pairs := [][2]string{
		{"Caf\u00e9", "Cafe\u0301"},
		{"Z\u00fcrich", "Zu\u0308rich"},
		{"\u00c5ngstr\u00f6m", "A\u030angstro\u0308m"},
		{"\u0439\u043e\u0433\u0443\u0440\u0442", "\u0438\u0306\u043e\u0433\u0443\u0440\u0442"}, // йогурт
		{"\u0451\u0436", "\u0435\u0308\u0436"},                                                 // ёж
		{"\u0457\u0436\u0430\u043a", "\u0456\u0308\u0436\u0430\u043a"},                         // їжак
		{"Ho\u0302tel \u0161ef", "Ho\u0302tel s\u030cef"},
	}
	for _, p := range pairs {
		if a, b := Slug(p[0]), Slug(p[1]); a != b {
			t.Errorf("Slug(%q) = %q, Slug(%q) = %q", p[0], a, p[1], b)
		}
		if a, b := FoldName(p[0]+".json"), FoldName(p[1]+".json"); a != b {
			t.Errorf("FoldName(%q) = %q, FoldName(%q) = %q", p[0], a, p[1], b)
		}
	}
}

func TestIsWindowsReserved(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"CON", "con", "con.json", "Nul.JSON", "aux", "prn.txt", "COM1", "com9.json", "lpt0", "LPT¹", "conin$", "name.", "name ", "", "nul .json"} {
		if !IsWindowsReserved(name) {
			t.Errorf("IsWindowsReserved(%q) = false", name)
		}
	}
	for _, name := range []string{"console", "con-2.json", "com10", "com", "lpt", "auxiliary.json", "nul-request", "a.b.json", "icon.json"} {
		if IsWindowsReserved(name) {
			t.Errorf("IsWindowsReserved(%q) = true", name)
		}
	}
}

func TestUniqueName(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		existing []string
		slug     string
		ext      string
		root     bool
		want     string
	}{
		{"free", []string{"other.json"}, "create-user", ".json", false, "create-user.json"},
		{"taken", []string{"create-user.json"}, "create-user", ".json", false, "create-user-2.json"},
		{"numbers continue", []string{"a.json", "a-2.json", "a-3.json"}, "a", ".json", false, "a-4.json"},
		{"gap is reused", []string{"a.json", "a-3.json"}, "a", ".json", false, "a-2.json"},
		{"case-insensitive", []string{"Create-User.JSON"}, "create-user", ".json", false, "create-user-2.json"},
		{"NFD entry folds onto the slug", []string{"Cafe\u0301.json"}, "cafe", ".json", false, "cafe-2.json"},
		{"NFC entry folds onto the slug", []string{"café"}, "cafe", "", false, "cafe-2"},
		{"folder name vs file name do not collide", []string{"users.json"}, "users", "", false, "users"},
		{"any entry takes a folder name", []string{"users"}, "users", "", false, "users-2"},
		{"a file entry takes a folder name", []string{"USERS"}, "users", "", false, "users-2"},
		{"windows device", nil, "con", ".json", false, "con-2.json"},
		{"windows device folder", nil, "lpt1", "", false, "lpt1-2"},
		{"environments at root", nil, "environments", "", true, "environments-2"},
		{"environments below root", nil, "environments", "", false, "environments"},
		{"postsonde.json at root", nil, "postsonde", ".json", true, "postsonde-2.json"},
		{"postsonde folder at root", nil, "postsonde", "", true, "postsonde"},
		{"empty slug", []string{"untitled.json"}, "", ".json", false, "untitled-2.json"},
		{"tmp leftovers do not collide", []string{".a.json.tmp-1"}, "a", ".json", false, "a.json"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := uniqueName(tt.existing, tt.slug, tt.ext, tt.root); got != tt.want {
				t.Errorf("uniqueName(%q, %q, %q, %v) = %q, want %q", tt.existing, tt.slug, tt.ext, tt.root, got, tt.want)
			}
		})
	}
}

func TestUniqueNameExhaustedNumbers(t *testing.T) {
	t.Parallel()
	existing := []string{"a.json"}
	for n := 2; n <= maxNumberedCandidates; n++ {
		existing = append(existing, "a-"+strconv.Itoa(n)+".json")
	}
	got := uniqueName(existing, "a", ".json", false)
	if !regexp.MustCompile(`^a-[0-9a-f]{16}\.json$`).MatchString(got) {
		t.Errorf("uniqueName = %q, want a random suffix", got)
	}
}

func TestUniqueNameOnDisk(t *testing.T) {
	t.Parallel()
	dir := tempDir(t)
	writeFile(t, dir, "Health.json", "{}")
	writeFile(t, dir, "users/_folder.json", "{}")

	for _, tt := range []struct {
		slug, ext string
		root      bool
		want      string
	}{
		{"health", ".json", false, "health-2.json"},
		{"users", "", true, "users-2"},
		{"environments", "", true, "environments-2"},
		{"orders", "", true, "orders"},
	} {
		got, err := UniqueName(dir, tt.slug, tt.ext, tt.root)
		if err != nil || got != tt.want {
			t.Errorf("UniqueName(%q, %q) = %q, %v; want %q", tt.slug, tt.ext, got, err, tt.want)
		}
	}

	// The name really is free: creating it does not clobber anything,
	// whatever the file system's case and normalization rules.
	name, _ := UniqueName(dir, Slug("HEALTH"), ".json", true)
	f, err := os.OpenFile(filepath.Join(dir, name), os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err != nil {
		t.Fatalf("creating %s: %v", name, err)
	}
	_ = f.Close()

	if got, err := UniqueName(filepath.Join(dir, "missing"), "a", ".json", false); err != nil || got != "a.json" {
		t.Errorf("UniqueName in a missing dir = %q, %v", got, err)
	}
	if _, err := UniqueName(filepath.Join(dir, "Health.json"), "a", ".json", false); err == nil {
		t.Error("UniqueName on a file succeeded")
	} else {
		checkErr(t, err, errs.KindOf(err), CodeReadDirFailed)
	}

}
