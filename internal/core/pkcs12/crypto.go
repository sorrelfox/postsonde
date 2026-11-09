package pkcs12

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/des"
	"crypto/hmac"
	"crypto/pbkdf2"
	"crypto/rc4"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/sha512"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"hash"
	"math/big"
	"unicode/utf16"
)

// MaxIterations bounds the iteration counts of key derivations, so a
// crafted file cannot keep the app busy for minutes.
const MaxIterations = 10_000_000

// Object identifiers.
var (
	oidData          = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 1}
	oidSignedData    = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 2}
	oidEnvelopedData = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 3}
	oidEncryptedData = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 7, 6}

	oidKeyBag          = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 12, 10, 1, 1}
	oidShroudedKeyBag  = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 12, 10, 1, 2}
	oidCertBag         = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 12, 10, 1, 3}
	oidSafeContentsBag = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 12, 10, 1, 6}
	oidX509Certificate = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 22, 1}
	oidFriendlyName    = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 20}
	oidLocalKeyID      = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 9, 21}

	oidPBEWithSHAAnd3KeyTripleDESCBC = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 12, 1, 3}
	oidPBES2                         = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 5, 13}
	oidPBKDF2                        = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 5, 12}
	oidPBMAC1                        = asn1.ObjectIdentifier{1, 2, 840, 113549, 1, 5, 14}
)

// algorithmNames names the algorithms messages and Info mention; the
// unsupported ones are listed so errors can name them.
var algorithmNames = map[string]string{
	"1.2.840.113549.1.12.1.1": "PBE-SHA1-RC4-128",
	"1.2.840.113549.1.12.1.2": "PBE-SHA1-RC4-40",
	"1.2.840.113549.1.12.1.3": "PBE-SHA1-3DES",
	"1.2.840.113549.1.12.1.4": "PBE-SHA1-2DES",
	"1.2.840.113549.1.12.1.5": "PBE-SHA1-RC2-128",
	"1.2.840.113549.1.12.1.6": "PBE-SHA1-RC2-40",
	"1.2.840.113549.1.5.1":    "PBE-MD2-DES",
	"1.2.840.113549.1.5.3":    "PBE-MD5-DES",
	"1.2.840.113549.1.5.4":    "PBE-MD2-RC2-64",
	"1.2.840.113549.1.5.6":    "PBE-MD5-RC2-64",
	"1.2.840.113549.1.5.10":   "PBE-SHA1-DES",
	"1.2.840.113549.1.5.11":   "PBE-SHA1-RC2-64",
	"1.2.840.113549.1.5.13":   "PBES2",
	"1.2.840.113549.1.5.14":   "PBMAC1",
	"1.2.840.113549.3.2":      "RC2-CBC",
	"1.2.840.113549.3.7":      "DES-EDE3-CBC",
	"1.3.14.3.2.7":            "DES-CBC",
	"2.16.840.1.101.3.4.1.2":  "AES-128-CBC",
	"2.16.840.1.101.3.4.1.22": "AES-192-CBC",
	"2.16.840.1.101.3.4.1.42": "AES-256-CBC",
	"1.3.14.3.2.26":           "SHA-1",
	"2.16.840.1.101.3.4.2.4":  "SHA-224",
	"2.16.840.1.101.3.4.2.1":  "SHA-256",
	"2.16.840.1.101.3.4.2.2":  "SHA-384",
	"2.16.840.1.101.3.4.2.3":  "SHA-512",
	"1.2.840.113549.2.7":      "HMAC-SHA1",
	"1.2.840.113549.2.8":      "HMAC-SHA224",
	"1.2.840.113549.2.9":      "HMAC-SHA256",
	"1.2.840.113549.2.10":     "HMAC-SHA384",
	"1.2.840.113549.2.11":     "HMAC-SHA512",
}

func algorithmName(oid asn1.ObjectIdentifier) string {
	if n, ok := algorithmNames[oid.String()]; ok {
		return n
	}
	return oid.String()
}

