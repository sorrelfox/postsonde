package serverdef

import (
	"bytes"
	"os"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/servers"
	"github.com/sorrelfox/postsonde/internal/core/servers/echo"
	"github.com/sorrelfox/postsonde/internal/core/servers/graphqlmock"
	"github.com/sorrelfox/postsonde/internal/core/servers/grpcmock"
	"github.com/sorrelfox/postsonde/internal/core/servers/mockhttp"
	"github.com/sorrelfox/postsonde/internal/core/servers/sioserver"
	"github.com/sorrelfox/postsonde/internal/core/servers/sseserver"
	"github.com/sorrelfox/postsonde/internal/core/servers/tcpserver"
	"github.com/sorrelfox/postsonde/internal/core/servers/udpserver"
	"github.com/sorrelfox/postsonde/internal/core/servers/webhook"
	"github.com/sorrelfox/postsonde/internal/core/servers/wsserver"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

func manager() *servers.Manager {
	return servers.NewManager(servers.Options{Types: []servers.Type{
		echo.New(), mockhttp.New(mockhttp.Options{}), wsserver.New(wsserver.Options{}), tcpserver.New(tcpserver.Options{}), webhook.New(webhook.Options{}),
		sseserver.New(sseserver.Options{}), udpserver.New(udpserver.Options{}), grpcmock.New(grpcmock.Options{}),
		sioserver.New(sioserver.Options{}), graphqlmock.New(graphqlmock.Options{}),
	}})
}

// roundTrip loads a folder file, turns its server into a checked Spec and
// back, and returns the folder file written again.
func roundTrip(t *testing.T, m *servers.Manager, data []byte) ([]byte, servers.Spec) {
	t.Helper()
	folder, err := projectfile.DecodeFolder(data)
	if err != nil {
		t.Fatal(err)
	}
	spec, err := m.Check(ToSpec(folder.Server))
	if err != nil {
		t.Fatalf("Check: %v", err)
	}
	label := ""
	for _, typ := range m.Types() {
		if typ.Name() == spec.Type {
			label = typ.Label()
		}
	}
	folder.Server = FromSpec(folder.Server, spec, label)
	out, err := projectfile.Marshal(folder)
	if err != nil {
		t.Fatal(err)
	}
	return out, spec
}

// TestExamplesRoundTrip: the example server configs of docs/file-format.md
// load into a Spec the Manager accepts and are written back byte for byte.
func TestExamplesRoundTrip(t *testing.T) {
	t.Parallel()
	m := manager()
	for _, name := range []string{"folder_server_mock", "folder_server_websocket", "folder_server_sse", "folder_server_udp", "folder_server_grpc_mock", "folder_server_socketio", "folder_server_graphql_mock"} {
		t.Run(name, func(t *testing.T) {
			data, err := os.ReadFile(filepath.Join("..", "..", "storage", "projectfile", "testdata", "examples", name+".golden"))
			if err != nil {
				t.Fatal(err)
			}
			out, spec := roundTrip(t, m, data)
			if !bytes.Equal(out, data) {
				t.Errorf("round trip changed the file:\n%s\nwant\n%s", out, data)
			}
			switch c := spec.Config.(type) {
			case mockhttp.Config:
				if len(c.Routes) != 2 || c.Routes[1].Delay.MaxMs != 900 || c.Routes[0].Headers[0].Name != "X-Mock" || !c.Routes[0].Enabled {
					t.Errorf("mock config = %+v", c)
				}
			case wsserver.Config:
				if c.Path != "/ws" || !c.Echo || !reflect.DeepEqual(c.Subprotocols, []string{"chat"}) {
					t.Errorf("websocket config = %+v", c)
				}
				wantRules := []wsserver.Rule{
					{Enabled: true, Match: wsserver.MatchExact, Pattern: "ping", Reply: "pong"},
					{Enabled: true, Match: wsserver.MatchRegex, Pattern: `^get (?P<id>\d+)$`, Reply: `{"id": ${id}, "name": "Zoë"}`, DelayMs: 200},
					{Enabled: false, Match: wsserver.MatchContains, Pattern: "debug", Reply: "debug is off"},
				}
				if !reflect.DeepEqual(c.Rules, wantRules) {
					t.Errorf("websocket rules = %+v", c.Rules)
				}
				if spec.Host != "localhost" || spec.Port != 8081 {
					t.Errorf("spec = %+v", spec)
				}
			case sseserver.Config:
				want := sseserver.Config{Path: "/events", AllowOrigin: "*", KeepAliveSec: 15, RetryMs: 3000, AutoID: true, Timers: []sseserver.Timer{
					{Enabled: true, Event: "price", Data: `{"symbol": "ACME", "at": {{$timestamp}}}`, IntervalMs: 1000},
					{Enabled: false, Event: "heartbeat", Data: "{{$isoTimestamp}}", IntervalMs: 30000},
				}}
				if !reflect.DeepEqual(c, want) || spec.Port != 8082 {
					t.Errorf("sse config = %+v", c)
				}
			case udpserver.Config:
				if c != (udpserver.Config{Echo: true, FixedReply: true, Reply: "06", ReplyFormat: udpserver.FormatHex}) || spec.Port != 9125 {
					t.Errorf("udp config = %+v", c)
				}
			case grpcmock.Config:
				if !c.Reflection || !reflect.DeepEqual(c.Files, []string{"protos/shop/v1/orders.proto"}) || len(c.Methods) != 4 || spec.Port != 50051 {
					t.Errorf("grpc mock config = %+v", c)
				}
				m := c.Methods[2]
				if m.Status != 5 || m.StatusMessage != "no such order" || !reflect.DeepEqual(m.Trailers, []grpcmock.Metadata{{Enabled: true, Name: "x-trace-bin", Value: "AAEC"}}) {
					t.Errorf("method 3 = %+v", m)
				}
				if c.Methods[0].DelayMs != 250 || len(c.Methods[1].Messages) != 2 || c.Methods[1].IntervalMs != 500 || c.Methods[3].Bidi != grpcmock.BidiEcho || c.Methods[0].Bidi != grpcmock.BidiEach {
					t.Errorf("methods = %+v", c.Methods)
				}
			case sioserver.Config:
				want := sioserver.Config{
					Path: sioserver.DefaultPath, Namespaces: []string{"/admin", "/chat"}, Auth: `{"token":"dev-token"}`, AllowOrigin: "http://localhost:5173",
					PingIntervalMs: 25000, PingTimeoutMs: 20000, MaxPayload: sioserver.DefaultMaxPayload, Echo: true, AckMode: sioserver.AckNone,
					Rules: []sioserver.Rule{
						{Enabled: true, Match: sioserver.MatchExact, Pattern: "ping", Ack: `["pong", {"at": {{$timestamp}}}]`},
						{Enabled: true, Match: sioserver.MatchRegex, Pattern: "^order:", Emit: "order:updated", EmitArgs: `{"status": "shipped"}`, DelayMs: 500},
						{Enabled: false, Match: sioserver.MatchContains, Pattern: "debug", Ack: `"off"`},
					},
				}
				if !reflect.DeepEqual(c, want) || spec.Port != 3000 {
					t.Errorf("socket.io config = %+v", c)
				}
			case graphqlmock.Config:
				want := graphqlmock.Config{
					Path: graphqlmock.DefaultPath, SchemaFile: "schema/catalog.graphql", Introspection: true, ListLength: 5, NullPercent: 10, Seed: 42, AllowOrigin: "*",
					Overrides: []graphqlmock.Override{
						{Enabled: true, Target: "Query.product", Args: `{"id": "1"}`, Mode: graphqlmock.ModeValue, Value: "{\n  \"id\": \"1\",\n  \"name\": \"Teapot\"\n}"},
						{Enabled: true, Target: "Query.product", Args: `{"id": "404"}`, Mode: graphqlmock.ModeError, Error: "Product not found.", Code: "NOT_FOUND"},
						{Enabled: false, Target: "DateTime", Mode: graphqlmock.ModeValue, Value: `"2026-01-01T00:00:00Z"`},
					},
				}
				if !reflect.DeepEqual(c, want) || spec.Port != 4000 {
					t.Errorf("graphql mock config = %+v", c)
				}
			default:
				t.Errorf("config = %T", spec.Config)
			}
		})
	}
}

// TestDefaultsAndUnknown: a server without its configuration gets the
// type's default (written out on the first save), and members this
// version does not know survive at every level.
func TestDefaultsAndUnknown(t *testing.T) {
	t.Parallel()
	m := manager()
	out, spec := roundTrip(t, m, []byte(`{"schemaVersion":1,"kind":"folder","id":"f","name":"F","auth":{"type":"inherit"},
		"server":{"type":"mock_http","autoStart":true}}`))
	if c, ok := spec.Config.(mockhttp.Config); !ok || len(c.Routes) != 1 || c.Routes[0].Path != "/hello" {
		t.Errorf("default mock config = %+v", spec.Config)
	}
	if spec.Name != "Mock HTTP" || strings.Contains(string(out), `"name": "Mock HTTP"`) {
		t.Errorf("name = %q, file:\n%s", spec.Name, out)
	}
	if !strings.Contains(string(out), `"autoStart": true`) || !strings.Contains(string(out), `"/hello"`) {
		t.Errorf("file:\n%s", out)
	}

	out, _ = roundTrip(t, m, []byte(`{"schemaVersion":1,"kind":"folder","id":"f","name":"F","auth":{"type":"inherit"},
		"server":{"type":"webhook","webSocket":{"path":"/kept"},"webhook":{"status":202,"forward":"x",
		"headers":[{"name":"A","value":"1","description":"why","origin":"hand"}]}}}`))
	for _, want := range []string{`"/kept"`, `"forward": "x"`, `"description": "why"`, `"origin": "hand"`, `"status": 202`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("lost %s:\n%s", want, out)
		}
	}

	out, _ = roundTrip(t, m, []byte(`{"schemaVersion":1,"kind":"folder","id":"f","name":"F","auth":{"type":"inherit"},
		"server":{"type":"mock_http","mock":{"routes":[{"id":"rt_1","method":"GET","path":"/a","status":200,"body":{"mode":"none"},
		"match":{"q":1},"delay":{"mode":"none","jitter":2}}]}}}`))
	for _, want := range []string{`"match"`, `"jitter": 2`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("lost %s:\n%s", want, out)
		}
	}

	// A route's other responses (MOCK-05, MOCK-07) and their unknown members.
	out, spec = roundTrip(t, m, []byte(`{"schemaVersion":1,"kind":"folder","id":"f","name":"F","auth":{"type":"inherit"},
		"server":{"type":"mock_http","mock":{"routes":[{"id":"rt_1","method":"GET","path":"/a","status":200,"body":{"mode":"none"},
		"responses":[{"name":"err","status":500,"body":{"mode":"raw","type":"text","text":"boom","charset":"x"},"weight":3,
		"headers":[{"name":"X-A","value":"1","description":"why"}],
		"rules":[{"source":"query","name":"fail","op":"exists","negate":true}]}]}]}}}`))
	c := spec.Config.(mockhttp.Config)
	want := []mockhttp.Response{{Name: "err", Status: 500, Headers: []mockhttp.Header{{Enabled: true, Name: "X-A", Value: "1"}},
		Body: mockhttp.Body{Mode: "raw", Type: "text", Text: "boom"}, Rules: []mockhttp.Rule{{Source: "query", Name: "fail", Op: "exists"}}}}
	if !reflect.DeepEqual(c.Routes[0].Responses, want) {
		t.Errorf("responses = %+v", c.Routes[0].Responses)
	}
	for _, want := range []string{`"weight": 3`, `"negate": true`, `"charset": "x"`, `"description": "why"`, `"text": "boom"`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("lost %s:\n%s", want, out)
		}
	}
	// A WebSocket server saved before auto-reply rules loads without any
	// and is written back without the member; a rule's unknown members
	// stay with it, and a rule without a match mode is exact.
	old := []byte(`{"schemaVersion":1,"kind":"folder","id":"f","name":"F","auth":{"type":"inherit"},"server":{"type":"websocket","webSocket":{"path":"/ws","echo":true}}}`)
	out, spec = roundTrip(t, m, old)
	if c := spec.Config.(wsserver.Config); !c.Echo || len(c.Rules) != 0 || strings.Contains(string(out), `"rules"`) {
		t.Errorf("old websocket server = %+v, file:\n%s", c, out)
	}
	out, spec = roundTrip(t, m, []byte(`{"schemaVersion":1,"kind":"folder","id":"f","name":"F","auth":{"type":"inherit"},
		"server":{"type":"websocket","webSocket":{"rules":[{"pattern":"a","reply":"b","ignoreCase":true}]}}}`))
	if c := spec.Config.(wsserver.Config); len(c.Rules) != 1 || c.Rules[0].Match != wsserver.MatchExact || !c.Rules[0].Enabled {
		t.Errorf("rules = %+v", c.Rules)
	}
	for _, want := range []string{`"ignoreCase": true`, `"match": "exact"`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("lost %s:\n%s", want, out)
		}
	}

	// A GraphQL mock server: a new one is written with the example schema
	// and nothing at its default; listLength 0 is kept; unknown members
	// stay with their override.
	out, spec = roundTrip(t, m, []byte(`{"schemaVersion":1,"kind":"folder","id":"f","name":"F","auth":{"type":"inherit"},"server":{"type":"graphql_mock"}}`))
	if g := spec.Config.(graphqlmock.Config); g.SchemaText != graphqlmock.ExampleSchema || !g.Introspection || g.ListLength != 2 ||
		strings.Contains(string(out), `"listLength"`) || strings.Contains(string(out), `"path"`) || !strings.Contains(string(out), `"schemaText"`) {
		t.Errorf("default graphql mock = %+v, file:\n%s", g, out)
	}
	out, spec = roundTrip(t, m, []byte(`{"schemaVersion":1,"kind":"folder","id":"f","name":"F","auth":{"type":"inherit"},
		"server":{"type":"graphql_mock","graphqlMock":{"listLength":0,"noIntrospection":true,"federation":2,
		"overrides":[{"target":"Query.a","value":"1","weight":3}]}}}`))
	if g := spec.Config.(graphqlmock.Config); g.ListLength != 0 || g.Introspection || g.SchemaText != "" || len(g.Overrides) != 1 {
		t.Errorf("graphql mock = %+v", g)
	}
	for _, want := range []string{`"listLength": 0`, `"noIntrospection": true`, `"federation": 2`, `"weight": 3`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("lost %s:\n%s", want, out)
		}
	}

	// Templated responses (MOCK-06): the flag is kept where it is set and
	// left out where it is not.
	out, spec = roundTrip(t, m, []byte(`{"schemaVersion":1,"kind":"folder","id":"f","name":"F","auth":{"type":"inherit"},
		"server":{"type":"mock_http","mock":{"routes":[{"id":"rt_1","method":"GET","path":"/a","status":200,"templated":true,
		"body":{"mode":"raw","type":"text","text":"{{request.method}}"},
		"responses":[{"name":"t","status":201,"templated":true,"body":{"mode":"none"}},{"name":"p","status":202,"body":{"mode":"none"}}]}]}}}`))
	c = spec.Config.(mockhttp.Config)
	if r := c.Routes[0]; !r.Templated || !r.Responses[0].Templated || r.Responses[1].Templated {
		t.Errorf("templated = %v, %v, %v", r.Templated, r.Responses[0].Templated, r.Responses[1].Templated)
	}
	if n := strings.Count(string(out), `"templated": true`); n != 2 || strings.Contains(string(out), `"templated": false`) {
		t.Errorf("templated members:\n%s", out)
	}
}

