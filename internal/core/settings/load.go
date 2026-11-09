package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/stablejson"
)

// RootSection is the key of Unknown for top-level members.
const RootSection = ""

// Unknown holds the members of a settings file this version does not know,
// by section: Unknown[RootSection] for top-level members, Unknown["ui"] for
// members inside "ui". Values are the raw JSON as read. Save writes them
// back after the known members, sorted by key.
type Unknown map[string]map[string]json.RawMessage

// Report describes what Load found in the file besides the settings.
type Report struct {
	// Exists is true when the file was there (even if it was empty or
	// broken).
	Exists bool
	// FileVersion is the version the file declares: 1 when it has none, 0
	// when there was no file or it could not be parsed.
	FileVersion int
	// Newer is true when FileVersion is above CurrentVersion. A Store does
	// not write such a file.
	Newer bool
	// Problems lists the values that were replaced by their defaults
	// (CodeInvalidValue) and a CodeNewerVersion warning. They do not stop
	// the file from loading.
	Problems []*errs.Error
	// Unknown are the members this version does not know, kept for Save.
	Unknown Unknown
}

// Load reads the settings file at path.
//
// A missing or empty file gives the defaults and a nil error. Invalid
// values fall back to their defaults and are listed in the report. A file
// that is not a JSON object returns the defaults and an errs.Parse error
// (CodeParseFailed); a file that cannot be read returns the defaults and an
// error with CodeReadFailed. Load never writes.
func Load(path string) (Settings, Report, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Defaults(), Report{}, nil
	}
	if err != nil {
		return Defaults(), Report{Exists: true}, readFailed(path, err)
	}
	s, r, err := parse(path, data)
	r.Exists = true
	return s, r, err
}

// rawFile and rawUI split the file into its known members. RawMessage
// fields never fail to decode, so each value is validated on its own.
type rawFile struct {
	Version json.RawMessage `json:"version"`
	UI      json.RawMessage `json:"ui"`
	Network json.RawMessage `json:"network"`
	Editor  json.RawMessage `json:"editor"`
	Privacy json.RawMessage `json:"privacy"`
	// Shortcuts is a map of ids, so it has no unknown members.
	Shortcuts json.RawMessage `json:"shortcuts"`
}

type rawPrivacy struct {
	AllowedDirs             json.RawMessage `json:"allowedDirs"`
	SecretsLockMinutes      json.RawMessage `json:"secretsLockMinutes"`
	RequireSecretEncryption json.RawMessage `json:"requireSecretEncryption"`
	SecretsKeyStore         json.RawMessage `json:"secretsKeyStore"`
}

type rawUI struct {
	Theme            json.RawMessage `json:"theme"`
	SidebarCollapsed json.RawMessage `json:"sidebarCollapsed"`
	SplitRatio       json.RawMessage `json:"splitRatio"`
	ResponsePosition json.RawMessage `json:"responsePosition"`
	SplitRatioBelow  json.RawMessage `json:"splitRatioBelow"`
	CustomTheme      json.RawMessage `json:"customTheme"`
	Locale           json.RawMessage `json:"locale"`
}

type rawEditor struct {
	FontFamily    json.RawMessage `json:"fontFamily"`
	FontSize      json.RawMessage `json:"fontSize"`
	TabSize       json.RawMessage `json:"tabSize"`
	WordWrap      json.RawMessage `json:"wordWrap"`
	LineNumbers   json.RawMessage `json:"lineNumbers"`
	MatchBrackets json.RawMessage `json:"matchBrackets"`
}

type rawNetwork struct {
	DefaultTimeoutMs  json.RawMessage `json:"defaultTimeoutMs"`
	VerifyTLS         json.RawMessage `json:"verifyTls"`
	CABundles         json.RawMessage `json:"caBundles"`
	ClientCerts       json.RawMessage `json:"clientCerts"`
	MaxResponseBodyMb json.RawMessage `json:"maxResponseBodyMb"`
	ProxyMode         json.RawMessage `json:"proxyMode"`
	ProxyURL          json.RawMessage `json:"proxyUrl"`
	ProxyBypass       json.RawMessage `json:"proxyBypass"`
}

