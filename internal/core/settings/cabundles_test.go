package settings

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/tlsconf"
)

// abs returns an absolute path for name on any OS.
func abs(t *testing.T, name string) string {
	t.Helper()
	p, err := filepath.Abs(name)
	if err != nil {
		t.Fatal(err)
	}
	return p
}

func jsonString(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// TestCABundles: network.caBundles (NET-04) is read, validated, patched,
// compared and written; an empty list is left out of the file.
func TestCABundles(t *testing.T) {
	a, b := abs(t, "corp.pem"), abs(t, "dev.pem")

	// Load: a valid list.
	s, r, err := Load(writeSettings(t, `{"network":{"caBundles":`+jsonString(t, []string{a, b})+`}}`))
	if err != nil || len(r.Problems) != 0 || !slices.Equal(s.Network.CABundles, []string{a, b}) {
		t.Fatalf("Load = %+v, %+v, %v", s, r.Problems, err)
	}

	// Load: invalid lists fall back to none with a problem.
	for _, bad := range []string{`"ca.pem"`, `["ca.pem"]`, `[""]`, jsonString(t, []string{a, a}), `[1]`} {
		s, r, err := Load(writeSettings(t, `{"network":{"caBundles":`+bad+`}}`))
		if err != nil || s.Network.CABundles != nil || len(r.Problems) != 1 || r.Problems[0].Details["field"] != "network.caBundles" {
			t.Errorf("%s: Load = %+v, %+v, %v", bad, s.Network, r.Problems, err)
		}
	}

	// Apply replaces the list, copying it; an empty list clears it.
	list := []string{a}
	next, err := Defaults().Apply(Patch{Network: NetworkPatch{CABundles: &list}})
	if err != nil || !slices.Equal(next.Network.CABundles, list) {
		t.Fatalf("Apply = %+v, %v", next.Network, err)
	}
	list[0] = b
	if next.Network.CABundles[0] != a {
		t.Error("Apply kept the caller's slice")
	}
	if next.Equal(Defaults()) || !next.Equal(with(func(s *Settings) { s.Network.CABundles = []string{a} })) {
		t.Error("Equal ignores caBundles")
	}
	empty := []string{}
	cleared, err := next.Apply(Patch{Network: NetworkPatch{CABundles: &empty}})
	if err != nil || cleared.Network.CABundles != nil || !cleared.Equal(Defaults()) {
		t.Errorf("clearing = %+v, %v", cleared.Network, err)
	}
	for _, bad := range [][]string{{"relative.pem"}, {a, a}, {" "}} {
		if _, err := Defaults().Apply(Patch{Network: NetworkPatch{CABundles: &bad}}); err == nil {
			t.Errorf("Apply(%q) accepted", bad)
		}
	}

	// Marshal: written when set, left out when empty.
	data, err := Marshal(next, nil)
	if err != nil || !strings.Contains(string(data), `"caBundles"`) {
		t.Errorf("Marshal = %s, %v", data, err)
	}
	data, err = Marshal(Defaults(), nil)
	if err != nil || strings.Contains(string(data), "caBundles") {
		t.Errorf("Marshal defaults = %s, %v", data, err)
	}
}

// TestClientCertsSetting: network.clientCerts (AUTH-30) is read,
// validated, patched, compared and written; an empty list is left out.
func TestClientCertsSetting(t *testing.T) {
	pfx, pem := abs(t, "client.p12"), abs(t, "client.pem")
	list := []tlsconf.ClientCertEntry{
		{ID: "cc_1", Host: "*.example.com", PFXFile: pfx},
		{ID: "cc_2", Host: "localhost:8443", CertFile: pem},
	}
	s, r, err := Load(writeSettings(t, `{"network":{"clientCerts":`+jsonString(t, list)+`}}`))
	if err != nil || len(r.Problems) != 0 || !slices.Equal(s.Network.ClientCerts, list) {
		t.Fatalf("Load = %+v, %+v, %v", s.Network, r.Problems, err)
	}
	for _, bad := range []string{
		`{}`, `[{"id":"a"}]`, `[{"id":"a","host":"h","certFile":"rel.pem"}]`,
		jsonString(t, []tlsconf.ClientCertEntry{list[0], list[0]}),
		`[{"id":"a","host":"https://h","pfxFile":` + jsonString(t, pfx) + `}]`,
	} {
		s, r, err := Load(writeSettings(t, `{"network":{"clientCerts":`+bad+`}}`))
		if err != nil || s.Network.ClientCerts != nil || len(r.Problems) != 1 || r.Problems[0].Details["field"] != "network.clientCerts" {
			t.Errorf("%s: Load = %+v, %+v, %v", bad, s.Network, r.Problems, err)
		}
	}
	next, err := Defaults().Apply(Patch{Network: NetworkPatch{ClientCerts: &list}})
	if err != nil || !slices.Equal(next.Network.ClientCerts, list) || next.Equal(Defaults()) {
		t.Fatalf("Apply = %+v, %v", next.Network, err)
	}
	if err := next.Validate(); err != nil {
		t.Errorf("Validate = %v", err)
	}
	data, err := Marshal(next, nil)
	if err != nil || !strings.Contains(string(data), `"clientCerts"`) || !strings.Contains(string(data), `"pfxFile"`) {
		t.Errorf("Marshal = %s, %v", data, err)
	}
	empty := []tlsconf.ClientCertEntry{}
	cleared, err := next.Apply(Patch{Network: NetworkPatch{ClientCerts: &empty}})
	if err != nil || cleared.Network.ClientCerts != nil || !cleared.Equal(Defaults()) {
		t.Errorf("clearing = %+v, %v", cleared.Network, err)
	}
	if data, _ := Marshal(Defaults(), nil); strings.Contains(string(data), "clientCerts") {
		t.Errorf("Marshal defaults = %s", data)
	}
}
