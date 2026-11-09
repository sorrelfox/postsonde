package themes

import core "github.com/sorrelfox/postsonde/internal/core/themes"

// DTOs are the only types ThemesService methods take or return. See
// docs/bindings.md and, for the behaviour, docs/themes.md.

// ThemesDTO is what List returns.
type ThemesDTO struct {
	// Dir is the themes folder, "" without a data directory.
	Dir    string     `json:"dir"`
	Themes []ThemeDTO `json:"themes"`
}

// ThemeDTO is one theme file.
type ThemeDTO struct {
	// ID is the file name, the value of ui.customTheme that selects it.
	ID   string `json:"id"`
	Name string `json:"name"`
	// Base is "dark" or "light" ("" when the file has none).
	Base string `json:"base"`
	// Vars are the CSS custom properties to set on the document root,
	// each a known token with a colour normalised to #rrggbb or
	// #rrggbbaa.
	Vars []ThemeVarDTO `json:"vars"`
	// Usable is false when the file has errors: it is not applied.
	Usable   bool              `json:"usable"`
	Problems []ThemeProblemDTO `json:"problems"`
}

// ThemeVarDTO is a CSS custom property a theme sets.
type ThemeVarDTO struct {
	// Name is the property, e.g. "--bg0".
	Name  string `json:"name"`
	Value string `json:"value"`
}

// ThemeProblemDTO is something wrong with a theme file.
type ThemeProblemDTO struct {
	// Severity is "error" (the theme is not used) or "warning" (the
	// member is ignored).
	Severity string `json:"severity"`
	Code     string `json:"code"`
	// Key is the member, e.g. "colors.bg0"; "" for the whole file.
	Key     string `json:"key"`
	Message string `json:"message"`
}

func toThemeDTO(t core.Theme) ThemeDTO {
	out := ThemeDTO{
		ID:       t.ID,
		Name:     t.Name,
		Base:     t.Base,
		Vars:     make([]ThemeVarDTO, 0, len(t.Vars)),
		Usable:   t.Usable(),
		Problems: make([]ThemeProblemDTO, 0, len(t.Problems)),
	}
	for _, v := range t.Vars {
		out.Vars = append(out.Vars, ThemeVarDTO{Name: v.Name, Value: v.Value})
	}
	for _, p := range t.Problems {
		out.Problems = append(out.Problems, ThemeProblemDTO{Severity: p.Severity, Code: p.Code, Key: p.Key, Message: p.Message})
	}
	return out
}