// TestTLS: the TLS member (SRV-05) converts both ways with its paths and
// unknown members, a generated certificate drops stray paths, and a kind
// this version does not know is refused.
func TestTLS(t *testing.T) {
	t.Parallel()
	m := manager()
	dir := t.TempDir()
	cert, key := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	in := `{"schemaVersion":1,"kind":"folder","id":"f","name":"F","auth":{"type":"inherit"},
		"server":{"type":"echo_http","tls":{"certificate":"files","certFile":` + strconv.Quote(cert) + `,"keyFile":` + strconv.Quote(key) + `,"minVersion":"1.3"}}}`
	out, spec := roundTrip(t, m, []byte(in))
	want := servers.TLS{Certificate: servers.CertFiles, CertFile: cert, KeyFile: key}
	if spec.TLS == nil || *spec.TLS != want {
		t.Fatalf("TLS = %+v, want %+v", spec.TLS, want)
	}
	for _, member := range []string{`"certificate": "files"`, `"certFile"`, `"keyFile"`, `"minVersion": "1.3"`} {
		if !strings.Contains(string(out), member) {
			t.Errorf("lost %s:\n%s", member, out)
		}
	}
	if strings.Contains(strings.ToLower(string(out)), "passphrase") {
		t.Errorf("a passphrase member in the file:\n%s", out)
	}

	out, spec = roundTrip(t, m, []byte(`{"schemaVersion":1,"kind":"folder","id":"f","name":"F","auth":{"type":"inherit"},
		"server":{"type":"websocket","tls":{"certificate":"generated","certFile":"/x.pem"}}}`))
	if spec.TLS == nil || *spec.TLS != (servers.TLS{Certificate: servers.CertGenerated}) || strings.Contains(string(out), "/x.pem") {
		t.Errorf("generated TLS = %+v, file:\n%s", spec.TLS, out)
	}

	out, spec = roundTrip(t, m, []byte(`{"schemaVersion":1,"kind":"folder","id":"f","name":"F","auth":{"type":"inherit"},"server":{"type":"tcp"}}`))
	if spec.TLS != nil || strings.Contains(string(out), `"tls"`) {
		t.Errorf("plain server TLS = %+v, file:\n%s", spec.TLS, out)
	}

	for _, bad := range []string{`{"certificate":"acme"}`, `{}`, `{"certificate":"files"}`, `{"certificate":"files","certFile":"rel/cert.pem"}`} {
		folder, err := projectfile.DecodeFolder([]byte(`{"schemaVersion":1,"kind":"folder","id":"f","name":"F","auth":{"type":"inherit"},"server":{"type":"echo_http","tls":` + bad + `}}`))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := m.Check(ToSpec(folder.Server)); err == nil {
			t.Errorf("Check accepted tls %s", bad)
		}
	}
}

// TestUnknownType: a type this version does not know converts, and the
// Manager refuses it.
func TestUnknownType(t *testing.T) {
	t.Parallel()
	spec := ToSpec(&projectfile.FolderServer{Type: "mqtt", Port: 9000})
	if _, err := manager().Check(spec); err == nil {
		t.Error("Check accepted an unknown type")
	}
}
