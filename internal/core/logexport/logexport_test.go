package logexport

import (
	"encoding/json"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/httpclient"
)

const (
	token  = "tok-9f8e7d6c5b"
	pass   = `pa"ss\word <&>`
	apiKey = "key with space/and+plus"
)

func TestMaskText(t *testing.T) {
	m := NewMasker([]string{token, pass, apiKey, ""})
	tests := []struct {
		name, in, want string
	}{
		{"plain", "nothing secret here", "nothing secret here"},
		{"value", "sent " + token + " twice " + token, "sent ●● twice ●●"},
		{"query escaped", "wss://h/x?k=" + url.QueryEscape(apiKey), "wss://h/x?k=●●"},
		{"path escaped", "/p/" + url.PathEscape(apiKey), "/p/●●"},
		{"bearer line", "GET / HTTP/1.1\r\nAuthorization: Bearer abc.def\r\nHost: h\r\n", "GET / HTTP/1.1\r\nAuthorization: Bearer ●●\r\nHost: h\r\n"},
		{"lower-case h2 line", "  authorization: Basic dXNlcjpwYXNz\n", "  authorization: Basic ●●\n"},
		{"proxy line", "Proxy-Authorization: opaque", "Proxy-Authorization: ●●"},
		{"already masked", "Authorization: Bearer ••••••", "Authorization: Bearer ••••••"},
		{"digest keeps params", `Authorization: Digest username="u", response="0123abcd"`, `Authorization: Digest username="u", response="●●"`},
		{"not at line start", "the authorization: header", "the authorization: header"},
		{"secret with quotes", "password " + pass, "password ●●"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := m.Text(tt.in)
			want := strings.ReplaceAll(tt.want, "●●", httpclient.Mask)
			if got != want {
				t.Errorf("Text(%q)\n got %q\nwant %q", tt.in, got, want)
			}
			out, masked, err := m.Mask(tt.in, Text)
			if err != nil || out != got || masked != (got != tt.in) {
				t.Errorf("Mask(Text) = %q, %v, %v", out, masked, err)
			}
		})
	}
}

func TestMaskJSON(t *testing.T) {
	m := NewMasker([]string{token, pass})
	doc := map[string]any{
		"format": "postsonde-log",
		"entries": []any{
			map[string]any{"text": "hello " + token, "size": 12, "ok": true, "none": nil},
			map[string]any{"text": "pw=" + pass, "meta": map[string]any{token: "as a key"}},
			map[string]any{"head": "GET / HTTP/1.1\r\nAuthorization: Bearer xyz\r\n"},
			map[string]any{"headers": map[string]any{"authorization": "Bearer raw-credential", "Proxy-Authorization": "Basic Zm9vOmJhcg=="}},
			map[string]any{"html": "<a & b>"},
		},
	}
	for _, indent := range []string{"", "  "} {
		raw, err := json.MarshalIndent(doc, "", indent)
		if err != nil {
			t.Fatal(err)
		}
		out, masked, err := m.JSON(string(raw))
		if err != nil {
			t.Fatal(err)
		}
		if !masked {
			t.Error("masked = false")
		}
		for _, secret := range []string{token, pass, `pa\"ss\\word`, "xyz", "raw-credential", "Zm9vOmJhcg=="} {
			if strings.Contains(out, secret) {
				t.Errorf("output holds %q:\n%s", secret, out)
			}
		}
		var back map[string]any
		if err := json.Unmarshal([]byte(out), &back); err != nil {
			t.Fatalf("output is not JSON: %v\n%s", err, out)
		}
		entries := back["entries"].([]any)
		if got := entries[0].(map[string]any)["text"]; got != "hello "+httpclient.Mask {
			t.Errorf("entries[0].text = %q", got)
		}
		if got := entries[3].(map[string]any)["headers"].(map[string]any)["authorization"]; got != "Bearer "+httpclient.Mask {
			t.Errorf("authorization member = %q", got)
		}
		if got := entries[4].(map[string]any)["html"]; got != "<a & b>" {
			t.Errorf("html = %q", got)
		}
		if _, ok := entries[1].(map[string]any)["meta"].(map[string]any)[httpclient.Mask]; !ok {
			t.Errorf("secret member name not masked: %v", entries[1])
		}
		if indent != "" && !strings.Contains(out, "\n  \"entries\"") {
			t.Errorf("layout not kept:\n%s", out)
		}
	}

	// Nothing to mask: the text comes back as it was.
	in := `{"a": [1, 2.50, "x\u00e9"], "b": null}`
	out, masked, err := m.JSON(in)
	if err != nil || masked || out != in {
		t.Errorf("JSON(%s) = %s, %v, %v", in, out, masked, err)
	}
	if _, _, err := m.Mask("{not json", JSON); !errors.Is(err, ErrInvalidJSON) {
		t.Errorf("invalid JSON err = %v", err)
	}
}

func TestZeroMasker(t *testing.T) {
	var m *Masker
	if got := m.Text("Authorization: Bearer x\nplain " + token); got != "Authorization: Bearer "+httpclient.Mask+"\nplain "+token {
		t.Errorf("nil Masker Text = %q", got)
	}
}

func TestFormat(t *testing.T) {
	if !Text.Valid() || !JSON.Valid() || Format("csv").Valid() {
		t.Error("Valid")
	}
	if Text.Ext() != ".txt" || JSON.Ext() != ".json" {
		t.Error("Ext")
	}
}

func TestReadSecrets(t *testing.T) {
	local := t.TempDir()
	requests := filepath.Join(local, "secrets", "requests")
	envs := filepath.Join(local, "secrets", "environments")
	write := func(path, data string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	write(filepath.Join(requests, "req_1.json"), `{"version":1,"itemId":"req_1","values":{"auth.token":"`+token+`","auth.password":""}}`)
	write(filepath.Join(requests, "fld_1.json"), `{"version":1,"values":{"var.apiKey":"k-1","auth.token":"`+token+`"}}`)
	write(filepath.Join(envs, "env_1.json"), `{"version":1,"environmentId":"env_1","values":{"password":"p-2"}}`)
	write(filepath.Join(envs, "notes.txt"), `{"values":{"x":"not a side file"}}`)
	write(filepath.Join(envs, "broken.json"), `{"values":`)

	got, err := ReadSecrets(requests, envs, filepath.Join(local, "missing"), "")
	if want := []string{"k-1", "p-2", token}; !slices.Equal(got, want) {
		t.Errorf("ReadSecrets = %q, want %q", got, want)
	}
	if err == nil || !strings.Contains(err.Error(), "broken.json") {
		t.Errorf("err = %v, want the broken file", err)
	}

	got, err = ReadSecrets(filepath.Join(local, "missing"))
	if err != nil || len(got) != 0 {
		t.Errorf("missing dir = %q, %v", got, err)
	}
}
