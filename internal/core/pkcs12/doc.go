// Package pkcs12 decodes PKCS#12 (PFX, .p12/.pfx) files into a private
// key and certificate chain for TLS client authentication, which the Go
// standard library cannot do.
//
//	b, err := pkcs12.Decode(data, password) // err is an *errs.Error with a pkcs12.* code
//	cert := b.TLSCertificate()              // leaf first, then the chain
//
// The file is parsed with encoding/asn1 (BER input, such as indefinite
// lengths and chunked octet strings, is normalized to DER first). The
// password-integrity MAC is checked (HMAC-SHA-1/224/256/384/512 with the
// PKCS#12 key derivation, or PBMAC1 with PBKDF2). Encrypted contents and
// shrouded keys are decrypted with:
//
//   - PBES2: PBKDF2 (crypto/pbkdf2) with HMAC-SHA-1/224/256/384/512, and
//     AES-128/192/256-CBC, DES-EDE3-CBC, DES-CBC or RC2-CBC;
//   - PKCS#12 PBE: SHA-1 with 3-key or 2-key triple DES, 40- or 128-bit
//     RC2-CBC, or 40- or 128-bit RC4 (crypto/rc4).
//
// RC2 (RFC 2268) is implemented here, since Go has none: the macOS
// Keychain, OpenSSL 1.x and "openssl pkcs12 -legacy" encrypt certificates
// with 40-bit RC2. PKCS#5 v1.5 PBES1 and the public-key privacy and
// integrity modes are reported as unsupported, with a hint on re-exporting
// the file.
//
// Bags read: keyBag, pkcs8ShroudedKeyBag, certBag (X.509) and nested
// safeContentsBag; CRL and secret bags are skipped. Keys are anything
// x509.ParsePKCS8PrivateKey reads (RSA, ECDSA, Ed25519). The leaf is the
// certificate whose public key matches the key; the chain follows issuers
// from it. See docs/pkcs12.md.
package pkcs12
