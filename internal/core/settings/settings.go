package settings

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/themes"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf"
)

// FileName is the name of the settings file in the config directory.
const FileName = "settings.json"

// CurrentVersion is the schema version this build reads and writes. Bump it
// only for a change old files must be migrated for (a renamed or
// re-interpreted field), and add the step to migrations. Adding a field
// with a default needs no bump.
const CurrentVersion = 1

// Error codes, in addition to the errs kinds.
const (
	// CodeInvalidValue: a value has the wrong type, is out of range or is
	// not one of the allowed strings (kind invalid_argument). Details:
	// field, value (as JSON), expected.
	CodeInvalidValue = "settings.invalid_value"
	// CodeParseFailed: the file is not a JSON object (kind parse).
	// Details: path, and line and column for a syntax error.
	CodeParseFailed = "settings.parse_failed"
	// CodeNewerVersion: the file was written by a newer PostSonde (kind
	// unsupported). Details: version, supported.
	CodeNewerVersion = "settings.newer_version"
	// CodeReadFailed: the file exists but could not be read. The kind
	// comes from the cause (permission_denied, io, …).
	CodeReadFailed = "settings.read_failed"
	// CodeSaveFailed: writing the file (or its backup) failed. The kind
	// comes from the cause.
	CodeSaveFailed = "settings.save_failed"
)

// Theme values for UI.Theme.
const (
	ThemeSystem = "system"
	ThemeDark   = "dark"
	ThemeLight  = "light"
)

// Themes lists the allowed UI.Theme values.
var Themes = []string{ThemeSystem, ThemeDark, ThemeLight}

// Response positions for UI.ResponsePosition (UI-02).
const (
	// ResponseBeside puts the response to the right of the request.
	ResponseBeside = "beside"
	// ResponseBelow puts the response under the request.
	ResponseBelow = "below"
)

// LocaleSystem is the UI.Locale that follows the OS language (UI-10).
const LocaleSystem = "system"

// MaxLocaleLen is the longest UI.Locale language tag, in bytes.
const MaxLocaleLen = 35

// ResponsePositions lists the allowed UI.ResponsePosition values.
var ResponsePositions = []string{ResponseBeside, ResponseBelow}

// Bounds of UI.SplitRatio and UI.SplitRatioBelow.
const (
	MinSplitRatio = 0.2
	MaxSplitRatio = 0.8
)

// Bounds of Network.DefaultTimeoutMs, besides 0 (no timeout): at least a
// second, at most a day.
const (
	MinDefaultTimeoutMs = 1000
	MaxDefaultTimeoutMs = 24 * 60 * 60 * 1000
)

// Bounds and default of Network.MaxResponseBodyMb (HTTP-37), in MiB.
const (
	MinMaxResponseBodyMb     = 1
	MaxMaxResponseBodyMb     = 16 << 10
	DefaultMaxResponseBodyMb = 512
)

// Editor settings (UI-08): the default font, the bounds of the font size
// and tab size, and the longest font name.
const (
	DefaultEditorFontFamily = "JetBrains Mono"
	MinEditorFontSize       = 8
	MaxEditorFontSize       = 32
	MinEditorTabSize        = 1
	MaxEditorTabSize        = 8
	MaxEditorFontFamilyLen  = 64
)

// Settings are the app settings, as written to settings.json.
type Settings struct {
	// Version is the schema version, always CurrentVersion in memory.
	Version int `json:"version"`
	// UI holds the window and layout preferences.
	UI UI `json:"ui"`
	// Network holds the defaults for sending requests.
	Network Network `json:"network"`
	// Editor holds the code editor preferences.
	Editor Editor `json:"editor"`
	// Privacy holds what local files the app may read (PRV-10).
	Privacy Privacy `json:"privacy"`
	// Shortcuts are the custom keyboard shortcuts (UI-07): chords by
	// shortcut id, replacing the default ones. Default none; left out of
	// the file when empty. See shortcuts.go.
	Shortcuts map[string][]string `json:"shortcuts,omitempty"`
}