// hashAlg is a hash function with its PKCS#12 KDF block size.
type hashAlg struct {
	name  string
	new   func() hash.Hash
	block int // v in RFC 7292 appendix B
}

var (
	hSHA1   = hashAlg{"SHA-1", sha1.New, 64}
	hSHA224 = hashAlg{"SHA-224", sha256.New224, 64}
	hSHA256 = hashAlg{"SHA-256", sha256.New, 64}
	hSHA384 = hashAlg{"SHA-384", sha512.New384, 128}
	hSHA512 = hashAlg{"SHA-512", sha512.New, 128}
)

// digests maps digest algorithm OIDs (MAC) to hashes.
var digests = map[string]hashAlg{
	"1.3.14.3.2.26":          hSHA1,
	"2.16.840.1.101.3.4.2.4": hSHA224,
	"2.16.840.1.101.3.4.2.1": hSHA256,
	"2.16.840.1.101.3.4.2.2": hSHA384,
	"2.16.840.1.101.3.4.2.3": hSHA512,
}

// hmacs maps HMAC algorithm OIDs (PBKDF2 PRFs, PBMAC1) to hashes.
var hmacs = map[string]hashAlg{
	"1.2.840.113549.2.7":  hSHA1,
	"1.2.840.113549.2.8":  hSHA224,
	"1.2.840.113549.2.9":  hSHA256,
	"1.2.840.113549.2.10": hSHA384,
	"1.2.840.113549.2.11": hSHA512,
}

// errUnsupportedAlg is an algorithm this package does not implement.
type errUnsupportedAlg struct{ name string }

func (e *errUnsupportedAlg) Error() string { return "pkcs12: unsupported algorithm " + e.name }

var (
	errDecrypt    = errors.New("pkcs12: decryption failed")
	errParams     = errors.New("pkcs12: malformed algorithm parameters")
	errIterations = errors.New("pkcs12: too many iterations")
)

// bmpPassword encodes a password as a NUL-terminated big-endian UTF-16
// string, as the PKCS#12 KDF wants. The empty password is two zero bytes.
func bmpPassword(password string) []byte {
	u := utf16.Encode([]rune(password))
	out := make([]byte, 0, 2*len(u)+2)
	for _, c := range u {
		out = append(out, byte(c>>8), byte(c))
	}
	return append(out, 0, 0)
}

// pkcs12KDF is the key derivation of RFC 7292 appendix B.2. id is 1 for
// keys, 2 for IVs and 3 for MAC keys.
func pkcs12KDF(h hashAlg, password, salt []byte, iterations int, id byte, size int) []byte {
	v := h.block
	u := h.new().Size()
	fill := func(src []byte) []byte {
		if len(src) == 0 {
			return nil
		}
		out := make([]byte, v*((len(src)+v-1)/v))
		for i := range out {
			out[i] = src[i%len(src)]
		}
		return out
	}
	D := make([]byte, v)
	for i := range D {
		D[i] = id
	}
	I := append(fill(salt), fill(password)...)
	var out []byte
	one := big.NewInt(1)
	for len(out) < size {
		hh := h.new()
		hh.Write(D)
		hh.Write(I)
		A := hh.Sum(nil)
		for range iterations - 1 {
			hh.Reset()
			hh.Write(A)
			A = hh.Sum(A[:0])
		}
		out = append(out, A...)
		if len(out) >= size {
			break
		}
		// I_j = (I_j + B + 1) mod 2^(8v), with B = A repeated to v bytes.
		B := make([]byte, v)
		for i := range B {
			B[i] = A[i%u]
		}
		bPlus1 := new(big.Int).Add(new(big.Int).SetBytes(B), one)
		for j := 0; j < len(I); j += v {
			sum := new(big.Int).Add(new(big.Int).SetBytes(I[j:j+v]), bPlus1)
			sb := sum.Bytes()
			if len(sb) > v {
				sb = sb[len(sb)-v:]
			}
			block := I[j : j+v]
			clear(block)
			copy(block[v-len(sb):], sb)
		}
	}
	return out[:size]
}

