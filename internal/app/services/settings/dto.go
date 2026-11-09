package settings

import (
	"slices"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	core "github.com/sorrelfox/postsonde/internal/core/settings"
)

// DTOs are the only types SettingsService methods take or return. See
// docs/bindings.md and, for the fields, docs/settings.md.

// SettingsDTO is the settings in effect.
type SettingsDTO struct {
	// Version is the settings schema version (core CurrentVersion).
	Version int `json:"version"`
	// UI holds the window and layout preferences.
	UI UISettingsDTO `json:"ui"`
	// Network holds the defaults for sending requests.
	Network NetworkSettingsDTO `json:"network"`
	// Editor holds the code editor preferences.
	Editor EditorSettingsDTO `json:"editor"`
	// Shortcuts are the custom keyboard shortcuts (UI-07): chords by
	// shortcut id, replacing the default ones. Empty when none is changed.
	Shortcuts map[string][]string `json:"shortcuts"`
}

// UISettingsDTO is the "ui" section.
type UISettingsDTO struct {
	// Theme is "system", "dark" or "light".
	Theme string `json:"theme"`
	// SidebarCollapsed hides the sidebar.
	SidebarCollapsed bool `json:"sidebarCollapsed"`
	// SplitRatio is the request pane's share of the editor area, 0.2–0.8.
	SplitRatio float64 `json:"splitRatio"`
	// ResponsePosition is "beside" (right of the request) or "below" (UI-02).
	ResponsePosition string `json:"responsePosition"`
	// SplitRatioBelow is the request pane's share of the height while the
	// response is below it, 0.2–0.8.
	SplitRatioBelow float64 `json:"splitRatioBelow"`
	// CustomTheme is the file name of the custom theme in the themes
	// folder (UI-05), "" for none.
	CustomTheme string `json:"customTheme"`
	// Locale is the UI language (UI-10): "system" or a language tag such
	// as "en".
	Locale string `json:"locale"`
}

// EditorSettingsDTO is the "editor" section (UI-08).
type EditorSettingsDTO struct {
	// FontFamily is the name of one font ("monospace" or "ui-monospace" for
	// the system's).
	FontFamily string `json:"fontFamily"`
	// FontSize is in CSS pixels, 8–32.
	FontSize int `json:"fontSize"`
	// TabSize is the tab width and indent unit in spaces, 1–8.
	TabSize int `json:"tabSize"`
	// WordWrap wraps long lines.
	WordWrap bool `json:"wordWrap"`
	// LineNumbers shows the line-number gutter.
	LineNumbers bool `json:"lineNumbers"`
	// MatchBrackets highlights the matching bracket at the cursor.
	MatchBrackets bool `json:"matchBrackets"`
}

// NetworkSettingsDTO is the "network" section.
type NetworkSettingsDTO struct {
	// DefaultTimeoutMs is the timeout of a request without its own, in
	// milliseconds: 0 for none, else 1000 to 86400000.
	DefaultTimeoutMs int `json:"defaultTimeoutMs"`
	// VerifyTLS verifies certificates for requests without their own
	// verifyTls (HTTP-23); false is the global "insecure" switch.
	VerifyTLS bool `json:"verifyTls"`
	// MaxResponseBodyMb is the largest response body kept, in MiB,
	// 1–16384 (HTTP-37); a longer one is cut there.
	MaxResponseBodyMb int `json:"maxResponseBodyMb"`
}

// SettingsPatchDTO is a partial update for Update: null leaves a section or
// field as it is.
type SettingsPatchDTO struct {
	UI      *UISettingsPatchDTO      `json:"ui"`
	Network *NetworkSettingsPatchDTO `json:"network"`
	Editor  *EditorSettingsPatchDTO  `json:"editor"`
	// Shortcuts replaces all custom shortcuts; {} removes them.
	Shortcuts *map[string][]string `json:"shortcuts"`
}

// UISettingsPatchDTO is a partial update of the "ui" section.
type UISettingsPatchDTO struct {
	Theme            *string  `json:"theme"`
	SidebarCollapsed *bool    `json:"sidebarCollapsed"`
	SplitRatio       *float64 `json:"splitRatio"`
	ResponsePosition *string  `json:"responsePosition"`
	SplitRatioBelow  *float64 `json:"splitRatioBelow"`
	CustomTheme      *string  `json:"customTheme"`
	Locale           *string  `json:"locale"`
}

// EditorSettingsPatchDTO is a partial update of the "editor" section.
type EditorSettingsPatchDTO struct {
	FontFamily    *string `json:"fontFamily"`
	FontSize      *int    `json:"fontSize"`
	TabSize       *int    `json:"tabSize"`
	WordWrap      *bool   `json:"wordWrap"`
	LineNumbers   *bool   `json:"lineNumbers"`
	MatchBrackets *bool   `json:"matchBrackets"`
}

