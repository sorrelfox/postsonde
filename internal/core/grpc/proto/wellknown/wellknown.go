// Package wellknown embeds the .proto sources of the Protocol Buffers
// well-known types, which protoc ships built in, so that imports such as
// "google/protobuf/timestamp.proto" resolve without a protobuf installation
// (GRPC-04). The linker searches them after every configured import path.
//
// The google/protobuf files are unmodified copies from Protocol Buffers
// v34.0, as packaged in com.google.protobuf:protobuf-java:4.34.0 (Maven
// Central), under the BSD-3-Clause license, Copyright 2008 Google Inc. Each
// file keeps its license header, so the notice ships with the binary; see
// THIRD_PARTY_NOTICES.md. To update, copy the newer upstream files over
// these and update Version, the notices and docs/proto.md.
//
// google/rpc/status.proto, the error model gRPC uses for rich error
// details, and google/rpc/error_details.proto, its standard detail messages
// (ErrorInfo, RetryInfo, BadRequest, ...), are unmodified copies from
// googleapis as packaged in com.google.api.grpc:proto-google-common-protos:2.63.2
// (Maven Central), under the Apache License 2.0, Copyright 2025 Google LLC.
// The license text is embedded next to them (google/rpc/LICENSE) so that it
// ships with the binary. RPCVersion names the package version.
//
// Not embedded: google/protobuf/compiler/plugin.proto and the language
// feature files (cpp_features.proto, java_features.proto, go_features.proto),
// which describe code generators rather than message types.
package wellknown

import (
	"embed"
	"io/fs"
	"slices"
	"strings"
)

//go:embed google/protobuf/*.proto google/rpc/status.proto google/rpc/error_details.proto google/rpc/LICENSE
var files embed.FS

// Version is the Protocol Buffers release the google/protobuf files come
// from.
const Version = "34.0"

// RPCVersion is the proto-google-common-protos release that the
// google/rpc files come from.
const RPCVersion = "2.63.2"

// FS returns the embedded files, rooted like import paths
// ("google/protobuf/any.proto"). It also holds google/rpc/LICENSE.
func FS() fs.FS { return files }

// Paths returns the import paths of the embedded .proto files, sorted.
func Paths() []string {
	var paths []string
	for _, dir := range []string{"google/protobuf", "google/rpc"} {
		entries, _ := fs.ReadDir(files, dir)
		for _, e := range entries {
			if strings.HasSuffix(e.Name(), ".proto") {
				paths = append(paths, dir+"/"+e.Name())
			}
		}
	}
	slices.Sort(paths)
	return paths
}

// Source returns the contents of the embedded .proto file at import path p
// and whether it exists.
func Source(p string) (string, bool) {
	if !fs.ValidPath(p) || !strings.HasSuffix(p, ".proto") {
		return "", false
	}
	b, err := files.ReadFile(p)
	if err != nil {
		return "", false
	}
	return string(b), true
}
