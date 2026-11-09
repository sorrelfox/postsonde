package curlimport_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/curl"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/importer/curlimport"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

const script = "#!/bin/sh\n" +
	"set -e\n" +
	"\n" +
	"# List users\n" +
	"curl https://api.test/users \\\n" +
	"  -H 'Accept: application/json'\n" +
	"\n" +
	"# Create a user\n" +
	"$ curl -X POST https://api.test/users \\\n" +
	"  --data-raw '{\n" +
	"curl is not a command here\n" +
	"}' --retry 3\n" +
	"\n" +
	"curl -H\n" +
	"curl.exe https://api.test/health | jq .\n" +
	"# trailing comment\n"

func TestDetect(t *testing.T) {
	im := curlimport.Importer{}
	cases := []struct {
		name, data string
		want       int
	}{
		{"a.sh", script, importer.Maybe + 10},
		{"a.log", "  \n# note\ncurl https://x.test", importer.Likely},
		{"a.txt", `{"curl": 1}`, importer.No},
		{"a.txt", "", importer.No},
		{"a.txt", "echo hi\ncurl x", importer.Maybe + 10},
		{"a.json", "[\"curl x\"]", importer.No},
		{"a.cmd", "C:\\Users\\ada>curl.exe x", importer.Likely + 10},
	}
	for _, c := range cases {
		if got := im.Detect(importer.Source{Name: c.name, Data: []byte(c.data)}); got != c.want {
			t.Errorf("Detect(%q, %q) = %d, want %d", c.name, c.data, got, c.want)
		}
	}
}

func TestParseFile(t *testing.T) {
	m, err := curlimport.Importer{}.Parse(context.Background(), importer.Source{Name: "api.sh", Data: []byte(script)})
	if err != nil {
		t.Fatal(err)
	}
	if m.Name != "api" {
		t.Errorf("name = %q", m.Name)
	}
	if len(m.Diagnostics) != 1 || m.Diagnostics[0].Code != curlimport.CodeNotCommand || m.Diagnostics[0].Source.Line != 2 {
		t.Errorf("file diagnostics = %+v", m.Diagnostics)
	}
	items := m.Root.Items
	if len(items) != 4 {
		t.Fatalf("items = %d", len(items))
	}

	list := items[0].Request
	if list == nil || list.Name != "List users" || list.Source.Line != 5 || list.Source.Path != "command 1" {
		t.Fatalf("first = %+v", items[0])
	}
	if f := list.File.(*projectfile.HTTPRequest); f.URL != "https://api.test/users" || len(f.Headers) != 1 {
		t.Errorf("first request = %+v", f)
	}

	create := items[1].Request
	if create == nil || create.Name != "Create a user" || create.Source.Line != 9 {
		t.Fatalf("second = %+v", items[1])
	}
	if f := create.File.(*projectfile.HTTPRequest); f.Method != "POST" || !strings.Contains(f.Body.Text, "curl is not a command here") {
		t.Errorf("second request = %+v", f)
	}
	// --retry is ignored, reported at its place in the file.
	var retry *importer.Diagnostic
	for i := range create.Diagnostics {
		if create.Diagnostics[i].Code == "curl.ignored_option" {
			retry = &create.Diagnostics[i]
		}
	}
	if retry == nil || retry.Source.Line != 12 || retry.Source.Column != 4 || retry.Source.Path != "command 2" {
		t.Errorf("retry diagnostic = %+v", retry)
	}

	// A command with an error is skipped with the reason.
	skipped := items[2].Skipped
	if skipped == nil || skipped.Code != "curl.missing_url" || skipped.Source.Line != 14 {
		t.Errorf("third = %+v", *items[2].Skipped)
	}

	health := items[3].Request
	if health == nil || health.Name != "health" {
		t.Fatalf("fourth = %+v", items[3])
	}
	var trailing bool
	for _, d := range health.Diagnostics {
		trailing = trailing || d.Code == "curl.trailing_input"
		if d.Code == "curl.trailing_input" && d.Source.Line != 15 {
			t.Errorf("trailing at %+v", d.Source)
		}
	}
	if !trailing {
		t.Errorf("no trailing_input: %+v", health.Diagnostics)
	}
}

func TestParseRejects(t *testing.T) {
	im := curlimport.Importer{}
	for _, data := range []string{"", "# only a comment\n", "\xff\xfe"} {
		_, err := im.Parse(context.Background(), importer.Source{Data: []byte(data)})
		if !errors.Is(err, &errs.Error{Code: importer.CodeInvalidFile}) {
			t.Errorf("Parse(%q) = %v", data, err)
		}
	}
}

func TestCommand(t *testing.T) {
	m, err := curlimport.Command("curl -u ada:pw https://api.test/orders", "  ")
	if err != nil {
		t.Fatal(err)
	}
	r := m.Root.Items[0].Request
	if r.Name != "orders" || r.File.(*projectfile.HTTPRequest).Auth.Password != "pw" {
		t.Errorf("request = %+v", r)
	}
	if m, _ := curlimport.Command("curl https://api.test/orders", "Mine"); m.Root.Items[0].Request.Name != "Mine" {
		t.Errorf("name not kept")
	}
	if _, err := curlimport.Command("curl -H", ""); !errors.Is(err, &errs.Error{Code: curl.CodeInvalidCommand}) {
		t.Errorf("error = %v", err)
	}
}

func TestStartsCommand(t *testing.T) {
	yes := []string{"curl x", "curl", "$ curl x", "PS C:\\> curl.exe x", "/usr/bin/curl -s x", "CURL.EXE x", "C:\\Users\\ada>curl x"}
	no := []string{"curlx", "echo curl", `"curl" x`, "", "# curl x", "curly x"}
	for _, l := range yes {
		if !curl.StartsCommand(l) {
			t.Errorf("StartsCommand(%q) = false", l)
		}
	}
	for _, l := range no {
		if curl.StartsCommand(l) {
			t.Errorf("StartsCommand(%q) = true", l)
		}
	}
}
