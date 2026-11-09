package transport

import (
	"net/http"
	"slices"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// MDEntry is one metadata key and value. For keys ending in "-bin" the
// value is the binary data itself; the transport base64-encodes it on the
// wire and decodes it on receipt.
type MDEntry struct {
	Key   string
	Value string
}

// Metadata is an ordered list of entries; a key may repeat.
type Metadata []MDEntry

// Get returns the values of key, in order. key is compared
// case-insensitively.
func (md Metadata) Get(key string) []string {
	var out []string
	for _, e := range md {
		if strings.EqualFold(e.Key, key) {
			out = append(out, e.Value)
		}
	}
	return out
}

// IsBinaryKey reports whether key names binary metadata ("-bin" suffix).
func IsBinaryKey(key string) bool {
	return len(key) > len(binarySuffix) && strings.HasSuffix(strings.ToLower(key), binarySuffix)
}

// reservedHeaders cannot be set through metadata: the transport sets them
// from CallOptions, or HTTP/2 forbids them.
var reservedHeaders = []string{
	HeaderContentType, HeaderTE, HeaderUserAgent,
	"host", "connection", "keep-alive", "proxy-connection", "transfer-encoding", "upgrade",
	"content-length", "trailer",
}

// ReservedKey reports whether key may not be sent as metadata: the
// "grpc-" prefix (reserved by the protocol), the headers the transport
// sets itself (content-type, te, user-agent) and connection-level HTTP
// fields.
func ReservedKey(key string) bool {
	k := strings.ToLower(key)
	return strings.HasPrefix(k, "grpc-") || slices.Contains(reservedHeaders, k)
}

// ValidateMetadata checks md as a call does before sending it (see
// validateMetadata); the gRPC mock server checks the metadata it is
// configured to send with it. Errors are grpctransport.invalid_metadata
// and grpctransport.reserved_metadata with the details row and key.
func ValidateMetadata(md Metadata) error { return validateMetadata(md) }

// validateMetadata checks md before a call. Keys must be 1+ characters of
// a-z, 0-9, '-', '_' and '.' (lower case: the spec's Header-Name) and not
// reserved; "-bin" values are any bytes; other values must be printable
// ASCII (space to '~').
func validateMetadata(md Metadata) error {
	for i, e := range md {
		bad := func(code, msg string) error {
			return errs.New(errs.InvalidArgument, msg).
				WithCode(code).
				WithDetail("row", strconv.Itoa(i)).
				WithDetail("key", e.Key)
		}
		if e.Key == "" {
			return bad(CodeInvalidMetadata, "A metadata key is empty.")
		}
		for j := 0; j < len(e.Key); j++ {
			c := e.Key[j]
			if !('a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '_' || c == '.') {
				return bad(CodeInvalidMetadata, "The metadata key "+strconv.Quote(e.Key)+" may only contain lower-case letters, digits, '-', '_' and '.'.")
			}
		}
		if ReservedKey(e.Key) {
			return bad(CodeReservedMetadata, "The metadata key "+strconv.Quote(e.Key)+" is reserved and set by the transport.")
		}
		if IsBinaryKey(e.Key) {
			continue
		}
		for j := 0; j < len(e.Value); j++ {
			if c := e.Value[j]; c < 0x20 || c > 0x7E {
				return bad(CodeInvalidMetadata, "The value of metadata key "+strconv.Quote(e.Key)+" may only contain printable ASCII characters; use a -bin key for binary data.")
			}
		}
	}
	return nil
}

// addMetadata writes md into h, keeping the order of repeated keys.
// Header maps do not keep the order between different keys, and neither
// does the HTTP/2 client when it writes them.
func addMetadata(h http.Header, md Metadata) {
	for _, e := range md {
		v := e.Value
		if IsBinaryKey(e.Key) {
			v = EncodeBinaryValue([]byte(v))
		}
		h[e.Key] = append(h[e.Key], v)
	}
}

// metadataFromHeader converts received header fields into Metadata: keys
// in lower case and sorted, values in the order received. "-bin" values
// are base64-decoded, each comma-separated element as its own entry; a
// value that does not decode is kept as received.
func metadataFromHeader(h http.Header) Metadata {
	if len(h) == 0 {
		return nil
	}
	keys := make([]string, 0, len(h))
	for k := range h {
		keys = append(keys, k)
	}
	slices.SortFunc(keys, func(a, b string) int { return strings.Compare(strings.ToLower(a), strings.ToLower(b)) })
	md := make(Metadata, 0, len(h))
	for _, k := range keys {
		key := strings.ToLower(k)
		for _, v := range h[k] {
			if !IsBinaryKey(key) {
				md = append(md, MDEntry{Key: key, Value: v})
				continue
			}
			for part := range strings.SplitSeq(v, ",") {
				part = strings.TrimSpace(part)
				if b, err := DecodeBinaryValue(part); err == nil {
					md = append(md, MDEntry{Key: key, Value: string(b)})
				} else {
					md = append(md, MDEntry{Key: key, Value: part})
				}
			}
		}
	}
	return md
}