type pbeParams struct {
	Salt       []byte
	Iterations int
}

type pbes2Params struct {
	KDF    pkix.AlgorithmIdentifier
	Scheme pkix.AlgorithmIdentifier
}

type pbkdf2Params struct {
	Salt       asn1.RawValue
	Iterations int
	KeyLength  int                      `asn1:"optional"`
	PRF        pkix.AlgorithmIdentifier `asn1:"optional"`
}

// decrypt decrypts data encrypted with alg under password. It returns
// *errUnsupportedAlg, errParams, errIterations or errDecrypt (bad
// padding: most likely a wrong password).
func decrypt(alg pkix.AlgorithmIdentifier, password string, emptyAsNil bool, data []byte) ([]byte, error) {
	if alg.Algorithm.Equal(oidPBES2) {
		return decryptPBES2(alg, password, data)
	}
	pbe, ok := pkcs12PBEs[alg.Algorithm.String()]
	if !ok {
		return nil, &errUnsupportedAlg{algorithmName(alg.Algorithm)}
	}
	var p pbeParams
	if err := unmarshalParams(alg.Parameters.FullBytes, &p); err != nil {
		return nil, err
	}
	if p.Iterations < 1 || p.Iterations > MaxIterations {
		return nil, errIterations
	}
	pw := bmpPassword(password)
	if emptyAsNil && password == "" {
		pw = nil
	}
	key := pkcs12KDF(hSHA1, pw, p.Salt, p.Iterations, 1, pbe.keyLen)
	if pbe.stream {
		c, err := rc4.NewCipher(key)
		if err != nil {
			return nil, errParams
		}
		out := make([]byte, len(data))
		c.XORKeyStream(out, data)
		return out, nil
	}
	iv := pkcs12KDF(hSHA1, pw, p.Salt, p.Iterations, 2, 8)
	block, err := pbe.newBlock(key)
	if err != nil {
		return nil, errParams
	}
	return cbcDecrypt(block, iv, data)
}

// pkcs12PBE is one of the PKCS#12 password-based encryption schemes of
// RFC 7292 appendix C (all with SHA-1).
type pkcs12PBE struct {
	keyLen   int
	stream   bool // RC4: no IV, no padding
	newBlock func(key []byte) (cipher.Block, error)
}

var pkcs12PBEs = map[string]pkcs12PBE{
	"1.2.840.113549.1.12.1.1": {keyLen: 16, stream: true},
	"1.2.840.113549.1.12.1.2": {keyLen: 5, stream: true},
	"1.2.840.113549.1.12.1.3": {keyLen: 24, newBlock: des.NewTripleDESCipher},
	"1.2.840.113549.1.12.1.4": {keyLen: 16, newBlock: func(k []byte) (cipher.Block, error) {
		return des.NewTripleDESCipher(append(k[:16:16], k[:8]...)) // K1 K2 K1
	}},
	"1.2.840.113549.1.12.1.5": {keyLen: 16, newBlock: func(k []byte) (cipher.Block, error) { return newRC2(k, 128) }},
	"1.2.840.113549.1.12.1.6": {keyLen: 5, newBlock: func(k []byte) (cipher.Block, error) { return newRC2(k, 40) }},
}

// rc2CBCParams are the RC2-CBC parameters of RFC 8018 appendix B.2.3.
type rc2CBCParams struct {
	Version int `asn1:"optional,default:-1"`
	IV      []byte
}

