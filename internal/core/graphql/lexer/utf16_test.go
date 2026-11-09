package lexer

import (
	"testing"
	"unicode/utf16"
	"unicode/utf8"
)

// utf16Len is the reference: the UTF-16 length JavaScript would see.
func utf16Len(s string) int { return len(utf16.Encode([]rune(s))) }

func TestUTF16Index(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		src  string
	}{
		{"empty", ""},
		{"ASCII", "query { a }"},
		{"two-byte", "a\U000000e9b\U000000e9"},
		{"three-byte", "x\U0000FEFFy"},
		{"astral", "\U0001F600a\U0001F600\U0001F600b"},
		{"invalid UTF-8", "a\xffb\xe2\x82c"},
		{"mixed", "# \U000000e9\n{ f(s: \"\U0001F600\") }"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			x := NewUTF16Index(tt.src)
			if got, want := x.Len16(), utf16Len(tt.src); got != want {
				t.Errorf("Len16() = %d, want %d", got, want)
			}
			// Every character boundary maps both ways.
			for off := 0; off <= len(tt.src); {
				want := utf16Len(tt.src[:off])
				if got := x.ToUTF16(off); got != want {
					t.Errorf("ToUTF16(%d) = %d, want %d", off, got, want)
				}
				if got := x.ToByte(want); got != off {
					t.Errorf("ToByte(%d) = %d, want %d", want, got, off)
				}
				if off == len(tt.src) {
					break
				}
				_, size := utf8.DecodeRuneInString(tt.src[off:])
				off += size
			}
		})
	}
}

func TestUTF16IndexInside(t *testing.T) {
	t.Parallel()

	src := "a\U0001F600b" // bytes: a=0, emoji 1..4, b=5; UTF-16: a=0, emoji 1..2, b=3
	x := NewUTF16Index(src)
	tests := []struct {
		name     string
		conv     func(int) int
		in, want int
	}{
		{"byte inside emoji", x.ToUTF16, 3, 1},
		{"byte after emoji", x.ToUTF16, 5, 3},
		{"byte past end", x.ToUTF16, 99, 4},
		{"negative byte", x.ToUTF16, -1, 0},
		{"unit inside surrogate pair", x.ToByte, 2, 1},
		{"unit after emoji", x.ToByte, 3, 5},
		{"unit past end", x.ToByte, 99, 6},
		{"negative unit", x.ToByte, -5, 0},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if got := tt.conv(tt.in); got != tt.want {
				t.Errorf("conv(%d) = %d, want %d", tt.in, got, tt.want)
			}
		})
	}
}