// parse decodes the content of the settings file at path (path is only
// used in messages).
func parse(path string, data []byte) (Settings, Report, error) {
	s := Defaults()
	var r Report
	if len(bytes.TrimSpace(data)) == 0 {
		return s, r, nil
	}

	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil || top == nil {
		return s, r, parseFailed(path, data, err)
	}

	var head rawFile
	if _, err := stablejson.SplitUnknown(data, &head); err != nil {
		return s, r, parseFailed(path, data, err)
	}
	version, problem := parseVersion(head.Version)
	if problem != nil {
		r.Problems = append(r.Problems, problem)
	}
	r.FileVersion = version

	switch {
	case version > CurrentVersion:
		r.Newer = true
		r.Problems = append(r.Problems, newerVersion(version))
	case version < CurrentVersion:
		migrated, err := migrate(version, top)
		if err != nil {
			return Defaults(), Report{}, errs.Wrap(err, errs.Parse,
				fmt.Sprintf("%s (version %d) could not be upgraded, so PostSonde is using the default settings.", FileName, version)).
				WithCode(CodeParseFailed).
				WithDetail("path", path)
		}
		if data, err = json.Marshal(migrated); err != nil {
			return Defaults(), Report{}, errs.Wrap(err, errs.Internal, "The upgraded settings could not be encoded.")
		}
	}

	head = rawFile{} // a migration may have removed members
	extra, err := stablejson.SplitUnknown(data, &head)
	if err != nil {
		return Defaults(), Report{}, parseFailed(path, data, err)
	}
	r.Unknown.add(RootSection, extra)

	d := Defaults()
	if !isAbsent(head.UI) {
		var ui rawUI
		extra, err := stablejson.SplitUnknown(head.UI, &ui)
		if err != nil {
			r.Problems = append(r.Problems, invalid("ui", compactJSON(head.UI), sectionExpected, "the default for each UI setting"))
		} else {
			r.Unknown.add("ui", extra)
			decodeField(&r.Problems, "ui.theme", ui.Theme, &s.UI.Theme, validTheme, themeExpected, strconv.Quote(d.UI.Theme))
			decodeField(&r.Problems, "ui.sidebarCollapsed", ui.SidebarCollapsed, &s.UI.SidebarCollapsed, nil, boolExpected, strconv.FormatBool(d.UI.SidebarCollapsed))
			decodeField(&r.Problems, "ui.splitRatio", ui.SplitRatio, &s.UI.SplitRatio, validSplitRatio, splitRatioExpected, formatFloat(d.UI.SplitRatio))
			decodeField(&r.Problems, "ui.responsePosition", ui.ResponsePosition, &s.UI.ResponsePosition, validResponsePosition, responsePositionExpected, strconv.Quote(d.UI.ResponsePosition))
			decodeField(&r.Problems, "ui.splitRatioBelow", ui.SplitRatioBelow, &s.UI.SplitRatioBelow, validSplitRatio, splitRatioExpected, formatFloat(d.UI.SplitRatioBelow))
			decodeField(&r.Problems, "ui.customTheme", ui.CustomTheme, &s.UI.CustomTheme, validCustomTheme, customThemeExpected, `"" (none)`)
			decodeField(&r.Problems, "ui.locale", ui.Locale, &s.UI.Locale, validLocale, localeExpected, strconv.Quote(d.UI.Locale))
		}
	}
	if !isAbsent(head.Network) {
		var network rawNetwork
		extra, err := stablejson.SplitUnknown(head.Network, &network)
		if err != nil {
			r.Problems = append(r.Problems, invalid("network", compactJSON(head.Network), sectionExpected, "the default for each network setting"))
		} else {
			r.Unknown.add("network", extra)
			decodeField(&r.Problems, "network.defaultTimeoutMs", network.DefaultTimeoutMs, &s.Network.DefaultTimeoutMs, validDefaultTimeout, defaultTimeoutExpected, strconv.Itoa(d.Network.DefaultTimeoutMs))
			decodeField(&r.Problems, "network.verifyTls", network.VerifyTLS, &s.Network.VerifyTLS, nil, boolExpected, strconv.FormatBool(d.Network.VerifyTLS))
			decodeField(&r.Problems, "network.maxResponseBodyMb", network.MaxResponseBodyMb, &s.Network.MaxResponseBodyMb, validMaxResponseBody, maxResponseBodyExpected, strconv.Itoa(d.Network.MaxResponseBodyMb))
			decodeField(&r.Problems, "network.caBundles", network.CABundles, &s.Network.CABundles, validCABundles, caBundlesExpected, "none")
			decodeField(&r.Problems, "network.clientCerts", network.ClientCerts, &s.Network.ClientCerts, validClientCerts, clientCertsExpected, "none")
			decodeField(&r.Problems, "network.proxyMode", network.ProxyMode, &s.Network.ProxyMode, validProxyMode, proxyModeExpected, strconv.Quote(d.Network.ProxyMode))
			decodeProxyURL(&r.Problems, network.ProxyURL, &s.Network.ProxyURL)
			decodeProxyBypass(&r.Problems, network.ProxyBypass, &s.Network.ProxyBypass)
		}
	}
	if !isAbsent(head.Editor) {
		var editor rawEditor
		extra, err := stablejson.SplitUnknown(head.Editor, &editor)
		if err != nil {
			r.Problems = append(r.Problems, invalid("editor", compactJSON(head.Editor), sectionExpected, "the default for each editor setting"))
		} else {
			r.Unknown.add("editor", extra)
			decodeField(&r.Problems, "editor.fontFamily", editor.FontFamily, &s.Editor.FontFamily, validFontFamily, fontFamilyExpected, strconv.Quote(d.Editor.FontFamily))
			decodeField(&r.Problems, "editor.fontSize", editor.FontSize, &s.Editor.FontSize, validFontSize, fontSizeExpected, strconv.Itoa(d.Editor.FontSize))
			decodeField(&r.Problems, "editor.tabSize", editor.TabSize, &s.Editor.TabSize, validTabSize, tabSizeExpected, strconv.Itoa(d.Editor.TabSize))
			decodeField(&r.Problems, "editor.wordWrap", editor.WordWrap, &s.Editor.WordWrap, nil, boolExpected, strconv.FormatBool(d.Editor.WordWrap))
			decodeField(&r.Problems, "editor.lineNumbers", editor.LineNumbers, &s.Editor.LineNumbers, nil, boolExpected, strconv.FormatBool(d.Editor.LineNumbers))
			decodeField(&r.Problems, "editor.matchBrackets", editor.MatchBrackets, &s.Editor.MatchBrackets, nil, boolExpected, strconv.FormatBool(d.Editor.MatchBrackets))
		}
	}
	if !isAbsent(head.Privacy) {
		var privacy rawPrivacy
		extra, err := stablejson.SplitUnknown(head.Privacy, &privacy)
		if err != nil {
			r.Problems = append(r.Problems, invalid("privacy", compactJSON(head.Privacy), sectionExpected, "the default for each privacy setting"))
		} else {
			r.Unknown.add("privacy", extra)
			decodeField(&r.Problems, "privacy.allowedDirs", privacy.AllowedDirs, &s.Privacy.AllowedDirs, validAllowedDirs, allowedDirsExpected, "none")
			decodeField(&r.Problems, "privacy.secretsLockMinutes", privacy.SecretsLockMinutes, &s.Privacy.SecretsLockMinutes, validSecretsLockMinutes, secretsLockExpected, "0")
			decodeField(&r.Problems, "privacy.requireSecretEncryption", privacy.RequireSecretEncryption, &s.Privacy.RequireSecretEncryption, nil, boolExpected, "false")
			decodeField(&r.Problems, "privacy.secretsKeyStore", privacy.SecretsKeyStore, &s.Privacy.SecretsKeyStore, validSecretsKeyStore, secretsKeyStoreExpected, strconv.Quote(d.Privacy.SecretsKeyStore))
		}
	}
	s.Shortcuts = decodeShortcuts(&r.Problems, head.Shortcuts)
	return s, r, nil
}