// Privacy holds the privacy settings, section "privacy". It is left out of
// the file while it is empty.
type Privacy struct {
	// AllowedDirs are the absolute paths of the directories whose files
	// features driven by project data may read, such as {{$file PATH}}
	// (PRV-10, DYN-08), besides the open project's folder, which is always
	// allowed. Kept in the app settings, never in a project, so a project
	// cannot widen it. Default none; left out when empty.
	AllowedDirs []string `json:"allowedDirs,omitempty"`
	// SecretsLockMinutes locks the encrypted secrets once none has
	// been used for that many minutes, 0 to 1440; 0 (the default) never
	// does. Left out when 0.
	SecretsLockMinutes int `json:"secretsLockMinutes,omitempty"`
	// RequireSecretEncryption, meant to be locked by the admin policy
	// (SET-02), requires a master password for the secrets:
	// turning encryption off is refused, and so is storing a secret while
	// it is off. Default false; left out when false.
	RequireSecretEncryption bool `json:"requireSecretEncryption,omitempty"`
	// SecretsKeyStore is where the key of the encrypted secrets is kept
	// besides behind the master password: SecretsKeyStoreKeychain
	// (the default; left out of the file) also keeps it in the OS
	// keychain, so the secrets unlock at startup without the password;
	// SecretsKeyStorePassword keeps it behind the password only.
	SecretsKeyStore string `json:"secretsKeyStore,omitempty"`
}

// The values of Privacy.SecretsKeyStore.
const (
	SecretsKeyStoreKeychain = "keychain"
	SecretsKeyStorePassword = "password"
)

// SecretsKeyStores lists the values of Privacy.SecretsKeyStore.
var SecretsKeyStores = []string{SecretsKeyStoreKeychain, SecretsKeyStorePassword}

// UI holds the window and layout preferences, section "ui".
type UI struct {
	// Theme is "system" (follow the OS), "dark" or "light". Default "system".
	Theme string `json:"theme"`
	// SidebarCollapsed hides the sidebar. Default false.
	SidebarCollapsed bool `json:"sidebarCollapsed"`
	// SplitRatio is the share of the editor area taken by the request
	// pane, from 0.2 to 0.8. Default 0.5.
	SplitRatio float64 `json:"splitRatio"`
	// ResponsePosition is where the response pane sits (UI-02): "beside"
	// (right of the request) or "below". Default "beside".
	ResponsePosition string `json:"responsePosition"`
	// SplitRatioBelow is the share of the height taken by the request pane
	// while the response is below it, from 0.2 to 0.8. Default 0.5.
	SplitRatioBelow float64 `json:"splitRatioBelow"`
	// CustomTheme is the file name of a custom theme in the themes folder
	// of the data directory (UI-05, docs/themes.md), e.g. "ocean.json";
	// "" for none, the default. While one is set and usable it replaces
	// Theme's colours. Left out of the file when empty.
	CustomTheme string `json:"customTheme,omitempty"`
	// Locale is the language of the UI (UI-10): "system" (the OS
	// language, when the app has strings for it, else English) or a
	// language tag such as "en". A tag the app has no strings for falls
	// back to English. Default "system".
	Locale string `json:"locale"`
}

// Editor holds the code editor preferences (UI-08), section "editor". They
// apply to every code editor and response viewer; the font also to the
// one-line fields such as the URL.
type Editor struct {
	// FontFamily is the name of one font, e.g. "Menlo", or "monospace" /
	// "ui-monospace" for the system's. A font that is not installed falls
	// back to the bundled one. Default "JetBrains Mono".
	FontFamily string `json:"fontFamily"`
	// FontSize is the font size in CSS pixels, from MinEditorFontSize to
	// MaxEditorFontSize. Default 12.
	FontSize int `json:"fontSize"`
	// TabSize is the width of a tab and of one indent level, in spaces,
	// from MinEditorTabSize to MaxEditorTabSize. Default 2.
	TabSize int `json:"tabSize"`
	// WordWrap wraps long lines instead of scrolling sideways. Default false.
	WordWrap bool `json:"wordWrap"`
	// LineNumbers shows the line-number gutter. Default true.
	LineNumbers bool `json:"lineNumbers"`
	// MatchBrackets highlights the bracket matching the one at the cursor.
	// Default true.
	MatchBrackets bool `json:"matchBrackets"`
}

