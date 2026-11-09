package snippet

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/curl"
)

// grpcCases are the gRPC calls of the grpcurl goldens.
func grpcCases() []struct {
	name    string
	r       GRPCRequest
	secrets []string
} {
	type c = struct {
		name    string
		r       GRPCRequest
		secrets []string
	}
	return []c{
		{name: "unary_plaintext", r: GRPCRequest{Address: "localhost:50051", Service: "shop.v1.Orders", Method: "Get",
			Message: `{"id": "o-1"}`, Metadata: []Header{{"authorization", "Bearer t0ken-s3cret"}, {"x-trace", "abc"}}},
			secrets: []string{"t0ken-s3cret"}},
		{name: "tls_proto", r: GRPCRequest{Address: "api.test:8443", TLS: true, Insecure: true,
			ProtoFiles:  []string{"/work/protos/shop/v1/orders.proto", "/elsewhere/my protos/extra.proto", "/elsewhere/my protos/more.proto"},
			ImportPaths: []string{"/work/protos", "/work/third_party"},
			Service:     "shop.v1.Orders", Method: "List", Message: "{\n  \"page\": 2\n}",
			ConnectTimeout: 3 * time.Second, Deadline: 1500 * time.Millisecond}},
		{name: "tls_verified", r: GRPCRequest{Address: "api.test", TLS: true, Service: "a.B", Method: "C"}},
		{name: "reflection_list", r: GRPCRequest{Address: "[::1]"}},
		{name: "grpc_web_gzip", r: GRPCRequest{Address: "localhost:8080", Transport: "grpc-web-text", Compression: "gzip",
			Service: "echo.Echo", Method: "Say", Message: "{}"}},
		{name: "binary_metadata", r: GRPCRequest{Address: "localhost:50051", Service: "a.B", Method: "C",
			Metadata: []Header{{"trace-bin", "AAEC"}, {"x-empty", ""}}}},
		{name: "quoting", r: GRPCRequest{Address: "localhost:50051", Service: "a.B", Method: "C",
			Message:  `{"text": "it's $(id) ` + "`tick`" + ` \"q\" é ✓", "tab": "a\tb"}`,
			Metadata: []Header{{"x-tricky", trickyText}, {"x-control", "a\x1bb"}}}},
		{name: "nul_byte", r: GRPCRequest{Address: "localhost:50051", Service: "a.B", Method: "C",
			Metadata: []Header{{"x-nul", "a\x00b"}}}},
	}
}

// wsCases are the handshakes of the websocat and wscat goldens.
func wsCases() []struct {
	name    string
	r       WebSocketRequest
	secrets []string
} {
	type c = struct {
		name    string
		r       WebSocketRequest
		secrets []string
	}
	return []c{
		{name: "full", r: WebSocketRequest{URL: "wss://echo.test/socket?room=1&x=a%20b",
			Headers:      []Header{{"Authorization", "Bearer t0ken-s3cret"}, {"Origin", "https://app.test"}, {"X-Tricky", trickyText}},
			Subprotocols: []string{"chat.v1", "graphql-ws"}, Insecure: true,
			PingInterval: 30 * time.Second, PongTimeout: 10 * time.Second}, secrets: []string{"t0ken-s3cret"}},
		{name: "minimal", r: WebSocketRequest{URL: "ws://localhost:8080/"}},
		{name: "duplicate_headers", r: WebSocketRequest{URL: "ws://localhost:8080/ws",
			Headers: []Header{{"Cookie", "a=1"}, {"X-A", "1"}, {"cookie", "b=2"}, {"x-a", "2"}, {"X-Empty", ""}}}},
		{name: "ping_rounding", r: WebSocketRequest{URL: "ws://localhost:8080/ws", Subprotocols: []string{"v1"},
			PingInterval: 1500 * time.Millisecond}},
		{name: "templated", r: WebSocketRequest{URL: "{{wsBase}}/live?token={{token}}",
			Headers: []Header{{"Authorization", "Bearer {{token}}"}}, Subprotocols: []string{"{{proto}}"}}},
		{name: "nul_byte", r: WebSocketRequest{URL: "ws://localhost:8080/ws", Headers: []Header{{"X-Nul", "a\x00b"}}}},
	}
}

