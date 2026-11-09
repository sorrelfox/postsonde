package requestsecrets

import (
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

func TestSplitMergeProxy(t *testing.T) {
	local := []string{"password"}
	tests := []struct {
		name     string
		in       projectfile.RequestProxy
		wantFile projectfile.RequestProxy
		values   map[string]string
	}{
		{
			name:     "typed password goes local",
			in:       projectfile.RequestProxy{Mode: "custom", URL: "socks5h://bob@proxy.lan:1080", Password: "hunter2"},
			wantFile: projectfile.RequestProxy{Mode: "custom", URL: "socks5h://bob@proxy.lan:1080", LocalSecrets: local},
			values:   map[string]string{"proxy.password": "hunter2"},
		},
		{
			name:     "password in the URL moves out of it",
			in:       projectfile.RequestProxy{Mode: "custom", URL: "socks5://bob:p%40ss@proxy.lan:1080"},
			wantFile: projectfile.RequestProxy{Mode: "custom", URL: "socks5://bob@proxy.lan:1080", LocalSecrets: local},
			values:   map[string]string{"proxy.password": "p@ss"},
		},
		{
			name:     "reference in the URL stays in the file, out of the URL",
			in:       projectfile.RequestProxy{Mode: "custom", URL: "socks5h://{{user}}:{{pass}}@{{host}}:1080"},
			wantFile: projectfile.RequestProxy{Mode: "custom", URL: "socks5h://{{user}}@{{host}}:1080", Password: "{{pass}}"},
		},
		{
			name:     "reference stays in the file",
			in:       projectfile.RequestProxy{Mode: "custom", URL: "socks5h://bob@proxy.lan", Password: "{{proxyPassword}}", LocalSecrets: local},
			wantFile: projectfile.RequestProxy{Mode: "custom", URL: "socks5h://bob@proxy.lan", Password: "{{proxyPassword}}"},
		},
		{
			name:     "empty and marked stays marked",
			in:       projectfile.RequestProxy{Mode: "custom", URL: "socks5h://bob@proxy.lan", LocalSecrets: local},
			wantFile: projectfile.RequestProxy{Mode: "custom", URL: "socks5h://bob@proxy.lan", LocalSecrets: local},
		},
		{
			name:     "kept in another mode",
			in:       projectfile.RequestProxy{Mode: "none", URL: "socks5h://bob@proxy.lan", Password: "pw"},
			wantFile: projectfile.RequestProxy{Mode: "none", URL: "socks5h://bob@proxy.lan", LocalSecrets: local},
			values:   map[string]string{"proxy.password": "pw"},
		},
		{
			name: "inherit",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, values := SplitProxy(tt.in)
			if !reflect.DeepEqual(got, tt.wantFile) {
				t.Errorf("file = %+v, want %+v", got, tt.wantFile)
			}
			if !reflect.DeepEqual(values, tt.values) {
				t.Errorf("values = %v, want %v", values, tt.values)
			}
			back := MergeProxy(got, values)
			if pw := tt.values["proxy.password"]; pw != "" && back.Password != pw {
				t.Errorf("merged password = %q, want %q", back.Password, pw)
			}
		})
	}
	if !ProxyPasswordMissing(projectfile.RequestProxy{LocalSecrets: local}) || ProxyPasswordMissing(projectfile.RequestProxy{Password: "x", LocalSecrets: local}) {
		t.Error("ProxyPasswordMissing")
	}
}

// Every request kind keeps its own proxy's password out of the project
// file and gets it back from the side file.
func TestSplitMergeFileProxy(t *testing.T) {
	proxy := projectfile.RequestProxy{Mode: projectfile.ProxyCustom, URL: "socks5h://bob:s3cr3t-proxy@proxy.lan:1080"}
	files := []projectfile.File{
		&projectfile.HTTPRequest{Head: head(projectfile.KindHTTP), ID: "req_h", Name: "h", Method: "GET", URL: "http://x/", Settings: projectfile.RequestSettings{Proxy: proxy}},
		&projectfile.GraphQLRequest{Head: head(projectfile.KindGraphQL), ID: "req_g", Name: "g", URL: "http://x/", Settings: projectfile.RequestSettings{Proxy: proxy}},
		&projectfile.SSERequest{Head: head(projectfile.KindSSE), ID: "req_s", Name: "s", URL: "http://x/", Settings: projectfile.RequestSettings{Proxy: proxy}},
		&projectfile.TCPRequest{Head: head(projectfile.KindTCP), ID: "req_t", Name: "t", Address: "x:1", Settings: projectfile.TCPSettings{Proxy: proxy}},
		&projectfile.UDPRequest{Head: head(projectfile.KindUDP), ID: "req_u", Name: "u", Address: "x:1", Settings: projectfile.UDPSettings{Proxy: proxy}},
		&projectfile.WebSocketRequest{Head: head(projectfile.KindWebSocket), ID: "req_w", Name: "w", URL: "ws://x/", Settings: projectfile.WebSocketSettings{Proxy: proxy}},
		&projectfile.SocketIORequest{Head: head(projectfile.KindSocketIO), ID: "req_i", Name: "i", URL: "http://x/", Settings: projectfile.SocketIOSettings{Proxy: proxy}},
		&projectfile.GRPCRequest{Head: head(projectfile.KindGRPC), ID: "req_r", Name: "r", Address: "x:1", Settings: projectfile.GRPCSettings{Proxy: proxy}},
	}
	store := New(t.TempDir())
	for _, f := range files {
		id := string(f.FileKind())
		out, err := store.MoveOut(id, f)
		if err != nil {
			t.Fatalf("%T: %v", f, err)
		}
		data, err := projectfile.Marshal(out)
		if err != nil {
			t.Fatalf("%T: %v", f, err)
		}
		if strings.Contains(string(data), "s3cr3t-proxy") {
			t.Errorf("%T: the file holds the proxy password:\n%s", f, data)
		}
		if !strings.Contains(string(data), `"url": "socks5h://bob@proxy.lan:1080"`) || !strings.Contains(string(data), `"localSecrets": [`) {
			t.Errorf("%T: file = %s", f, data)
		}
		side, err := os.ReadFile(store.Path(id))
		if err != nil || !strings.Contains(string(side), "s3cr3t-proxy") {
			t.Errorf("%T: side file = %s, %v", f, side, err)
		}
		if !NeedsLocal(out) {
			t.Errorf("%T: NeedsLocal = false", f)
		}
		values, err := store.Read(id)
		if err != nil {
			t.Fatal(err)
		}
		back := MergeFile(out, values)
		if p := projectfile.ProxyOf(back); p == nil || p.Password != "s3cr3t-proxy" {
			t.Errorf("%T: merged proxy = %+v", f, p)
		}
		// The input is not changed.
		if projectfile.ProxyOf(f).URL != proxy.URL {
			t.Errorf("%T: MoveOut changed its input", f)
		}
	}
}

func head(k projectfile.Kind) projectfile.Head {
	return projectfile.Head{SchemaVersion: projectfile.CurrentVersion, Kind: k}
}