// Network holds the defaults for sending requests, section "network".
type Network struct {
	// DefaultTimeoutMs is the timeout of a request that has none of its own
	// (HTTP-21), in milliseconds: 0 for no timeout, or from
	// MinDefaultTimeoutMs to MaxDefaultTimeoutMs. Default 0.
	DefaultTimeoutMs int `json:"defaultTimeoutMs"`
	// VerifyTLS verifies server certificates for requests that do not set
	// their own verifyTls (HTTP-23). Default true; false is shown as a
	// warning on every such request.
	VerifyTLS bool `json:"verifyTls"`
	// CABundles are the absolute paths of PEM files whose certificates
	// are trusted, on top of the system's certificate authorities, by
	// every TLS connection (NET-04). Default none; left out of the file
	// when empty.
	CABundles []string `json:"caBundles,omitempty"`
	// ClientCerts are the per-host client certificates for mTLS (AUTH-30,
	// NET-05): a host pattern and the absolute paths of a PEM certificate
	// and key or of a PKCS#12 file. Passphrases are never here; they are
	// in the local secret storage (appsecrets). Default none; left out of
	// the file when empty.
	ClientCerts []tlsconf.ClientCertEntry `json:"clientCerts,omitempty"`
	// MaxResponseBodyMb is the largest response body kept, in MiB
	// (HTTP-37), from MinMaxResponseBodyMb to MaxMaxResponseBodyMb: a
	// longer body is cut there and marked truncated. Bodies are written
	// to disk while they arrive, so this bounds the disk space, not
	// memory. Default DefaultMaxResponseBodyMb.
	MaxResponseBodyMb int `json:"maxResponseBodyMb"`
	// ProxyMode is where the proxy every connection goes through comes
	// from (SOCKS-04, NET-01): "system" (the operating system's proxy
	// settings), "env" (the ALL_PROXY environment variable, when it is
	// set), "manual" (ProxyURL) or "off" (no proxy). Default "system";
	// files written before it keep the "env" they have.
	// See proxy.go and docs/network.md#proxy-settings.
	ProxyMode string `json:"proxyMode"`
	// ProxyURL is the manual proxy, socks5h://[user@]host:port. The app
	// keeps it without a password: that is in the local secret storage
	// (appsecrets). A password written here by hand is used too. Kept
	// while ProxyMode is not "manual", so switching back restores it.
	// Default ""; left out of the file when empty.
	ProxyURL string `json:"proxyUrl,omitempty"`
	// ProxyBypass lists the targets that connect directly whatever proxy
	// is in effect (SOCKS-09): example.com, .example.com, *.example.com,
	// <local>, IP addresses and CIDR ranges, each optionally with :port
	// (netx.ParseBypass). It applies to the proxy of every scope.
	// localhost is always bypassed. Default none; left out of the file
	// when empty.
	ProxyBypass []string `json:"proxyBypass,omitempty"`
}

// Defaults returns the settings used for every field the file leaves out.
func Defaults() Settings {
	return Settings{
		Version: CurrentVersion,
		UI: UI{
			Theme:            ThemeSystem,
			SidebarCollapsed: false,
			SplitRatio:       0.5,
			ResponsePosition: ResponseBeside,
			SplitRatioBelow:  0.5,
			Locale:           LocaleSystem,
		},
		Network: Network{DefaultTimeoutMs: 0, VerifyTLS: true, MaxResponseBodyMb: DefaultMaxResponseBodyMb, ProxyMode: ProxyModeSystem},
		Editor: Editor{
			FontFamily:    DefaultEditorFontFamily,
			FontSize:      12,
			TabSize:       2,
			WordWrap:      false,
			LineNumbers:   true,
			MatchBrackets: true,
		},
		Privacy: Privacy{SecretsKeyStore: SecretsKeyStoreKeychain},
	}
}

