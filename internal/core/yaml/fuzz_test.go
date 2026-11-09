package yaml

import (
	"encoding/json"
	"errors"
	"testing"
)

// FuzzParse parses random input. ParseAll must not panic and must return
// an *Error with a position (or the whole-input position 0:0); a parsed
// tree must convert to valid JSON or fail with an *Error.
func FuzzParse(f *testing.F) {
	for _, s := range []string{
		"a: 1\nb: [x, {y: z}]\nc: |\n  lit\n  eral\nd: >-\n  fo\n  ld\n",
		"- &a {x: 1}\n- *a\n- <<: *a\n  y: 2\n",
		"'q''s': \"esc \\u00e9 \\\n  more\"\n? k\n: v\n",
		"%YAML 1.2\n---\n- - a\n  - b\n...\n--- !!str x\n",
		"k:\n- a\n- b\nm: {a, b: }\nn: [a: b]\n",
		"a: &a [*a]\n\tb: 1\n",
		"x: !!int 0x1F\ny: !Custom z\nz: .inf\n",
	} {
		f.Add([]byte(s))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		lim := Limits{MaxDepth: 100, MaxNodes: 10_000}
		docs, err := ParseAllLimits(data, lim)
		if err != nil {
			var e *Error
			if !errors.As(err, &e) {
				t.Fatalf("unexpected error type %T: %v", err, err)
			}
			if e.Line < 0 || e.Column < 0 || (e.Line == 0) != (e.Column == 0) || e.Message == "" {
				t.Fatalf("bad error %+v", e)
			}
			_ = AsErr(err).Error()
			return
		}
		for _, d := range docs {
			js, err := d.JSONLimits(lim)
			if err != nil {
				var e *Error
				if !errors.As(err, &e) || e.Line < 1 || e.Column < 1 {
					t.Fatalf("bad JSON error %T %v", err, err)
				}
				continue
			}
			if !json.Valid(js) {
				t.Fatalf("invalid JSON %q", js)
			}
		}
	})
}
