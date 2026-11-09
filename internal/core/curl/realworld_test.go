package curl

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

// realWorld are commands written in the shapes that browser DevTools, API
// clients and API documentation produce. Each has explicit expectations
// for the important fields and a golden dump of the whole Command.
var realWorld = []struct {
	name string
	src  string
	want expect
}{
	{
		name: "chrome_bash_post_json",
		src: `curl 'https://api.example.com/v1/orders?page=2&sort=desc' \
  -H 'accept: application/json, text/plain, */*' \
  -H 'accept-language: en-US,en;q=0.9,de;q=0.8' \
  -H 'authorization: Bearer eyJhbGciOiJIUzI1NiJ9.e30.c2lnbmF0dXJl' \
  -H 'content-type: application/json' \
  -b 'session=abc123; theme=dark' \
  -H 'origin: https://app.example.com' \
  -H 'priority: u=1, i' \
  -H 'referer: https://app.example.com/orders' \
  -H 'sec-ch-ua: "Chromium";v="128", "Not;A=Brand";v="24", "Google Chrome";v="128"' \
  -H 'sec-ch-ua-mobile: ?0' \
  -H 'sec-ch-ua-platform: "macOS"' \
  -H 'sec-fetch-dest: empty' \
  -H 'sec-fetch-mode: cors' \
  -H 'sec-fetch-site: same-site' \
  -H 'user-agent: Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36' \
  --data-raw '{"items":[{"sku":"A-1","qty":2}],"note":"leave at door"}'`,
		want: expect{
			dialect: DialectPOSIX,
			method:  "POST",
			url:     "https://api.example.com/v1/orders?page=2&sort=desc",
			headers: []string{
				"accept: application/json, text/plain, */*",
				"accept-language: en-US,en;q=0.9,de;q=0.8",
				"authorization: Bearer eyJhbGciOiJIUzI1NiJ9.e30.c2lnbmF0dXJl",
				"content-type: application/json",
				"origin: https://app.example.com",
				"priority: u=1, i",
				"referer: https://app.example.com/orders",
				`sec-ch-ua: "Chromium";v="128", "Not;A=Brand";v="24", "Google Chrome";v="128"`,
				"sec-ch-ua-mobile: ?0",
				`sec-ch-ua-platform: "macOS"`,
				"sec-fetch-dest: empty",
				"sec-fetch-mode: cors",
				"sec-fetch-site: same-site",
				"user-agent: Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/128.0.0.0 Safari/537.36",
			},
			cookies: []string{"session=abc123; theme=dark"},
			body:    `{"items":[{"sku":"A-1","qty":2}],"note":"leave at door"}`,
		},
	},
	{
		// Chrome switches to $'...' when the body has a quote, a control
		// character or "!": \' for the quote, \n for a newline, \\ for a
		// backslash and \u0021 for "!".
		name: "chrome_bash_ansi_c_body",
		src: "curl 'https://app.example.com/api/notes' \\\n" +
			"  -H 'content-type: application/json' \\\n" +
			"  -H $'x-note: caf\\u00e9 \\u2615' \\\n" +
			`  --data-raw $'{\n  "msg": "it\'s done\u0021",\n  "esc": "a\\nb",\n  "tab": "\t"\n}'`,
		want: expect{
			dialect: DialectPOSIX,
			method:  "POST",
			url:     "https://app.example.com/api/notes",
			headers: []string{"content-type: application/json", "x-note: café ☕"},
			body:    "{\n  \"msg\": \"it's done!\",\n  \"esc\": \"a\\nb\",\n  \"tab\": \"\t\"\n}",
		},
	},
	{
		// Chrome's "Copy as cURL (cmd)": every argument in ^"...^", \ doubled,
		// " as \^", other specials ^-escaped, %^ before a name character,
		// and ^ plus two line breaks for a newline inside a value.
		name: "chrome_cmd",
		src: "curl ^\"https://api.example.com/v1/orders?page=2^&sort=desc^\" ^\r\n" +
			"  -H ^\"accept: application/json, text/plain, */*^\" ^\r\n" +
			"  -H ^\"content-type: application/json^\" ^\r\n" +
			"  -b ^\"session=abc123; theme=dark^\" ^\r\n" +
			"  -H ^\"sec-ch-ua: ^\\^\"Chromium^\\^\";v=^\\^\"128^\\^\"^\" ^\r\n" +
			"  -H ^\"user-agent: Mozilla/5.0 (Windows NT 10.0; Win64; x64)^\" ^\r\n" +
			"  --data-raw ^\"^{^\\^\"note^\\^\":^\\^\"100^%^\\^\",^\\^\"env^\\^\":^\\^\"^%^PATH^%^\\^\",^\\^\"path^\\^\":^\\^\"C:^\\^\\^\\^\\tmp^\\^\",^\\^\"text^\\^\":^\\^\"a^\r\n\r\nb^\\^\"^}^\" ^\r\n" +
			"  --compressed",
		want: expect{
			dialect: DialectCmd,
			method:  "POST",
			url:     "https://api.example.com/v1/orders?page=2&sort=desc",
			headers: []string{
				"accept: application/json, text/plain, */*",
				"content-type: application/json",
				`sec-ch-ua: "Chromium";v="128"`,
				"user-agent: Mozilla/5.0 (Windows NT 10.0; Win64; x64)",
			},
			cookies: []string{"session=abc123; theme=dark"},
			body:    `{"note":"100%","env":"%PATH%","path":"C:\\tmp","text":"a` + "\n" + `b"}`,
			flags:   []string{"compressed"},
		},
	},
	{
		// Older Chrome and Firefox: plain "..." with "" for a quote.
		name: "cmd_plain_quotes",
		src:  `curl.exe "https://example.com/api" -H "Content-Type: application/json" -d "{""a"":""b c""}" -d "{\"x\":1}"`,
		want: expect{
			dialect: DialectPOSIX,
			method:  "POST",
			url:     "https://example.com/api",
			headers: []string{"Content-Type: application/json"},
			// Read as POSIX (nothing marks it as cmd): "" is an empty string.
			body: `{a:b c}&{"x":1}`,
		},
	},
	{
		name: "cmd_plain_quotes_forced",
		src:  `curl.exe "https://example.com/api" -H "Content-Type: application/json" -d "{""a"":""b c""}" -d "{\"x\":1}" ^` + "\n" + ` -k`,
		want: expect{
			dialect: DialectCmd,
			method:  "POST",
			url:     "https://example.com/api",
			headers: []string{"Content-Type: application/json"},
			body:    `{"a":"b c"}&{"x":1}`,
			flags:   []string{"insecure"},
		},
	},
	{
		name: "firefox_bash_form_urlencoded",
		src:  `curl 'https://shop.example.org/api/cart' --compressed -X POST -H 'User-Agent: Mozilla/5.0 (X11; Linux x86_64; rv:130.0) Gecko/20100101 Firefox/130.0' -H 'Accept: */*' -H 'Accept-Language: en-US,en;q=0.5' -H 'Accept-Encoding: gzip, deflate, br, zstd' -H 'Content-Type: application/x-www-form-urlencoded; charset=UTF-8' -H 'X-Requested-With: XMLHttpRequest' -H 'Origin: https://shop.example.org' -H 'Connection: keep-alive' -H 'Cookie: cart=7f3a; consent=yes' -H 'Sec-Fetch-Dest: empty' -H 'Sec-Fetch-Mode: cors' -H 'Sec-Fetch-Site: same-origin' -H 'TE: trailers' --data-raw 'product_id=42&qty=1&note=gift%20wrap'`,
		want: expect{
			dialect:        DialectPOSIX,
			method:         "POST",
			methodExplicit: true,
			url:            "https://shop.example.org/api/cart",
			headers: []string{
				"User-Agent: Mozilla/5.0 (X11; Linux x86_64; rv:130.0) Gecko/20100101 Firefox/130.0",
				"Accept: */*",
				"Accept-Language: en-US,en;q=0.5",
				"Accept-Encoding: gzip, deflate, br, zstd",
				"Content-Type: application/x-www-form-urlencoded; charset=UTF-8",
				"X-Requested-With: XMLHttpRequest",
				"Origin: https://shop.example.org",
				"Connection: keep-alive",
				"Cookie: cart=7f3a; consent=yes",
				"Sec-Fetch-Dest: empty",
				"Sec-Fetch-Mode: cors",
				"Sec-Fetch-Site: same-origin",
				"TE: trailers",
			},
			body:  "product_id=42&qty=1&note=gift%20wrap",
			flags: []string{"compressed"},
		},
	},
	{
		name: "safari_graphql",
		src: `curl 'https://www.example.com/graphql' \
-X 'POST' \
-H 'Content-Type: application/json' \
-H 'Accept: application/json' \
-H 'Sec-Fetch-Site: same-origin' \
-H 'Accept-Language: en-GB,en;q=0.9' \
-H 'Accept-Encoding: gzip, deflate, br' \
-H 'Origin: https://www.example.com' \
-H 'User-Agent: Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15' \
-H 'Content-Length: 55' \
-H 'Connection: keep-alive' \
--data-binary '{"query":"query Me { me { id name } }","variables":{}}'
`,
		want: expect{
			dialect:        DialectPOSIX,
			method:         "POST",
			methodExplicit: true,
			url:            "https://www.example.com/graphql",
			headers: []string{
				"Content-Type: application/json",
				"Accept: application/json",
				"Sec-Fetch-Site: same-origin",
				"Accept-Language: en-GB,en;q=0.9",
				"Accept-Encoding: gzip, deflate, br",
				"Origin: https://www.example.com",
				"User-Agent: Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.5 Safari/605.1.15",
				"Content-Length: 55",
				"Connection: keep-alive",
			},
			body: `{"query":"query Me { me { id name } }","variables":{}}`,
		},
	},
	{
		name: "postman_export",
		src: `curl --location 'https://api.example.com/v2/users?limit=10' \
--header 'Content-Type: application/json' \
--header 'Authorization: Bearer {{token}}' \
--data '{
    "name": "Ada",
    "email": "ada@example.com"
}'`,
		want: expect{
			dialect: DialectPOSIX,
			method:  "POST",
			url:     "https://api.example.com/v2/users?limit=10",
			headers: []string{"Content-Type: application/json", "Authorization: Bearer {{token}}"},
			body:    "{\n    \"name\": \"Ada\",\n    \"email\": \"ada@example.com\"\n}",
			flags:   []string{"location"},
		},
	},
	{
		name: "insomnia_export",
		src: `curl --request PUT \
  --url https://api.example.com/v1/items/17 \
  --header 'Content-Type: application/json' \
  --header 'User-Agent: insomnia/10.0.0' \
  --data '{"name":"Widget","price":9.5}'`,
		want: expect{
			dialect:        DialectPOSIX,
			method:         "PUT",
			methodExplicit: true,
			url:            "https://api.example.com/v1/items/17",
			headers:        []string{"Content-Type: application/json", "User-Agent: insomnia/10.0.0"},
			body:           `{"name":"Widget","price":9.5}`,
		},
	},
	{
		name: "stripe_style_form_params",
		src: `curl https://api.example.com/v1/charges \
  -u sk_test_example: \
  -d amount=2000 \
  -d currency=usd \
  -d "metadata[order_id]=6735" \
  -d source=tok_visa`,
		want: expect{
			dialect: DialectPOSIX,
			method:  "POST",
			url:     "https://api.example.com/v1/charges",
			user:    "sk_test_example:",
			body:    "amount=2000&currency=usd&metadata[order_id]=6735&source=tok_visa",
		},
	},
	{
		name: "stripe_style_get_list",
		src: `curl -G https://api.example.com/v1/customers \
  -u sk_test_example: \
  -d limit=3 \
  --data-urlencode "email=ada+test@example.com"`,
		want: expect{
			dialect: DialectPOSIX,
			method:  "GET",
			url:     "https://api.example.com/v1/customers",
			user:    "sk_test_example:",
			body:    "limit=3&email=ada%2Btest%40example.com",
			flags:   []string{"get"},
		},
	},
	{
		name: "github_style",
		src: `curl -L \
  -X POST \
  -H "Accept: application/vnd.github+json" \
  -H "Authorization: Bearer <YOUR-TOKEN>" \
  -H "X-GitHub-Api-Version: 2022-11-28" \
  https://api.example.com/repos/OWNER/REPO/issues \
  -d '{"title":"Found a bug","body":"I'"'"'m having a problem with this.","labels":["bug"]}'`,
		want: expect{
			dialect:        DialectPOSIX,
			method:         "POST",
			methodExplicit: true,
			url:            "https://api.example.com/repos/OWNER/REPO/issues",
			headers: []string{
				"Accept: application/vnd.github+json",
				"Authorization: Bearer <YOUR-TOKEN>",
				"X-GitHub-Api-Version: 2022-11-28",
			},
			body:  `{"title":"Found a bug","body":"I'm having a problem with this.","labels":["bug"]}`,
			flags: []string{"location"},
		},
	},
	{
		name: "twilio_style_urlencode_and_variables",
		src: `curl -X POST "https://api.example.com/2010-04-01/Accounts/$TWILIO_ACCOUNT_SID/Messages.json" \
--data-urlencode "Body=Hello from PostSonde & friends" \
--data-urlencode "From=+15017122661" \
--data-urlencode "To=+15558675310" \
-u $TWILIO_ACCOUNT_SID:$TWILIO_AUTH_TOKEN`,
		want: expect{
			dialect:        DialectPOSIX,
			method:         "POST",
			methodExplicit: true,
			url:            "https://api.example.com/2010-04-01/Accounts/$TWILIO_ACCOUNT_SID/Messages.json",
			user:           "$TWILIO_ACCOUNT_SID:$TWILIO_AUTH_TOKEN",
			body:           "Body=Hello%20from%20PostSonde%20%26%20friends&From=%2B15017122661&To=%2B15558675310",
			codes:          []string{CodeShellExpansion, CodeShellExpansion, CodeShellExpansion},
		},
	},
	{
		name: "openai_style_multiline_json",
		src: `curl https://api.example.com/v1/chat/completions \
  -H "Content-Type: application/json" \
  -H "Authorization: Bearer $OPENAI_API_KEY" \
  -d '{
    "model": "model-x",
    "messages": [{"role": "user", "content": "Say hello!"}]
  }'`,
		want: expect{
			dialect: DialectPOSIX,
			method:  "POST",
			url:     "https://api.example.com/v1/chat/completions",
			headers: []string{"Content-Type: application/json", "Authorization: Bearer $OPENAI_API_KEY"},
			body:    "{\n    \"model\": \"model-x\",\n    \"messages\": [{\"role\": \"user\", \"content\": \"Say hello!\"}]\n  }",
			codes:   []string{CodeShellExpansion},
		},
	},
	{
		name: "multipart_upload",
		src: `curl -X POST https://api.example.com/v1/files \
  -H "Authorization: Bearer abc" \
  -F purpose="fine-tune" \
  -F file="@my data.jsonl;type=application/jsonl" \
  -F 'meta={"a":1};type=application/json' \
  -F 'avatar=@"img;1.png";filename=me.png' \
  -F "notes=<notes.txt" \
  --form-string 'raw=@not-a-file;really'`,
		want: expect{
			dialect:        DialectPOSIX,
			method:         "POST",
			methodExplicit: true,
			url:            "https://api.example.com/v1/files",
			headers:        []string{"Authorization: Bearer abc"},
			form: []string{
				"text purpose=fine-tune",
				"file file=@my data.jsonl type=application/jsonl",
				`text meta={"a":1} type=application/json`,
				"file avatar=@img;1.png filename=me.png",
				"file_content notes=@notes.txt",
				"text raw=@not-a-file;really",
			},
			codes: []string{CodeFileReference, CodeFileReference, CodeFileReference},
		},
	},
	{
		name: "json_option",
		src:  `curl --json '{"tool":"curl"}' -H 'accept: text/plain' https://example.com/api`,
		want: expect{
			dialect: DialectPOSIX,
			method:  "POST",
			url:     "https://example.com/api",
			headers: []string{"accept: text/plain", "Content-Type: application/json (implied)"},
			body:    `{"tool":"curl"}`,
		},
	},
	{
		name: "docs_prompt_and_pipe",
		src:  "$ curl -s https://api.example.com/users/1 | jq .name\n",
		want: expect{
			dialect: DialectPOSIX,
			method:  "GET",
			url:     "https://api.example.com/users/1",
			codes:   []string{CodeIgnoredOption, CodeTrailingInput},
		},
	},
	{
		name: "short_option_clusters",
		src:  `curl -sSLkXPOST -HX-Trace:1 -d@payload.json -uadmin -m5 https://localhost:8443/api`,
		want: expect{
			dialect:        DialectPOSIX,
			method:         "POST",
			methodExplicit: true,
			url:            "https://localhost:8443/api",
			headers:        []string{"X-Trace: 1"},
			user:           "admin",
			body:           "<incomplete>",
			maxTime:        5 * time.Second,
			flags:          []string{"insecure", "location"},
			codes: []string{
				CodeIgnoredOption, CodeIgnoredOption, CodeFileReference, CodePasswordMissing,
			},
		},
	},
	{
		name: "powershell",
		src: "PS C:\\Users\\ada> curl.exe -X POST `\r\n" +
			"  -H \"Content-Type: application/json\" `\r\n" +
			"  -H \"Authorization: Bearer $env:TOKEN\" `\r\n" +
			"  -d '{\"name\":\"it''s\",\"tab\":\"`t\"}' `\r\n" +
			"  https://api.example.com/items",
		want: expect{
			dialect:        DialectPowerShell,
			method:         "POST",
			methodExplicit: true,
			url:            "https://api.example.com/items",
			headers:        []string{"Content-Type: application/json", "Authorization: Bearer $env:TOKEN"},
			body:           `{"name":"it's","tab":"` + "`t" + `"}`,
			codes:          []string{CodeShellExpansion},
		},
	},
	{
		name: "headers_semantics",
		src:  `curl https://example.com -H 'X-Empty;' -H 'Accept:' -H 'x-dup: 1' -H 'X-Dup: 2' -H 'bogus' -H @headers.txt -H '  X-Spaced :  v  '`,
		want: expect{
			dialect: DialectPOSIX,
			method:  "GET",
			url:     "https://example.com",
			headers: []string{"X-Empty;", "Accept:", "x-dup: 1", "X-Dup: 2", "  X-Spaced : v"},
			codes:   []string{CodeInvalidValue, CodeFileReference},
		},
	},
	{
		name: "connection_options",
		src: `curl --http2 -m 2.5 --connect-timeout 3 -x http://proxy.local:3128 -U bob:pw --noproxy localhost \
  -A 'agent/1.0' -e 'https://ref.example/;auto' --max-redirs 5 --no-compressed -k --digest \
  -u ada:secret --cacert ca.pem -o out.json -w '%{http_code}\n' \
  --url https://example.com/ https://second.example/`,
		want: expect{
			dialect:        DialectPOSIX,
			method:         "GET",
			url:            "https://example.com/",
			user:           "ada:secret",
			flags:          []string{"insecure"},
			httpVersion:    "2",
			maxTime:        2500 * time.Millisecond,
			connectTimeout: 3 * time.Second,
			codes: []string{
				CodeUnsupportedOption, CodeIgnoredOption, CodeIgnoredOption, CodeMultipleURLs,
			},
		},
	},
	{
		name: "unknown_options",
		src:  `curl --frobnicate 7 --wibble https://example.com/x --zap=1 -Z9 -W`,
		want: expect{
			dialect: DialectPOSIX,
			method:  "GET",
			url:     "https://example.com/x",
			codes: []string{
				CodeUnknownOption, CodeUnknownOption, CodeUnknownOption,
				CodeIgnoredOption, CodeUnknownOption, CodeUnknownOption,
			},
		},
	},
	{
		name: "missing_continuation_and_redirect",
		src:  "curl https://example.com/upload\n  -T report.pdf 2>&1 > /dev/null",
		want: expect{
			dialect: DialectPOSIX,
			method:  "PUT",
			url:     "https://example.com/upload",
			codes: []string{
				CodeMissingContinuation, CodeFileReference, CodeRedirectIgnored, CodeRedirectIgnored,
			},
		},
	},
	{
		name: "unterminated_quote",
		src:  `curl https://example.com -d '{"a":1}`,
		want: expect{
			dialect: DialectPOSIX,
			method:  "POST",
			url:     "https://example.com",
			body:    `{"a":1}`,
			codes:   []string{CodeUnterminatedQuote},
			err:     true,
		},
	},
	{
		name: "missing_value_and_url",
		src:  `curl -X POST -H`,
		want: expect{
			dialect:        DialectPOSIX,
			method:         "POST",
			methodExplicit: true,
			codes:          []string{CodeMissingURL, CodeMissingValue},
			err:            true,
		},
	},
	{
		name: "not_curl",
		src:  `wget https://example.com`,
		want: expect{dialect: DialectPOSIX, codes: []string{CodeNotCurl}, err: true},
	},
}

