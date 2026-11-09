package framing

import (
	"bytes"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

func norm(t *testing.T, o Options) Options {
	t.Helper()
	n, err := o.Normalize()
	if err != nil {
		t.Fatalf("Normalize(%+v): %v", o, err)
	}
	return n
}

func codeOf(err error) string {
	if e, ok := errors.AsType[*errs.Error](err); ok {
		return e.Code
	}
	return ""
}

// chunkings splits stream in awkward ways: every byte alone, fixed sizes
// that straddle prefixes and delimiters, everything at once, and seeded
// random cuts.
func chunkings(stream []byte) map[string][][]byte {
	out := map[string][][]byte{}
	fixed := func(size int) [][]byte {
		var cs [][]byte
		for i := 0; i < len(stream); i += size {
			cs = append(cs, stream[i:min(i+size, len(stream))])
		}
		return cs
	}
	for _, size := range []int{1, 2, 3, 5, 7, 13, len(stream)} {
		if size > 0 {
			out[fmt.Sprintf("size%d", size)] = fixed(size)
		}
	}
	for seed := range uint64(8) {
		r := rand.New(rand.NewPCG(seed, 219))
		var cs [][]byte
		for i := 0; i < len(stream); {
			n := 1 + r.IntN(9)
			cs = append(cs, stream[i:min(i+n, len(stream))])
			i += n
		}
		out[fmt.Sprintf("random%d", seed)] = cs
	}
	return out
}

func feedAll(t *testing.T, s *Splitter, chunks [][]byte) [][]byte {
	t.Helper()
	var msgs [][]byte
	for _, c := range chunks {
		got, err := s.Feed(c)
		if err != nil {
			t.Fatalf("Feed(%q): %v", c, err)
		}
		msgs = append(msgs, got...)
	}
	return msgs
}

func sameMessages(t *testing.T, got, want [][]byte) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %d messages %q, want %d %q", len(got), got, len(want), want)
	}
	for i := range want {
		if !bytes.Equal(got[i], want[i]) {
			t.Fatalf("message %d = %q, want %q (all: %q)", i, got[i], want[i], got)
		}
	}
}

func TestNewlineSplitsInAnyChunking(t *testing.T) {
	stream := []byte("HELLO\r\nworld\n\nlast line\r\n\r\nbare\rcr\nunfinished")
	want := [][]byte{[]byte("HELLO"), []byte("world"), {}, []byte("last line"), {}, []byte("bare\rcr")}
	o := norm(t, Options{Mode: ModeNewline})
	for name, chunks := range chunkings(stream) {
		t.Run(name, func(t *testing.T) {
			s := NewSplitter(o)
			sameMessages(t, feedAll(t, s, chunks), want)
			if rest := s.Rest(); string(rest) != "unfinished" {
				t.Fatalf("Rest = %q", rest)
			}
		})
	}
}

func TestLengthSplitsInAnyChunking(t *testing.T) {
	msgs := [][]byte{[]byte("hi"), {}, bytes.Repeat([]byte{0xAB}, 300), []byte("\n\x00\xff"), []byte("end")}
	for _, size := range []int{1, 2, 4, 8} {
		for _, order := range []string{BigEndian, LittleEndian} {
			o := norm(t, Options{Mode: ModeLength, PrefixSize: size, ByteOrder: order})
			var stream []byte
			var want [][]byte
			for _, m := range msgs {
				if size == 1 && len(m) > 255 {
					continue
				}
				framed, err := Encode(o, m)
				if err != nil {
					t.Fatal(err)
				}
				stream = append(stream, framed...)
				want = append(want, m)
			}
			// An unfinished message: its prefix and part of the body.
			partial, _ := Encode(o, []byte("partial"))
			partial = partial[:len(partial)-3]
			stream = append(stream, partial...)
			for name, chunks := range chunkings(stream) {
				t.Run(fmt.Sprintf("%d%s/%s", size, order, name), func(t *testing.T) {
					s := NewSplitter(o)
					sameMessages(t, feedAll(t, s, chunks), want)
					if rest := s.Rest(); !bytes.Equal(rest, partial) {
						t.Fatalf("Rest = %x, want %x", rest, partial)
					}
				})
			}
		}
	}
}

