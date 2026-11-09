// Package request turns the request files of a project
// (storage/projectfile) into the requests the protocol clients send.
//
// It is the pure mapping step between what the editor saves and what goes
// on the wire: which rows are sent, in which order and spelling, and which
// rows are rejected before anything is sent. Sending itself lives in the
// protocol packages, such as httpclient; BuildHTTP puts the steps below
// together into the httpclient.Request of an HTTP request file (HTTP-20,
// HTTP-21).
//
// Headers maps a header table: disabled rows and rows without a name are
// skipped; the others keep their order, duplicates and spelling, and are
// validated with errors that name the row. docs/http-client.md lists the
// rules.
//
// Body maps a body: raw text, or a URL-encoded form (FormURLEncoded), with
// the auto Content-Type of its mode (ContentType), which an enabled
// Content-Type header overrides (ImplicitContentType).
//
// Method checks a method token, sent as typed, and BodyForMethod drops the
// body of HEAD and TRACE with a warning. PathParams finds the :name and
// {name} placeholders in a URL's path, and SubstitutePath fills them from
// the pathParams rows, percent-encoded, reporting those without a value.
//
// Auth maps a request's auth (none, Basic, bearer, API key) onto headers
// and query parameters, WithAuthHeaders and WithAuthQuery add them so that
// what the user typed in the tables wins, and ResolveAuth follows
// "inherit" up the folders. docs/auth.md lists the rules.
//
// The package imports only the standard library, like all of internal/core.
package request