// add records the unknown members of section, if there are any.
func (u *Unknown) add(section string, members map[string]json.RawMessage) {
	if len(members) == 0 {
		return
	}
	if *u == nil {
		*u = Unknown{}
	}
	(*u)[section] = members
}

// isAbsent reports whether a member is missing or null: either way its
// default applies.
func isAbsent(raw json.RawMessage) bool {
	return len(raw) == 0 || bytes.Equal(bytes.TrimSpace(raw), []byte("null"))
}

// decodeField decodes raw into *dst if it is a valid T, and otherwise
// leaves *dst at its default and records a CodeInvalidValue problem.
func decodeField[T any](problems *[]*errs.Error, field string, raw json.RawMessage, dst *T, valid func(T) bool, expected, fallback string) {
	if isAbsent(raw) {
		return
	}
	var v T
	if err := json.Unmarshal(raw, &v); err != nil || (valid != nil && !valid(v)) {
		*problems = append(*problems, invalid(field, compactJSON(raw), expected, fallback))
		return
	}
	*dst = v
}

// maxVersion bounds the version so a huge number is reported instead of
// overflowing.
const maxVersion = 1 << 20

// parseVersion returns the file's version: 1 when it is missing or null,
// and 1 with a problem when it is not a whole number from 1.
func parseVersion(raw json.RawMessage) (int, *errs.Error) {
	if isAbsent(raw) {
		return 1, nil
	}
	var f float64
	if err := json.Unmarshal(raw, &f); err != nil || f != math.Trunc(f) || f < 1 || f > maxVersion {
		return 1, invalid("version", compactJSON(raw), versionExpected, "1")
	}
	return int(f), nil
}

