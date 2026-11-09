# PKCS#12 (PFX) decoder

`internal/core/pkcs12` reads client certificates stored as PKCS#12 files
(`.p12`, `.pfx`), which the Go standard library cannot do, for mTLS client
certificates (AUTH-30). It uses only the standard library: `encoding/asn1`,
`crypto/pbkdf2`, `crypto/aes`, `crypto/des`, `crypto/rc4` and
`crypto/hmac`, plus its own RC2.

| Piece | Where |
|---|---|
| `Decode`, `DecryptPKCS8`, bags, chain order, error codes | `pkcs12/decode.go` |
| Algorithms: PKCS#12 KDF, PBES2, PKCS#12 PBEs, MACs, OIDs | `pkcs12/crypto.go` |
| RC2 (RFC 2268) block cipher | `pkcs12/rc2.go` |
| BER to DER normalization | `pkcs12/ber.go` |
| Fixtures and the script that makes them | `pkcs12/testdata/`, `testdata/gen.sh` |

The per-host client certificates (AUTH-30) use it: `tlsconf.LoadClientCert`
decodes a `.p12`/`.pfx` entry with `Decode` and an encrypted PEM key
(`ENCRYPTED PRIVATE KEY`) with `DecryptPKCS8`, with the passphrase from
the app's local secret storage ([network.md](network.md#client-certificates)).
The request-level TLS options of gRPC (`tlsconf.Options`) still take
unencrypted PEM only. The committed fixtures double as mTLS test material
for every protocol client (`tlstest.Fixture`, `tlstest.FixtureClientCAs`).

## Use

```go
b, err := pkcs12.Decode(data, password) // err: *errs.Error with a pkcs12.* code
cfg.Certificates = []tls.Certificate{b.TLSCertificate()} // leaf, then chain
b.Leaf, b.Chain, b.PrivateKey, b.FriendlyName
b.Info // {MAC: "HMAC-SHA-256", KeyEncryption: "PBES2 AES-256-CBC (PBKDF2 HMAC-SHA256)", CertEncryption: ...}

key, err := pkcs12.DecryptPKCS8(block.Bytes, password) // an "ENCRYPTED PRIVATE KEY" PEM block
```

- **Key and leaf:** the first private key that has a certificate with the
  same public key; with several candidates the one with the key's
  `localKeyID` wins. Keys are what `x509.ParsePKCS8PrivateKey` reads: RSA,
  ECDSA, Ed25519.
- **Chain:** the leaf's issuers in order (matched by subject), then any
  other certificates in file order. `TLSCertificate` sends them all.
- **Passwords:** the PKCS#12 KDF (MAC and SHA1-3DES) uses the password as
  NUL-terminated UTF-16; PBES2 and PBMAC1 use its UTF-8 bytes. An empty
  password is tried both as two zero bytes and as no bytes, since tools
  differ.
- **Input:** DER or BER (indefinite lengths, chunked OCTET STRINGs, as
  some Java and NSS tools write), at most 1 MiB (`MaxSize`); key
  derivations over 10,000,000 iterations (`MaxIterations`) are refused.

## Supported algorithms

| Part | Supported | Unsupported (error) |
|---|---|---|
| Integrity (MAC) | HMAC-SHA-1, -224, -256, -384, -512 with the PKCS#12 KDF; PBMAC1 (RFC 9579) with PBKDF2; no MAC | public-key integrity (signedData) |
| Encryption | PBES2 with PBKDF2 (PRF HMAC-SHA-1/224/256/384/512) and AES-128/192/256-CBC, DES-EDE3-CBC, DES-CBC, RC2-CBC; PKCS#12 PBE SHA-1 with 3-key and 2-key 3DES, 40- and 128-bit RC2-CBC, 40- and 128-bit RC4; unencrypted bags | PKCS#5 v1.5 PBES1 (MD2/MD5/SHA1 with DES or RC2), public-key privacy (envelopedData) |
| Bags | keyBag, pkcs8ShroudedKeyBag, certBag (X.509), safeContentsBag | SDSI certificates; CRL and secret bags are skipped |