type expect struct {
	dialect        Dialect
	method         string
	methodExplicit bool
	url            string
	// headers are "Name: value", "Name;" or "Name:", with " (implied)"
	// appended for --json headers.
	headers []string
	cookies []string
	// body is Body(); "<incomplete>" when it is not complete. Empty means
	// no data parts.
	body string
	// form parts are "kind name=value [type=...] [filename=...]", with
	// "@path" as the value of file parts.
	form []string
	// user is "user:password", or "user" without a password.
	user string
	// flags are the boolean settings that are on: insecure, compressed,
	// location, get, head.
	flags          []string
	httpVersion    string
	maxTime        time.Duration
	connectTimeout time.Duration
	// codes are the diagnostics codes in order, excluding shell_glob.
	codes []string
	err   bool
}

func TestRealWorldCommands(t *testing.T) {
	t.Parallel()
	for _, tt := range realWorld {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			c := Parse(tt.src)
			w := tt.want
			check := func(what string, got, want any) {
				t.Helper()
				if fmt.Sprint(got) != fmt.Sprint(want) {
					t.Errorf("%s:\n got %q\nwant %q", what, got, want)
				}
			}
			check("dialect", c.Dialect, w.dialect)
			check("method", c.Method, w.method)
			check("methodExplicit", c.MethodExplicit, w.methodExplicit)
			check("url", c.URL, w.url)
			check("headers", renderHeaders(c.Headers), orEmpty(w.headers))
			check("cookies", orEmpty(c.Cookies), orEmpty(w.cookies))
			check("body", renderBody(c), w.body)
			check("form", renderForm(c.Form), orEmpty(w.form))
			check("user", renderUser(c.User), w.user)
			check("flags", renderFlags(c), orEmpty(w.flags))
			check("httpVersion", c.HTTPVersion, w.httpVersion)
			check("maxTime", c.MaxTime, w.maxTime)
			check("connectTimeout", c.ConnectTimeout, w.connectTimeout)
			check("codes", diagCodes(c.Diagnostics), orEmpty(w.codes))
			check("err", c.Err() != nil, w.err)
			checkRanges(t, tt.src, c)

			golden.Assert(t, "realworld/"+tt.name, dump(tt.src, c))
		})
	}
}

