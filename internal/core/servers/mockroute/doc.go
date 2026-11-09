// Package mockroute matches incoming HTTP requests to the routes of a mock
// server (MOCK-01, MOCK-02). It is a pure matcher: no sockets, no
// net/http server.
//
// Compile checks and indexes a list of Routes (method + path pattern) and
// reports problems as Diagnostics with byte positions. Matcher.Match takes
// a request method and its escaped path and returns the most specific
// route that accepts both, with the path parameters and the catch-all
// remainder, or a method-not-allowed result with the methods for an Allow
// header.
//
// Pattern syntax, specificity order, trailing slashes, case and
// percent-decoding are described in docs/mock-routes.md.
package mockroute
