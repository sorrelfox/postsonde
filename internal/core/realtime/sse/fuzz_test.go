package sse

import (
	"math/rand/v2"
	"strings"
	"testing"
	"unicode/utf8"
)

func newRand(seed uint64) *rand.Rand { return rand.New(rand.NewPCG(seed, 0x55e)) }

// FuzzParse checks that parsing a stream whole and in random chunks gives
// the same result, and that events are well formed.
func FuzzParse(f *testing.F) {
	for _, tc := range specCases {
		f.Add([]byte(tc.in), uint64(1), false)
	}
	for _, tc := range edgeCases {
		f.Add([]byte(tc.in), uint64(2), true)
	}
	f.Add([]byte("data: a\r"), uint64(3), false)
	f.Add([]byte("\xEF\xBB\xBF\xEF\xBB\xBFdata: \xE2\x82\n\r\n"), uint64(4), true)
	f.Fuzz(func(t *testing.T, data []byte, seed uint64, small bool) {
		opts := Options{}
		if small {
			opts = Options{MaxLineSize: 16, MaxEventSize: 32}
		}
		s := string(data)
		whole := runWhole(opts, s)
		rng := newRand(seed)
		for range 4 {
			if d := sameResult(whole, run(opts, randomChunks(rng, s))); d != "" {
				t.Fatalf("chunking changed the result for %q\n%s", s, d)
			}
		}
		for _, e := range whole.Events {
			if !utf8.ValidString(e.Data) || !utf8.ValidString(e.Type) || !utf8.ValidString(e.ID) {
				t.Fatalf("invalid UTF-8 in %+v", e)
			}
			if strings.ContainsAny(e.Data, "\r") || strings.ContainsAny(e.Type+e.ID, "\r\n") || strings.ContainsRune(e.ID, 0) {
				t.Fatalf("line break or NULL in %+v", e)
			}
			if e.Type == "" || e.End.Line < e.Start.Line || e.End.Offset < e.Start.Offset {
				t.Fatalf("bad event %+v", e)
			}
		}
	})
}
