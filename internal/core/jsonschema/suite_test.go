package jsonschema

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/jsonpath"
)

// Two trees of fixtures in the file format of the official
// JSON-Schema-Test-Suite (<draft>/*.json, with optional/format for format
// assertions; each file an array of groups, each with a schema and test
// values):
//
//   - testdata/JSON-Schema-Test-Suite: the official suite, copied unchanged
//     at the commit its README names, for draft 2020-12 and draft-07, with
//     the remote documents its references need (remotes/, given to the
//     compiler as Options.Resources under http://localhost:1234/…; nothing
//     is fetched).
//   - testdata/suite: cases written for this package, one file per keyword
//     family (exact numbers, ECMA-262 escapes, draft-07 spellings…).
//
// officialSkips lists the official cases this package deliberately does
// not pass, each with the reason; docs/jsonschema.md describes them.

type suiteGroup struct {
	Description string          `json:"description"`
	Schema      json.RawMessage `json:"schema"`
	Tests       []suiteCase     `json:"tests"`
}

type suiteCase struct {
	Description string          `json:"description"`
	Data        json.RawMessage `json:"data"`
	Valid       bool            `json:"valid"`
}

// officialDir is the copy of the official suite.
var officialDir = filepath.Join("testdata", "JSON-Schema-Test-Suite")

// officialDrafts are the suite's directories this package is tested
// against, with the draft each is compiled as.
var officialDrafts = []struct {
	dir   string
	draft Draft
}{{"draft2020-12", Draft2020}, {"draft7", Draft7}}

// Reasons of the skips.
const (
	skipMetaSchema = "references a meta-schema: they are not bundled, and nothing is fetched"
	skipVocabulary = "needs $vocabulary (a custom meta-schema turning vocabularies off), which is not supported"
	skipCrossDraft = "mixes drafts in one schema (a resource with another $schema), which is not supported"
	skipContent    = "contentEncoding and contentMediaType are annotations only"
	skipUTS46      = "needs the UTS #46 mapping (width folding, ignored characters), which is not applied"
)

// officialSkips maps "<draft>/<file>/<group description>" (a whole group)
// or "<draft>/<file>/<group description>/<test description>" (one case)
// to the reason it is skipped. Skipped cases still run: a skip that
// matches no case, or whose cases all pass, fails the test, so the list
// stays exact.
var officialSkips = map[string]string{
	"draft2020-12/defs.json/validate definition against metaschema":  skipMetaSchema,
	"draft2020-12/ref.json/remote ref, containing refs itself":       skipMetaSchema,
	"draft7/definitions.json/validate definition against metaschema": skipMetaSchema,
	"draft7/ref.json/remote ref, containing refs itself":             skipMetaSchema,

	"draft2020-12/vocabulary.json/schema that uses custom metaschema with with no validation vocabulary/no validation: invalid number, but it still validates": skipVocabulary,
	"draft2020-12/optional/format-assertion.json/schema that uses custom metaschema with format-assertion: false/format-assertion: false: invalid string":      skipVocabulary,
	"draft2020-12/optional/format-assertion.json/schema that uses custom metaschema with format-assertion: true/format-assertion: true: invalid string":        skipVocabulary,

	"draft2020-12/optional/cross-draft.json/refs to historic drafts are processed as historic drafts/first item not a string is valid": skipCrossDraft,
	"draft7/optional/cross-draft.json/refs to future drafts are processed as future drafts/missing bar is invalid":                     skipCrossDraft,

	"draft7/optional/content.json/validation of string-encoded content based on media type/an invalid JSON document":             skipContent,
	"draft7/optional/content.json/validation of binary string-encoding/an invalid base64 string (% is not a valid character)":    skipContent,
	"draft7/optional/content.json/validation of binary-encoded media type documents/a validly-encoded invalid JSON document":     skipContent,
	"draft7/optional/content.json/validation of binary-encoded media type documents/an invalid base64 string that is valid JSON": skipContent,

	"draft2020-12/optional/format/idn-hostname.json/validation of internationalized host names/a zero width space is ignored by the mapping":                        skipUTS46,
	"draft2020-12/optional/format/idn-hostname.json/validation of internationalized host names/a label of 63 fullwidth characters is valid after mapping":           skipUTS46,
	"draft2020-12/optional/format/idn-hostname.json/validation of internationalized host names/an ACE prefix produced by the mapping step is decoded as an A-label": skipUTS46,
	"draft7/optional/format/idn-hostname.json/validation of internationalized host names/a zero width space is ignored by the mapping":                              skipUTS46,
	"draft7/optional/format/idn-hostname.json/validation of internationalized host names/a label of 63 fullwidth characters is valid after mapping":                 skipUTS46,
	"draft7/optional/format/idn-hostname.json/validation of internationalized host names/an ACE prefix produced by the mapping step is decoded as an A-label":       skipUTS46,
}