RC2 is implemented in `rc2.go` from RFC 2268 (key expansion with an
effective key length, mixing and mashing rounds; checked against the RFC's
test vectors), because Go has no RC2 and the macOS Keychain and OpenSSL 1.x
encrypt certificates with 40-bit RC2. For PBES2 RC2-CBC the effective key
length comes from the RC2 parameter version (32 bits when absent, as RFC
8018 says) and the key length from the PBKDF2 parameters (16 bytes when
absent). RC4 uses `crypto/rc4`. These ciphers are only used to read old
files, never to write anything. For the remaining unsupported algorithms
the error names the algorithm and says how to re-export with AES.

### What exporters write

| Exporter | Key | Certificates | MAC | Result |
|---|---|---|---|---|
| Windows, "AES256-SHA256" option | PBES2 AES-256 | PBES2 AES-256 | SHA-256 | decoded |
| Windows, "TripleDES-SHA1" option | SHA1-3DES | SHA1-3DES | SHA-1 | decoded |
| OpenSSL 3 (default), Java 12+ keytool | PBES2 AES-256 | PBES2 AES-256 | SHA-256 | decoded |
| OpenSSL 3.4+ `-pbmac1_pbkdf2` | PBES2 AES-256 | PBES2 AES-256 | PBMAC1 | decoded |
| macOS Keychain Access | SHA1-3DES | SHA1-RC2-40 | SHA-1 | decoded |
| OpenSSL 1.x default, OpenSSL 3 `-legacy`, old Windows | SHA1-3DES | SHA1-RC2-40 | SHA-1 | decoded |

## Errors

Every error is an `*errs.Error`:

| Code | Kind | When |
|---|---|---|
| `pkcs12.invalid` | `parse` | not PKCS#12, truncated or malformed ASN.1, not version 3, a bad certificate or key |
| `pkcs12.wrong_password` | `invalid_argument` | the MAC does not match (wrong password, or changed bytes), or, without a MAC, decryption gives bad padding or no valid key |
| `pkcs12.unsupported_algorithm` | `unsupported` | PBES1 or another unknown algorithm; detail `algorithm`; hint to re-export with AES |
| `pkcs12.unsupported` | `unsupported` | public-key integrity or privacy mode, non-X.509 certificates, too many iterations |
| `pkcs12.too_large` | `invalid_argument` | over 1 MiB |
| `pkcs12.no_private_key` / `pkcs12.no_certificate` | `invalid_argument` | the file has no key / no certificate |
| `pkcs12.key_mismatch` | `invalid_argument` | no certificate belongs to the key |

Messages never include the password.

## Tests

`testdata/gen.sh` (OpenSSL 3.4 or later) makes a test root CA,
intermediate and RSA, ECDSA P-256 and Ed25519 client certificates, and
exports them with the algorithm profiles above plus 2-key 3DES, RC4-128,
RC4-40 with RC2-128, PBES1 (unsupported), AES-128 and AES-192, SHA-384 and
SHA-512 MACs, no MAC, no encryption, an empty and a
non-ASCII password, a certificates-only and a key-only file, and encrypted
PKCS#8 keys (including PBES2 with RC2-CBC and RC2-40-CBC). The fixtures
are committed; rerun the script only to change them (then update the expected `Info` values in the tests).

`pkcs12_test.go` decodes every fixture and compares key, leaf and chain
with the PEM originals, checks every error code, flips each byte of three
files and truncates them (always a coded error, never a panic), converts a
file to BER, checks the PKCS#12 KDF against a published vector and RC2
against the RFC 2268 vectors, and runs a
TLS 1.3 handshake over `net.Pipe` with a server that requires and verifies
the client certificate. `FuzzDecode` fuzzes `Decode`:

```sh
go test ./internal/core/pkcs12 -run XXX -fuzz FuzzDecode -fuzztime 60s
```