func decryptPBES2(alg pkix.AlgorithmIdentifier, password string, data []byte) ([]byte, error) {
	var p pbes2Params
	if err := unmarshalParams(alg.Parameters.FullBytes, &p); err != nil {
		return nil, err
	}
	var newCipher func([]byte) (cipher.Block, error)
	keyLen := 0
	switch algorithmName(p.Scheme.Algorithm) {
	case "AES-128-CBC":
		newCipher, keyLen = aes.NewCipher, 16
	case "AES-192-CBC":
		newCipher, keyLen = aes.NewCipher, 24
	case "AES-256-CBC":
		newCipher, keyLen = aes.NewCipher, 32
	case "DES-EDE3-CBC":
		newCipher, keyLen = des.NewTripleDESCipher, 24
	case "DES-CBC":
		newCipher, keyLen = des.NewCipher, 8
	case "RC2-CBC":
		return decryptPBES2RC2(p, password, data)
	default:
		return nil, &errUnsupportedAlg{"PBES2 with " + algorithmName(p.Scheme.Algorithm)}
	}
	var iv []byte
	if err := unmarshalParams(p.Scheme.Parameters.FullBytes, &iv); err != nil {
		return nil, err
	}
	key, err := pbkdf2Key(p.KDF, password, keyLen)
	if err != nil {
		return nil, err
	}
	block, err := newCipher(key)
	if err != nil || len(iv) != block.BlockSize() {
		return nil, errParams
	}
	return cbcDecrypt(block, iv, data)
}

// pbkdf2Key derives a key with the PBKDF2 algorithm identifier kdf. The
// password is used as UTF-8 bytes. keyLen 0 takes the length from the
// parameters.
func pbkdf2Key(kdf pkix.AlgorithmIdentifier, password string, keyLen int) ([]byte, error) {
	if !kdf.Algorithm.Equal(oidPBKDF2) {
		return nil, &errUnsupportedAlg{"key derivation " + algorithmName(kdf.Algorithm)}
	}
	var p pbkdf2Params
	if err := unmarshalParams(kdf.Parameters.FullBytes, &p); err != nil {
		return nil, err
	}
	if p.Salt.Class != asn1.ClassUniversal || p.Salt.Tag != asn1.TagOctetString {
		return nil, &errUnsupportedAlg{"PBKDF2 with a salt source"}
	}
	if p.Iterations < 1 || p.Iterations > MaxIterations {
		return nil, errIterations
	}
	h := hSHA1
	if len(p.PRF.Algorithm) > 0 {
		var ok bool
		if h, ok = hmacs[p.PRF.Algorithm.String()]; !ok {
			return nil, &errUnsupportedAlg{"PBKDF2 with " + algorithmName(p.PRF.Algorithm)}
		}
	}
	if keyLen == 0 {
		keyLen = p.KeyLength
	}
	if keyLen <= 0 || keyLen > 1024 || p.KeyLength != 0 && p.KeyLength != keyLen {
		return nil, errParams
	}
	key, err := pbkdf2.Key(h.new, password, p.Salt.Bytes, p.Iterations, keyLen)
	if err != nil {
		return nil, errParams
	}
	return key, nil
}

// decryptPBES2RC2 decrypts PBES2 with RC2-CBC. The key length comes from
// the PBKDF2 parameters (16 bytes when absent), the effective key bits
// from the RC2 parameter version (32 when absent, as RFC 8018 says).
func decryptPBES2RC2(p pbes2Params, password string, data []byte) ([]byte, error) {
	var rp rc2CBCParams
	if err := unmarshalParams(p.Scheme.Parameters.FullBytes, &rp); err != nil {
		return nil, err
	}
	effective := 32
	if rp.Version >= 0 {
		var ok bool
		if effective, ok = rc2ParamBits(rp.Version); !ok {
			return nil, errParams
		}
	}
	var kp pbkdf2Params
	if err := unmarshalParams(p.KDF.Parameters.FullBytes, &kp); err != nil {
		return nil, err
	}
	keyLen := kp.KeyLength
	if keyLen == 0 {
		keyLen = 16
	}
	if keyLen > 128 {
		return nil, errParams
	}
	key, err := pbkdf2Key(p.KDF, password, keyLen)
	if err != nil {
		return nil, err
	}
	block, err := newRC2(key, effective)
	if err != nil {
		return nil, errParams
	}
	return cbcDecrypt(block, rp.IV, data)
}