// checkGolden compares got with testdata/<dir>/<name>.golden (rewritten
// with -update), and checks it holds none of secrets.
func checkGolden(t *testing.T, dir, name, got string, secrets []string) {
	t.Helper()
	got += "\n"
	for _, s := range secrets {
		if strings.Contains(got, s) {
			t.Errorf("masked code holds %q:\n%s", s, got)
		}
	}
	path := filepath.Join("testdata", dir, name+".golden")
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(got), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update)", err)
	}
	if got != string(want) {
		t.Errorf("code:\n%s\nwant:\n%s", got, want)
	}
}

// codeText is code, or err as "error <code>: <message>".
func codeText(code string, err error) string {
	if err != nil {
		return "error " + codeOf(err) + ": " + err.Error()
	}
	return code
}

// TestRealtimeLanguagesGolden: the code of every gRPC language for every
// gRPC case and of every WebSocket language for every WebSocket case,
// masked when the case has secrets, in testdata/<language>/<name>.golden
// (regenerate with -run TestRealtimeLanguagesGolden -update).
func TestRealtimeLanguagesGolden(t *testing.T) {
	for _, lang := range GRPCLanguages() {
		for _, c := range grpcCases() {
			t.Run(lang.ID+"/"+c.name, func(t *testing.T) {
				r := c.r
				if c.secrets != nil {
					r, _ = r.Masked(c.secrets)
				}
				checkGolden(t, lang.ID, c.name, codeText(lang.Generate(r)), c.secrets)
			})
		}
	}
	for _, lang := range WebSocketLanguages() {
		for _, c := range wsCases() {
			t.Run(lang.ID+"/"+c.name, func(t *testing.T) {
				r := c.r
				if c.secrets != nil {
					r, _ = r.Masked(c.secrets, false)
				}
				checkGolden(t, lang.ID, c.name, codeText(lang.Generate(r)), c.secrets)
			})
		}
	}
}

func TestRealtimeLanguageByID(t *testing.T) {
	if l, err := GRPCLanguageByID(""); err != nil || l.ID != LanguageGrpcurl {
		t.Errorf("GRPCLanguageByID(\"\") = %v, %v", l.ID, err)
	}
	if l, err := WebSocketLanguageByID(""); err != nil || l.ID != LanguageWebsocat {
		t.Errorf("WebSocketLanguageByID(\"\") = %v, %v", l.ID, err)
	}
	if l, err := WebSocketLanguageByID("wscat"); err != nil || l.ID != "wscat" {
		t.Errorf("WebSocketLanguageByID(wscat) = %v, %v", l.ID, err)
	}
	if _, err := GRPCLanguageByID("curl"); codeOf(err) != CodeUnknownLanguage {
		t.Errorf("GRPCLanguageByID(curl): %v", err)
	}
	if _, err := WebSocketLanguageByID("grpcurl"); codeOf(err) != CodeUnknownLanguage {
		t.Errorf("WebSocketLanguageByID(grpcurl): %v", err)
	}
}

func TestRealtimeMasked(t *testing.T) {
	g, changed := GRPCRequest{Metadata: []Header{{"authorization", "Bearer abc"}, {"x-key", "k-123"}}, Message: `{"k":"k-123"}`}.
		Masked([]string{"k-123"})
	if !changed || g.Metadata[0].Value != "Bearer "+Mask || g.Metadata[1].Value != Mask || g.Message != `{"k":"`+Mask+`"}` {
		t.Errorf("gRPC masked: %+v %v", g, changed)
	}
	if _, changed := (GRPCRequest{Metadata: []Header{{"x-a", "1"}}}).Masked(nil); changed {
		t.Error("nothing to mask, but changed")
	}
	w := WebSocketRequest{URL: "wss://a.test/?k=k-123", Headers: []Header{{"Authorization", "Bearer {{token}}"}, {"Proxy-Authorization", "Basic dTpw"}},
		Subprotocols: []string{"k-123"}}
	m, _ := w.Masked([]string{"k-123"}, false)
	if m.URL != "wss://a.test/?k="+Mask || m.Headers[0].Value != "Bearer "+Mask || m.Headers[1].Value != "Basic "+Mask || m.Subprotocols[0] != Mask {
		t.Errorf("ws masked: %+v", m)
	}
	m, _ = w.Masked(nil, true)
	if m.Headers[0].Value != "Bearer {{token}}" || m.Headers[1].Value != "Basic "+Mask {
		t.Errorf("ws templated: %+v", m)
	}
}