func orEmpty(s []string) []string {
	if s == nil {
		return []string{}
	}
	return s
}

func renderHeaders(hs []Header) []string {
	out := []string{}
	for _, h := range hs {
		var s string
		switch h.Kind {
		case HeaderSet:
			s = h.Name + ": " + h.Value
		case HeaderEmpty:
			s = h.Name + ";"
		case HeaderRemove:
			s = h.Name + ":"
		}
		if h.Implied {
			s += " (implied)"
		}
		out = append(out, s)
	}
	return out
}

func renderBody(c *Command) string {
	if len(c.Data) == 0 {
		return ""
	}
	body, complete := c.Body()
	if !complete {
		return "<incomplete>"
	}
	return body
}

func renderForm(fs []FormPart) []string {
	out := []string{}
	for _, f := range fs {
		s := string(f.Kind) + " " + f.Name + "=" + f.Value
		if f.File != "" {
			s += "@" + f.File
		}
		if f.ContentType != "" {
			s += " type=" + f.ContentType
		}
		if f.Filename != "" {
			s += " filename=" + f.Filename
		}
		out = append(out, s)
	}
	return out
}

func renderUser(u *Credentials) string {
	switch {
	case u == nil:
		return ""
	case u.HasPassword:
		return u.User + ":" + u.Password
	}
	return u.User
}

