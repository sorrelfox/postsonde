package wsframe

import "unicode/utf8"

// utf8Validator checks UTF-8 across fragment boundaries. It keeps the bytes
// of an incomplete sequence at the end of the last chunk and fails as soon
// as a chunk contains bytes that cannot start or continue a valid sequence,
// without waiting for the rest of the message.
type utf8Validator struct {
	carry [utf8.UTFMax]byte
	n     int
}

func (v *utf8Validator) reset() { v.n = 0 }

// write validates the next chunk and reports whether the text so far is a
// valid UTF-8 string or a valid prefix of one.
func (v *utf8Validator) write(p []byte) bool {
	if v.n > 0 {
		// Complete the carried sequence with bytes from p.
		var tmp [utf8.UTFMax]byte
		copy(tmp[:], v.carry[:v.n])
		k := copy(tmp[v.n:], p)
		seq := tmp[:v.n+k]
		if !utf8.FullRune(seq) {
			// FullRune is false only for a valid but incomplete prefix.
			v.carry = tmp
			v.n = len(seq)
			return true
		}
		r, size := utf8.DecodeRune(seq)
		if r == utf8.RuneError && size == 1 {
			return false
		}
		p = p[size-v.n:]
		v.n = 0
	}
	if utf8.Valid(p) {
		return true
	}
	for i := 0; i < len(p); {
		if p[i] < utf8.RuneSelf {
			i++
			continue
		}
		if !utf8.FullRune(p[i:]) {
			v.n = copy(v.carry[:], p[i:])
			return true
		}
		r, size := utf8.DecodeRune(p[i:])
		if r == utf8.RuneError && size == 1 {
			return false
		}
		i += size
	}
	return true
}

// complete reports whether the text ended on a sequence boundary.
func (v *utf8Validator) complete() bool { return v.n == 0 }
