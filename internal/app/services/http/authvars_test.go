package http

import (
	"crypto/md5"
	"encoding/hex"
	"fmt"
	"io"
	nethttp "net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// authVarsProject is a project with a request four folders deep that
// inherits its auth: api (the collection) sets it, v1, users and admins
// inherit. dev and prod define the same names with other values, secret
// ones included; admins defines "scheme" itself. It returns the IDs of
// the environments.
func authVarsProject(t *testing.T, f *projectFixture, base string) (dev, prod string) {
	t.Helper()
	if _, err := collection.SaveGlobalVariables(f.coll, t.Context(), []projectfile.Variable{{Name: "base", Value: base}}); err != nil {
		t.Fatal(err)
	}
	ids := map[string]string{}
	for _, name := range []string{"dev", "prod"} {
		env, err := f.envs.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.envs.SaveVariables(env.ID, []environments.Variable{
			{Name: "user", Value: name + "-user"},
			{Name: "pass", Value: name + "-pass", Secret: true},
			{Name: "token", Value: name + "-token", Secret: true},
			{Name: "scheme", Value: "Tok"},
			{Name: "keyName", Value: "X-Key"},
			{Name: "qname", Value: name + "_key"},
			{Name: "apiKey", Value: name + "-api-key", Secret: true},
		}); err != nil {
			t.Fatal(err)
		}
		ids[name] = env.ID
	}
	f.write(t, "api/v1/_folder.json", `{"kind": "folder", "id": "fld_v1", "name": "v1", "auth": {"type": "inherit"}}`)
	f.write(t, "api/v1/users/_folder.json", `{"kind": "folder", "id": "fld_users", "name": "Users"}`)
	f.write(t, "api/v1/users/admins/_folder.json", `{"kind": "folder", "id": "fld_admins", "name": "Admins", "auth": {"type": "inherit"}, "variables": [{"name": "scheme", "value": "Admin"}]}`)
	f.write(t, "api/v1/users/admins/list.json", `{"kind": "http", "id": "req_list", "name": "List", "method": "GET", "url": "{{base}}/list", "auth": {"type": "inherit"}}`)
	return ids["dev"], ids["prod"]
}

// TestInheritedAuthResolvesVariables (AUTH-01, AUTH-32): a request deep
// inside nested folders sends the nearest folder's auth that sets one,
// with the {{variables}} of every field of every type resolved with the
// request's own scopes (the active environment, and a variable of the
// request's folder below the folder that set the auth), secret ones
// included; switching the environment changes what is sent.
func TestInheritedAuthResolvesVariables(t *testing.T) {
	t.Parallel()
	f := newProjectFixture(t)
	dev, prod := authVarsProject(t, f, f.servers.URL)

	// The server answers "<uri>|<Authorization>|<basic user>:<basic password>|<X-Key>".
	tests := []struct {
		name string
		auth string
		want func(env string) string
	}{
		{"bearer", `{"type": "bearer", "prefix": "{{scheme}}", "token": "{{token}}"}`,
			func(env string) string { return "/list|Admin " + env + "-token|:|" }},
		{"basic", `{"type": "basic", "username": "{{user}}", "password": "{{pass}}"}`,
			func(env string) string {
				return fmt.Sprintf("/list|Basic %s|%s-user:%s-pass|", basic(env+"-user", env+"-pass"), env, env)
			}},
		{"api key in a header", `{"type": "apikey", "key": "{{keyName}}", "value": "{{apiKey}}"}`,
			func(env string) string { return "/list||:|" + env + "-api-key" }},
		{"api key in the query", `{"type": "apikey", "key": "{{qname}}", "value": "{{apiKey}}", "in": "query"}`,
			func(env string) string { return "/list?" + env + "_key=" + env + "-api-key||:|" }},
	}
	for _, tt := range tests {
		f.write(t, "api/_folder.json", `{"kind": "folder", "id": "fld_api", "name": "API", "auth": `+tt.auth+`}`)
		f.reload(t)
		for env, id := range map[string]string{"dev": dev, "prod": prod} {
			if err := f.envs.SetActive(id); err != nil {
				t.Fatal(err)
			}
			if got, want := f.sendBody(t, "req_list"), tt.want(env); got != want {
				t.Errorf("%s, %s: sent %q, want %q", tt.name, env, got, want)
			}
		}
	}

	// The nearest folder that sets auth wins over the ones above.
	if err := f.envs.SetActive(prod); err != nil {
		t.Fatal(err)
	}
	f.write(t, "api/v1/users/_folder.json", `{"kind": "folder", "id": "fld_users", "name": "Users", "auth": {"type": "bearer", "prefix": "", "token": "users-{{user}}"}}`)
	f.reload(t)
	if got := f.sendBody(t, "req_list"); got != "/list|users-prod-user|:|" {
		t.Errorf("nearest folder: sent %q", got)
	}

	// An undefined name in an inherited auth field is reported like any
	// other before sending.
	f.write(t, "api/v1/users/_folder.json", `{"kind": "folder", "id": "fld_users", "name": "Users", "auth": {"type": "bearer", "token": "{{nope}}"}}`)
	f.reload(t)
	if _, err := f.svc.Send(t.Context(), SendInputDTO{RequestID: "req_list"}); err == nil || !strings.Contains(err.Error(), "nope") {
		t.Errorf("undefined variable in inherited auth: err = %v", err)
	}
}

func basic(user, pass string) string {
	r, _ := nethttp.NewRequest("GET", "http://x", nil)
	r.SetBasicAuth(user, pass)
	return strings.TrimPrefix(r.Header.Get("Authorization"), "Basic ")
}

// TestInheritedDigestResolvesVariables (AUTH-06, AUTH-32): Digest's
// username and password, inherited from a folder, are rendered per
// environment before the challenge is answered.
func TestInheritedDigestResolvesVariables(t *testing.T) {
	t.Parallel()
	h := func(s string) string {
		sum := md5.Sum([]byte(s))
		return hex.EncodeToString(sum[:])
	}
	passwords := map[string]string{"dev-user": "dev-pass", "prod-user": "prod-pass"}
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		p := map[string]string{}
		for part := range strings.SplitSeq(strings.TrimPrefix(r.Header.Get("Authorization"), "Digest "), ", ") {
			k, v, _ := strings.Cut(part, "=")
			p[k] = strings.Trim(v, `"`)
		}
		ha1 := h(p["username"] + ":r:" + passwords[p["username"]])
		ha2 := h(r.Method + ":" + p["uri"])
		if pass, ok := passwords[p["username"]]; ok && pass != "" && p["response"] == h(ha1+":n1:"+p["nc"]+":"+p["cnonce"]+":auth:"+ha2) {
			io.WriteString(w, "welcome "+p["username"])
			return
		}
		w.Header().Set("WWW-Authenticate", `Digest realm="r", nonce="n1", qop="auth"`)
		w.WriteHeader(nethttp.StatusUnauthorized)
	}))
	t.Cleanup(srv.Close)
	f := newProjectFixture(t)
	dev, prod := authVarsProject(t, f, srv.URL)
	f.write(t, "api/_folder.json", `{"kind": "folder", "id": "fld_api", "name": "API", "auth": {"type": "digest", "username": "{{user}}", "password": "{{pass}}"}}`)
	f.reload(t)
	for env, id := range map[string]string{"dev": dev, "prod": prod} {
		if err := f.envs.SetActive(id); err != nil {
			t.Fatal(err)
		}
		if got := f.sendBody(t, "req_list"); got != "welcome "+env+"-user" {
			t.Errorf("%s: got %q", env, got)
		}
	}
}
