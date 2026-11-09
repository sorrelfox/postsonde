package pkcs12

import (
	"os"
	"path/filepath"
	"testing"
)

// FuzzDecode decodes random input: Decode must not panic, and every
// error must be a coded *errs.Error.
func FuzzDecode(f *testing.F) {
	for _, name := range []string{"unencrypted-nomac.p12", "nomac-3des.p12", "windows-3des-sha1.p12", "pbmac1.p12", "macos-keychain.p12", "rc2-128-rc4-40.p12"} {
		b, err := os.ReadFile(filepath.Join("testdata", name))
		if err != nil {
			f.Fatal(err)
		}
		f.Add(b, testPassword)
	}
	f.Add([]byte{0x30, 0x80, 0x02, 0x01, 0x03, 0x00, 0x00}, "")
	f.Fuzz(func(t *testing.T, data []byte, password string) {
		b, err := Decode(data, password)
		if err != nil {
			if codeOf(err) == "" {
				t.Fatalf("uncoded error %T %v", err, err)
			}
			return
		}
		if b.Leaf == nil || b.PrivateKey == nil || !publicKeyMatches(b.PrivateKey, b.Leaf) {
			t.Fatal("bundle without a matching key and leaf")
		}
		_ = b.TLSCertificate()
	})
}

func codeOf(err error) string {
	_, c := errCode(err)
	return c
}