func TestLengthPrefixEncoding(t *testing.T) {
	cases := []struct {
		size  int
		order string
		want  string
	}{
		{1, BigEndian, "03"},
		{2, BigEndian, "0003"},
		{2, LittleEndian, "0300"},
		{4, BigEndian, "00000003"},
		{4, LittleEndian, "03000000"},
		{8, BigEndian, "0000000000000003"},
		{8, LittleEndian, "0300000000000000"},
	}
	for _, c := range cases {
		o := norm(t, Options{Mode: ModeLength, PrefixSize: c.size, ByteOrder: c.order})
		got, err := Encode(o, []byte("abc"))
		if err != nil {
			t.Fatal(err)
		}
		if want := c.want + "616263"; fmt.Sprintf("%x", got) != want {
			t.Errorf("%d %s: %x, want %s", c.size, c.order, got, want)
		}
	}
}

func TestEncode(t *testing.T) {
	if got, _ := Encode(norm(t, Options{}), []byte("raw")); string(got) != "raw" {
		t.Errorf("none: %q", got)
	}
	if got, _ := Encode(norm(t, Options{Mode: ModeNewline}), []byte("a")); string(got) != "a\n" {
		t.Errorf("lf: %q", got)
	}
	if got, _ := Encode(norm(t, Options{Mode: ModeNewline, Delimiter: DelimiterCRLF}), []byte("a")); string(got) != "a\r\n" {
		t.Errorf("crlf: %q", got)
	}
	_, err := Encode(norm(t, Options{Mode: ModeLength, PrefixSize: 1}), make([]byte, 256))
	if codeOf(err) != CodeMessageTooLarge || errs.KindOf(err) != errs.Protocol {
		t.Errorf("256 bytes with a 1-byte prefix: %v", err)
	}
	if _, err := Encode(norm(t, Options{Mode: ModeLength, PrefixSize: 1}), make([]byte, 255)); err != nil {
		t.Errorf("255 bytes with a 1-byte prefix: %v", err)
	}
	if _, err := Encode(norm(t, Options{Mode: ModeLength, PrefixSize: 2}), make([]byte, 65536)); codeOf(err) != CodeMessageTooLarge {
		t.Errorf("65536 bytes with a 2-byte prefix: %v", err)
	}
}

func TestNoneReturnsChunks(t *testing.T) {
	s := NewSplitter(norm(t, Options{}))
	chunk := []byte("a\nb")
	got, err := s.Feed(chunk)
	if err != nil {
		t.Fatal(err)
	}
	chunk[0] = 'x'
	sameMessages(t, got, [][]byte{[]byte("a\nb")})
	if s.Rest() != nil {
		t.Error("none keeps no rest")
	}
}

func TestMessagesAreCopies(t *testing.T) {
	s := NewSplitter(norm(t, Options{Mode: ModeNewline}))
	first, _ := s.Feed([]byte("one\ntw"))
	second, _ := s.Feed([]byte("o\nthree\n"))
	sameMessages(t, append(first, second...), [][]byte{[]byte("one"), []byte("two"), []byte("three")})
}

func TestOversizeLength(t *testing.T) {
	o := norm(t, Options{Mode: ModeLength, PrefixSize: 2, MaxMessageSize: 4})
	s := NewSplitter(o)
	// A 4-byte message is fine; the 5-byte one after it fails as soon as
	// its prefix is known, without waiting for the body.
	msgs, err := s.Feed([]byte{0, 4, 'a', 'b', 'c', 'd', 0})
	if err != nil || len(msgs) != 1 {
		t.Fatalf("msgs %q, err %v", msgs, err)
	}
	msgs, err = s.Feed([]byte{5})
	if codeOf(err) != CodeMessageTooLarge || len(msgs) != 0 {
		t.Fatalf("msgs %q, err %v", msgs, err)
	}
	e, _ := errors.AsType[*errs.Error](err)
	if e.Details["size"] != "5" || e.Details["maxSize"] != "4" || !strings.Contains(e.Message, "5 bytes") {
		t.Errorf("error %+v", e)
	}
	// The stream cannot be followed any more.
	if _, err := s.Feed([]byte{0, 1, 'x'}); codeOf(err) != CodeMessageTooLarge {
		t.Errorf("after failure: %v", err)
	}
	if s.Rest() != nil {
		t.Error("Rest after failure")
	}
}