func TestWebSocketURL(t *testing.T) {
	for _, tc := range []struct{ in, url, auth string }{
		{"wss://a.test/x?y=1", "wss://a.test/x?y=1", ""},
		{"wss://ann:p%40ss@a.test/x", "wss://a.test/x", "Basic YW5uOnBAc3M="},
		{"ws://ann@a.test", "ws://a.test", "Basic YW5uOg=="},
		{"ws://a.test/p@th", "ws://a.test/p@th", ""},
		{"ws://u:p@a.test/a b?q=é", "ws://a.test/a%20b?q=%C3%A9", "Basic dTpw"},
		{"ws://bad%zz@a.test", "ws://bad%zz@a.test", ""},
		{"no scheme", "no scheme", ""},
	} {
		if u, a := WebSocketURL(tc.in); u != tc.url || a != tc.auth {
			t.Errorf("WebSocketURL(%q) = %q, %q; want %q, %q", tc.in, u, a, tc.url, tc.auth)
		}
	}
	got := WithCookies([]Header{{"X", "1"}, {"Cookie", "a=1"}}, "b=2; c=3")
	if want := []Header{{"X", "1"}, {"Cookie", "a=1; b=2; c=3"}}; !reflect.DeepEqual(got, want) {
		t.Errorf("WithCookies = %v", got)
	}
	if got := WithCookies([]Header{{"X", "1"}}, "b=2"); !reflect.DeepEqual(got, []Header{{"X", "1"}, {"Cookie", "b=2"}}) {
		t.Errorf("WithCookies new row = %v", got)
	}
}

// argv reads cmd back as a POSIX shell does (curl's tokenizer), after its
// "#" comment lines.
func argv(t *testing.T, cmd string) []string {
	t.Helper()
	lines := strings.Split(cmd, "\n")
	for len(lines) > 0 && strings.HasPrefix(lines[0], "#") {
		lines = lines[1:]
	}
	toks := curl.Tokenize(strings.Join(lines, "\n"), curl.DialectPOSIX)
	for _, d := range toks.Diagnostics {
		t.Fatalf("tokenize: %s %s\n%s", d.Code, d.Message, cmd)
	}
	var out []string
	for _, w := range toks.Words {
		out = append(out, w.Value)
	}
	return out
}

// parseGrpcurl reads grpcurl's argv as grpcurl does (Go flags, then the
// address and the symbol; -H "key: value" split at the first colon and
// trimmed, keys in lower case) into a GRPCRequest, with the -proto files
// joined to the import path that holds them in files.
func parseGrpcurl(t *testing.T, args []string, exists func(string) bool) GRPCRequest {
	t.Helper()
	if len(args) == 0 || args[0] != "grpcurl" {
		t.Fatalf("not grpcurl: %q", args)
	}
	r := GRPCRequest{TLS: true}
	var protos []string
	i := 1
	val := func() string {
		i++
		if i >= len(args) {
			t.Fatalf("%s without a value", args[i-1])
		}
		return args[i]
	}
	dur := func(s string) time.Duration {
		d, err := time.ParseDuration(s + "s")
		if err != nil {
			t.Fatalf("duration %q: %v", s, err)
		}
		return d
	}
	for ; i < len(args) && strings.HasPrefix(args[i], "-"); i++ {
		switch args[i] {
		case "-plaintext":
			r.TLS = false
		case "-insecure":
			r.Insecure = true
		case "-import-path":
			r.ImportPaths = append(r.ImportPaths, val())
		case "-proto":
			protos = append(protos, val())
		case "-H":
			k, v, _ := strings.Cut(val(), ":")
			r.Metadata = append(r.Metadata, Header{strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)})
		case "-d":
			r.Message = val()
		case "-connect-timeout":
			r.ConnectTimeout = dur(val())
		case "-max-time":
			r.Deadline = dur(val())
		default:
			t.Fatalf("unknown flag %q", args[i])
		}
	}
	if len(args)-i != 2 {
		t.Fatalf("want address and symbol, got %q", args[i:])
	}
	r.Address = args[i]
	if args[i+1] != "list" {
		r.Service, r.Method, _ = strings.Cut(args[i+1], "/")
	}
	for _, p := range protos {
		found := ""
		for _, root := range r.ImportPaths {
			if f := filepath.Join(root, filepath.FromSlash(p)); exists(f) {
				found = f
				break
			}
		}
		if found == "" {
			t.Fatalf("-proto %s is in no import path %q", p, r.ImportPaths)
		}
		r.ProtoFiles = append(r.ProtoFiles, found)
	}
	return r
}