// A migration upgrades the top-level members of a file by one version.
type migration func(top map[string]json.RawMessage) (map[string]json.RawMessage, error)

// migrations[v] upgrades a file from version v to v+1. There are none yet:
// version 1 is the first. When CurrentVersion goes to 2, add
// migrations[1], which receives the version-1 members and returns them in
// the version-2 layout (the "version" member itself need not change).
var migrations = map[int]migration{}

// migrate upgrades the members of a file of version from to CurrentVersion.
func migrate(from int, top map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	return migrateWith(migrations, from, CurrentVersion, top)
}

func migrateWith(steps map[int]migration, from, to int, top map[string]json.RawMessage) (map[string]json.RawMessage, error) {
	for v := from; v < to; v++ {
		step, ok := steps[v]
		if !ok {
			return nil, fmt.Errorf("settings: no migration from version %d to %d", v, v+1)
		}
		var err error
		if top, err = step(top); err != nil {
			return nil, fmt.Errorf("settings: migrate from version %d: %w", v, err)
		}
	}
	return top, nil
}

func newerVersion(version int) *errs.Error {
	return errs.New(errs.Unsupported,
		fmt.Sprintf("%s was written by a newer version of PostSonde (settings version %d; this version supports %d).", FileName, version, CurrentVersion)).
		WithCode(CodeNewerVersion).
		WithHint("PostSonde uses the settings it knows but does not save changes to the file, so the newer version's settings stay intact. Update PostSonde to change settings.").
		WithDetail("version", strconv.Itoa(version)).
		WithDetail("supported", strconv.Itoa(CurrentVersion))
}

func parseFailed(path string, data []byte, cause error) *errs.Error {
	// "not using it": at startup that means the defaults, while running it
	// means the settings in effect are kept (see Store.Reload).
	msg := fmt.Sprintf("%s is not valid JSON, so PostSonde is not using it.", FileName)
	var syntax *json.SyntaxError
	isSyntax := errors.As(cause, &syntax)
	if cause == nil || !isSyntax {
		msg = fmt.Sprintf("%s does not contain a JSON object, so PostSonde is not using it.", FileName)
	}
	if cause == nil {
		cause = errors.New("settings: the file is not a JSON object")
	}
	e := errs.Wrap(cause, errs.Parse, msg).
		WithCode(CodeParseFailed).
		WithHint(fmt.Sprintf("Fix the file by hand. If you change a setting in PostSonde first, the broken file is kept as %s.bak-<time> and a new one is written.", FileName)).
		WithDetail("path", path)
	if isSyntax {
		line, col := position(data, syntax.Offset)
		e = e.WithDetail("line", strconv.Itoa(line)).WithDetail("column", strconv.Itoa(col))
	}
	return e
}

// position converts a byte offset into a 1-based line and column.
func position(data []byte, offset int64) (line, col int) {
	if offset > int64(len(data)) {
		offset = int64(len(data))
	}
	before := data[:max(offset-1, 0)]
	line = bytes.Count(before, []byte("\n")) + 1
	col = len(before) - bytes.LastIndexByte(before, '\n')
	return line, col
}

func readFailed(path string, err error) *errs.Error {
	return errs.Wrap(err, errs.KindOf(err), fmt.Sprintf("%s could not be read, so PostSonde is not using it.", FileName)).
		WithCode(CodeReadFailed).
		WithHint("Check that the file is readable. Changes are not saved to it until it can be read.").
		WithDetail("path", path)
}