// Patch is a partial update: nil fields are left as they are.
type Patch struct {
	UI      UIPatch
	Network NetworkPatch
	Editor  EditorPatch
	Privacy PrivacyPatch
	// Shortcuts replaces all custom shortcuts; an empty map removes them.
	Shortcuts *map[string][]string
}

// PrivacyPatch is a partial update of the "privacy" section.
type PrivacyPatch struct {
	// AllowedDirs replaces the whole list; an empty list removes them all.
	AllowedDirs             *[]string
	SecretsLockMinutes      *int
	RequireSecretEncryption *bool
	SecretsKeyStore         *string
}

// UIPatch is a partial update of the "ui" section.
type UIPatch struct {
	Theme            *string
	SidebarCollapsed *bool
	SplitRatio       *float64
	ResponsePosition *string
	SplitRatioBelow  *float64
	CustomTheme      *string
	Locale           *string
}

// EditorPatch is a partial update of the "editor" section.
type EditorPatch struct {
	FontFamily    *string
	FontSize      *int
	TabSize       *int
	WordWrap      *bool
	LineNumbers   *bool
	MatchBrackets *bool
}

// NetworkPatch is a partial update of the "network" section.
type NetworkPatch struct {
	DefaultTimeoutMs *int
	VerifyTLS        *bool
	// CABundles replaces the whole list; an empty list removes them all.
	CABundles *[]string
	// ClientCerts replaces the whole list; an empty list removes them all.
	ClientCerts       *[]tlsconf.ClientCertEntry
	MaxResponseBodyMb *int
	ProxyMode         *string
	ProxyURL          *string
	// ProxyBypass replaces the whole list; an empty list removes it.
	ProxyBypass *[]string
}

