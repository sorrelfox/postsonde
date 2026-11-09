package http

import (
	"encoding/json"
	"fmt"
	nethttp "net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/app/services/collection"
	"github.com/sorrelfox/postsonde/internal/core/environments"
	"github.com/sorrelfox/postsonde/internal/core/localvalues"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// echoHeaders is a server that answers with the X-V-* headers it got, as
// a JSON object keyed by the rest of the header name, lower case.
func echoHeaders(t *testing.T) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(nethttp.HandlerFunc(func(w nethttp.ResponseWriter, r *nethttp.Request) {
		out := map[string]string{}
		for name, values := range r.Header {
			if rest, ok := strings.CutPrefix(strings.ToLower(name), "x-v-"); ok {
				out[rest] = values[0]
			}
		}
		_ = json.NewEncoder(w).Encode(out)
	}))
	t.Cleanup(srv.Close)
	return srv
}

// headersRequest writes request id at rel: a GET to url with one
// "X-V-<name>: {{name}}" header per name.
func headersRequest(t *testing.T, f *projectFixture, rel, id, url string, names ...string) {
	t.Helper()
	headers := make([]string, 0, len(names))
	for _, n := range names {
		headers = append(headers, fmt.Sprintf(`{"name": "X-V-%s", "value": "{{%s}}"}`, n, n))
	}
	f.write(t, rel, fmt.Sprintf(`{"kind": "http", "id": %q, "name": %q, "method": "GET", "url": %q, "headers": [%s]}`,
		id, id, url, strings.Join(headers, ", ")))
}

func sentHeaders(t *testing.T, body string) map[string]string {
	t.Helper()
	got := map[string]string{}
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("response %q: %v", body, err)
	}
	return got
}