// NetworkSettingsPatchDTO is a partial update of the "network" section.
type NetworkSettingsPatchDTO struct {
	DefaultTimeoutMs  *int  `json:"defaultTimeoutMs"`
	VerifyTLS         *bool `json:"verifyTls"`
	MaxResponseBodyMb *int  `json:"maxResponseBodyMb"`
}

// LocationDTO says where the settings file is, for the settings screen.
type LocationDTO struct {
	// Path is the settings file ("" when the settings are kept in memory).
	Path string `json:"path"`
	// Exists is true when the file exists (it is created by the first
	// change, or by OpenFile).
	Exists bool `json:"exists"`
	// DataDir is the app data directory (history, cookies, tokens), or ""
	// when the app did not say.
	DataDir string `json:"dataDir"`
}

// Values of ChangedEventDTO.Source.
const (
	// SourceApp: a successful Update.
	SourceApp = "app"
	// SourceFile: the file changed on disk (a hand edit).
	SourceFile = "file"
)

// ChangedEventDTO is the payload of the settings:changed event.
type ChangedEventDTO struct {
	// Settings are the settings now in effect.
	Settings SettingsDTO `json:"settings"`
	// Problems are the current load problems, as Problems returns them.
	Problems []apperr.ErrorDTO `json:"problems"`
	// Source is "app" after an Update and "file" after a change on disk.
	Source string `json:"source"`
}

func toSettingsDTO(s core.Settings) SettingsDTO {
	return SettingsDTO{
		Version: s.Version,
		UI: UISettingsDTO{
			Theme:            s.UI.Theme,
			SidebarCollapsed: s.UI.SidebarCollapsed,
			SplitRatio:       s.UI.SplitRatio,
			ResponsePosition: s.UI.ResponsePosition,
			SplitRatioBelow:  s.UI.SplitRatioBelow,
			CustomTheme:      s.UI.CustomTheme,
			Locale:           s.UI.Locale,
		},
		Network: NetworkSettingsDTO{DefaultTimeoutMs: s.Network.DefaultTimeoutMs, VerifyTLS: s.Network.VerifyTLS, MaxResponseBodyMb: s.Network.MaxResponseBodyMb},
		Editor: EditorSettingsDTO{
			FontFamily:    s.Editor.FontFamily,
			FontSize:      s.Editor.FontSize,
			TabSize:       s.Editor.TabSize,
			WordWrap:      s.Editor.WordWrap,
			LineNumbers:   s.Editor.LineNumbers,
			MatchBrackets: s.Editor.MatchBrackets,
		},
		Shortcuts: shortcutsDTO(s.Shortcuts),
	}
}

// shortcutsDTO copies the overrides, {} rather than null when there are none.
func shortcutsDTO(v map[string][]string) map[string][]string {
	out := make(map[string][]string, len(v))
	for id, chords := range v {
		out[id] = slices.Clone(chords)
	}
	return out
}

func (p SettingsPatchDTO) toCore() core.Patch {
	var out core.Patch
	if p.UI != nil {
		out.UI = core.UIPatch{
			Theme:            p.UI.Theme,
			SidebarCollapsed: p.UI.SidebarCollapsed,
			SplitRatio:       p.UI.SplitRatio,
			ResponsePosition: p.UI.ResponsePosition,
			SplitRatioBelow:  p.UI.SplitRatioBelow,
			CustomTheme:      p.UI.CustomTheme,
			Locale:           p.UI.Locale,
		}
	}
	if p.Network != nil {
		out.Network = core.NetworkPatch{DefaultTimeoutMs: p.Network.DefaultTimeoutMs, VerifyTLS: p.Network.VerifyTLS, MaxResponseBodyMb: p.Network.MaxResponseBodyMb}
	}
	if p.Editor != nil {
		out.Editor = core.EditorPatch{
			FontFamily:    p.Editor.FontFamily,
			FontSize:      p.Editor.FontSize,
			TabSize:       p.Editor.TabSize,
			WordWrap:      p.Editor.WordWrap,
			LineNumbers:   p.Editor.LineNumbers,
			MatchBrackets: p.Editor.MatchBrackets,
		}
	}
	out.Shortcuts = p.Shortcuts
	return out
}

func toProblemDTOs(problems []*errs.Error) []apperr.ErrorDTO {
	out := make([]apperr.ErrorDTO, 0, len(problems))
	for _, p := range problems {
		out = append(out, apperr.ToDTO(p))
	}
	return out
}
