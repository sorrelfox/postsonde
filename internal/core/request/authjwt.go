package request

// authjwt.go maps a jwt auth (AUTH-16): the token is built and signed at
// send time by package jwt. See docs/auth.md#jwt-bearer-auth-16.

import (
	"errors"
	"io/fs"
	"os"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/fileaccess"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/jwt"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// maxKeyFile is the largest JWT key file read: a PEM key is a few KiB.
const maxKeyFile = 1 << 20

func (m authMapper) jwt() (AuthResult, error) {
	a := m.auth
	in := a.In
	if in == "" {
		in = projectfile.APIKeyInHeader
	}
	if in != projectfile.APIKeyInHeader && in != projectfile.APIKeyInQuery {
		return AuthResult{}, m.detail(errs.New(errs.Unsupported, "This version of PostSonde cannot send a JWT there.").
			WithCode(CodeAuthUnsupported).
			WithHint("Send the JWT in a header or the query string."), "in")
	}
	v, err := m.fields("algorithm", a.Algorithm, "secret", a.JWTSecret, "keyFile", a.KeyFile, "prefix", a.BearerPrefix(), "key", a.Key)
	if err != nil {
		return AuthResult{}, err
	}
	alg, secret, keyFile, prefix, name := v[0], v[1], v[2], v[3], v[4]
	if secret == "" && keyFile == "" {
		return AuthResult{}, nil
	}
	if alg == "" {
		alg = jwt.DefaultAlgorithm
	}
	if !jwt.Supported(alg) {
		return AuthResult{}, m.detail(errs.New(errs.Unsupported, "PostSonde cannot sign a JWT with this algorithm.").
			WithCode(CodeAuthUnsupported).
			WithHint("Choose one of HS256/384/512, RS256/384/512, PS256/384/512, ES256/384/512 or EdDSA."), "algorithm")
	}

	keyField, data := "secret", []byte(secret)
	if keyFile != "" {
		keyField = "keyFile"
		if data, err = m.readKeyFile(keyFile); err != nil {
			return AuthResult{}, err
		}
	}
	key, err := jwt.ParseKey(alg, data, a.SecretBase64)
	if err != nil {
		return AuthResult{}, m.jwtError(err, keyField)
	}
	v, err = m.fields("jwtHeader", a.JWTHeader, "payload", a.Payload)
	if err != nil {
		return AuthResult{}, err
	}
	now := time.Now()
	if m.opts.Now != nil {
		now = m.opts.Now()
	}
	tok, err := jwt.Build(jwt.Input{
		Algorithm: alg,
		Header:    v[0],
		Payload:   v[1],
		Claims:    jwt.Claims{IssuedAt: a.IssuedAt, NotBefore: a.NotBefore, ID: a.JWTID, ExpiresIn: a.ExpiresIn},
		Key:       key,
		Now:       now,
		Rand:      m.opts.Rand,
	})
	if err != nil {
		return AuthResult{}, m.jwtError(err, keyField)
	}
	out := AuthResult{JWT: &tok, Secrets: []string{tok.Compact}, Credentials: &Credentials{Token: tok.Compact}}
	if keyFile == "" {
		out.Secrets = append(out.Secrets, secret)
	}

	if in == projectfile.APIKeyInQuery {
		if name == "" {
			name = projectfile.DefaultJWTQueryParam
		}
		out.Query = []QueryParam{{Name: name, Value: tok.Compact}}
		return out, nil
	}
	if name == "" {
		name = AuthorizationHeader
	}
	if !isToken(name) {
		return AuthResult{}, m.invalid("key", "The JWT's header name is not a valid header name.",
			"Header names may contain only letters, digits and !#$%&'*+-.^_`|~, with no spaces.")
	}
	if hasHeaderBreak(prefix) {
		return AuthResult{}, m.invalid("prefix", "The JWT prefix contains a line break or another control character.",
			"Remove control characters from the prefix, or from the variables it uses.")
	}
	value := tok.Compact
	if prefix != "" {
		value = prefix + " " + value
	}
	out.Headers = []httpclient.Header{{Name: name, Value: value, Enabled: true}}
	return out, nil
}

// readKeyFile reads the key file stored as stored (variables rendered):
// absolute, "~/" the home folder, or relative to the project root, and
// only from the project folder or an allowed directory (PRV-10). A
// relative path without a project is reported missing rather than read
// from the working directory.
func (m authMapper) readKeyFile(stored string) ([]byte, error) {
	fail := func(err error, kind errs.Kind, msg, hint string) error {
		return m.detail(errs.Wrap(err, kind, msg).
			WithCode(CodeAuthKeyFileFailed).
			WithHint(hint).
			WithDetail(DetailPath, stored), "keyFile")
	}
	p := m.filePolicy("The JWT key file")
	p.MaxSize = maxKeyFile
	data, err := p.ReadFile(stored)
	var e *errs.Error
	errors.As(err, &e)
	switch {
	case err == nil:
		return data, nil
	case fileaccess.IsNotAllowed(err):
		return nil, m.detail(e, "keyFile")
	case e != nil && e.Code == fileaccess.CodeInvalidPath:
		return nil, fail(os.ErrNotExist, errs.NotFound, "The JWT key file was not found: "+stored+".",
			"Use an absolute path, or a path relative to the project folder.")
	case e != nil && e.Code == fileaccess.CodeTooLarge:
		return nil, fail(err, errs.InvalidArgument, "The JWT key file is larger than 1 MiB: "+stored+".",
			"Choose the PEM file of the private key, or the file of the secret.")
	case errors.Is(err, fs.ErrNotExist):
		return nil, fail(err, errs.NotFound, "The JWT key file was not found: "+stored+".",
			"Choose the file again, or restore it at that path.")
	case errs.KindOf(err) == errs.PermissionDenied:
		return nil, fail(err, errs.PermissionDenied, "The JWT key file could not be read: "+stored+".",
			"Check that the file can be read, or choose another one.")
	}
	return nil, fail(err, errs.IO, "The JWT key file could not be read: "+stored+".",
		"Check that the file can be read, or choose another one.")
}

// filePolicy is what a file the auth names is read through (PRV-10): the
// project folder and the allowed directories. subject names the file in
// messages.
func (m authMapper) filePolicy(subject string) fileaccess.Policy {
	return fileaccess.Policy{ProjectRoot: m.opts.Root, Allowed: m.opts.AllowedDirs, Subject: subject}
}

// jwtError gives an error of package jwt the auth codes and details: the
// field is the part the error is about, keyField for the key.
func (m authMapper) jwtError(err error, keyField string) error {
	e, ok := err.(*errs.Error)
	if !ok {
		return m.detail(errs.Wrap(err, errs.Internal, "The JWT could not be built."), keyField)
	}
	field := keyField
	switch e.Details[jwt.DetailPart] {
	case jwt.PartAlgorithm:
		field = "algorithm"
	case jwt.PartHeader:
		field = "jwtHeader"
	case jwt.PartPayload:
		field = "payload"
	}
	code := CodeAuthInvalid
	if e.Code == jwt.CodeUnsupportedAlgorithm {
		code = CodeAuthUnsupported
	}
	return m.detail(e.WithCode(code), field)
}