// TestGrpcurlRoundTrip reads every grpcurl command back as grpcurl would
// and checks it makes the same call: address with its port, TLS,
// metadata, message, method, timeouts and the .proto files.
func TestGrpcurlRoundTrip(t *testing.T) {
	for _, c := range grpcCases() {
		t.Run(c.name, func(t *testing.T) {
			cmd, err := Grpcurl(c.r)
			if codeOf(err) == CodeNulByte {
				t.Skip(err)
			}
			if err != nil {
				t.Fatal(err)
			}
			want := c.r
			files := map[string]bool{}
			for _, f := range want.ProtoFiles {
				files[f] = true
			}
			got := parseGrpcurl(t, argv(t, cmd), func(f string) bool { return files[f] })
			want.Address = grpcAddress(want.Address, want.TLS)
			want.Insecure = want.Insecure && want.TLS
			want.Transport, want.Compression = "", ""
			if want.Message == "" {
				want.Message = got.Message
			}
			for i, h := range want.Metadata {
				want.Metadata[i].Value = strings.TrimSpace(h.Value)
			}
			got.ImportPaths = want.ImportPaths // checked through the files
			if !reflect.DeepEqual(got, want) {
				t.Errorf("round trip differs\ncommand:\n%s\ngot:  %+v\nwant: %+v", cmd, got, want)
			}
		})
	}
}

// parseWebSocket reads a websocat or wscat argv as the tool does into a
// WebSocketRequest: -H "Name: value" split at the first colon and
// trimmed; websocat's --protocol is the Sec-WebSocket-Protocol value.
func parseWebSocket(t *testing.T, args []string) WebSocketRequest {
	t.Helper()
	var r WebSocketRequest
	if len(args) == 0 {
		t.Fatal("no command")
	}
	tool := args[0]
	for i := 1; i < len(args); i++ {
		a := args[i]
		val := func() string {
			i++
			if i >= len(args) {
				t.Fatalf("%s without a value", a)
			}
			return args[i]
		}
		secs := func() time.Duration {
			d, err := time.ParseDuration(val() + "s")
			if err != nil {
				t.Fatal(err)
			}
			return d
		}
		switch {
		case a == "-H":
			k, v, _ := strings.Cut(val(), ":")
			r.Headers = append(r.Headers, Header{strings.TrimSpace(k), strings.TrimSpace(v)})
		case tool == "websocat" && a == "--protocol":
			for p := range strings.SplitSeq(val(), ",") {
				r.Subprotocols = append(r.Subprotocols, strings.TrimSpace(p))
			}
		case tool == "websocat" && a == "-k":
			r.Insecure = true
		case tool == "websocat" && a == "--ping-interval":
			r.PingInterval = secs()
		case tool == "websocat" && a == "--ping-timeout":
			r.PongTimeout = secs()
		case tool == "wscat" && a == "-c":
			r.URL = val()
		case tool == "wscat" && a == "-s":
			r.Subprotocols = append(r.Subprotocols, val())
		case tool == "wscat" && a == "-n":
			r.Insecure = true
		case tool == "websocat" && !strings.HasPrefix(a, "-") && r.URL == "":
			r.URL = a
		default:
			t.Fatalf("%s: unexpected argument %q", tool, a)
		}
	}
	return r
}

// TestWebSocketRoundTrip reads every websocat and wscat command back as
// the tool would and checks it makes the same handshake (wscat with the
// rows of one name joined, pings rounded up to whole seconds and left out
// by wscat).
func TestWebSocketRoundTrip(t *testing.T) {
	for _, lang := range WebSocketLanguages() {
		for _, c := range wsCases() {
			t.Run(lang.ID+"/"+c.name, func(t *testing.T) {
				cmd, err := lang.Generate(c.r)
				if codeOf(err) == CodeNulByte {
					t.Skip(err)
				}
				if err != nil {
					t.Fatal(err)
				}
				got := parseWebSocket(t, argv(t, cmd))
				want := c.r
				switch lang.ID {
				case "wscat":
					want.Headers = joinedHeaders(want.Headers)
					want.PingInterval, want.PongTimeout = 0, 0
				default:
					if want.PingInterval%time.Second != 0 {
						want.PingInterval = want.PingInterval.Truncate(time.Second) + time.Second
					}
				}
				if !reflect.DeepEqual(got, want) {
					t.Errorf("round trip differs\ncommand:\n%s\ngot:  %+v\nwant: %+v", cmd, got, want)
				}
			})
		}
	}
}