// Apply returns s with p applied. An invalid value in p is an
// errs.InvalidArgument error with code CodeInvalidValue, and s is returned
// unchanged.
func (s Settings) Apply(p Patch) (Settings, error) {
	next := s
	if v := p.UI.Theme; v != nil {
		if !validTheme(*v) {
			return s, invalid("ui.theme", strconv.Quote(*v), themeExpected, "")
		}
		next.UI.Theme = *v
	}
	if v := p.UI.SidebarCollapsed; v != nil {
		next.UI.SidebarCollapsed = *v
	}
	if v := p.UI.SplitRatio; v != nil {
		if !validSplitRatio(*v) {
			return s, invalid("ui.splitRatio", formatFloat(*v), splitRatioExpected, "")
		}
		next.UI.SplitRatio = *v
	}
	if v := p.UI.ResponsePosition; v != nil {
		if !validResponsePosition(*v) {
			return s, invalid("ui.responsePosition", strconv.Quote(*v), responsePositionExpected, "")
		}
		next.UI.ResponsePosition = *v
	}
	if v := p.UI.SplitRatioBelow; v != nil {
		if !validSplitRatio(*v) {
			return s, invalid("ui.splitRatioBelow", formatFloat(*v), splitRatioExpected, "")
		}
		next.UI.SplitRatioBelow = *v
	}
	if v := p.UI.CustomTheme; v != nil {
		if !validCustomTheme(*v) {
			return s, invalid("ui.customTheme", strconv.Quote(*v), customThemeExpected, "")
		}
		next.UI.CustomTheme = *v
	}
	if v := p.UI.Locale; v != nil {
		if !validLocale(*v) {
			return s, invalid("ui.locale", strconv.Quote(*v), localeExpected, "")
		}
		next.UI.Locale = *v
	}
	if v := p.Network.DefaultTimeoutMs; v != nil {
		if !validDefaultTimeout(*v) {
			return s, invalid("network.defaultTimeoutMs", strconv.Itoa(*v), defaultTimeoutExpected, "")
		}
		next.Network.DefaultTimeoutMs = *v
	}
	if v := p.Network.VerifyTLS; v != nil {
		next.Network.VerifyTLS = *v
	}
	if v := p.Network.MaxResponseBodyMb; v != nil {
		if !validMaxResponseBody(*v) {
			return s, invalid("network.maxResponseBodyMb", strconv.Itoa(*v), maxResponseBodyExpected, "")
		}
		next.Network.MaxResponseBodyMb = *v
	}
	if v := p.Network.ProxyMode; v != nil {
		if !validProxyMode(*v) {
			return s, invalid("network.proxyMode", strconv.Quote(*v), proxyModeExpected, "")
		}
		next.Network.ProxyMode = *v
	}
	if v := p.Network.ProxyURL; v != nil {
		if err := checkProxyURL(*v); err != nil {
			return s, invalidProxyURL(*v, err, "")
		}
		next.Network.ProxyURL = *v
	}
	if v := p.Network.ProxyBypass; v != nil {
		if err := checkProxyBypass(*v); err != nil {
			return s, invalidProxyBypass(*v, err, "")
		}
		next.Network.ProxyBypass = nil
		if len(*v) > 0 {
			next.Network.ProxyBypass = slices.Clone(*v)
		}
	}
	if v := p.Editor.FontFamily; v != nil {
		if !validFontFamily(*v) {
			return s, invalid("editor.fontFamily", strconv.Quote(*v), fontFamilyExpected, "")
		}
		next.Editor.FontFamily = *v
	}
	if v := p.Editor.FontSize; v != nil {
		if !validFontSize(*v) {
			return s, invalid("editor.fontSize", strconv.Itoa(*v), fontSizeExpected, "")
		}
		next.Editor.FontSize = *v
	}
	if v := p.Editor.TabSize; v != nil {
		if !validTabSize(*v) {
			return s, invalid("editor.tabSize", strconv.Itoa(*v), tabSizeExpected, "")
		}
		next.Editor.TabSize = *v
	}
	if v := p.Editor.WordWrap; v != nil {
		next.Editor.WordWrap = *v
	}
	if v := p.Editor.LineNumbers; v != nil {
		next.Editor.LineNumbers = *v
	}
	if v := p.Editor.MatchBrackets; v != nil {
		next.Editor.MatchBrackets = *v
	}
	if v := p.Network.CABundles; v != nil {
		if !validCABundles(*v) {
			return s, invalid("network.caBundles", compactJSON(mustJSON(*v)), caBundlesExpected, "")
		}
		next.Network.CABundles = nil
		if len(*v) > 0 {
			next.Network.CABundles = slices.Clone(*v)
		}
	}
	if v := p.Shortcuts; v != nil {
		if !validShortcuts(*v) {
			return s, invalid("shortcuts", compactJSON(mustJSON(*v)), shortcutsExpected, "")
		}
		next.Shortcuts = cloneShortcuts(*v)
	}
	if v := p.Network.ClientCerts; v != nil {
		if !validClientCerts(*v) {
			return s, invalid("network.clientCerts", compactJSON(mustJSON(*v)), clientCertsExpected, "")
		}
		next.Network.ClientCerts = nil
		if len(*v) > 0 {
			next.Network.ClientCerts = slices.Clone(*v)
		}
	}
	if v := p.Privacy.AllowedDirs; v != nil {
		if !validAllowedDirs(*v) {
			return s, invalid("privacy.allowedDirs", compactJSON(mustJSON(*v)), allowedDirsExpected, "")
		}
		next.Privacy.AllowedDirs = nil
		if len(*v) > 0 {
			next.Privacy.AllowedDirs = slices.Clone(*v)
		}
	}
	if v := p.Privacy.SecretsLockMinutes; v != nil {
		if !validSecretsLockMinutes(*v) {
			return s, invalid("privacy.secretsLockMinutes", strconv.Itoa(*v), secretsLockExpected, "")
		}
		next.Privacy.SecretsLockMinutes = *v
	}
	if v := p.Privacy.RequireSecretEncryption; v != nil {
		next.Privacy.RequireSecretEncryption = *v
	}
	if v := p.Privacy.SecretsKeyStore; v != nil {
		if !validSecretsKeyStore(*v) {
			return s, invalid("privacy.secretsKeyStore", strconv.Quote(*v), secretsKeyStoreExpected, "")
		}
		next.Privacy.SecretsKeyStore = *v
	}
	return next, nil
}