func TestOversizeLengthEightBytes(t *testing.T) {
	s := NewSplitter(norm(t, Options{Mode: ModeLength, PrefixSize: 8}))
	_, err := s.Feed(bytes.Repeat([]byte{0xff}, 8))
	if codeOf(err) != CodeMessageTooLarge {
		t.Fatalf("err %v", err)
	}
}

func TestOversizeNewline(t *testing.T) {
	o := norm(t, Options{Mode: ModeNewline, MaxMessageSize: 4})
	// Exactly the maximum, with CRLF split across chunks.
	s := NewSplitter(o)
	msgs := feedAll(t, s, [][]byte{[]byte("abcd\r"), []byte("\nwxyz"), []byte("\n")})
	sameMessages(t, msgs, [][]byte{[]byte("abcd"), []byte("wxyz")})

	// A line that grows past the maximum fails before its end arrives.
	s = NewSplitter(o)
	if _, err := s.Feed([]byte("ab")); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Feed([]byte("cde")); codeOf(err) != CodeMessageTooLarge {
		t.Fatalf("err %v", err)
	}

	// A long line inside one chunk: the lines before it are returned.
	s = NewSplitter(o)
	msgs, err := s.Feed([]byte("ok\ntoo long\nnext\n"))
	if codeOf(err) != CodeMessageTooLarge {
		t.Fatalf("err %v", err)
	}
	sameMessages(t, msgs, [][]byte{[]byte("ok")})
}

func TestNormalize(t *testing.T) {
	o := norm(t, Options{Mode: ModeLength})
	if o.PrefixSize != 4 || o.ByteOrder != BigEndian || o.Delimiter != DelimiterLF || o.MaxMessageSize != DefaultMaxMessageSize {
		t.Errorf("defaults %+v", o)
	}
	if norm(t, Options{}).Framed() || !norm(t, Options{Mode: ModeNewline}).Framed() {
		t.Error("Framed")
	}
	bad := map[string]Options{
		"mode":           {Mode: "json"},
		"delimiter":      {Mode: ModeNewline, Delimiter: "cr"},
		"prefixSize":     {Mode: ModeLength, PrefixSize: 3},
		"byteOrder":      {Mode: ModeLength, ByteOrder: "middle"},
		"maxMessageSize": {Mode: ModeLength, MaxMessageSize: LimitMaxMessageSize + 1},
	}
	for field, o := range bad {
		_, err := o.Normalize()
		e, ok := errors.AsType[*errs.Error](err)
		if !ok || e.Code != CodeInvalidOptions || e.Kind != errs.InvalidArgument || e.Details["field"] != field {
			t.Errorf("%s: %v", field, err)
		}
	}
	if _, err := (Options{MaxMessageSize: -1}).Normalize(); codeOf(err) != CodeInvalidOptions {
		t.Errorf("negative max: %v", err)
	}
}

// FuzzSplitter checks that any chunking of any stream gives the messages
// splitting it whole gives.
func FuzzSplitter(f *testing.F) {
	f.Add([]byte("a\r\nb\nc"), uint8(3), uint8(0))
	f.Add([]byte{0, 0, 0, 2, 'h', 'i', 0, 0}, uint8(1), uint8(1))
	f.Fuzz(func(t *testing.T, stream []byte, cut uint8, mode uint8) {
		o := Options{Mode: ModeNewline, MaxMessageSize: 64}
		if mode%2 == 1 {
			o = Options{Mode: ModeLength, PrefixSize: 1 << (mode / 2 % 4), MaxMessageSize: 64}
		}
		o, _ = o.Normalize()
		whole := NewSplitter(o)
		want, wantErr := whole.Feed(stream)
		size := 1 + int(cut)%7
		s := NewSplitter(o)
		var got [][]byte
		var err error
		for i := 0; i < len(stream) && err == nil; i += size {
			var m [][]byte
			m, err = s.Feed(stream[i:min(i+size, len(stream))])
			got = append(got, m...)
		}
		if (err == nil) != (wantErr == nil) {
			t.Fatalf("chunked err %v, whole err %v", err, wantErr)
		}
		if len(got) != len(want) {
			t.Fatalf("chunked %q, whole %q", got, want)
		}
		for i := range want {
			if !bytes.Equal(got[i], want[i]) {
				t.Fatalf("chunked %q, whole %q", got, want)
			}
		}
		if err == nil && !bytes.Equal(s.Rest(), whole.Rest()) {
			t.Fatalf("rest %q, whole %q", s.Rest(), whole.Rest())
		}
	})
}
