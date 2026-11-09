package pkcs12

import "errors"

// maxBERDepth bounds the nesting berToDER accepts.
const maxBERDepth = 64

var errBER = errors.New("pkcs12: malformed ASN.1")

// berToDER re-encodes BER as the DER subset encoding/asn1 reads:
// indefinite lengths become definite, lengths become minimal and
// constructed OCTET STRINGs are joined into one primitive OCTET STRING.
// The contents of primitive values are copied as they are.
func berToDER(in []byte) ([]byte, error) {
	var out []byte
	for len(in) > 0 {
		v, n, err := convertBER(in, 0)
		if err != nil {
			return nil, err
		}
		out = append(out, v...)
		in = in[n:]
	}
	return out, nil
}

// convertBER converts the element at the start of in and returns it and
// the number of bytes it used.
func convertBER(in []byte, depth int) ([]byte, int, error) {
	if depth > maxBERDepth || len(in) < 2 {
		return nil, 0, errBER
	}
	// Identifier octets.
	p := 1
	if in[0]&0x1f == 0x1f {
		for {
			if p >= len(in) || p > 5 {
				return nil, 0, errBER
			}
			b := in[p]
			p++
			if b&0x80 == 0 {
				break
			}
		}
	}
	tag := in[:p]
	constructed := in[0]&0x20 != 0
	if p >= len(in) {
		return nil, 0, errBER
	}
	// Length octets.
	l := int(in[p])
	p++
	indefinite := false
	switch {
	case l == 0x80:
		if !constructed {
			return nil, 0, errBER
		}
		indefinite = true
	case l > 0x80:
		n := l & 0x7f
		if n > 4 || p+n > len(in) {
			return nil, 0, errBER
		}
		l = 0
		for _, b := range in[p : p+n] {
			l = l<<8 | int(b)
		}
		p += n
	}
	var content []byte
	end := 0
	switch {
	case indefinite:
		for {
			if p+2 > len(in) {
				return nil, 0, errBER
			}
			if in[p] == 0 && in[p+1] == 0 {
				end = p + 2
				break
			}
			v, n, err := convertBER(in[p:], depth+1)
			if err != nil {
				return nil, 0, err
			}
			content = append(content, v...)
			p += n
		}
	case l < 0 || l > len(in)-p:
		return nil, 0, errBER
	case constructed:
		body := in[p : p+l]
		for len(body) > 0 {
			v, n, err := convertBER(body, depth+1)
			if err != nil {
				return nil, 0, err
			}
			content = append(content, v...)
			body = body[n:]
		}
		end = p + l
	default:
		content = in[p : p+l]
		end = p + l
	}
	if len(tag) == 1 && tag[0] == 0x24 { // constructed OCTET STRING
		var joined []byte
		for rest := content; len(rest) > 0; {
			c, n, err := derContent(rest)
			if err != nil || rest[0] != 0x04 {
				return nil, 0, errBER
			}
			joined = append(joined, c...)
			rest = rest[n:]
		}
		tag, content = []byte{0x04}, joined
	}
	out := append([]byte(nil), tag...)
	out = appendLength(out, len(content))
	return append(out, content...), end, nil
}

// derContent returns the content of the single-byte-tag DER element at
// the start of in and the element's size.
func derContent(in []byte) ([]byte, int, error) {
	if len(in) < 2 {
		return nil, 0, errBER
	}
	l, p := int(in[1]), 2
	if l > 0x80 {
		n := l & 0x7f
		if n > 4 || p+n > len(in) {
			return nil, 0, errBER
		}
		l = 0
		for _, b := range in[p : p+n] {
			l = l<<8 | int(b)
		}
		p += n
	} else if l == 0x80 {
		return nil, 0, errBER
	}
	if l > len(in)-p {
		return nil, 0, errBER
	}
	return in[p : p+l], p + l, nil
}

func appendLength(b []byte, n int) []byte {
	if n < 0x80 {
		return append(b, byte(n))
	}
	var tmp [4]byte
	i := 4
	for n > 0 {
		i--
		tmp[i] = byte(n)
		n >>= 8
	}
	b = append(b, 0x80|byte(4-i))
	return append(b, tmp[i:]...)
}
