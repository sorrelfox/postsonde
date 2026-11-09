package grpc

import (
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/core/curl"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/snippet"
)

// grpcurlInput reads a grpcurl command as a POSIX shell and grpcurl do
// (Go flags, then the address and the method; -H "key: value" split at
// the first colon and trimmed) into the call it makes, with each -proto
// file found in the -import-path that holds it.
func grpcurlInput(t *testing.T, cmd string) InvokeInputDTO {
	t.Helper()
	lines := strings.Split(cmd, "\n")
	for len(lines) > 0 && strings.HasPrefix(lines[0], "#") {
		lines = lines[1:]
	}
	toks := curl.Tokenize(strings.Join(lines, "\n"), curl.DialectPOSIX)
	if len(toks.Diagnostics) > 0 {
		t.Fatalf("tokenize: %+v\n%s", toks.Diagnostics, cmd)
	}
	var args []string
	for _, w := range toks.Words {
		args = append(args, w.Value)
	}
	if len(args) == 0 || args[0] != "grpcurl" {
		t.Fatalf("not grpcurl: %q", args)
	}
	in := InvokeInputDTO{RequestID: "req_snippet", TLS: true}
	var protos []string
	i := 1
	val := func() string {
		i++
		if i >= len(args) {
			t.Fatalf("%s without a value", args[i-1])
		}
		return args[i]
	}
	ms := func(s string) *int64 {
		f, err := strconv.ParseFloat(s, 64)
		if err != nil {
			t.Fatal(err)
		}
		v := int64(f * 1000)
		return &v
	}
	for ; i < len(args) && strings.HasPrefix(args[i], "-"); i++ {
		switch args[i] {
		case "-plaintext":
			in.TLS = false
		case "-insecure":
			verify := false
			in.VerifyTLS = &verify
		case "-import-path":
			in.ImportPaths = append(in.ImportPaths, val())
		case "-proto":
			protos = append(protos, val())
		case "-H":
			k, v, _ := strings.Cut(val(), ":")
			in.Metadata = append(in.Metadata, MetadataRowDTO{Name: strings.TrimSpace(k), Value: strings.TrimSpace(v), Enabled: true})
		case "-d":
			in.Message = val()
		case "-connect-timeout":
			in.ConnectTimeoutMs = ms(val())
		case "-max-time":
			in.DeadlineMs = ms(val())
		default:
			t.Fatalf("unknown flag %q", args[i])
		}
	}
	if len(args)-i != 2 {
		t.Fatalf("want address and method, got %q", args[i:])
	}
	in.Address = args[i]
	in.Service, in.Method, _ = strings.Cut(args[i+1], "/")
	for _, p := range protos {
		for _, root := range in.ImportPaths {
			f := filepath.Join(root, filepath.FromSlash(p))
			if _, err := os.Stat(f); err == nil {
				in.Files = append(in.Files, f)
				break
			}
		}
	}
	if len(in.Files) != len(protos) {
		t.Fatalf("-proto %q not found in %q", protos, in.ImportPaths)
	}
	return in
}

