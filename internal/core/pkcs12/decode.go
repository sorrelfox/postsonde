package pkcs12

import (
	"bytes"
	"crypto"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/asn1"
	"errors"
	"strconv"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// MaxSize is the largest file Decode reads. Real PFX files are a few KiB.
const MaxSize = 1 << 20

// Error codes of the *errs.Error values Decode and DecryptPKCS8 return.
const (
	// CodeInvalid: not a PKCS#12 file, or a damaged one. Kind parse.
	CodeInvalid = "pkcs12.invalid"
	// CodeWrongPassword: the password is wrong (the MAC does not match, or
	// decryption fails). Kind invalid_argument.
	CodeWrongPassword = "pkcs12.wrong_password"
	// CodeUnsupportedAlgorithm: an encryption or MAC algorithm that is not
	// supported, such as PKCS#5 v1.5 PBES1. Kind unsupported; detail
	// algorithm.
	CodeUnsupportedAlgorithm = "pkcs12.unsupported_algorithm"
	// CodeUnsupported: a PKCS#12 feature that is not supported: the
	// public-key privacy or integrity modes, non-X.509 certificates, key
	// derivations over MaxIterations. Kind unsupported.
	CodeUnsupported = "pkcs12.unsupported"
	// CodeTooLarge: the file is over MaxSize. Kind invalid_argument.
	CodeTooLarge = "pkcs12.too_large"
	// CodeNoPrivateKey: the file holds no private key. Kind
	// invalid_argument.
	CodeNoPrivateKey = "pkcs12.no_private_key"
	// CodeNoCertificate: the file holds no certificate. Kind
	// invalid_argument.
	CodeNoCertificate = "pkcs12.no_certificate"
	// CodeKeyMismatch: no certificate in the file belongs to the private
	// key. Kind invalid_argument.
	CodeKeyMismatch = "pkcs12.key_mismatch"
)

// Bundle is a decoded PKCS#12 file.
type Bundle struct {
	// PrivateKey is the key: *rsa.PrivateKey, *ecdsa.PrivateKey or
	// ed25519.PrivateKey (anything x509.ParsePKCS8PrivateKey returns).
	PrivateKey crypto.PrivateKey
	// Leaf is the certificate of PrivateKey.
	Leaf *x509.Certificate
	// Chain holds the other certificates: the leaf's issuers in order
	// first, then any others in file order.
	Chain []*x509.Certificate
	// FriendlyName is the key's (or leaf's) friendly name, if any.
	FriendlyName string
	// Info says which algorithms protect the file.
	Info Info
}

// Info names the algorithms of a file, e.g. "PBES2 AES-256-CBC
// (PBKDF2 HMAC-SHA256)", "PBE-SHA1-3DES" or "HMAC-SHA-256". An empty
// field means none (no MAC, or unencrypted).
type Info struct {
	MAC            string
	KeyEncryption  string
	CertEncryption string
}

// TLSCertificate returns the bundle as a tls.Certificate: the leaf, then
// the chain.
func (b *Bundle) TLSCertificate() tls.Certificate {
	c := tls.Certificate{PrivateKey: b.PrivateKey, Leaf: b.Leaf, Certificate: [][]byte{b.Leaf.Raw}}
	for _, x := range b.Chain {
		c.Certificate = append(c.Certificate, x.Raw)
	}
	return c
}

type pfxPdu struct {
	Version  int
	AuthSafe contentInfo
	MacData  macData `asn1:"optional"`
}

type contentInfo struct {
	ContentType asn1.ObjectIdentifier
	Content     asn1.RawValue `asn1:"tag:0,explicit,optional"`
}

type encryptedData struct {
	Version              int
	EncryptedContentInfo encryptedContentInfo
}

type encryptedContentInfo struct {
	ContentType                asn1.ObjectIdentifier
	ContentEncryptionAlgorithm pkix.AlgorithmIdentifier
	EncryptedContent           asn1.RawValue `asn1:"tag:0,optional"`
}

type safeBag struct {
	ID         asn1.ObjectIdentifier
	Value      asn1.RawValue     `asn1:"tag:0,explicit"`
	Attributes []pkcs12Attribute `asn1:"set,optional"`
}

type pkcs12Attribute struct {
	ID    asn1.ObjectIdentifier
	Value asn1.RawValue `asn1:"set"`
}

type certBag struct {
	ID   asn1.ObjectIdentifier
	Data []byte `asn1:"tag:0,explicit"`
}

type encryptedPrivateKeyInfo struct {
	Algorithm     pkix.AlgorithmIdentifier
	EncryptedData []byte
}

// decoder holds the state of one Decode.
type decoder struct {
	password   string
	emptyAsNil bool // PKCS#12 KDF with an empty (not NUL-terminated) password
	hasMAC     bool
	info       Info
	keys       []keyEntry
	certs      []certEntry
}

type keyEntry struct {
	key          crypto.PrivateKey
	localKeyID   []byte
	friendlyName string
}

type certEntry struct {
	cert         *x509.Certificate
	localKeyID   []byte
	friendlyName string
}

func invalid(msg string, err error) *errs.Error {
	return errs.Wrap(err, errs.Parse, msg).WithCode(CodeInvalid).
		WithHint("Check that the file is a .p12 or .pfx certificate file and was copied completely.")
}

func wrongPassword() *errs.Error {
	return errs.New(errs.InvalidArgument, "The password of the PKCS#12 file is wrong, or the file is damaged.").
		WithCode(CodeWrongPassword).
		WithHint("Enter the password that was set when the certificate was exported.")
}

// convert turns the internal errors of decryption and MAC checks into
// errs errors.
func convert(err error, what string) error {
	var ua *errUnsupportedAlg
	switch {
	case errors.As(err, &ua):
		e := errs.Wrap(err, errs.Unsupported, what+" uses "+ua.name+", which PostSonde does not support.").
			WithCode(CodeUnsupportedAlgorithm).WithDetail("algorithm", ua.name)
		return e.WithHint("Re-export the certificate with AES: on Windows choose \"AES256-SHA256\"; with OpenSSL 3 run " +
			"\"openssl pkcs12 -legacy -in old.p12 -nodes -out tmp.pem\" and \"openssl pkcs12 -export -in tmp.pem -out new.p12\", then delete tmp.pem.")
	case errors.Is(err, errIterations):
		return errs.Wrap(err, errs.Unsupported, what+" uses more than "+strconv.Itoa(MaxIterations)+" key derivation iterations.").
			WithCode(CodeUnsupported)
	case errors.Is(err, errDecrypt):
		return wrongPassword()
	}
	return invalid(what+" has malformed encryption parameters.", err)
}

// unmarshal parses BER or DER into v and rejects trailing data.
func unmarshal(in []byte, v any) error {
	der, err := berToDER(in)
	if err != nil {
		return err
	}
	rest, err := asn1.Unmarshal(der, v)
	if err != nil {
		return err
	}
	if len(rest) > 0 {
		return errors.New("pkcs12: trailing data")
	}
	return nil
}

// Decode decodes a PKCS#12 file (DER or BER) with its password. Errors
// are *errs.Error with one of the Code constants.
func Decode(data []byte, password string) (*Bundle, error) {
	if len(data) > MaxSize {
		return nil, errs.New(errs.InvalidArgument, "The file is larger than 1 MiB; it is not a PKCS#12 certificate file.").WithCode(CodeTooLarge)
	}
	var pfx pfxPdu
	if err := unmarshal(data, &pfx); err != nil {
		return nil, invalid("The file is not a PKCS#12 (.p12/.pfx) file.", err)
	}
	if pfx.Version != 3 {
		return nil, invalid("The file is not a PKCS#12 version 3 file (version "+strconv.Itoa(pfx.Version)+").", nil)
	}
	if pfx.AuthSafe.ContentType.Equal(oidSignedData) {
		return nil, errs.New(errs.Unsupported, "The PKCS#12 file is protected with a public key (signed), which PostSonde does not support.").
			WithCode(CodeUnsupported).WithHint("Export the certificate with password protection.")
	}
	if !pfx.AuthSafe.ContentType.Equal(oidData) {
		return nil, invalid("The PKCS#12 file has an unknown content type "+pfx.AuthSafe.ContentType.String()+".", nil)
	}
	var authSafe []byte
	if err := unmarshal(pfx.AuthSafe.Content.Bytes, &authSafe); err != nil {
		return nil, invalid("The PKCS#12 file's contents are malformed.", err)
	}
	d := &decoder{password: password}
	if len(pfx.MacData.Mac.Algorithm.Algorithm) > 0 {
		d.hasMAC = true
		d.info.MAC = pfx.MacData.name()
		ok, err := pfx.MacData.verify(authSafe, password, false)
		if err == nil && !ok && password == "" {
			if ok, err = pfx.MacData.verify(authSafe, password, true); ok {
				d.emptyAsNil = true
			}
		}
		if err != nil {
			return nil, convert(err, "The file's integrity check")
		}
		if !ok {
			return nil, wrongPassword()
		}
	}
	var infos []contentInfo
	if err := unmarshal(authSafe, &infos); err != nil {
		return nil, invalid("The PKCS#12 file's contents are malformed.", err)
	}
	for _, ci := range infos {
		if err := d.contentInfo(ci); err != nil {
			return nil, err
		}
	}
	return d.bundle()
}

func (d *decoder) contentInfo(ci contentInfo) error {
	switch {
	case ci.ContentType.Equal(oidData):
		var safe []byte
		if err := unmarshal(ci.Content.Bytes, &safe); err != nil {
			return invalid("The PKCS#12 file's contents are malformed.", err)
		}
		return d.safeContents(safe, 0)
	case ci.ContentType.Equal(oidEncryptedData):
		var ed encryptedData
		if err := unmarshal(ci.Content.Bytes, &ed); err != nil {
			return invalid("The PKCS#12 file's encrypted contents are malformed.", err)
		}
		eci := ed.EncryptedContentInfo
		ciphertext := eci.EncryptedContent.Bytes
		if eci.EncryptedContent.IsCompound { // BER: [0] holding OCTET STRING chunks
			ciphertext = nil
			for rest := eci.EncryptedContent.Bytes; len(rest) > 0; {
				c, n, err := derContent(rest)
				if err != nil {
					return invalid("The PKCS#12 file's encrypted contents are malformed.", err)
				}
				ciphertext = append(ciphertext, c...)
				rest = rest[n:]
			}
		}
		if d.info.CertEncryption == "" {
			d.info.CertEncryption = describe(eci.ContentEncryptionAlgorithm)
		}
		plain, err := d.decrypt(eci.ContentEncryptionAlgorithm, ciphertext)
		if err != nil {
			return convert(err, "The file's certificate section")
		}
		return d.safeContents(plain, 0)
	case ci.ContentType.Equal(oidEnvelopedData):
		return errs.New(errs.Unsupported, "The PKCS#12 file is encrypted for a public key (enveloped), which PostSonde does not support.").
			WithCode(CodeUnsupported).WithHint("Export the certificate with password protection.")
	}
	return invalid("The PKCS#12 file has an unknown content type "+ci.ContentType.String()+".", nil)
}

// decrypt decrypts with the password, trying the empty password both
// ways when there is no MAC to tell which one the file uses.
func (d *decoder) decrypt(alg pkix.AlgorithmIdentifier, data []byte) ([]byte, error) {
	plain, err := decrypt(alg, d.password, d.emptyAsNil, data)
	if errors.Is(err, errDecrypt) && !d.hasMAC && d.password == "" && !d.emptyAsNil {
		if p, e := decrypt(alg, d.password, true, data); e == nil {
			d.emptyAsNil = true
			return p, nil
		}
	}
	return plain, err
}

func (d *decoder) safeContents(data []byte, depth int) error {
	if depth > 8 {
		return invalid("The PKCS#12 file nests its contents too deeply.", nil)
	}
	var bags []safeBag
	if err := unmarshal(data, &bags); err != nil {
		if !d.hasMAC {
			// Without a MAC, a wrong password can decrypt to garbage
			// that happens to have valid padding.
			return wrongPassword()
		}
		return invalid("The PKCS#12 file's contents are malformed.", err)
	}
	for _, bag := range bags {
		localKeyID, name := bagAttributes(bag.Attributes)
		switch {
		case bag.ID.Equal(oidKeyBag):
			key, err := parseKey(bag.Value.Bytes)
			if err != nil {
				return invalid("The private key in the PKCS#12 file is not valid.", err)
			}
			d.keys = append(d.keys, keyEntry{key, localKeyID, name})
		case bag.ID.Equal(oidShroudedKeyBag):
			var epki encryptedPrivateKeyInfo
			if err := unmarshal(bag.Value.Bytes, &epki); err != nil {
				return invalid("The encrypted private key in the PKCS#12 file is malformed.", err)
			}
			if d.info.KeyEncryption == "" {
				d.info.KeyEncryption = describe(epki.Algorithm)
			}
			plain, err := d.decrypt(epki.Algorithm, epki.EncryptedData)
			if err != nil {
				return convert(err, "The private key")
			}
			key, err := parseKey(plain)
			if err != nil {
				if !d.hasMAC {
					return wrongPassword()
				}
				return invalid("The private key in the PKCS#12 file is not valid.", err)
			}
			d.keys = append(d.keys, keyEntry{key, localKeyID, name})
		case bag.ID.Equal(oidCertBag):
			var cb certBag
			if err := unmarshal(bag.Value.Bytes, &cb); err != nil {
				return invalid("A certificate in the PKCS#12 file is malformed.", err)
			}
			if !cb.ID.Equal(oidX509Certificate) {
				return errs.New(errs.Unsupported, "The PKCS#12 file holds a certificate that is not X.509.").WithCode(CodeUnsupported)
			}
			cert, err := x509.ParseCertificate(cb.Data)
			if err != nil {
				return invalid("A certificate in the PKCS#12 file is not valid.", err)
			}
			d.certs = append(d.certs, certEntry{cert, localKeyID, name})
		case bag.ID.Equal(oidSafeContentsBag):
			if err := d.safeContents(bag.Value.Bytes, depth+1); err != nil {
				return err
			}
		}
		// CRL, secret and unknown bags are not needed for TLS.
	}
	return nil
}

// parseKey parses a PKCS#8 PrivateKeyInfo.
func parseKey(der []byte) (crypto.PrivateKey, error) {
	if norm, err := berToDER(der); err == nil {
		der = norm
	}
	return x509.ParsePKCS8PrivateKey(der)
}

func bagAttributes(attrs []pkcs12Attribute) (localKeyID []byte, friendlyName string) {
	for _, a := range attrs {
		switch {
		case a.ID.Equal(oidLocalKeyID):
			var id []byte
			if _, err := asn1.Unmarshal(a.Value.Bytes, &id); err == nil {
				localKeyID = id
			}
		case a.ID.Equal(oidFriendlyName):
			var name string
			if _, err := asn1.Unmarshal(a.Value.Bytes, &name); err == nil {
				friendlyName = name
			}
		}
	}
	return localKeyID, friendlyName
}

// describe names an encryption algorithm for Info.
func describe(alg pkix.AlgorithmIdentifier) string {
	if !alg.Algorithm.Equal(oidPBES2) {
		return algorithmName(alg.Algorithm)
	}
	var p pbes2Params
	if unmarshalParams(alg.Parameters.FullBytes, &p) != nil {
		return "PBES2"
	}
	prf := "HMAC-SHA1"
	var kp pbkdf2Params
	if unmarshalParams(p.KDF.Parameters.FullBytes, &kp) == nil && len(kp.PRF.Algorithm) > 0 {
		prf = algorithmName(kp.PRF.Algorithm)
	}
	return "PBES2 " + algorithmName(p.Scheme.Algorithm) + " (PBKDF2 " + prf + ")"
}

func publicKeyMatches(key crypto.PrivateKey, cert *x509.Certificate) bool {
	signer, ok := key.(interface{ Public() crypto.PublicKey })
	if !ok {
		return false
	}
	pub, ok := signer.Public().(interface{ Equal(crypto.PublicKey) bool })
	return ok && pub.Equal(cert.PublicKey)
}

// bundle picks the first key that has a certificate and orders the chain.
func (d *decoder) bundle() (*Bundle, error) {
	if len(d.keys) == 0 {
		return nil, errs.New(errs.InvalidArgument, "The PKCS#12 file has no private key.").WithCode(CodeNoPrivateKey).
			WithHint("Export the certificate together with its private key.")
	}
	if len(d.certs) == 0 {
		return nil, errs.New(errs.InvalidArgument, "The PKCS#12 file has no certificate.").WithCode(CodeNoCertificate).
			WithHint("Export the private key together with its certificate.")
	}
	for _, k := range d.keys {
		leaf := -1
		for i, c := range d.certs {
			if !publicKeyMatches(k.key, c.cert) {
				continue
			}
			if leaf < 0 || len(k.localKeyID) > 0 && bytes.Equal(k.localKeyID, c.localKeyID) {
				leaf = i
			}
		}
		if leaf < 0 {
			continue
		}
		b := &Bundle{PrivateKey: k.key, Leaf: d.certs[leaf].cert, FriendlyName: k.friendlyName, Info: d.info}
		if b.FriendlyName == "" {
			b.FriendlyName = d.certs[leaf].friendlyName
		}
		b.Chain = orderChain(d.certs, leaf)
		return b, nil
	}
	return nil, errs.New(errs.InvalidArgument, "No certificate in the PKCS#12 file belongs to its private key.").WithCode(CodeKeyMismatch)
}

// orderChain returns the certificates other than leaf: the issuer chain
// from the leaf first, then the rest in file order.
func orderChain(certs []certEntry, leaf int) []*x509.Certificate {
	used := make([]bool, len(certs))
	used[leaf] = true
	var chain []*x509.Certificate
	cur := certs[leaf].cert
	for {
		if bytes.Equal(cur.RawIssuer, cur.RawSubject) {
			break // self-issued: the root
		}
		next := -1
		for i, c := range certs {
			if !used[i] && bytes.Equal(c.cert.RawSubject, cur.RawIssuer) {
				next = i
				break
			}
		}
		if next < 0 {
			break
		}
		used[next] = true
		cur = certs[next].cert
		chain = append(chain, cur)
	}
	for i, c := range certs {
		if !used[i] {
			chain = append(chain, c.cert)
		}
	}
	return chain
}

// DecryptPKCS8 decrypts a DER EncryptedPrivateKeyInfo (the contents of an
// "ENCRYPTED PRIVATE KEY" PEM block) with the same algorithms as Decode
// and parses the key. Errors are *errs.Error with a Code constant.
func DecryptPKCS8(der []byte, password string) (crypto.PrivateKey, error) {
	var epki encryptedPrivateKeyInfo
	if err := unmarshal(der, &epki); err != nil {
		return nil, invalid("The encrypted private key is malformed.", err)
	}
	plain, err := decrypt(epki.Algorithm, password, false, epki.EncryptedData)
	if err != nil {
		return nil, convert(err, "The private key")
	}
	key, err := parseKey(plain)
	if err != nil {
		return nil, wrongPassword()
	}
	return key, nil
}
