// Package projectzip is PostSonde's own folder zip (EXP-02,
// docs/import.md#folder-zips): a folder of the project, with its
// subfolders and requests, written as a zip of native project files
// (docs/file-format.md), and read back as an import format.
//
// The zip holds one top-level directory, the folder as it is on disk:
// its _folder.json, its request files and its subfolders, and a manifest,
// _export.json, that marks the zip as an export and names its format and
// schema versions. Secrets are never in it: they live in the workspace's
// local side files, which the export does not read, and a secret typed
// into a project file by hand is left out (requestsecrets.SplitFile).
//
// Reading never extracts anything: the zip's files are decoded in memory
// into an importer.Model, and importer.Write picks the names on disk.
// Entries are still checked strictly (no absolute or parent paths, no
// symbolic links or other special files, no encryption, no duplicates) and
// the number of entries and the bytes they decompress to are limited, so a
// crafted zip is refused as a whole. Standard library only.
package projectzip

import (
	"path"
	"strings"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/collection"
)

// Format identifies the zip in its manifest.
const (
	// FormatID is the import format's ID and the manifest's "format".
	FormatID = "postsonde-folder"
	// FormatVersion is the manifest's "formatVersion" this build writes
	// and the newest it reads. It changes only when the zip's layout does;
	// the project files inside carry their own schemaVersion.
	FormatVersion = 1
	// ManifestName is the manifest's name in the top-level directory. Like
	// every name starting with "_", the tree ignores it, so a zip unpacked
	// by hand into a project is a plain folder.
	ManifestName = "_export.json"
	// Ext is the extension of an exported zip.
	Ext = ".zip"
)

// Limits of a zip that can be read, and so of an export.
const (
	// MaxEntries is the most entries (files and directories) a zip may have.
	MaxEntries = 20000
	// MaxEntrySize is the most bytes one file may decompress to.
	MaxEntrySize = 32 << 20
	// MaxTotalSize is the most bytes all files together may decompress to.
	MaxTotalSize = 256 << 20
	// MaxNameLength is the longest entry name, in bytes.
	MaxNameLength = 1024
)

// Error codes.
const (
	// CodeUnsafeEntry: the zip has an entry that is not a plain file or
	// directory inside its folder: an absolute path, "..", a backslash, a
	// symbolic link or another special file, an encrypted file, a
	// duplicate name, or folders nested deeper than collection.MaxDepth
	// (kind invalid_argument). Details: entry, reason.
	CodeUnsafeEntry = "projectzip.unsafe_entry"
	// CodeTooLarge: the zip has more than MaxEntries entries, or its files
	// decompress to more than MaxEntrySize or MaxTotalSize bytes (kind
	// invalid_argument). Details: limit; entry for one file. An export
	// that would be too large to import fails with it too.
	CodeTooLarge = "projectzip.too_large"
	// CodeNewerFormat: the manifest's formatVersion or schemaVersion is
	// newer than this build reads (kind unsupported). Details: version,
	// supported.
	CodeNewerFormat = "projectzip.newer_format"
	// A zip that is not a folder export (no manifest, several top-level
	// entries) fails with importer.CodeInvalidFile.

	// CodeIgnored: files in the zip that are not part of the folder tree
	// were not imported (info on the file).
	CodeIgnored = "projectzip.ignored"
	// CodeSettingsSkipped: a folder's _folder.json could not be read, or
	// comes from a newer PostSonde, so the folder is imported without its
	// settings (warning).
	CodeSettingsSkipped = "projectzip.settings_skipped"
	// CodeSkipped: a file could not be imported: it did not load, is of a
	// kind this version does not know, comes from a newer PostSonde, or is
	// not a request (warning, on the skipped item).
	CodeSkipped = "projectzip.skipped"

	// CodeExportSkipped: an item the export left out (warning).
	CodeExportSkipped = "projectzip.export_skipped"
	// CodeExportSecret: a secret typed into a project file was left out
	// of the zip (info).
	CodeExportSecret = "projectzip.export_secret"
	// CodeExportSettings: a folder's settings could not be read, so the
	// zip has a _folder.json with its name only (warning).
	CodeExportSettings = "projectzip.export_settings"
)

// manifest is _export.json.
type manifest struct {
	Format        string `json:"format"`
	FormatVersion int    `json:"formatVersion"`
	// SchemaVersion is the project-file schema version of the files.
	SchemaVersion int `json:"schemaVersion"`
	// Name is the exported folder's display name.
	Name string `json:"name"`
}

// FileName returns the suggested file name of the export of a folder
// called name: "<name>.zip", with characters file systems refuse
// replaced.
func FileName(name string) string {
	name = strings.TrimSpace(name)
	var b strings.Builder
	for _, r := range name {
		switch {
		case r < 0x20 || r == 0x7f || strings.ContainsRune(`/\:*?"<>|`, r):
			b.WriteRune('-')
		default:
			b.WriteRune(r)
		}
	}
	base := strings.Trim(b.String(), ". ")
	if base == "" {
		base = "folder"
	}
	return base + Ext
}

// checkName returns why name, an entry's path in the zip without a
// trailing "/", is not allowed, or "".
func checkName(name string) string {
	switch {
	case name == "":
		return "an empty name"
	case len(name) > MaxNameLength:
		return "a name that is too long"
	case !utf8.ValidString(name):
		return "a name that is not valid UTF-8"
	case strings.ContainsRune(name, '\\'):
		return "a backslash in its name"
	case strings.ContainsRune(name, 0):
		return "a NUL character in its name"
	case strings.HasPrefix(name, "/"):
		return "an absolute path"
	case len(name) >= 2 && name[1] == ':':
		return "a drive letter"
	}
	parts := strings.Split(name, "/")
	for _, p := range parts {
		switch p {
		case "":
			return "an empty path element"
		case ".", "..":
			return "a path element " + p
		}
		for _, r := range p {
			if r < 0x20 || r == 0x7f {
				return "a control character in its name"
			}
		}
	}
	// The top-level directory, then at most MaxDepth folders below it,
	// then a file.
	if len(parts) > collection.MaxDepth+2 {
		return "folders nested too deep"
	}
	if path.Clean(name) != name {
		return "a path that is not clean"
	}
	return ""
}

// validEntry reports whether entry, a file or directory name on disk, can
// be written to a zip as it is: what checkName accepts as one element,
// and not reserved by the zip itself.
func validEntry(entry string) bool {
	return !strings.Contains(entry, "/") && checkName(entry) == "" && entry != ManifestName
}
