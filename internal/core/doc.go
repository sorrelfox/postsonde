// Package core is the root of PostSonde's engine: protocol clients, local
// servers, parsers, storage and crypto.
//
// Everything under internal/core imports only the Go standard library and
// other packages of this module. No third-party code, and in particular no
// Wails or UI code, may be imported here. The rule is enforced by
// deps_test.go and scripts/check-core-deps.sh; see docs/architecture.md.
//
// Protocol and storage code lives in subpackages (httpclient, graphql, grpc,
// realtime, request, servers, storage, netx, tlsconf, logging, settings, errs,
// template, workspace, assert, history, snippet), which are added as they are implemented.
package core
