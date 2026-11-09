package request

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

func TestBuildProxy(t *testing.T) {
	vars := map[string]string{"host": "proxy.lan", "port": "1081", "user": "bob", "pw": "s3cret"}
	expand := func(s string) (string, error) {
		for k, v := range vars {
			s = strings.ReplaceAll(s, "{{"+k+"}}", v)
		}
		return s, nil
	}
	ok := []struct {
		name string
		in   projectfile.RequestProxy
		mode string
		url  string
		pw   string
	}{
		{"missing member inherits", projectfile.RequestProxy{}, projectfile.ProxyInherit, "", ""},
		{"inherit keeps nothing", projectfile.RequestProxy{Mode: "inherit", URL: "socks5h://x"}, projectfile.ProxyInherit, "", ""},
		{"none", projectfile.RequestProxy{Mode: "none", URL: "socks5h://x"}, projectfile.ProxyNone, "", ""},
		{"socks5h", projectfile.RequestProxy{Mode: "custom", URL: "socks5h://proxy.lan:1080"}, projectfile.ProxyCustom, "socks5h://proxy.lan:1080", ""},
		{"variables and a password", projectfile.RequestProxy{Mode: "custom", URL: "socks5://{{user}}@{{host}}:{{port}}", Password: "{{pw}}"}, projectfile.ProxyCustom, "socks5://bob@proxy.lan:1081", "s3cret"},
		{"typed password", projectfile.RequestProxy{Mode: "custom", URL: "http://bob@proxy.lan:3128", Password: "p:w", LocalSecrets: []string{"password"}}, projectfile.ProxyCustom, "http://bob@proxy.lan:3128", "p:w"},
		{"socks4a", projectfile.RequestProxy{Mode: "custom", URL: "socks4a://proxy.lan"}, projectfile.ProxyCustom, "socks4a://proxy.lan:1080", ""},
	}
	for _, tt := range ok {
		t.Run(tt.name, func(t *testing.T) {
			p, err := BuildProxy(tt.in, expand)
			if err != nil {
				t.Fatal(err)
			}
			if p.Mode() != tt.mode || p.URL.String() != tt.url || p.URL.Password != tt.pw {
				t.Errorf("BuildProxy = %s %q (password %q), want %s %q (%q)", p.Mode(), p.URL.String(), p.URL.Password, tt.mode, tt.url, tt.pw)
			}
			if strings.Contains(p.String(), "s3cret") {
				t.Errorf("String() = %q shows the password", p.String())
			}
			ctx := p.Context(context.Background())
			got, set := netx.RequestProxyFrom(ctx)
			if set != (tt.mode != projectfile.ProxyInherit) || got != p.URL {
				t.Errorf("Context: %v %v", got, set)
			}
		})
	}

	bad := []struct {
		name  string
		in    projectfile.RequestProxy
		code  string
		field string
	}{
		{"unknown mode", projectfile.RequestProxy{Mode: "pac"}, CodeInvalidSetting, FieldProxyMode},
		{"no URL", projectfile.RequestProxy{Mode: "custom"}, netx.CodeProxyInvalidURL, FieldProxyURL},
		{"bad scheme", projectfile.RequestProxy{Mode: "custom", URL: "ftp://proxy.lan"}, netx.CodeProxyInvalidURL, FieldProxyURL},
		{"password without user", projectfile.RequestProxy{Mode: "custom", URL: "socks5h://proxy.lan", Password: "s3cret"}, netx.CodeProxyInvalidURL, FieldProxyPassword},
		{"socks4 password", projectfile.RequestProxy{Mode: "custom", URL: "socks4://bob@proxy.lan", Password: "s3cret"}, netx.CodeProxyInvalidURL, FieldProxyPassword},
		{"password elsewhere", projectfile.RequestProxy{Mode: "custom", URL: "socks5h://bob@proxy.lan", LocalSecrets: []string{"password"}}, CodeProxyPasswordMissing, FieldProxyPassword},
	}
	for _, tt := range bad {
		t.Run(tt.name, func(t *testing.T) {
			_, err := BuildProxy(tt.in, expand)
			e, ok := errors.AsType[*errs.Error](err)
			if !ok || e.Code != tt.code || e.Details["field"] != tt.field {
				t.Fatalf("err = %v (%+v), want %s on %s", err, e, tt.code, tt.field)
			}
			if strings.Contains(e.Message+e.Hint+strings.Join(detailValues(e), " "), "s3cret") {
				t.Errorf("the error shows the password: %+v", e)
			}
		})
	}
	if _, err := BuildProxy(projectfile.RequestProxy{Mode: "custom", URL: "{{nope}}"}, func(string) (string, error) { return "", errors.New("unresolved") }); err == nil || err.Error() != "unresolved" {
		t.Errorf("an expand error = %v, want it returned", err)
	}
}

func detailValues(e *errs.Error) []string {
	var out []string
	for _, v := range e.Details {
		out = append(out, v)
	}
	return out
}