// Equal reports whether s and o are the same settings.
func (s Settings) Equal(o Settings) bool {
	return s.Version == o.Version && s.UI == o.UI &&
		s.Network.DefaultTimeoutMs == o.Network.DefaultTimeoutMs && s.Network.VerifyTLS == o.Network.VerifyTLS &&
		s.Network.MaxResponseBodyMb == o.Network.MaxResponseBodyMb &&
		s.Network.ProxyMode == o.Network.ProxyMode && s.Network.ProxyURL == o.Network.ProxyURL &&
		slices.Equal(s.Network.ProxyBypass, o.Network.ProxyBypass) &&
		slices.Equal(s.Network.CABundles, o.Network.CABundles) && slices.Equal(s.Network.ClientCerts, o.Network.ClientCerts) &&
		s.Editor == o.Editor && slices.Equal(s.Privacy.AllowedDirs, o.Privacy.AllowedDirs) &&
		s.Privacy.SecretsLockMinutes == o.Privacy.SecretsLockMinutes && s.Privacy.RequireSecretEncryption == o.Privacy.RequireSecretEncryption &&
		s.Privacy.SecretsKeyStore == o.Privacy.SecretsKeyStore && equalShortcuts(s.Shortcuts, o.Shortcuts)
}

// Validate reports the first invalid value in s, as Apply would.
func (s Settings) Validate() error {
	_, err := Defaults().Apply(Patch{
		UI: UIPatch{
			Theme:            &s.UI.Theme,
			SidebarCollapsed: &s.UI.SidebarCollapsed,
			SplitRatio:       &s.UI.SplitRatio,
			ResponsePosition: &s.UI.ResponsePosition,
			SplitRatioBelow:  &s.UI.SplitRatioBelow,
			CustomTheme:      &s.UI.CustomTheme,
			Locale:           &s.UI.Locale,
		},
		Editor: EditorPatch{
			FontFamily:    &s.Editor.FontFamily,
			FontSize:      &s.Editor.FontSize,
			TabSize:       &s.Editor.TabSize,
			WordWrap:      &s.Editor.WordWrap,
			LineNumbers:   &s.Editor.LineNumbers,
			MatchBrackets: &s.Editor.MatchBrackets,
		},
		Network: NetworkPatch{DefaultTimeoutMs: &s.Network.DefaultTimeoutMs, VerifyTLS: &s.Network.VerifyTLS, CABundles: &s.Network.CABundles,
			ClientCerts: &s.Network.ClientCerts, MaxResponseBodyMb: &s.Network.MaxResponseBodyMb,
			ProxyMode: &s.Network.ProxyMode, ProxyURL: &s.Network.ProxyURL, ProxyBypass: &s.Network.ProxyBypass},
		Shortcuts: &s.Shortcuts,
		Privacy: PrivacyPatch{AllowedDirs: &s.Privacy.AllowedDirs, SecretsLockMinutes: &s.Privacy.SecretsLockMinutes,
			RequireSecretEncryption: &s.Privacy.RequireSecretEncryption, SecretsKeyStore: &s.Privacy.SecretsKeyStore},
	})
	return err
}

