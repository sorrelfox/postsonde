// Package settings reads and writes PostSonde's settings file (SET-01): one
// human-readable JSON file, settings.json in the config directory (see
// package appdirs), that the app writes and the user may edit by hand.
// docs/settings.md describes the file for users.
//
// # File layout
//
// The file is a JSON object with a schema version and one object per
// section. Every field is optional; a missing field has its default:
//
//	{
//	  "version": 1,
//	  "ui": {
//	    "theme": "system",
//	    "sidebarCollapsed": false,
//	    "splitRatio": 0.5,
//	    "responsePosition": "beside",
//	    "splitRatioBelow": 0.5
//	  },
//	  "network": {
//	    "defaultTimeoutMs": 0,
//	    "verifyTls": true,
//	    "maxResponseBodyMb": 512
//	  },
//	  "editor": {
//	    "fontFamily": "JetBrains Mono",
//	    "fontSize": 12,
//	    "tabSize": 2,
//	    "wordWrap": false,
//	    "lineNumbers": true,
//	    "matchBrackets": true
//	  }
//	}
//
// The "privacy" section holds allowedDirs, the directories whose files
// {{$file}} may read (PRV-10); it is written only when it is not empty.
// Later network settings (proxy) go into "network".
//
// # Loading
//
// [Load] never fails the whole file for one bad value:
//
//   - A missing (or empty) file gives [Defaults], without an error, and
//     nothing is written.
//   - A value of the wrong type, out of range or not one of the allowed
//     strings falls back to its default. The [Report] lists each one as an
//     errs.InvalidArgument error with code [CodeInvalidValue] and details
//     field, value and expected.
//   - Members this version does not know, at the top level and inside known
//     sections, are kept in [Report.Unknown] and written back by [Save].
//   - A file that is not a JSON object is an errs.Parse error with code
//     [CodeParseFailed]; the settings are the defaults. A [Store] then backs
//     the file up with [Backup] before it first writes over it.
//   - A missing version counts as 1. An older version is upgraded in memory
//     by the migration steps (migrate); the file is rewritten on the next
//     save. A newer version than [CurrentVersion] loads the fields this
//     version knows and reports [CodeNewerVersion]; a Store never writes
//     such a file.
//
// # Writing
//
// [Save] writes with atomicfile (temp file, fsync, rename) in the stable
// JSON format of stablejson, with permission 0600 for a new file. [Store]
// wraps the file for the app: it serialises updates, validates patches
// ([Settings.Apply]) before anything is written, and re-reads the file on
// [Store.Reload] so hand edits are picked up while the app runs.
//
// # Admin policy
//
// [LoadPolicy] reads the optional administrator policy file (SET-02) from
// [SystemPolicyPath]; [WithPolicy] makes a Store apply it. Locked
// settings always have the policy's value, updates that change one fail
// with [CodeLocked], and hand edits of them are ignored without the file
// being rewritten. See policy.go.
package settings