func renderFlags(c *Command) []string {
	out := []string{}
	for _, f := range []struct {
		name string
		on   bool
	}{{"insecure", c.Insecure}, {"compressed", c.Compressed}, {"location", c.FollowRedirects}, {"get", c.Get}, {"head", c.Head}} {
		if f.on {
			out = append(out, f.name)
		}
	}
	return out
}

func diagCodes(ds []Diagnostic) []string {
	out := []string{}
	for _, d := range ds {
		if d.Code != CodeShellGlob {
			out = append(out, d.Code)
		}
	}
	return out
}

// checkRanges checks every range in c lies inside src.
func checkRanges(t *testing.T, src string, c *Command) {
	t.Helper()
	ok := func(what string, start, end int) {
		t.Helper()
		if start < 0 || end < start || end > len(src) {
			t.Errorf("%s range [%d, %d) outside the source (len %d)", what, start, end, len(src))
		}
	}
	for _, d := range c.Diagnostics {
		ok("diagnostic "+d.Code, d.Start, d.End)
	}
	for _, h := range c.Headers {
		ok("header "+h.Name, h.Start, h.End)
	}
	for _, d := range c.Data {
		ok("data", d.Start, d.End)
	}
	for _, f := range c.Form {
		ok("form", f.Start, f.End)
	}
	for _, u := range c.URLs {
		ok("url", u.Start, u.End)
	}
	if !slices.IsSortedFunc(c.Diagnostics, func(a, b Diagnostic) int { return a.Start - b.Start }) {
		t.Errorf("diagnostics are not sorted by Start")
	}
}

// dump renders the command as JSON, with each diagnostic followed by the
// source text it covers.
func dump(src string, c *Command) []byte {
	type diag struct {
		Diagnostic
		Text string `json:"text"`
	}
	out := struct {
		*Command
		Diagnostics []diag `json:"diagnostics"`
	}{Command: c, Diagnostics: []diag{}}
	for _, d := range c.Diagnostics {
		out.Diagnostics = append(out.Diagnostics, diag{d, src[d.Start:d.End]})
	}
	b, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		panic(err)
	}
	return append(b, '\n')
}

func TestIgnoredOptionsAreKnown(t *testing.T) {
	t.Parallel()
	for name := range ignoredOptions {
		if spec, _ := lookupLong(name); spec == nil {
			t.Errorf("ignoredOptions has %q, which options.txt does not list", name)
		}
	}
	for _, name := range []string{"data", "data-raw", "data-binary", "data-urlencode", "json", "form", "form-string", "header", "request", "user", "insecure", "compressed", "location", "url", "url-query", "get", "head", "cookie", "user-agent", "referer", "max-time", "connect-timeout", "proxy", "http2"} {
		if spec, _ := lookupLong(name); spec == nil {
			t.Errorf("options.txt does not list %q", name)
		}
	}
}