func cbcDecrypt(block cipher.Block, iv, data []byte) ([]byte, error) {
	bs := block.BlockSize()
	if len(iv) != bs || len(data) == 0 || len(data)%bs != 0 {
		return nil, errDecrypt
	}
	out := make([]byte, len(data))
	cipher.NewCBCDecrypter(block, iv).CryptBlocks(out, data)
	pad := int(out[len(out)-1])
	if pad == 0 || pad > bs {
		return nil, errDecrypt
	}
	for _, b := range out[len(out)-pad:] {
		if int(b) != pad {
			return nil, errDecrypt
		}
	}
	return out[:len(out)-pad], nil
}

// unmarshalParams parses DER algorithm parameters, with no trailing data.
func unmarshalParams(der []byte, v any) error {
	rest, err := asn1.Unmarshal(der, v)
	if err != nil || len(rest) > 0 {
		return errParams
	}
	return nil
}

// macData is the password integrity of a PFX.
type macData struct {
	Mac        digestInfo
	MacSalt    []byte
	Iterations int `asn1:"optional,default:1"`
}

type digestInfo struct {
	Algorithm pkix.AlgorithmIdentifier
	Digest    []byte
}

type pbmac1Params struct {
	KDF    pkix.AlgorithmIdentifier
	Scheme pkix.AlgorithmIdentifier
}

// macName returns a description of the MAC algorithm.
func (m *macData) name() string {
	if m.Mac.Algorithm.Algorithm.Equal(oidPBMAC1) {
		var p pbmac1Params
		if unmarshalParams(m.Mac.Algorithm.Parameters.FullBytes, &p) == nil {
			prf := "HMAC-SHA1"
			var kp pbkdf2Params
			if unmarshalParams(p.KDF.Parameters.FullBytes, &kp) == nil && len(kp.PRF.Algorithm) > 0 {
				prf = algorithmName(kp.PRF.Algorithm)
			}
			return "PBMAC1 " + algorithmName(p.Scheme.Algorithm) + " (PBKDF2 " + prf + ")"
		}
		return "PBMAC1"
	}
	return "HMAC-" + algorithmName(m.Mac.Algorithm.Algorithm)
}

// verify checks the MAC over content. ok is false for a wrong password
// (or damaged content); err is for unsupported or malformed parameters.
func (m *macData) verify(content []byte, password string, emptyAsNil bool) (ok bool, err error) {
	if m.Mac.Algorithm.Algorithm.Equal(oidPBMAC1) {
		var p pbmac1Params
		if err := unmarshalParams(m.Mac.Algorithm.Parameters.FullBytes, &p); err != nil {
			return false, err
		}
		h, known := hmacs[p.Scheme.Algorithm.String()]
		if !known {
			return false, &errUnsupportedAlg{"PBMAC1 with " + algorithmName(p.Scheme.Algorithm)}
		}
		key, err := pbkdf2Key(p.KDF, password, 0)
		if err != nil {
			return false, err
		}
		mac := hmac.New(h.new, key)
		mac.Write(content)
		return hmac.Equal(mac.Sum(nil), m.Mac.Digest), nil
	}
	h, known := digests[m.Mac.Algorithm.Algorithm.String()]
	if !known {
		return false, &errUnsupportedAlg{"MAC with " + algorithmName(m.Mac.Algorithm.Algorithm)}
	}
	if m.Iterations < 1 || m.Iterations > MaxIterations {
		return false, errIterations
	}
	pw := bmpPassword(password)
	if emptyAsNil && password == "" {
		pw = nil
	}
	key := pkcs12KDF(h, pw, m.MacSalt, m.Iterations, 3, h.new().Size())
	mac := hmac.New(h.new, key)
	mac.Write(content)
	return hmac.Equal(mac.Sum(nil), m.Mac.Digest), nil
}