var (
	themeExpected            = `one of "system", "dark", "light"`
	splitRatioExpected       = fmt.Sprintf("a number from %s to %s", formatFloat(MinSplitRatio), formatFloat(MaxSplitRatio))
	defaultTimeoutExpected   = fmt.Sprintf("0 (no timeout) or a whole number of milliseconds from %d to %d", MinDefaultTimeoutMs, MaxDefaultTimeoutMs)
	responsePositionExpected = `one of "beside", "below"`
	customThemeExpected      = `"" (none) or the file name of a theme in the themes folder, such as "ocean.json"`
	localeExpected           = `"system" or a language tag such as "en" or "en-US"`
	fontFamilyExpected       = fmt.Sprintf("the name of one font, up to %d characters, without quotes, commas, semicolons, backslashes, braces or angle brackets", MaxEditorFontFamilyLen)
	fontSizeExpected         = fmt.Sprintf("a whole number of pixels from %d to %d", MinEditorFontSize, MaxEditorFontSize)
	tabSizeExpected          = fmt.Sprintf("a whole number from %d to %d", MinEditorTabSize, MaxEditorTabSize)
	boolExpected             = "true or false"
	sectionExpected          = "an object"
	versionExpected          = "a whole number from 1"
	maxResponseBodyExpected  = fmt.Sprintf("a whole number of MiB from %d to %d", MinMaxResponseBodyMb, MaxMaxResponseBodyMb)
	caBundlesExpected        = fmt.Sprintf("a list of at most %d different absolute paths of PEM files", MaxCABundles)
	allowedDirsExpected      = fmt.Sprintf("a list of at most %d different absolute paths of directories", MaxAllowedDirs)
	secretsLockExpected      = fmt.Sprintf("0 (never) or a whole number of minutes from 1 to %d", MaxSecretsLockMinutes)
	secretsKeyStoreExpected  = `one of "keychain", "password"`
	proxyModeExpected        = `one of "system", "env", "manual", "off"`
	proxyURLExpected         = `"" or a proxy URL such as "socks5h://proxy.example.com:1080"`
	proxyBypassExpected      = fmt.Sprintf(`a list of at most %d hosts that bypass the proxy, such as "example.com", ".example.com", "*.example.com", "<local>", "10.1.2.3", "10.0.0.0/8" or "[::1]:8080"`, netx.MaxBypassEntries)
	clientCertsExpected      = fmt.Sprintf(`a list of at most %d client certificates with different IDs, each {"id", "host", and "certFile" (with an optional "keyFile") or "pfxFile", as absolute paths}`, MaxClientCerts)
)

func validTheme(v string) bool {
	for _, t := range Themes {
		if v == t {
			return true
		}
	}
	return false
}

// validCustomTheme accepts "" or a theme file name (themes.ValidID).
func validCustomTheme(v string) bool {
	return v == "" || themes.ValidID(v)
}

