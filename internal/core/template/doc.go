// Package template resolves {{variables}} in any text: URLs, headers,
// bodies, auth fields and messages of every protocol. It is the one place
// that knows the syntax, so the editor, the senders and the mock servers all
// agree on it. See docs/templates.md.
//
// Parse splits a text into segments with byte offsets, for highlighting and
// hover. A Resolver looks names up in a Chain of scopes (global, environment,
// collection, folder, runtime: later scopes win), expands variables whose
// values contain other variables, and returns the rendered text together
// with every reference (which scope supplied it) and every problem
// (undefined names, cycles, nesting too deep). Senders must call Result.Err
// and refuse to send when it is not nil; unresolved references are never
// sent silently.
//
//	r := template.Resolver{Scopes: template.Chain{
//		template.Vars("Globals", globals),
//		template.Vars("Environment: dev", env),
//	}}
//	res := r.Render("https://{{host}}/users/{{ id }}")
//	if err := res.Err(); err != nil {
//		return err // errs.InvalidArgument, code template.unresolved, ...
//	}
//
// Syntax:
//
//   - {{name}} is a variable. Whitespace around the name is ignored.
//   - {{auth.token}}, {{list.0.id}}: when no scope defines the dotted name
//     itself, the rest of the name is a path of keys and array indexes
//     into the JSON value of the longest defined prefix (Select).
//   - \{{ is a literal "{{". In a run of backslashes right before "{{", each
//     pair "\\" is one literal backslash and a remaining single backslash
//     makes the braces literal. Backslashes anywhere else are plain text.
//     Escape returns a text that renders to its input.
//   - In a run of three or more "{", the last two open the variable:
//     {"a":{{{x}}}} works as JSON.
//   - "{{" with no "}}" before the next "{{" is unterminated: it is sent as
//     literal text and reported as a warning.
//
// Dynamic values: a name no scope defines that IsDynamic accepts, such as
// {{$uuid}}, {{$timestamp +1d}}, {{$datetime YYYY-MM-DD}} or
// {{$randomInt 1 6}}, is generated for each reference with the Resolver's
// Now clock and Rand source (time.Now and crypto/rand by default). Fake
// data values ({{$randomFirstName}}, {{$randomEmail}}, {{$randomLoremSentence}})
// pick from small built-in word lists with the same Rand.
//
// Prompts: {{$prompt "API token" secret}} is a value the user types when
// the request is sent. The Resolver takes it from Prompts by label and
// reports a ProblemPrompt (template.prompt) for a label without one, so the
// sender can ask and render again (prompt.go).
//
// File contents: {{$file data/user.json}} is the text of a local file,
// read with the Resolver's Files, which decides which files may be read
// (the app's allowlist, internal/core/fileaccess). Without Files the
// reference is a ProblemFile; binary content is refused unless it is the
// input of a pipe, {{$file logo.png | base64}} (file.go).
//
// Filters: {{ token | base64 }}, {{ "user:pass" | base64 }} or
// {{ body | hmacSha256 key base64 }} pass a value through encoding and
// hashing functions (FilterNames). The result is secret when the input or a
// key is.
package template
