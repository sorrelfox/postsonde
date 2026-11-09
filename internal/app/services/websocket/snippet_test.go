package websocket

import (
	"context"
	"net/url"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/cookies"
	"github.com/sorrelfox/postsonde/internal/core/curl"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/snippet"
	"github.com/sorrelfox/postsonde/internal/core/template"
)

// commandInput reads a websocat or wscat command as a POSIX shell and the
// tool do into the handshake it makes: -H "Name: value" split at the
// first colon and trimmed, websocat's --protocol as the
// Sec-WebSocket-Protocol value. The command carries its cookies itself, so
// the jar is left out.
func commandInput(t *testing.T, cmd string) ConnectInputDTO {
	t.Helper()
	toks := curl.Tokenize(cmd, curl.DialectPOSIX)
	if len(toks.Diagnostics) > 0 {
		t.Fatalf("tokenize: %+v\n%s", toks.Diagnostics, cmd)
	}
	var args []string
	for _, w := range toks.Words {
		args = append(args, w.Value)
	}
	off := false
	in := ConnectInputDTO{RequestID: "", SendCookies: &off}
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
		switch {
		case a == "-H":
			k, v, _ := strings.Cut(val(), ":")
			in.Headers = append(in.Headers, HeaderDTO{Name: strings.TrimSpace(k), Value: strings.TrimSpace(v), Enabled: true})
		case tool == "websocat" && a == "--protocol":
			for p := range strings.SplitSeq(val(), ",") {
				in.Subprotocols = append(in.Subprotocols, strings.TrimSpace(p))
			}
		case tool == "websocat" && (a == "--ping-interval" || a == "--ping-timeout"):
			val()
		case tool == "websocat" && a == "-k", tool == "wscat" && a == "-n":
			verify := false
			in.VerifyTLS = &verify
		case tool == "wscat" && a == "-c":
			in.URL = val()
		case tool == "wscat" && a == "-s":
			in.Subprotocols = append(in.Subprotocols, val())
		case tool == "websocat" && in.URL == "" && !strings.HasPrefix(a, "-"):
			in.URL = a
		default:
			t.Fatalf("%s: unexpected argument %q\n%s", tool, a, cmd)
		}
	}
	return in
}

// handshakeOf connects with in, waits for the open connection, closes it
// and returns what the server saw.
func handshakeOf(t *testing.T, s *WebSocketService, ev *events, ch chan seen, in ConnectInputDTO) seen {
	t.Helper()
	conn, err := s.Connect(t.Context(), in)
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	ev.waitFor(t, "open", func(e *events) bool { return e.statesOf(conn.ConnID) == "connecting,open" })
	if err := s.Close(t.Context(), CloseInputDTO{ConnID: conn.ConnID}); err != nil {
		t.Fatal(err)
	}
	return recvSeen(t, ch)
}

// TestSnippetReproducesHandshake: the websocat and wscat commands of a
// handshake, read back as the tool reads them, make the same handshake
// against the test server (URL, userinfo, headers, the jar's cookies,
// subprotocols, secret variables); masked, they hold no secret.
func TestSnippetReproducesHandshake(t *testing.T) {
	t.Parallel()
	srv, seenCh := echoServer(t)
	jar := cookies.New(cookies.Options{})
	u, _ := url.Parse(srv.URL)
	jar.SetCookieLines(u, []string{"session=abc; Path=/"})
	vars := template.MapScope{Label: "Environment: dev", Variables: map[string]template.Variable{
		"base":  {Value: strings.TrimPrefix(wsURL(srv, ""), "ws://")},
		"room":  {Value: "lobby 1"},
		"token": {Value: "t0p-s3cret", Secret: true},
	}}
	scopes := func(context.Context, string) (template.Chain, error) { return template.Chain{vars}, nil }
	s, ev := newService(t, &fakeJars{jar: jar}, WithScopes(scopes))
	ping := int64(1500)
	in := ConnectInputDTO{
		RequestID: "req_feed",
		URL:       "ws://ann:p%40ss@{{base}}/feed?room={{room}}",
		Headers: []HeaderDTO{
			{Name: "X-Token", Value: "{{token}}", Enabled: true},
			{Name: "X-Quote", Value: ` it's "$HOME" `, Enabled: true},
			{Name: "Cookie", Value: "pref=1", Enabled: true},
			{Name: "X-Off", Value: "{{nope}}"},
		},
		Subprotocols:   []string{"chat.v2", "chat.v1"},
		PingIntervalMs: &ping,
	}
	// The server sets a cookie on the first handshake, which the later ones
	// (and the snippets) send.
	first := handshakeOf(t, s, ev, seenCh, in)
	if c := first.header.Get("Cookie"); c != "pref=1; session=abc" {
		t.Fatalf("first handshake cookies %q", c)
	}
	for _, lang := range []string{"websocat", "wscat"} {
		t.Run(lang, func(t *testing.T) {
			want := handshakeOf(t, s, ev, seenCh, in)
			masked, err := s.Snippet(t.Context(), SnippetInputDTO{Connect: in, Language: lang})
			if err != nil {
				t.Fatal(err)
			}
			if !masked.Masked || masked.Mode != SnippetResolved || strings.Contains(masked.Code, "t0p-s3cret") ||
				strings.Contains(masked.Code, "Basic YW5uOnBAc3M=") || len(masked.Unresolved) != 0 {
				t.Errorf("masked snippet %+v", masked)
			}
			shown, err := s.Snippet(t.Context(), SnippetInputDTO{Connect: in, Language: lang, RevealSecrets: true})
			if err != nil {
				t.Fatal(err)
			}
			if !shown.Revealed || shown.Masked || !strings.Contains(shown.Code, "t0p-s3cret") {
				t.Errorf("revealed snippet %+v", shown)
			}
			got := handshakeOf(t, s, ev, seenCh, commandInput(t, shown.Code))
			for _, name := range []string{"Authorization", "X-Token", "X-Quote", "Cookie", "Sec-Websocket-Protocol", "X-Off"} {
				if w, g := strings.Join(want.header.Values(name), "|"), strings.Join(got.header.Values(name), "|"); w != g {
					t.Errorf("%s: server got %q, want %q\n%s", name, g, w, shown.Code)
				}
			}
			if !strings.Contains(shown.Code, "room=lobby%201") || !strings.Contains(want.header.Get("Cookie"), "session=abc") {
				t.Errorf("URL or cookies not as sent:\n%s\n%v", shown.Code, want.header)
			}
		})
	}
}