// validLocale accepts "system" or the shape of a BCP 47 language tag: a
// primary language subtag of 2 or 3 letters, then subtags of 1 to 8
// letters or digits, joined by "-", at most MaxLocaleLen bytes. Whether
// the app has strings for it is the UI's business: it falls back to
// English, so a file written for a newer build stays valid.
func validLocale(v string) bool {
	if v == LocaleSystem {
		return true
	}
	if len(v) > MaxLocaleLen {
		return false
	}
	for i, sub := range strings.Split(v, "-") {
		n := len(sub)
		if (i == 0 && (n < 2 || n > 3)) || n < 1 || n > 8 {
			return false
		}
		for _, r := range sub {
			letter := (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')
			digit := r >= '0' && r <= '9'
			if !letter && (i == 0 || !digit) {
				return false
			}
		}
	}
	return true
}

func validResponsePosition(v string) bool {
	for _, p := range ResponsePositions {
		if v == p {
			return true
		}
	}
	return false
}

// validFontFamily accepts one font name: not blank, no surrounding spaces,
// at most MaxEditorFontFamilyLen characters, printable, and none of the
// characters that would end or escape a CSS font-family value (the UI
// quotes the name and appends the bundled font as a fallback).
func validFontFamily(v string) bool {
	if v == "" || strings.TrimSpace(v) != v || !utf8.ValidString(v) || utf8.RuneCountInString(v) > MaxEditorFontFamilyLen {
		return false
	}
	for _, r := range v {
		if !unicode.IsPrint(r) || strings.ContainsRune(`"'\,;{}<>`, r) {
			return false
		}
	}
	return true
}

// MaxCABundles bounds Network.CABundles.
const MaxCABundles = 100

// validCABundles accepts up to MaxCABundles absolute, different paths.
func validCABundles(v []string) bool {
	if len(v) > MaxCABundles {
		return false
	}
	seen := make(map[string]bool, len(v))
	for _, p := range v {
		if strings.TrimSpace(p) == "" || !filepath.IsAbs(p) || seen[p] {
			return false
		}
		seen[p] = true
	}
	return true
}

// MaxAllowedDirs bounds Privacy.AllowedDirs.
const MaxAllowedDirs = 100

// MaxSecretsLockMinutes bounds Privacy.SecretsLockMinutes (a day).
const MaxSecretsLockMinutes = 1440

func validSecretsLockMinutes(v int) bool { return v >= 0 && v <= MaxSecretsLockMinutes }

func validSecretsKeyStore(v string) bool { return slices.Contains(SecretsKeyStores, v) }

// validAllowedDirs accepts up to MaxAllowedDirs absolute, different paths.
// Whether they exist is not checked: a missing directory allows nothing.
func validAllowedDirs(v []string) bool {
	if len(v) > MaxAllowedDirs {
		return false
	}
	seen := make(map[string]bool, len(v))
	for _, p := range v {
		if strings.TrimSpace(p) == "" || !filepath.IsAbs(p) || seen[p] {
			return false
		}
		seen[p] = true
	}
	return true
}

// MaxClientCerts bounds Network.ClientCerts.
const MaxClientCerts = 100

// validClientCerts accepts up to MaxClientCerts entries with different
// IDs, each valid for tlsconf.ValidateClientCertEntry.
func validClientCerts(v []tlsconf.ClientCertEntry) bool {
	if len(v) > MaxClientCerts {
		return false
	}
	seen := make(map[string]bool, len(v))
	for _, e := range v {
		if seen[e.ID] || tlsconf.ValidateClientCertEntry(e) != nil {
			return false
		}
		seen[e.ID] = true
	}
	return true
}

func validFontSize(v int) bool {
	return v >= MinEditorFontSize && v <= MaxEditorFontSize
}

func validTabSize(v int) bool {
	return v >= MinEditorTabSize && v <= MaxEditorTabSize
}

// mustJSON encodes v, which cannot fail for the values it is given.
func mustJSON(v any) json.RawMessage {
	b, _ := json.Marshal(v)
	return b
}

// validSplitRatio also rejects NaN, which fails both comparisons.
func validSplitRatio(v float64) bool {
	return v >= MinSplitRatio && v <= MaxSplitRatio
}

// validDefaultTimeout accepts 0 (no timeout) and the range from
// MinDefaultTimeoutMs to MaxDefaultTimeoutMs.
func validDefaultTimeout(v int) bool {
	return v == 0 || (v >= MinDefaultTimeoutMs && v <= MaxDefaultTimeoutMs)
}

// validMaxResponseBody accepts MinMaxResponseBodyMb to MaxMaxResponseBodyMb.
func validMaxResponseBody(v int) bool {
	return v >= MinMaxResponseBodyMb && v <= MaxMaxResponseBodyMb
}

func formatFloat(
	v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}

// maxValueLen caps the value quoted in an error's details.
const maxValueLen = 64

// invalid builds the CodeInvalidValue error for field. value is the
// offending value as JSON text. fallback, when set, is the default used
// instead (for a value read from the file) and goes into the hint.
func invalid(field, value, expected, fallback string) *errs.Error {
	if len(value) > maxValueLen {
		value = value[:maxValueLen] + "…"
	}
	hint := "Use " + expected + "."
	if fallback != "" {
		hint = fmt.Sprintf("Use %s in %s. PostSonde uses the default, %s, until then.", expected, FileName, fallback)
	}
	return errs.New(errs.InvalidArgument, fmt.Sprintf("The setting %q has an invalid value, %s.", field, value)).
		WithCode(CodeInvalidValue).
		WithHint(hint).
		WithDetail("field", field).
		WithDetail("value", value).
		WithDetail("expected", expected)
}

// compactJSON returns raw without insignificant whitespace, for error
// details.
func compactJSON(raw json.RawMessage) string {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return string(bytes.TrimSpace(raw))
	}
	return b.String()
}