// TestBaseEnvironmentInheritance (ENV-05): the global variables are
// the base environment every environment inherits. A name the active
// environment does not define is the base's value; one it defines
// overrides the base. Secret and local values work at both levels: a
// local value replaces its shared value within its own level only, and a
// secret without a value on this computer, like a disabled variable,
// hands the name back to the base.
func TestBaseEnvironmentInheritance(t *testing.T) {
	t.Parallel()
	f := newProjectFixture(t)
	srv := echoHeaders(t)

	// The base: postsonde.json's variables, secrets in the side file,
	// local values in local-values/global/.
	if _, err := collection.SaveGlobalVariables(f.coll, t.Context(), []projectfile.Variable{
		{Name: "inherited", Value: "base-inherited"},
		{Name: "overridden", Value: "base-overridden"},
		{Name: "baseLocal", Value: "base-shared"},
		{Name: "baseLocalOverridden", Value: "base-shared"},
		{Name: "envLocal", Value: "base-envLocal"},
		{Name: "baseSecret", Value: "base-s3cret", Secret: true},
		{Name: "envSecret", Value: "base-envSecret"},
		{Name: "bothSecret", Value: "base-both-s3cret", Secret: true},
		{Name: "envSecretNoValue", Value: "base-fallback"},
		{Name: "envDisabled", Value: "base-disabled-fallback"},
	}); err != nil {
		t.Fatal(err)
	}
	if err := localvalues.New(f.ws.LocalDir).Write(localvalues.Global(f.ws.ID()), map[string]string{
		"baseLocal":           "base-local",
		"baseLocalOverridden": "base-local",
	}); err != nil {
		t.Fatal(err)
	}

	// A sub-environment that overrides some of them.
	staging, err := f.envs.Create("Staging")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.envs.SaveVariables(staging.ID, []environments.Variable{
		{Name: "overridden", Value: "staging-overridden"},
		{Name: "baseLocalOverridden", Value: "staging-shared"},
		{Name: "envLocal", Value: "staging-shared", Local: true, LocalValue: "staging-local"},
		{Name: "envSecret", Value: "staging-s3cret", Secret: true},
		{Name: "bothSecret", Value: "staging-both-s3cret", Secret: true},
		{Name: "envSecretNoValue", Secret: true},
		{Name: "envDisabled", Value: "staging-disabled", Disabled: true},
		{Name: "envOnly", Value: "staging-only"},
	}); err != nil {
		t.Fatal(err)
	}
	// A second one that overrides nothing.
	plain, err := f.envs.Create("Plain")
	if err != nil {
		t.Fatal(err)
	}

	names := []string{"inherited", "overridden", "baseLocal", "baseLocalOverridden", "envLocal", "baseSecret",
		"envSecret", "bothSecret", "envSecretNoValue", "envDisabled"}
	headersRequest(t, f, "all.json", "req_all", srv.URL+"/", names...)
	f.reload(t)

	type want struct {
		value, scope  string
		secret, local bool
	}
	cases := []struct {
		env  string
		want map[string]want
	}{
		{staging.ID, map[string]want{
			"inherited":           {"base-inherited", "Global", false, false},
			"overridden":          {"staging-overridden", "Environment: Staging", false, false},
			"baseLocal":           {"base-local", "Global", false, true},
			"baseLocalOverridden": {"staging-shared", "Environment: Staging", false, false},
			"envLocal":            {"staging-local", "Environment: Staging", false, true},
			"baseSecret":          {"base-s3cret", "Global", true, false},
			"envSecret":           {"staging-s3cret", "Environment: Staging", true, false},
			"bothSecret":          {"staging-both-s3cret", "Environment: Staging", true, false},
			"envSecretNoValue":    {"base-fallback", "Global", false, false},
			"envDisabled":         {"base-disabled-fallback", "Global", false, false},
		}},
		// Switching to an environment without overrides: every name is
		// the base's.
		{plain.ID, map[string]want{
			"inherited":           {"base-inherited", "Global", false, false},
			"overridden":          {"base-overridden", "Global", false, false},
			"baseLocal":           {"base-local", "Global", false, true},
			"baseLocalOverridden": {"base-local", "Global", false, true},
			"envLocal":            {"base-envLocal", "Global", false, false},
			"baseSecret":          {"base-s3cret", "Global", true, false},
			"envSecret":           {"base-envSecret", "Global", false, false},
			"bothSecret":          {"base-both-s3cret", "Global", true, false},
			"envSecretNoValue":    {"base-fallback", "Global", false, false},
			"envDisabled":         {"base-disabled-fallback", "Global", false, false},
		}},
	}
	for _, c := range cases {
		if err := f.envs.SetActive(c.env); err != nil {
			t.Fatal(err)
		}
		sent := sentHeaders(t, f.sendBody(t, "req_all"))
		for name, w := range c.want {
			if got := sent[strings.ToLower(name)]; got != w.value {
				t.Errorf("%s: sent %s = %q, want %q", c.env, name, got, w.value)
			}
			// The hover and completion say the same (ENV-12).
			v := f.variable(t, "req_all", name)
			if v.Scope != w.scope || v.Secret != w.secret || v.Local != w.local || (!w.secret && v.Value != w.value) || (w.secret && v.Value != "") {
				t.Errorf("%s: %s = %+v, want %+v", c.env, name, v, w)
			}
		}
	}

	// A name only the sub-environment defines is not defined in another.
	if err := f.envs.SetActive(staging.ID); err != nil {
		t.Fatal(err)
	}
	if v := f.variable(t, "req_all", "envOnly"); v.Value != "staging-only" {
		t.Errorf("envOnly = %+v", v)
	}
	if err := f.envs.SetActive(plain.ID); err != nil {
		t.Fatal(err)
	}
	got, err := f.svc.Variables(t.Context(), "req_all")
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range got.Variables {
		if v.Name == "envOnly" {
			t.Errorf("envOnly is defined in Plain: %+v", v)
		}
	}

	// No secret value, base or sub-environment, reached a project file.
	err = filepath.WalkDir(f.dir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		for _, secret := range []string{"base-s3cret", "staging-s3cret", "base-both-s3cret", "staging-both-s3cret", "base-local", "staging-local"} {
			if strings.Contains(string(data), secret) {
				t.Errorf("%s holds %q", path, secret)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
