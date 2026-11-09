package transport

import (
	"bytes"
	"math"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

func TestEncodeTimeout(t *testing.T) {
	t.Parallel()
	tests := []struct {
		d    time.Duration
		want string
	}{
		{0, "0n"},
		{-time.Second, "0n"},
		{1, "1n"},
		{99_999_999, "99999999n"},
		{100_000_000, "100000u"},
		{100_000_001, "100001u"}, // rounded up
		{time.Second, "1000000u"},
		{2 * time.Minute, "120000m"},
		{30 * time.Hour, "108000S"},
		{99_999_999 * time.Second, "99999999S"},
		{100_000_000 * time.Second, "1666667M"},
		{math.MaxInt64, "2562048H"},
	}
	for _, tt := range tests {
		if got := EncodeTimeout(tt.d); got != tt.want {
			t.Errorf("EncodeTimeout(%v) = %q, want %q", tt.d, got, tt.want)
		}
		if tt.d <= 0 {
			continue
		}
		back, err := ParseTimeout(EncodeTimeout(tt.d))
		if err != nil {
			t.Errorf("ParseTimeout(EncodeTimeout(%v)): %v", tt.d, err)
		} else if back < tt.d {
			t.Errorf("ParseTimeout(EncodeTimeout(%v)) = %v, shorter than the original", tt.d, back)
		}
	}
}

func TestParseTimeout(t *testing.T) {
	t.Parallel()
	good := map[string]time.Duration{
		"1H": time.Hour, "2M": 2 * time.Minute, "3S": 3 * time.Second,
		"4m": 4 * time.Millisecond, "5u": 5 * time.Microsecond, "6n": 6,
		"99999999H": math.MaxInt64,
	}
	for in, want := range good {
		if got, err := ParseTimeout(in); err != nil || got != want {
			t.Errorf("ParseTimeout(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	for _, in := range []string{"", "S", "1", "1x", "123456789S", "-1S", "1 S", "+1S"} {
		if _, err := ParseTimeout(in); err == nil {
			t.Errorf("ParseTimeout(%q) succeeded", in)
		}
	}
}

func TestStatusMessageEncoding(t *testing.T) {
	t.Parallel()
	tests := []struct{ raw, enc string }{
		{"plain text", "plain text"},
		{"100%", "100%25"},
		{"line\nbreak", "line%0Abreak"},
		{"héllo ✓", "h%C3%A9llo %E2%9C%93"},
		{"\x00\x7f~", "%00%7F~"},
	}
	for _, tt := range tests {
		if got := EncodeStatusMessage(tt.raw); got != tt.enc {
			t.Errorf("EncodeStatusMessage(%q) = %q, want %q", tt.raw, got, tt.enc)
		}
		if got := DecodeStatusMessage(tt.enc); got != tt.raw {
			t.Errorf("DecodeStatusMessage(%q) = %q, want %q", tt.enc, got, tt.raw)
		}
	}
	lenient := map[string]string{
		"%":         "%",
		"%4":        "%4",
		"%zz ok":    "%zz ok",
		"a%41%":     "aA%",
		"%e2%9c%93": "✓",
	}
	for in, want := range lenient {
		if got := DecodeStatusMessage(in); got != want {
			t.Errorf("DecodeStatusMessage(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestParseStatus(t *testing.T) {
	t.Parallel()
	h := http.Header{}
	if _, found := parseStatus(h); found {
		t.Fatal("status found in empty header")
	}
	h.Set("Grpc-Status", "5")
	h.Set("Grpc-Message", "no%20such%20thing")
	h.Set("Grpc-Status-Details-Bin", EncodeBinaryValue([]byte{1, 2, 3, 4}))
	st, found := parseStatus(h)
	if !found || st.Code != NotFound || st.Message != "no such thing" || !bytes.Equal(st.Details, []byte{1, 2, 3, 4}) {
		t.Fatalf("parseStatus = %+v, %v", st, found)
	}
	h.Set("Grpc-Status", "five")
	if st, _ := parseStatus(h); st.Code != Internal {
		t.Fatalf("malformed grpc-status gave %v, want INTERNAL", st.Code)
	}
	h.Set("Grpc-Status", "42")
	if st, _ := parseStatus(h); st.Code != 42 || st.Code.String() != "CODE(42)" {
		t.Fatalf("unknown code = %v", st.Code)
	}
}

func TestCodeForHTTPStatus(t *testing.T) {
	t.Parallel()
	want := map[int]Code{400: Internal, 401: Unauthenticated, 403: PermissionDenied, 404: Unimplemented,
		429: Unavailable, 502: Unavailable, 503: Unavailable, 504: Unavailable, 500: Unknown, 418: Unknown}
	for status, code := range want {
		if got := CodeForHTTPStatus(status); got != code {
			t.Errorf("CodeForHTTPStatus(%d) = %v, want %v", status, got, code)
		}
	}
}

func TestBinaryValues(t *testing.T) {
	t.Parallel()
	for _, in := range []string{"AQID", "AQIDBA", "AQIDBA=="} {
		if _, err := DecodeBinaryValue(in); err != nil {
			t.Errorf("DecodeBinaryValue(%q): %v", in, err)
		}
	}
	if got := EncodeBinaryValue([]byte{1, 2, 3, 4}); got != "AQIDBA" {
		t.Errorf("EncodeBinaryValue = %q, want unpadded AQIDBA", got)
	}
}

func TestValidContentType(t *testing.T) {
	t.Parallel()
	for ct, want := range map[string]bool{
		"application/grpc": true, "application/grpc+proto": true, "Application/GRPC;charset=x": true,
		"application/grpcx": false, "application/json": false, "": false, "application/grpc-web": false,
	} {
		if got := validContentType(ct); got != want {
			t.Errorf("validContentType(%q) = %v, want %v", ct, got, want)
		}
	}
}

func TestValidateMetadata(t *testing.T) {
	t.Parallel()
	ok := Metadata{{"x-id", "abc 123 ~"}, {"trace.id_2", ""}, {"blob-bin", "\x00\xff\n"}, {"x-id", "again"}}
	if err := validateMetadata(ok); err != nil {
		t.Fatalf("valid metadata: %v", err)
	}
	bad := []struct {
		md   MDEntry
		code string
	}{
		{MDEntry{"", "v"}, CodeInvalidMetadata},
		{MDEntry{"X-Upper", "v"}, CodeInvalidMetadata},
		{MDEntry{"sp ace", "v"}, CodeInvalidMetadata},
		{MDEntry{":path", "v"}, CodeInvalidMetadata},
		{MDEntry{"x-val", "line\nbreak"}, CodeInvalidMetadata},
		{MDEntry{"x-val", "é"}, CodeInvalidMetadata},
		{MDEntry{"grpc-timeout", "1S"}, CodeReservedMetadata},
		{MDEntry{"grpc-custom", "v"}, CodeReservedMetadata},
		{MDEntry{"content-type", "v"}, CodeReservedMetadata},
		{MDEntry{"te", "trailers"}, CodeReservedMetadata},
		{MDEntry{"user-agent", "v"}, CodeReservedMetadata},
		{MDEntry{"connection", "close"}, CodeReservedMetadata},
	}
	for _, tt := range bad {
		err := validateMetadata(Metadata{{"fine", "ok"}, tt.md})
		e, isErr := err.(*errs.Error)
		if !isErr || e.Kind != errs.InvalidArgument || e.Code != tt.code || e.Details["row"] != "1" {
			t.Errorf("validateMetadata(%q: %q) = %v, want %s at row 1", tt.md.Key, tt.md.Value, err, tt.code)
		}
	}
}

func TestMetadataFromHeader(t *testing.T) {
	t.Parallel()
	h := http.Header{
		"X-B":      {"2", "3"},
		"X-A":      {"1"},
		"Data-Bin": {EncodeBinaryValue([]byte{0, 1}) + ", " + "AAI=", "!!bad!!"},
	}
	got := metadataFromHeader(h)
	want := Metadata{
		{"data-bin", "\x00\x01"}, {"data-bin", "\x00\x02"}, {"data-bin", "!!bad!!"},
		{"x-a", "1"}, {"x-b", "2"}, {"x-b", "3"},
	}
	if !slices.Equal(got, want) {
		t.Fatalf("metadataFromHeader = %q, want %q", got, want)
	}
	if v := got.Get("X-B"); !slices.Equal(v, []string{"2", "3"}) {
		t.Fatalf("Get = %q", v)
	}
}

func TestValidateMethod(t *testing.T) {
	t.Parallel()
	for _, m := range []string{"/pkg.Service/Method", "/S/M", "/a.b.c.D/E_f"} {
		if err := validateMethod(m); err != nil {
			t.Errorf("validateMethod(%q): %v", m, err)
		}
	}
	for _, m := range []string{"", "pkg.Service/Method", "/Service", "/Service/", "//Method", "/a/b/c", "/a b/c", "/a/b?x", "/a/%62"} {
		if err := validateMethod(m); err == nil {
			t.Errorf("validateMethod(%q) succeeded", m)
		}
	}
}