// TestSnippetReproducesCall: the grpcurl command of a call, read back as
// grpcurl reads it, makes the same call against the test server (TLS,
// metadata, message, deadline, .proto files); masked, it holds no
// credential.
func TestSnippetReproducesCall(t *testing.T) {
	t.Parallel()
	for _, withTLS := range []bool{false, true} {
		t.Run("tls="+strconv.FormatBool(withTLS), func(t *testing.T) {
			t.Parallel()
			srv := newCallServer(t, withTLS)
			s, rec := newCallService(t)
			in := input(srv.Addr, "Echo")
			in.TLS = withTLS
			if withTLS {
				verify := false
				in.VerifyTLS = &verify
			}
			deadline := int64(5000)
			in.DeadlineMs = &deadline
			in.Message = "{\n  \"text\": \"it's $(id) `x` \\\"q\\\" é\",\n  \"count\": 7\n}"
			in.Metadata = []MetadataRowDTO{
				{Name: "Authorization", Value: "Bearer s3cret-t0ken", Enabled: true},
				{Name: " X-Trace ", Value: "a 'b' $c", Enabled: true},
				{Name: "x-off", Value: "no", Enabled: false},
				{Name: "x-token-bin", Value: "AAEC /w==", Enabled: true},
			}

			masked, err := s.Snippet(t.Context(), SnippetInputDTO{Call: in})
			if err != nil {
				t.Fatal(err)
			}
			if !masked.Masked || masked.Revealed || masked.Language != snippet.LanguageGrpcurl || strings.Contains(masked.Code, "s3cret-t0ken") {
				t.Errorf("masked snippet %+v", masked)
			}
			shown, err := s.Snippet(t.Context(), SnippetInputDTO{Call: in, Language: "grpcurl", RevealSecrets: true})
			if err != nil {
				t.Fatal(err)
			}
			if shown.Masked || !shown.Revealed || !strings.Contains(shown.Code, "Bearer s3cret-t0ken") {
				t.Errorf("revealed snippet %+v", shown)
			}

			want, wantDone := invoke(t, s, rec, in)
			wantMD := srv.lastMetadata()
			got, gotDone := invoke(t, s, rec, grpcurlInput(t, shown.Code))
			gotMD := srv.lastMetadata()
			if wantDone.Status != operation.StatusOK || gotDone.Status != operation.StatusOK {
				t.Fatalf("done %+v / %+v\n%s", wantDone, gotDone, shown.Code)
			}
			if w, g := compactJSON(t, want[2].JSON), compactJSON(t, got[2].JSON); w != g {
				t.Errorf("reply %s, want %s", g, w)
			}
			if want[0].Method != got[0].Method || got[0].DeadlineMs != 5000 {
				t.Errorf("request %+v, want %+v", got[0], want[0])
			}
			for _, key := range []string{"authorization", "x-trace", "x-token-bin", "x-off"} {
				if w, g := strings.Join(wantMD.Get(key), "|"), strings.Join(gotMD.Get(key), "|"); w != g {
					t.Errorf("%s: server got %q, want %q", key, g, w)
				}
			}
			if v := gotMD.Get("x-token-bin"); len(v) != 1 || v[0] != "\x00\x01\x02\xff" {
				t.Errorf("x-token-bin %q", v)
			}
		})
	}
}

func TestSnippetCall(t *testing.T) {
	t.Parallel()
	s, _ := newCallService(t)
	root := s.root()
	in := InvokeInputDTO{Address: "api.test", TLS: true, Transport: "grpc-web", Compression: "gzip",
		Files: []string{"proto/calls.proto"}, Service: "calls.v1.Calls", Method: "Echo", Message: " "}
	got, err := s.Snippet(t.Context(), SnippetInputDTO{Call: in})
	if err != nil {
		t.Fatal(err)
	}
	want := "# grpcurl calls over native gRPC (HTTP/2), not gRPC-Web: the server must accept both.\n" +
		"# grpcurl sends its messages uncompressed (no gzip).\n" +
		"grpcurl -import-path " + snippet.Quote(filepath.Join(root, "proto")) + " \\\n" +
		"  -proto calls.proto \\\n" +
		"  api.test:443 calls.v1.Calls/Echo"
	if got.Code != want || got.Masked || got.Mode != "resolved" || len(got.Unresolved) != 0 {
		t.Errorf("snippet %+v\nwant code:\n%s", got, want)
	}
	// No setup and no method: reflection lists the services.
	got, err = s.Snippet(t.Context(), SnippetInputDTO{Call: InvokeInputDTO{Address: "localhost:50051"}})
	if err != nil || got.Code != "grpcurl -plaintext \\\n  localhost:50051 list" {
		t.Errorf("list: %q %v", got.Code, err)
	}
	if _, err := s.Snippet(t.Context(), SnippetInputDTO{Call: in, Language: "curl"}); codeOf(err) != snippet.CodeUnknownLanguage {
		t.Errorf("curl: %v", err)
	}
	in.Transport = "carrier-pigeon"
	if _, err := s.Snippet(t.Context(), SnippetInputDTO{Call: in}); codeOf(err) != CodeInvalidSettings {
		t.Errorf("transport: %v", err)
	}
	if langs := s.SnippetLanguages(); len(langs) != 1 || langs[0].ID != "grpcurl" || langs[0].Syntax != "shell" {
		t.Errorf("languages %+v", langs)
	}
}

// codeOf is the code of err, an *errs.Error; "" otherwise.
func codeOf(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.Code
	}
	return ""
}
