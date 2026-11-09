// Package logging provides PostSonde's structured logger: log/slog writing
// to a size-capped, rotating file, with secrets redacted before any record
// reaches the output.
//
// # Setup
//
// The app opens one log file in the logs directory (see package appdirs) and
// builds one logger on top of it:
//
//	f, err := logging.OpenFile(logging.FileOptions{
//		Path: filepath.Join(dirs.Logs, logging.FileName),
//	})
//	...
//	defer f.Close()
//	redactor := logging.NewRedactor()
//	logger := logging.New(logging.Options{Output: f, Redactor: redactor})
//	slog.SetDefault(logger)
//
// New writes JSON by default (one object per line, easy to grep and parse)
// and text when asked, at slog.LevelInfo unless Options.Level says otherwise.
//
// # Rotation
//
// RotatingFile is an io.WriteCloser safe for concurrent use. When a write
// would take the file past its size cap (DefaultMaxSize, 5 MiB), the file is
// rotated first: postsonde.log becomes postsonde.log.1, postsonde.log.1
// becomes postsonde.log.2 and so on, keeping MaxBackups old files
// (DefaultMaxBackups, 3) and deleting the oldest. A single write is never
// split, so a record larger than the cap goes whole into a fresh file. Log
// files are created with permission 0600.
//
// # Redaction
//
// Every logger built by New passes its records through a Redactor, which
// replaces secrets with Mask ("[REDACTED]") in two ways:
//
//   - By key. An attribute whose key is a sensitive name is masked whatever
//     its value, at any depth inside groups. Keys are compared ignoring case,
//     '-' and '_', so "Authorization", "set-cookie", "API_KEY" and "apiKey"
//     all match. The built-in names are authorization, proxy-authorization,
//     cookie, set-cookie, password, passwd, secret, token, api_key, apikey,
//     x-api-key, access_token, refresh_token, id_token, client_secret and
//     private_key; AddKey adds more.
//   - By value. Values registered with AddSecret (for example the secret
//     variables of the active environment) are replaced wherever they appear:
//     in the message, in string attributes, in errors, in string slices and
//     in the text form of other values, at any depth inside groups. The
//     URL-escaped forms of a secret are masked too.
//
// Values shorter than MinSecretLen (4) characters are not registered:
// masking every "1" or "yes" in the log would make it useless while hiding
// little. AddSecret reports whether the value was registered.
//
// Secrets registered after a logger was derived with With or WithGroup are
// still masked in the attributes bound earlier. Maps with string keys (such
// as http.Header) are logged as groups, so their keys are checked too. Other
// values that contain a secret are replaced by their redacted fmt "%+v" text.
//
// # Console ring
//
// A Ring keeps the last records (DefaultRingSize, 2000) for the in-app
// console (DBG-06). Pass it as Options.Ring and New tees every record to it
// next to the output, below the Redactor, so the ring holds only redacted
// text; the output keeps its own level and format. The ring keeps debug
// records too unless RingOptions.Level says otherwise, flattens attributes
// to strings (MaxRingText bytes each), notes the operation a record belongs
// to (RingOptions.OpFromContext, or the opId/op/domain/kind attributes) and
// re-applies its Redactor when entries are read. Subscribe streams new
// records to the app layer.
//
// Redaction only catches what it knows: a secret must be registered, or sit
// under a sensitive key, and encodings other than URL escaping (base64 in a
// Basic auth header, for instance) are not recognised unless registered
// themselves.
package logging
