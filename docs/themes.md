# Custom themes

A custom theme (UI-05) is a local JSON file that changes the app's colours
and the code editor's syntax colours. It builds on the built-in light and
dark themes ([frontend.md](frontend.md#themes)): whatever it does not set
keeps the built-in value. Themes are only ever read from the local themes
folder; nothing is fetched.

| Piece | Where |
|---|---|
| File format, validation, colour parser | `internal/core/themes` |
| Bound service (`List`, `OpenFolder`) | `internal/app/services/themes` (`ThemesService`) |
| Applying a theme to the document | `frontend/src/styles/customTheme.ts` |
| Loaded themes, which one applies | `frontend/src/commands/themes.ts`, `components/shell/useCustomThemeSync.ts` |
| First paint | `frontend/public/theme-init.js` |
| Settings screen row | `frontend/src/components/settings/CustomThemes.tsx` |
| Example | [`examples/solarized-dark.theme.json`](examples/solarized-dark.theme.json) |

## Where themes live

```
<data>/themes/<name>.json
```

`<data>` is the app data directory ([settings.md](settings.md#data-directory-set-03)).
The folder is created (only the user can read it, `0700`) by **Open themes
folder**. Every `.json` file directly in it is a theme, except names starting
with `.` or `_`; at most 200 files (by name) and 256 KiB per file are read.
The file name (`ocean.json`) is the theme's id.

The chosen theme is `ui.customTheme` in `settings.json`
([settings.md](settings.md#the-file)): the file name, or `""` (left out of
the file) for none. It is a preference of this computer, so it is not part
of any project.

## Format

```json
{
  "$comment": "Anything starting with $ is ignored.",
  "name": "Ocean",
  "base": "dark",
  "colors": { "bg0": "#001018", "accent": "rgb(0, 160, 255)" },
  "editor": { "keyword": "#c792ea", "comment": "hsl(210, 10%, 45%)" }
}
```

| Member | Required | Meaning |
|---|---|---|
| `name` | yes | Shown in the settings screen; 1 to 100 printable characters. |
| `base` | yes | `"dark"` or `"light"`: the built-in token set underneath, and the light/dark mode of the editors and native form controls. |
| `colors` | no | App colours, token → colour. |
| `editor` | no | Syntax colours of the code editors, key → colour. |

Members starting with `$` are ignored anywhere. Other unknown members and
unknown token names are **warnings**: listed on the settings screen and
ignored. A theme whose colours fall below WCAG AA contrast (text 4.5:1,
the accent as focus ring 3:1, on the surfaces the app draws them on) is
still applied; the settings screen names the pairs it breaks, worst first
([frontend.md](frontend.md#accessibility)). The editor's background, gutter and selection come from `colors`
(`bg1`, `bg2`, `line`, `sel`, …).

**`colors` keys** (each is the CSS custom property of the same name in
`frontend/src/styles/tokens.css`):

| Keys | What |
|---|---|
| `bg0` `bg1` `bg2` `bg3` | Surfaces, darkest (dark) or lightest (light) first: window, panels, inputs and hovers, raised controls |
| `line` `line2` | Dividers and borders |
| `text` `text2` `text3` | Text: primary, secondary, muted |
| `accent` `accent-ink` `accent-dim` | The accent (also the focus ring), text on an accent fill, the accent's light tint (active buttons, highlights) |
| `m-get` `m-post` `m-put` `m-patch` `m-del` `m-gql` `m-dir` `m-ws` | Method and request-kind colours |
| `ok` `warn` `bad` | Status colours |
| `sel` `scrim` | Selection, the dimming behind dialogs |

`--shadow` is not a colour and cannot be set.

**`editor` keys**: `keyword`, `field`, `argument`, `variable`, `string`,
`number`, `type`, `directive`, `punctuation`, `comment`, `key`, `boolean`,
`plain`, `error` (the `--tk-*` tokens).

### Colours

A value must be one of:

- hex: `#rgb`, `#rgba`, `#rrggbb`, `#rrggbbaa`
- `rgb()` / `rgba()`: three numbers 0–255 or percentages, with commas or
  spaces, and an optional alpha (0–1 or a percentage; after a comma, or after
  `/` without commas): `rgb(0, 128, 255)`, `rgb(0 128 255 / 50%)`
- `hsl()` / `hsla()`: a hue (optionally `deg`), saturation and lightness
  percentages, the same optional alpha: `hsl(210 40% 20%)`
- `transparent`

Named colours (other than `transparent`), `currentColor`, `var()`, `calc()`,
other colour functions, exponents and anything else are refused. Go parses
every value with this small grammar and sends the UI only a normalised
`#rrggbb` or `#rrggbbaa`.

## Errors and warnings

A file with an **error** is listed with its errors and cannot be chosen; if
it is already chosen, the built-in theme shows and the settings screen says
why. Codes (`internal/core/themes`):

| Code | Severity | When |
|---|---|---|
| `themes.invalid_json` | error | The file is not a JSON object. |
| `themes.too_large` | error | Larger than 256 KiB. |
| `themes.read_failed` | error | The file could not be read. |
| `themes.invalid_name` | error | `name` missing, not a string, blank, too long or with control characters. |
| `themes.invalid_base` | error | `base` is not `"dark"` or `"light"`. |
| `themes.invalid_section` | error | `colors` or `editor` is not an object. |
| `themes.invalid_color` | error | A known token's value is not a string or not a colour as above. |
| `themes.unknown_key` | warning | A member or token PostSonde does not know. |

Service errors: `themes.list_failed` (the folder cannot be read),
`themes.open_failed` (it cannot be created or opened),
`themes.unavailable` (no data directory or file manager, e.g. tests).

## How a theme is applied

No CSS text is ever built from a theme file. The UI sets each colour with
CSSOM, `document.documentElement.style.setProperty("--bg0", "#001018")`,
which can only set that one custom property's value: a value cannot add
rules, end the declaration or reach another property. On top of Go's
checks, `customTheme.ts` accepts only the known token names and values of
the form `#rrggbb` / `#rrggbbaa` (a lower-case hex pattern); anything else is
dropped and logged. The Content Security Policy
([privacy.md](privacy.md#content-security-policy)) is unchanged: no `<style>`
element is written and no inline script is added, and even a value that got
through could not load anything, because every fetch directive is limited
to the app.

- **Base:** the theme's `base` pins `data-theme` (`setThemeOverride` in
  `styles/theme.ts`), whatever the Light/Dark/System preference is; the
  preference is kept and applies again when the custom theme is set to None.
  The title bar's theme button turns a custom theme off and goes on with the
  next built-in theme.
- **Loading:** `useCustomThemeSync` (called by `App`) reads the themes
  folder once at start (`ThemesService.List`) and applies the theme of
  `ui.customTheme` whenever it or the loaded files change. **Reload** on the
  settings screen reads the folder again, so a theme edited in another
  program is applied without a restart. The folder is not watched.
- **First paint:** the applied theme (base and colours) is cached in
  `localStorage` (`postsonde.customTheme`). `public/theme-init.js` applies it
  before the page paints, with the same checks, and `main.tsx` applies it
  again until the files have been read. The native window background before
  the first paint is still the built-in theme's `--bg0` (`internal/app/theme.go`).

## Settings screen

General, Appearance, **Custom theme**: a select with None and every theme
file (files with errors are shown as "(has errors)" and disabled), **Open
themes folder** (creates it and opens it in the file manager) and
**Reload**. The errors and warnings of each file are listed under it.

## Not handled yet

- Watching the themes folder for changes (use Reload).
- The native window background and frame follow the built-in themes only.
- Fonts, sizes and spacing cannot be themed; the editor font is an editor
  setting ([frontend.md](frontend.md#editor-settings)).
