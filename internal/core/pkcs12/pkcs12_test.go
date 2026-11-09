package pkcs12

import (
	"bytes"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

const testPassword = "correct horse"

func readFile(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func readCert(t *testing.T, name string) *x509.Certificate {
	t.Helper()
	blk, _ := pem.Decode(readFile(t, name))
	c, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func readKey(t *testing.T, name string) crypto.PrivateKey {
	t.Helper()
	blk, _ := pem.Decode(readFile(t, name))
	k, err := x509.ParsePKCS8PrivateKey(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	return k
}

func keysEqual(a, b crypto.PrivateKey) bool {
	e, ok := a.(interface{ Equal(crypto.PrivateKey) bool })
	return ok && e.Equal(b)
}

func errCode(err error) (errs.Kind, string) {
	var e *errs.Error
	if !errors.As(err, &e) {
		return "", ""
	}
	return e.Kind, e.Code
}

func TestDecodeFixtures(t *testing.T) {
	const (
		pbes2AES256 = "PBES2 AES-256-CBC (PBKDF2 HMAC-SHA256)"
		pbe3DES     = "PBE-SHA1-3DES"
	)
	tests := []struct {
		file, password, key string
		info                Info
		friendly            bool
	}{
		{"windows-aes256-sha256.p12", testPassword, "rsa", Info{"HMAC-SHA-256", pbes2AES256, pbes2AES256}, true},
		{"windows-3des-sha1.p12", testPassword, "rsa", Info{"HMAC-SHA-1", pbe3DES, pbe3DES}, true},
		{"openssl-default.p12", testPassword, "rsa", Info{"HMAC-SHA-256", pbes2AES256, pbes2AES256}, true},
		{"ec-aes128.p12", testPassword, "ec", Info{"HMAC-SHA-512", "PBES2 AES-128-CBC (PBKDF2 HMAC-SHA256)", "PBES2 AES-192-CBC (PBKDF2 HMAC-SHA256)"}, true},
		{"ed25519-pbes2-3des.p12", testPassword, "ed25519", Info{"HMAC-SHA-384", "PBES2 DES-EDE3-CBC (PBKDF2 HMAC-SHA256)", "PBES2 DES-EDE3-CBC (PBKDF2 HMAC-SHA256)"}, true},
		{"rsa-2des.p12", testPassword, "rsa", Info{"HMAC-SHA-1", "PBE-SHA1-2DES", ""}, true},
		{"pbmac1.p12", testPassword, "ec", Info{"PBMAC1 HMAC-SHA256 (PBKDF2 HMAC-SHA512)", pbes2AES256, pbes2AES256}, true},
		{"unencrypted-nomac.p12", "", "ec", Info{}, true},
		{"unencrypted-nomac.p12", "any password", "ec", Info{}, true},
		{"nomac-3des.p12", testPassword, "rsa", Info{"", pbe3DES, ""}, true},
		{"empty-password.p12", "", "rsa", Info{"HMAC-SHA-256", pbes2AES256, pbes2AES256}, true},
		{"unicode-password.p12", "pässwörd €", "ec", Info{"HMAC-SHA-256", pbes2AES256, pbes2AES256}, true},
		{"macos-keychain.p12", testPassword, "rsa", Info{"HMAC-SHA-1", pbe3DES, "PBE-SHA1-RC2-40"}, true},
		{"openssl-legacy.p12", testPassword, "rsa", Info{"HMAC-SHA-1", pbe3DES, "PBE-SHA1-RC2-40"}, true},
		{"rsa-rc4.p12", testPassword, "rsa", Info{"HMAC-SHA-1", "PBE-SHA1-RC4-128", ""}, true},
		{"rc2-128-rc4-40.p12", testPassword, "ec", Info{"HMAC-SHA-1", "PBE-SHA1-RC4-40", "PBE-SHA1-RC2-128"}, true},
	}
	inter, root := readCert(t, "inter.crt"), readCert(t, "root.crt")
	for _, tt := range tests {
		t.Run(tt.file, func(t *testing.T) {
			b, err := Decode(readFile(t, tt.file), tt.password)
			if err != nil {
				t.Fatalf("Decode: %v", err)
			}
			if !keysEqual(b.PrivateKey, readKey(t, tt.key+".key")) {
				t.Errorf("key %T does not match %s.key", b.PrivateKey, tt.key)
			}
			if !b.Leaf.Equal(readCert(t, tt.key+".crt")) {
				t.Errorf("leaf is %s", b.Leaf.Subject)
			}
			if len(b.Chain) != 2 || !b.Chain[0].Equal(inter) || !b.Chain[1].Equal(root) {
				t.Errorf("chain %v", b.Chain)
			}
			if b.Info != tt.info {
				t.Errorf("info %+v, want %+v", b.Info, tt.info)
			}
			if tt.friendly && b.FriendlyName != "PostSonde Test Client" {
				t.Errorf("friendly name %q", b.FriendlyName)
			}
			tc := b.TLSCertificate()
			if len(tc.Certificate) != 3 || !bytes.Equal(tc.Certificate[0], b.Leaf.Raw) || tc.Leaf != b.Leaf || !keysEqual(tc.PrivateKey, b.PrivateKey) {
				t.Errorf("TLSCertificate %+v", tc)
			}
		})
	}
}

func TestDecodeErrors(t *testing.T) {
	tests := []struct {
		name, file, password string
		kind                 errs.Kind
		code, contains       string
	}{
		{"wrong password pbes2", "windows-aes256-sha256.p12", "wrong", errs.InvalidArgument, CodeWrongPassword, "password"},
		{"wrong password 3des", "windows-3des-sha1.p12", "", errs.InvalidArgument, CodeWrongPassword, "password"},
		{"wrong password pbmac1", "pbmac1.p12", "Correct horse", errs.InvalidArgument, CodeWrongPassword, "password"},
		{"wrong password without MAC", "nomac-3des.p12", "wrong", errs.InvalidArgument, CodeWrongPassword, "password"},
		{"password for empty-password file", "empty-password.p12", "x", errs.InvalidArgument, CodeWrongPassword, "password"},
		{"wrong password rc2", "macos-keychain.p12", "wrong", errs.InvalidArgument, CodeWrongPassword, "password"},
		{"pbes1", "pbes1-md5des.p12", testPassword, errs.Unsupported, CodeUnsupportedAlgorithm, "PBE-MD5-DES"},
		{"certificates only", "certs-only.p12", testPassword, errs.InvalidArgument, CodeNoPrivateKey, "no private key"},
		{"key only", "key-only.p12", testPassword, errs.InvalidArgument, CodeNoCertificate, "no certificate"},
		{"not pkcs12", "rsa.crt", testPassword, errs.Parse, CodeInvalid, "not a PKCS#12"},
		{"pkcs8 is not pkcs12", "rsa-pbes2-sha1prf.p8", testPassword, errs.Parse, CodeInvalid, "not a PKCS#12"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Decode(readFile(t, tt.file), tt.password)
			kind, code := errCode(err)
			if kind != tt.kind || code != tt.code || !strings.Contains(err.Error(), tt.contains) {
				t.Errorf("got %v %s %q, want %v %s containing %q", kind, code, err, tt.kind, tt.code, tt.contains)
			}
		})
	}

	var e *errs.Error
	_, err := Decode(readFile(t, "pbes1-md5des.p12"), testPassword)
	if !errors.As(err, &e) || e.Details["algorithm"] != "PBE-MD5-DES" || !strings.Contains(e.Hint, "AES256-SHA256") {
		t.Errorf("unsupported algorithm details: %+v", e)
	}
	if _, err := Decode(make([]byte, MaxSize+1), ""); !hasCode(err, CodeTooLarge) {
		t.Errorf("too large: %v", err)
	}
	if _, err := Decode(nil, ""); !hasCode(err, CodeInvalid) {
		t.Errorf("empty: %v", err)
	}
}

func hasCode(err error, code string) bool {
	_, c := errCode(err)
	return c == code
}

// TestCorrupted flips every byte of a file in turn: Decode must fail
// with a coded error (a MAC or padding failure, or malformed ASN.1), or,
// where the byte does not matter, still succeed; it must never panic.
func TestCorrupted(t *testing.T) {
	for _, name := range []string{"windows-3des-sha1.p12", "nomac-3des.p12", "unencrypted-nomac.p12"} {
		data := readFile(t, name)
		for i := range data {
			c := bytes.Clone(data)
			c[i] ^= 0x5a
			_, err := Decode(c, testPassword)
			if err == nil {
				continue
			}
			if _, code := errCode(err); code == "" {
				t.Fatalf("%s byte %d: uncoded error %v", name, i, err)
			}
		}
		for n := range len(data) {
			if _, err := Decode(data[:n], testPassword); !hasCode(err, CodeInvalid) {
				t.Fatalf("%s truncated to %d: %v", name, n, err)
			}
		}
	}
	// A flipped byte inside the MAC-protected contents is a MAC failure.
	data := readFile(t, "windows-aes256-sha256.p12")
	c := bytes.Clone(data)
	c[len(c)/2] ^= 1
	if _, err := Decode(c, testPassword); !hasCode(err, CodeWrongPassword) {
		t.Errorf("damaged contents: %v", err)
	}
}

func TestDecryptPKCS8(t *testing.T) {
	for _, tt := range []struct{ file, key string }{
		{"rsa-pbes2-sha1prf.p8", "rsa"},
		{"ec-pbes2-sha512prf.p8", "ec"},
		{"ec-pkcs12-3des.p8", "ec"},
		{"rsa-pbes2-rc2.p8", "rsa"},
		{"ec-pbes2-rc2-40.p8", "ec"},
	} {
		k, err := DecryptPKCS8(readFile(t, tt.file), testPassword)
		if err != nil {
			t.Errorf("%s: %v", tt.file, err)
			continue
		}
		if !keysEqual(k, readKey(t, tt.key+".key")) {
			t.Errorf("%s: wrong key", tt.file)
		}
		if _, err := DecryptPKCS8(readFile(t, tt.file), "nope"); !hasCode(err, CodeWrongPassword) {
			t.Errorf("%s wrong password: %v", tt.file, err)
		}
	}
	if _, err := DecryptPKCS8(readFile(t, "rsa-pbes1-md5des.p8"), testPassword); !hasCode(err, CodeUnsupportedAlgorithm) || !strings.Contains(err.Error(), "PBE-MD5-DES") {
		t.Errorf("PBES1: %v", err)
	}
	if _, err := DecryptPKCS8([]byte{0x30, 0x00}, ""); !hasCode(err, CodeInvalid) {
		t.Errorf("malformed: %v", err)
	}
}

// TestTLSHandshake uses a decoded bundle as the client certificate of a
// TLS 1.3 handshake whose server trusts only the test root CA.
func TestTLSHandshake(t *testing.T) {
	b, err := Decode(readFile(t, "windows-aes256-sha256.p12"), testPassword)
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(readCert(t, "root.crt"))
	serverCert := b.TLSCertificate() // any certificate will do for the server
	cc, sc := net.Pipe()
	defer cc.Close()
	defer sc.Close()
	deadline := time.Now().Add(10 * time.Second)
	cc.SetDeadline(deadline)
	sc.SetDeadline(deadline)
	done := make(chan error, 1)
	go func() {
		s := tls.Server(sc, &tls.Config{
			Certificates: []tls.Certificate{serverCert},
			ClientAuth:   tls.RequireAndVerifyClientCert,
			ClientCAs:    pool,
			MinVersion:   tls.VersionTLS13,
		})
		err := s.Handshake()
		if err == nil && s.ConnectionState().PeerCertificates[0].Subject.CommonName != "PostSonde Test Client rsa" {
			err = errors.New("unexpected client certificate")
		}
		done <- err
	}()
	c := tls.Client(cc, &tls.Config{
		InsecureSkipVerify: true, // the server side is not under test
		Certificates:       []tls.Certificate{b.TLSCertificate()},
		MinVersion:         tls.VersionTLS13,
	})
	if err := c.Handshake(); err != nil {
		t.Fatalf("client handshake: %v", err)
	}
	if err := <-done; err != nil {
		t.Fatalf("server: %v", err)
	}
}

// toBER re-encodes DER with indefinite lengths for every constructed
// element and splits primitive OCTET STRINGs into constructed chunks,
// without descending into OCTET STRING contents.
func toBER(t *testing.T, der []byte) []byte {
	t.Helper()
	var out []byte
	for len(der) > 0 {
		var rv asn1.RawValue
		rest, err := asn1.Unmarshal(der, &rv)
		if err != nil {
			t.Fatal(err)
		}
		tag := rv.FullBytes[0]
		switch {
		case rv.IsCompound:
			out = append(out, tag, 0x80)
			out = append(out, toBER(t, rv.Bytes)...)
			out = append(out, 0, 0)
		case (tag == 0x04 || tag == 0x80) && len(rv.Bytes) > 10:
			out = append(out, tag|0x20, 0x80)
			for chunk := range slicesChunk(rv.Bytes, 7) {
				out = append(out, 0x04, byte(len(chunk)))
				out = append(out, chunk...)
			}
			out = append(out, 0, 0)
		default:
			out = append(out, rv.FullBytes...)
		}
		der = rest
	}
	return out
}

func slicesChunk(b []byte, n int) func(func([]byte) bool) {
	return func(yield func([]byte) bool) {
		for len(b) > 0 {
			k := min(n, len(b))
			if !yield(b[:k]) {
				return
			}
			b = b[k:]
		}
	}
}

func TestBER(t *testing.T) {
	// The outer PFX structure only: the MAC covers the contents of the
	// authSafe OCTET STRING, which chunking keeps.
	der := readFile(t, "windows-3des-sha1.p12")
	var pfx struct {
		Version  int
		AuthSafe asn1.RawValue
		MacData  asn1.RawValue
	}
	if _, err := asn1.Unmarshal(der, &pfx); err != nil {
		t.Fatal(err)
	}
	var ci struct {
		Type    asn1.ObjectIdentifier
		Content asn1.RawValue `asn1:"tag:0,explicit"`
	}
	if _, err := asn1.Unmarshal(pfx.AuthSafe.FullBytes, &ci); err != nil {
		t.Fatal(err)
	}
	// SEQUENCE { version, [contentInfo with a chunked, indefinite [0]], macData }
	var inner []byte
	inner = append(inner, 0x02, 0x01, 0x03)
	oid, _ := asn1.Marshal(ci.Type)
	body := append(oid, toBER(t, ci.Content.FullBytes)...) // [0] { OCTET STRING } as BER
	inner = append(inner, 0x30, 0x80)
	inner = append(inner, body...)
	inner = append(inner, 0, 0)
	inner = append(inner, pfx.MacData.FullBytes...)
	ber := append([]byte{0x30, 0x80}, inner...)
	ber = append(ber, 0, 0)
	if bytes.Equal(ber, der) {
		t.Fatal("no BER produced")
	}
	b, err := Decode(ber, testPassword)
	if err != nil {
		t.Fatalf("Decode(BER): %v", err)
	}
	if b.Leaf.Subject.CommonName != "PostSonde Test Client rsa" {
		t.Errorf("leaf %s", b.Leaf.Subject)
	}

	// An encryptedData whose [0] IMPLICIT content is chunked.
	var authSafe []byte
	if _, err := asn1.Unmarshal(ci.Content.Bytes, &authSafe); err != nil {
		t.Fatal(err)
	}
	var infos []asn1.RawValue
	if _, err := asn1.Unmarshal(authSafe, &infos); err != nil {
		t.Fatal(err)
	}
	var enc contentInfo
	if err := unmarshal(toBER(t, infos[0].FullBytes), &enc); err != nil || !enc.ContentType.Equal(oidEncryptedData) {
		t.Fatalf("encrypted content info: %v %v", err, enc.ContentType)
	}
	d := &decoder{password: testPassword, hasMAC: true}
	if err := d.contentInfo(enc); err != nil || len(d.certs) != 3 {
		t.Fatalf("chunked encryptedData: %v, %d certificates", err, len(d.certs))
	}

	// berToDER round-trips DER and rejects malformed input.
	if got, err := berToDER(der); err != nil || !bytes.Equal(got, der) {
		t.Errorf("DER changed: %v", err)
	}
	for _, bad := range [][]byte{
		{0x30},                   // no length
		{0x30, 0x05, 0x02},       // short
		{0x04, 0x80, 0x00, 0x00}, // indefinite primitive
		{0x30, 0x80, 0x02, 0x01}, // no end-of-contents
		{0x30, 0x85, 1, 1, 1, 1, 1},
		{0x24, 0x03, 0x02, 0x01, 0x00}, // constructed OCTET STRING of an INTEGER
		{0x1f, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0x00},
	} {
		if _, err := berToDER(bad); err == nil {
			t.Errorf("berToDER(% x) accepted", bad)
		}
	}
	deep := bytes.Repeat([]byte{0x30, 0x80}, maxBERDepth+2)
	if _, err := berToDER(deep); err == nil {
		t.Error("deep BER accepted")
	}
}

// TestKDF checks the PKCS#12 KDF against a vector computed with OpenSSL's
// PKCS12_key_gen_uni (password "smeg", salt 0A58CF64530D823F, 1 iteration,
// SHA-1, ID 1, 24 bytes), the vector used by several implementations.
func TestKDF(t *testing.T) {
	salt := []byte{0x0A, 0x58, 0xCF, 0x64, 0x53, 0x0D, 0x82, 0x3F}
	got := pkcs12KDF(hSHA1, bmpPassword("smeg"), salt, 1, 1, 24)
	want := []byte{0x8A, 0xAA, 0xE6, 0x29, 0x7B, 0x6C, 0xB0, 0x46, 0x42, 0xAB, 0x5B, 0x07, 0x78, 0x51, 0x28, 0x4E, 0xB7, 0x12, 0x8F, 0x1A, 0x2A, 0x7F, 0xBC, 0xA3}
	if !bytes.Equal(got, want) {
		t.Errorf("key % X", got)
	}
	got = pkcs12KDF(hSHA1, bmpPassword("smeg"), salt, 1, 2, 8)
	if want := []byte{0x79, 0x99, 0x3D, 0xFE, 0x04, 0x8D, 0x3B, 0x76}; !bytes.Equal(got, want) {
		t.Errorf("iv % X", got)
	}
	if got := bmpPassword("aé"); !bytes.Equal(got, []byte{0, 'a', 0, 0xe9, 0, 0}) {
		t.Errorf("bmp % x", got)
	}
}

// TestRC2Vectors checks RC2 against the test vectors of RFC 2268 section 5.
func TestRC2Vectors(t *testing.T) {
	seen := [256]bool{}
	for _, b := range piTable {
		seen[b] = true
	}
	for i, ok := range seen {
		if !ok {
			t.Fatalf("piTable lacks %d", i)
		}
	}
	for _, v := range []struct {
		bits                   int
		key, plain, ciphertext string
	}{
		{63, "0000000000000000", "0000000000000000", "ebb773f993278eff"},
		{64, "ffffffffffffffff", "ffffffffffffffff", "278b27e42e2f0d49"},
		{64, "3000000000000000", "1000000000000001", "30649edf9be7d2c2"},
		{64, "88", "0000000000000000", "61a8a244adacccf0"},
		{64, "88bca90e90875a", "0000000000000000", "6ccf4308974c267f"},
		{64, "88bca90e90875a7f0f79c384627bafb2", "0000000000000000", "1a807d272bbe5db1"},
		{128, "88bca90e90875a7f0f79c384627bafb2", "0000000000000000", "2269552ab0f85ca6"},
		{129, "88bca90e90875a7f0f79c384627bafb216f80a6f85920584c42fceb0be255daf1e", "0000000000000000", "5b78d3a43dfff1f1"},
	} {
		key, _ := hex.DecodeString(v.key)
		plain, _ := hex.DecodeString(v.plain)
		want, _ := hex.DecodeString(v.ciphertext)
		c, err := newRC2(key, v.bits)
		if err != nil {
			t.Fatal(err)
		}
		got := make([]byte, 8)
		c.Encrypt(got, plain)
		if !bytes.Equal(got, want) {
			t.Errorf("%d bits, key %s: encrypt %x, want %x", v.bits, v.key, got, want)
		}
		c.Decrypt(got, want)
		if !bytes.Equal(got, plain) {
			t.Errorf("%d bits, key %s: decrypt %x, want %x", v.bits, v.key, got, plain)
		}
	}
	for _, bad := range []struct{ keyLen, bits int }{{0, 40}, {129, 40}, {5, 0}, {5, 1025}} {
		if _, err := newRC2(make([]byte, bad.keyLen), bad.bits); err == nil {
			t.Errorf("newRC2(%d bytes, %d bits) accepted", bad.keyLen, bad.bits)
		}
	}
	for version, want := range map[int]int{160: 40, 120: 64, 58: 128, 256: 256, 1024: 1024} {
		if got, ok := rc2ParamBits(version); !ok || got != want {
			t.Errorf("rc2ParamBits(%d) = %d, %v", version, got, ok)
		}
	}
	if _, ok := rc2ParamBits(7); ok {
		t.Error("rc2ParamBits(7) accepted")
	}
}

func TestLimitsAndParams(t *testing.T) {
	salt := []byte("saltsalt")
	params := func(v any) pkix.AlgorithmIdentifier {
		b, err := asn1.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		return pkix.AlgorithmIdentifier{Parameters: asn1.RawValue{FullBytes: b}}
	}
	alg := params(pbeParams{Salt: salt, Iterations: MaxIterations + 1})
	alg.Algorithm = oidPBEWithSHAAnd3KeyTripleDESCBC
	if _, err := decrypt(alg, "", false, make([]byte, 8)); !errors.Is(err, errIterations) {
		t.Errorf("iterations: %v", err)
	}
	if !hasCode(convert(errIterations, "x"), CodeUnsupported) {
		t.Error("iterations code")
	}
	alg.Parameters = asn1.RawValue{FullBytes: []byte{0x30, 0x00}}
	if _, err := decrypt(alg, "", false, make([]byte, 8)); !errors.Is(err, errParams) {
		t.Errorf("params: %v", err)
	}
	if !hasCode(convert(errParams, "x"), CodeInvalid) {
		t.Error("params code")
	}
	alg.Algorithm = asn1.ObjectIdentifier{1, 2, 3}
	if _, err := decrypt(alg, "", false, nil); err == nil || !strings.Contains(err.Error(), "1.2.3") {
		t.Errorf("unknown algorithm: %v", err)
	}
}

func TestOrderChain(t *testing.T) {
	leaf, inter, root := readCert(t, "rsa.crt"), readCert(t, "inter.crt"), readCert(t, "root.crt")
	other := readCert(t, "ec.crt")
	certs := []certEntry{{cert: root}, {cert: other}, {cert: leaf}, {cert: inter}}
	got := orderChain(certs, 2)
	if len(got) != 3 || got[0] != inter || got[1] != root || got[2] != other {
		t.Errorf("chain %v", got)
	}
	d := &decoder{keys: []keyEntry{{key: readKey(t, "ec.key")}}, certs: []certEntry{{cert: leaf}}}
	if _, err := d.bundle(); !hasCode(err, CodeKeyMismatch) {
		t.Errorf("mismatch: %v", err)
	}
}
