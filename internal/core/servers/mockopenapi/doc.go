// Package mockopenapi generates the routes of an HTTP mock server
// (internal/core/servers/mockhttp) from an OpenAPI document (MOCK-09): one
// route per operation, with the method, the path converted to a mock route
// pattern, the status of the first success response and that response's
// headers and example body. Merge adds generated routes to a server's
// routes or replaces them. Standard library only. See docs/mock-routes.md.
package mockopenapi
