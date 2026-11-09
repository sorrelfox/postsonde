package app

import (
	"os"
	"path/filepath"
	"testing"

	coresettings "github.com/sorrelfox/postsonde/internal/core/settings"
)

func TestWindowBackground(t *testing.T) {
	t.Parallel()

	tests := []struct {
		theme  string
		osDark bool
		want   string
	}{
		{coresettings.ThemeDark, false, "dark"},
		{coresettings.ThemeDark, true, "dark"},
		{coresettings.ThemeLight, false, "light"},
		{coresettings.ThemeLight, true, "light"},
		{coresettings.ThemeSystem, true, "dark"},
		{coresettings.ThemeSystem, false, "light"},
	}
	for _, tt := range tests {
		got := windowBackground(tt.theme, tt.osDark)
		want := lightBackground
		if tt.want == "dark" {
			want = darkBackground
		}
		if got != want {
			t.Errorf("windowBackground(%q, %v) = %v, want the %s background", tt.theme, tt.osDark, got, tt.want)
		}
	}
}

func TestMainWindowBackground(t *testing.T) {
	t.Parallel()

	if got := mainWindowOptions(lightBackground).BackgroundColour; got != lightBackground {
		t.Errorf("BackgroundColour = %v, want %v", got, lightBackground)
	}
}

func TestSavedTheme(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	write := func(name, content string) string {
		path := filepath.Join(dir, name)
		if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
		return path
	}

	tests := []struct {
		name, path, want string
	}{
		{"in memory", "", coresettings.ThemeSystem},
		{"missing file", filepath.Join(dir, "missing.json"), coresettings.ThemeSystem},
		{"light", write("light.json", `{"version":1,"ui":{"theme":"light"}}`), coresettings.ThemeLight},
		{"dark", write("dark.json", `{"version":1,"ui":{"theme":"dark"}}`), coresettings.ThemeDark},
		{"invalid value", write("invalid.json", `{"version":1,"ui":{"theme":"neon"}}`), coresettings.ThemeSystem},
		{"broken file", write("broken.json", `{`), coresettings.ThemeSystem},
	}
	for _, tt := range tests {
		if got := savedTheme(tt.path, coresettings.Policy{}); got != tt.want {
			t.Errorf("%s: savedTheme = %q, want %q", tt.name, got, tt.want)
		}
	}

	// A theme locked by the admin policy wins over the file (SET-02).
	policy := coresettings.LoadPolicy(write(coresettings.PolicyFileName, `{"version":1,"locked":{"ui.theme":"light"}}`))
	if got := savedTheme(write("dark2.json", `{"version":1,"ui":{"theme":"dark"}}`), policy); got != coresettings.ThemeLight {
		t.Errorf("locked theme: savedTheme = %q, want light", got)
	}
	if got := savedTheme("", policy); got != coresettings.ThemeLight {
		t.Errorf("locked theme in memory: savedTheme = %q, want light", got)
	}
}
