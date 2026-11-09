package snippet

// grpcurl.go: a gRPC call as a grpcurl command (GEN-10), for a
// POSIX shell. docs/snippets.md#grpcurl.

import (
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

// GRPCRequest is a gRPC call as the gRPC editor holds it, for the gRPC
// generators (GRPCLanguages).
type GRPCRequest struct {
	// Address is host:port as typed; a missing port is 80, or 443 with
	// TLS, as the transport dials.
	Address string
	// TLS connects with TLS; otherwise plaintext HTTP/2 (h2c).
	TLS bool
	// Insecure turns TLS verification off.
	Insecure bool
	// Transport is "grpc" (also ""), "grpc-web" or "grpc-web-text".
	Transport string
	// Compression is "" or "identity" (none) or "gzip".
	Compression string
	// ConnectTimeout bounds the dial and the TLS handshake; zero is the
	// tool's default.
	ConnectTimeout time.Duration
	// Deadline is the call's deadline (grpc-timeout); zero is none.
	Deadline time.Duration
	// ProtoFiles and ImportPaths are the .proto setup as absolute paths;
	// with no files the schema comes from the server's reflection
	// service.
	ProtoFiles  []string
	ImportPaths []string
	// Service is the fully-qualified service and Method the method's
	// name; with either empty the command lists the services.
	Service string
	Method  string
	// Message is the request message as protobuf JSON; blank sends an
	// empty message.
	Message string
	// Metadata are the enabled rows, keys in lower case; values of "-bin"
	// keys are standard Base64.
	Metadata []Header
}

// Masked returns r with the credentials of its authorization and
// proxy-authorization metadata, and every secret in a value or the
// message, replaced by Mask, and whether anything was (PRV-09, GEN-13).
func (r GRPCRequest) Masked(secrets []string) (GRPCRequest, bool) {
	m := newMasker(secrets)
	out := r
	out.Metadata = m.headers(r.Metadata, false)
	out.Message = m.text(r.Message)
	return out, m.changed
}

// Grpcurl returns r as a grpcurl command for a POSIX shell (GEN-10): the
// options one per continued line, then the address and the method
// (pkg.Service/Method) on the last line, every word quoted with Quote.
//
// -plaintext for a call without TLS, -insecure without verification;
// -proto and -import-path for a .proto setup (each file relative to the
// first import path holding it, else to its own folder, added as an
// import path, since grpcurl needs import paths for absolute files), and
// none for reflection; -H 'key: value' per metadata row ("-bin" values as
// Base64, which grpcurl decodes); -d with the message unless blank;
// -connect-timeout and -max-time (the deadline) in seconds. Without a
// method it lists the services ("list"). grpcurl speaks native gRPC
// only: for gRPC-Web, and for gzip, which grpcurl does not send, the
// command starts with a "#" comment saying so. It fails with CodeNulByte
// for a NUL byte.
func Grpcurl(r GRPCRequest) (string, error) {
	var notes []string
	switch strings.TrimSpace(r.Transport) {
	case "", "grpc":
	default:
		notes = append(notes, "# grpcurl calls over native gRPC (HTTP/2), not gRPC-Web: the server must accept both.")
	}
	if strings.TrimSpace(r.Compression) == "gzip" {
		notes = append(notes, "# grpcurl sends its messages uncompressed (no gzip).")
	}

	var lines [][]string
	var err error
	add := func(opt, value, field string) {
		if err != nil {
			return
		}
		var w string
		if w, err = quoteArg(value, field); err == nil {
			lines = append(lines, []string{opt, w})
		}
	}
	switch {
	case !r.TLS:
		lines = append(lines, []string{"-plaintext"})
	case r.Insecure:
		lines = append(lines, []string{"-insecure"})
	}
	importPaths, files := protoArgs(r.ProtoFiles, r.ImportPaths)
	for _, p := range importPaths {
		add("-import-path", p, "import path")
	}
	for _, f := range files {
		add("-proto", f, ".proto file")
	}
	for _, h := range r.Metadata {
		v := h.Name + ": " + h.Value
		if h.Value == "" {
			v = h.Name + ":"
		}
		add("-H", v, "metadata "+h.Name)
	}
	if strings.TrimSpace(r.Message) != "" {
		add("-d", r.Message, "message")
	}
	if r.ConnectTimeout > 0 {
		lines = append(lines, []string{"-connect-timeout", seconds(r.ConnectTimeout)})
	}
	if r.Deadline > 0 {
		lines = append(lines, []string{"-max-time", seconds(r.Deadline)})
	}
	addr, aerr := quoteArg(grpcAddress(r.Address, r.TLS), "address")
	if err == nil {
		err = aerr
	}
	symbol := "list"
	if strings.TrimSpace(r.Service) != "" && strings.TrimSpace(r.Method) != "" {
		var serr error
		symbol, serr = quoteArg(strings.TrimSpace(r.Service)+"/"+strings.TrimSpace(r.Method), "method")
		if err == nil {
			err = serr
		}
	}
	if err != nil {
		return "", err
	}
	lines = append(lines, []string{addr, symbol})

	var b strings.Builder
	for _, n := range notes {
		b.WriteString(n + "\n")
	}
	b.WriteString("grpcurl")
	for i, words := range lines {
		if i == 0 {
			b.WriteString(" ")
		} else {
			b.WriteString(" \\\n  ")
		}
		b.WriteString(strings.Join(words, " "))
	}
	return b.String(), nil
}

// grpcAddress is address with the port the transport dials when it has
// none: 443 with TLS, else 80. grpcurl needs the port.
func grpcAddress(address string, tls bool) string {
	address = strings.TrimSpace(address)
	if address == "" {
		return address
	}
	if _, _, err := net.SplitHostPort(address); err == nil {
		return address
	}
	port := "80"
	if tls {
		port = "443"
	}
	return net.JoinHostPort(strings.Trim(address, "[]"), port)
}

// protoArgs returns the -import-path and -proto values of a setup: the
// import paths as given, and each file relative to the first of them that
// holds it; a file no import path holds is named relative to its folder,
// which is added as an import path (grpcurl refuses absolute file names
// without import paths, and resolves imports from them, as PostSonde
// does).
func protoArgs(files, importPaths []string) ([]string, []string) {
	var roots, names []string
	seen := map[string]bool{}
	addRoot := func(p string) {
		if !seen[p] {
			seen[p] = true
			roots = append(roots, p)
		}
	}
	for _, p := range importPaths {
		if p = strings.TrimSpace(p); p != "" {
			addRoot(p)
		}
	}
	given := len(roots)
	for _, f := range files {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		name := ""
		for _, root := range roots[:given] {
			if rel, err := filepath.Rel(root, f); err == nil && filepath.IsAbs(f) && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
				name = filepath.ToSlash(rel)
				break
			}
		}
		if name == "" {
			if filepath.IsAbs(f) {
				addRoot(filepath.Dir(f))
				name = filepath.Base(f)
			} else {
				name = filepath.ToSlash(f)
			}
		}
		names = append(names, name)
	}
	return roots, names
}

// seconds is d in seconds, as few digits as it takes ("1.5").
func seconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', -1, 64)
}
