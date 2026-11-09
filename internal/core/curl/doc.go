// Package curl reads curl command lines: the base of "paste a cURL command
// as a request" (IMP-01). It only reads text. Nothing is executed, no file
// is opened and no shell expansion is done; see docs/curl.md.
//
// Tokenize splits the first command in a text into words with the quoting
// rules of a shell dialect (POSIX sh with Bash's $'...', Windows cmd.exe,
// PowerShell), detected from the text when DialectAuto is given. Every word
// keeps the byte range it came from.
//
// Parse turns the words into a Command: method (explicit or inferred like
// curl), URL, headers in order with their -H semantics, data and form
// parts with their kind, credentials and the connection options PostSonde
// can import. Everything else is reported as a Diagnostic with a code, a
// severity and a byte range: errors (an unterminated quote, a missing
// value or URL), warnings (unknown or unsupported options, files that are
// not read, shell variables) and info (options that do not change the
// request).
//
//	c := curl.Parse(pasted)
//	if err := c.Err(); err != nil {
//		return err // errs.Parse, code curl.invalid_command
//	}
//	body, complete := c.Body()
//
// Which options exist and whether they take a value comes from
// options.txt, generated from `curl --help all`.
package curl