func TestSnippetModes(t *testing.T) {
	t.Parallel()
	vars := template.MapScope{Label: "Environment: dev", Variables: map[string]template.Variable{
		"host": {Value: "live.test"}, "token": {Value: "t0p-s3cret", Secret: true},
	}}
	scopes := func(context.Context, string) (template.Chain, error) { return template.Chain{vars}, nil }
	jar := cookies.New(cookies.Options{})
	jar.SetCookieLines(&url.URL{Scheme: "https", Host: "live.test", Path: "/"}, []string{"sid=1; Path=/"})
	s, _ := newService(t, &fakeJars{jar: jar}, WithScopes(scopes))
	verify := false
	in := ConnectInputDTO{RequestID: "req_1", URL: "wss://{{host}}/socket?k={{token}}", VerifyTLS: &verify,
		Headers:      []HeaderDTO{{Name: "Authorization", Value: "Bearer {{token}}", Enabled: true}, {Name: "X-Missing", Value: "{{nope}} {{gone}}", Enabled: true}},
		Subprotocols: []string{"graphql-ws"}}

	got, err := s.Snippet(t.Context(), SnippetInputDTO{Connect: in})
	if err != nil {
		t.Fatal(err)
	}
	want := "websocat 'wss://live.test/socket?k=••••••' \\\n" +
		"  -H 'Authorization: Bearer ••••••' \\\n" +
		"  -H 'X-Missing: {{nope}} {{gone}}' \\\n" +
		"  -H 'Cookie: sid=1' \\\n" +
		"  --protocol graphql-ws \\\n" +
		"  -k"
	if got.Code != want || !got.Masked || got.Language != "websocat" || strings.Join(got.Unresolved, ",") != "gone,nope" {
		t.Errorf("resolved %+v\nwant code:\n%s", got, want)
	}

	off := false
	in.SendCookies = &off
	got, err = s.Snippet(t.Context(), SnippetInputDTO{Connect: in, Language: "wscat", Mode: SnippetTemplated, RevealSecrets: true})
	if err != nil {
		t.Fatal(err)
	}
	want = "wscat -c 'wss://{{host}}/socket?k={{token}}' \\\n" +
		"  -H 'Authorization: Bearer {{token}}' \\\n" +
		"  -H 'X-Missing: {{nope}} {{gone}}' \\\n" +
		"  -s graphql-ws \\\n" +
		"  -n"
	if got.Code != want || got.Masked || got.Revealed || got.Mode != SnippetTemplated || len(got.Unresolved) != 0 {
		t.Errorf("templated %+v\nwant code:\n%s", got, want)
	}

	in.Headers = []HeaderDTO{{Name: "Authorization", Value: "Bearer typed-value", Enabled: true}}
	if got, err = s.Snippet(t.Context(), SnippetInputDTO{Connect: in, Mode: SnippetTemplated}); err != nil || !got.Masked || strings.Contains(got.Code, "typed-value") {
		t.Errorf("templated typed credential %+v %v", got, err)
	}

	_, err = s.Snippet(t.Context(), SnippetInputDTO{Connect: in, Mode: "both"})
	checkCode(t, err, errs.InvalidArgument, CodeSnippetMode)
	_, err = s.Snippet(t.Context(), SnippetInputDTO{Connect: in, Language: "grpcurl"})
	checkCode(t, err, errs.InvalidArgument, snippet.CodeUnknownLanguage)
	if langs := s.SnippetLanguages(); len(langs) != 2 || langs[0].ID != "websocat" || langs[1].ID != "wscat" {
		t.Errorf("languages %+v", langs)
	}
}