// suiteCounts are the outcomes of one tree or draft.
type suiteCounts struct{ pass, skip, fail int }

func TestOfficialSuite(t *testing.T) {
	remotes := loadRemotes(t, filepath.Join(officialDir, "remotes"))
	used := map[string]bool{}
	for _, d := range officialDrafts {
		root := filepath.Join(officialDir, "tests", d.dir)
		counts := runSuiteTree(t, root, d.dir+"/", func(rel string) Options {
			return Options{Draft: d.draft, FormatAssertion: strings.HasPrefix(rel, "optional/format/"), Resources: remotes}
		}, officialSkips, used)
		t.Logf("%s: %d passed, %d skipped, %d failed", d.dir, counts.pass, counts.skip, counts.fail)
		if counts.pass == 0 {
			t.Errorf("%s: no case ran", d.dir)
		}
	}
	var stale []string
	for k := range officialSkips {
		if !used[k] {
			stale = append(stale, k)
		}
	}
	sort.Strings(stale)
	for _, k := range stale {
		t.Errorf("skip %q matches no case", k)
	}
}

func TestSuite(t *testing.T) {
	root := filepath.Join("testdata", "suite")
	counts := runSuiteTree(t, root, "", func(rel string) Options {
		opts := Options{FormatAssertion: strings.Contains(rel, "optional/format/")}
		if strings.HasPrefix(rel, "draft7/") {
			opts.Draft = Draft7
		}
		return opts
	}, nil, nil)
	if counts.pass < 300 {
		t.Errorf("only %d suite cases ran", counts.pass)
	}
}

// loadRemotes reads the suite's remote documents, keyed by the URIs the
// suite serves them at.
func loadRemotes(t *testing.T, dir string) map[string]any {
	t.Helper()
	out := map[string]any{}
	err := filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".json" {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		doc, err := jsonpath.Decode(data)
		if err != nil {
			t.Fatalf("%s: %v", path, err)
		}
		rel, _ := filepath.Rel(dir, path)
		out["http://localhost:1234/"+filepath.ToSlash(rel)] = doc
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// runSuiteTree runs every file under root. The keys of skips start with
// prefix and the file's path under root; used records the keys that
// matched.
func runSuiteTree(t *testing.T, root, prefix string, optsFor func(rel string) Options, skips map[string]string, used map[string]bool) suiteCounts {
	t.Helper()
	var counts suiteCounts
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".json" {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		rel = filepath.ToSlash(rel)
		opts := optsFor(rel)
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		var groups []suiteGroup
		if err := json.Unmarshal(data, &groups); err != nil {
			t.Fatalf("%s: %v", rel, err)
		}
		for _, g := range groups {
			groupKey := prefix + rel + "/" + g.Description
			groupWhy, groupSkipped := skips[groupKey]
			if groupSkipped {
				used[groupKey] = true
			}
			t.Run(groupKey, func(t *testing.T) {
				s, compileErr := CompileJSON(g.Schema, opts)
				groupFailed := false
				for _, tc := range g.Tests {
					why := runSuiteCase(s, compileErr, tc)
					key := groupKey + "/" + tc.Description
					caseWhy, caseSkipped := skips[key]
					switch {
					case groupSkipped:
						counts.skip++
						groupFailed = groupFailed || why != ""
					case caseSkipped:
						used[key] = true
						counts.skip++
						if why == "" {
							t.Errorf("%s: passes, but is skipped (%s): remove the skip", tc.Description, caseWhy)
						}
					case why != "":
						counts.fail++
						t.Errorf("%s: %s", tc.Description, why)
					default:
						counts.pass++
					}
				}
				if groupSkipped && !groupFailed {
					t.Errorf("every case passes, but the group is skipped (%s): remove the skip", groupWhy)
				}
			})
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return counts
}

// runSuiteCase returns why the case fails, or "".
func runSuiteCase(s *Schema, compileErr error, tc suiteCase) string {
	if compileErr != nil {
		return "compile: " + compileErr.Error()
	}
	doc, err := jsonpath.Decode(tc.Data)
	if err != nil {
		return "data: " + err.Error()
	}
	res, err := s.Validate(context.Background(), doc, DefaultLimits)
	switch {
	case err != nil:
		return "validate: " + err.Error()
	case res.Valid != tc.Valid:
		return fmt.Sprintf("valid = %v, want %v (errors %v)", res.Valid, tc.Valid, res.Errors)
	case res.Valid != (len(res.Errors) == 0):
		return fmt.Sprintf("valid = %v with %d errors", res.Valid, len(res.Errors))
	}
	return ""
}
