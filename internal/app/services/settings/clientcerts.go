package settings

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"slices"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	"github.com/sorrelfox/postsonde/internal/core/appsecrets"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	core "github.com/sorrelfox/postsonde/internal/core/settings"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf"
)

// Per-host client certificates (AUTH-30, NET-05): network.clientCerts, a
// host pattern and a PEM certificate and key or a PKCS#12 file each. The
// settings screen lists, adds, edits and removes them with the methods
// below; the protocol services pick the certificate of each connection
// through a tlsconf.ClientCerts. A passphrase never goes into
// settings.json: it is kept in the local secret storage (appsecrets,
// WithSecrets). See docs/network.md#client-certificates.

// Error codes of the client certificate methods.
const (
	// CodeClientCertNotFound: no entry has the ID.
	CodeClientCertNotFound = "settings.client_cert_not_found"
	// CodeClientCertDuplicate: another entry has the same host pattern.
	CodeClientCertDuplicate = "settings.client_cert_duplicate"
	// CodeClientCertKind: PickClientCertFile got a kind other than
	// "cert", "key" or "pfx".
	CodeClientCertKind = "settings.client_cert_kind"
)

// Kinds of ClientCertDTO.Kind.
const (
	ClientCertPEM    = "pem"
	ClientCertPKCS12 = "pkcs12"
)

// WithSecrets sets where passphrases of client certificates are kept.
// Without it they are kept in memory for the session.
func WithSecrets(s *appsecrets.Store) Option {
	return func(svc *SettingsService) { svc.secrets = s }
}

// ClientCertDTO is one per-host client certificate, as read now.
type ClientCertDTO struct {
	ID string `json:"id"`
	// Host is the host pattern, as entered.
	Host string `json:"host"`
	// Kind is "pem" or "pkcs12".
	Kind     string `json:"kind"`
	CertFile string `json:"certFile"`
	KeyFile  string `json:"keyFile"`
	PFXFile  string `json:"pfxFile"`
	// HasPassphrase is true when a passphrase is stored. The passphrase
	// itself never leaves Go.
	HasPassphrase bool `json:"hasPassphrase"`
	// Subject, Issuer, NotBefore and NotAfter (RFC 3339, UTC), Expired and
	// SHA256 (fingerprint, lower-case hex) describe the client certificate;
	// empty when Error is set.
	Subject   string `json:"subject"`
	Issuer    string `json:"issuer"`
	NotBefore string `json:"notBefore"`
	NotAfter  string `json:"notAfter"`
	Expired   bool   `json:"expired"`
	SHA256    string `json:"sha256"`
	// ChainLength is the number of CA certificates sent after it.
	ChainLength int `json:"chainLength"`
	// Warnings: expired or not yet valid, not issued for client
	// authentication.
	Warnings []CABundleWarningDTO `json:"warnings"`
	// Error is why the certificate cannot be used now (a moved file, a
	// wrong passphrase): connections to matching hosts then fail until it
	// is fixed or removed. Null when it is fine.
	Error *apperr.ErrorDTO `json:"error"`
}

// ClientCertInputDTO is a client certificate to add, or the new values of
// one to update.
type ClientCertInputDTO struct {
	// Host is the host pattern: api.example.com, *.example.com,
	// localhost:8443, [::1]:8443.
	Host string `json:"host"`
	// CertFile and KeyFile (PEM), or PFXFile (PKCS#12): absolute paths.
	CertFile string `json:"certFile"`
	KeyFile  string `json:"keyFile"`
	PFXFile  string `json:"pfxFile"`
	// Passphrase of the PKCS#12 file or of an encrypted PKCS#8 key. Null
	// keeps the stored one (UpdateClientCert; none for AddClientCert), ""
	// removes it.
	Passphrase *string `json:"passphrase"`
}

// ClientCerts returns the configured client certificates, each read now,
// in the settings' order.
func (s *SettingsService) ClientCerts(ctx context.Context) ([]ClientCertDTO, error) {
	if err := ctx.Err(); err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "Loading the client certificates was interrupted.")
	}
	return s.describeClientCerts(s.store.Settings().Network.ClientCerts), nil
}

// PickClientCertFile shows the native file dialog for a client
// certificate file and returns the chosen absolute path, "" when the user
// cancels. kind is "cert" (PEM certificate), "key" (PEM private key) or
// "pfx" (PKCS#12).
func (s *SettingsService) PickClientCertFile(ctx context.Context, kind string) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", errs.Wrap(err, errs.KindOf(err), "Choosing a file was interrupted.")
	}
	var d FileDialog
	switch kind {
	case "cert":
		d = FileDialog{Title: "Choose the client certificate", Message: "Choose a PEM file with the client certificate (and any intermediates).",
			ButtonText: "Choose", FilterName: "PEM certificates (*.pem, *.crt, *.cer)", FilterPattern: "*.pem;*.crt;*.cer"}
	case "key":
		d = FileDialog{Title: "Choose the private key", Message: "Choose the PEM private key of the client certificate.",
			ButtonText: "Choose", FilterName: "PEM keys (*.key, *.pem)", FilterPattern: "*.key;*.pem"}
	case "pfx":
		d = FileDialog{Title: "Choose the PKCS#12 file", Message: "Choose a .p12 or .pfx file with the client certificate and its key.",
			ButtonText: "Choose", FilterName: "PKCS#12 files (*.p12, *.pfx)", FilterPattern: "*.p12;*.pfx"}
	default:
		return "", errs.New(errs.InvalidArgument, "Unknown kind of client certificate file "+kind+".").WithCode(CodeClientCertKind)
	}
	if s.pick == nil {
		return "", errs.New(errs.Unsupported, "Choosing a file is not available here.").WithCode(CodePickUnavailable)
	}
	path, err := s.pick(ctx, d)
	if err != nil {
		return "", errs.Wrap(err, errs.KindOf(err), "The file dialog could not be shown.").WithCode(CodePickUnavailable)
	}
	return path, nil
}

// AddClientCert checks in and adds it to the client certificates, then
// returns them as ClientCerts does. The files are read now: they must
// decode (PEM, or PKCS#12 with the passphrase) and the key must belong to
// the certificate; errors have the codes of tlsconf.LoadClientCert
// (tlsconf.*, pkcs12.wrong_password, ...) and nothing is saved. An
// expired certificate is added with a warning.
func (s *SettingsService) AddClientCert(ctx context.Context, in ClientCertInputDTO) ([]ClientCertDTO, error) {
	if err := ctx.Err(); err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "Adding a client certificate was interrupted.")
	}
	id, err := newClientCertID()
	if err != nil {
		return nil, err
	}
	pass := ""
	if in.Passphrase != nil {
		pass = *in.Passphrase
	}
	return s.saveClientCert(ctx, id, in, pass, false)
}

// UpdateClientCert replaces the host and files of the client certificate
// id, checked as AddClientCert does, and its passphrase unless
// in.Passphrase is null. It returns the certificates as ClientCerts does.
func (s *SettingsService) UpdateClientCert(ctx context.Context, id string, in ClientCertInputDTO) ([]ClientCertDTO, error) {
	if err := ctx.Err(); err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "Saving the client certificate was interrupted.")
	}
	if !slices.ContainsFunc(s.store.Settings().Network.ClientCerts, func(e tlsconf.ClientCertEntry) bool { return e.ID == id }) {
		return nil, notFound(id)
	}
	var pass string
	if in.Passphrase != nil {
		pass = *in.Passphrase
	} else {
		p, err := s.secretStore().Get(appsecrets.ClientCertPassphraseKey(id))
		if err != nil {
			return nil, err
		}
		pass = p
	}
	return s.saveClientCert(ctx, id, in, pass, true)
}

// RemoveClientCert removes the client certificate id and its passphrase,
// then returns the certificates as ClientCerts does. An unknown ID
// changes nothing.
func (s *SettingsService) RemoveClientCert(ctx context.Context, id string) ([]ClientCertDTO, error) {
	if err := ctx.Err(); err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "Removing the client certificate was interrupted.")
	}
	err := s.editList(func() error {
		list := s.store.Settings().Network.ClientCerts
		i := slices.IndexFunc(list, func(e tlsconf.ClientCertEntry) bool { return e.ID == id })
		if i < 0 {
			return nil
		}
		host := list[i].Host
		if err := s.updateClientCerts(slices.Delete(slices.Clone(list), i, i+1)); err != nil {
			return err
		}
		if err := s.secretStore().Delete(appsecrets.ClientCertPassphraseKey(id)); err != nil {
			s.logger.Warn("client certificate passphrase not removed", "err", err)
		}
		s.logger.Info("client certificate removed", "host", host)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.ClientCerts(ctx)
}

// saveClientCert checks the entry, stores the passphrase and saves the
// list with the entry added or replaced.
func (s *SettingsService) saveClientCert(ctx context.Context, id string, in ClientCertInputDTO, pass string, replace bool) ([]ClientCertDTO, error) {
	// Refused before the passphrase is stored for an entry that could not
	// be saved.
	if err := s.store.Policy().CheckLocked("network.clientCerts"); err != nil {
		return nil, err
	}
	e := tlsconf.ClientCertEntry{
		ID:       id,
		Host:     strings.TrimSpace(in.Host),
		CertFile: strings.TrimSpace(in.CertFile),
		KeyFile:  strings.TrimSpace(in.KeyFile),
		PFXFile:  strings.TrimSpace(in.PFXFile),
	}
	pattern, err := tlsconf.ParseHostPattern(e.Host)
	if err != nil {
		return nil, err
	}
	// From reading the list to saving it: one change at a time (listMu).
	s.listMu.Lock()
	defer s.listMu.Unlock()
	list := s.store.Settings().Network.ClientCerts
	for _, other := range list {
		if p, err := tlsconf.ParseHostPattern(other.Host); err == nil && other.ID != id && p == pattern {
			return nil, errs.New(errs.InvalidArgument, "There is already a client certificate for "+other.Host+".").
				WithCode(CodeClientCertDuplicate).WithHint("Edit that one, or change the host.").WithDetail("field", "host")
		}
	}
	cert, err := tlsconf.LoadClientCert(e, pass)
	if err != nil {
		return nil, err
	}

	secrets := s.secretStore()
	key := appsecrets.ClientCertPassphraseKey(id)
	old, err := secrets.Get(key)
	if err != nil {
		return nil, err
	}
	if err := secrets.Set(key, pass); err != nil {
		return nil, err
	}
	next := slices.Clone(list)
	if i := slices.IndexFunc(next, func(x tlsconf.ClientCertEntry) bool { return x.ID == id }); replace && i >= 0 {
		next[i] = e
	} else {
		next = append(next, e)
	}
	if err := s.updateClientCerts(next); err != nil {
		if rerr := secrets.Set(key, old); rerr != nil {
			s.logger.Warn("client certificate passphrase not restored", "err", rerr)
		}
		return nil, err
	}
	verb := "added"
	if replace {
		verb = "updated"
	}
	s.logger.Info("client certificate "+verb, "host", e.Host, "kind", kindOf(e), "subject", cert.Leaf.Subject.String(), "warnings", len(cert.Warnings))
	return s.ClientCerts(ctx)
}

func notFound(id string) error {
	return errs.New(errs.NotFound, "The client certificate is no longer in the settings.").
		WithCode(CodeClientCertNotFound).WithDetail("id", id)
}

// updateClientCerts saves the list and publishes the change.
func (s *SettingsService) updateClientCerts(list []tlsconf.ClientCertEntry) error {
	_, changed, err := s.store.Update(core.Patch{Network: core.NetworkPatch{ClientCerts: &list}})
	if err != nil {
		return err
	}
	if changed {
		s.publish(SourceApp)
	}
	return nil
}

// secretStore returns the passphrase store: WithSecrets', or one in
// memory made on first use.
func (s *SettingsService) secretStore() *appsecrets.Store {
	s.secretsMu.Lock()
	defer s.secretsMu.Unlock()
	if s.secrets == nil {
		s.secrets = appsecrets.New("")
	}
	return s.secrets
}

func newClientCertID() (string, error) {
	b := make([]byte, 8)
	if _, err := rand.Read(b); err != nil {
		return "", errs.Wrap(err, errs.Internal, "No ID could be made for the client certificate.")
	}
	return "cc_" + hex.EncodeToString(b), nil
}

func kindOf(e tlsconf.ClientCertEntry) string {
	if e.IsPKCS12() {
		return ClientCertPKCS12
	}
	return ClientCertPEM
}

// describeClientCerts loads each entry.
func (s *SettingsService) describeClientCerts(entries []tlsconf.ClientCertEntry) []ClientCertDTO {
	out := make([]ClientCertDTO, 0, len(entries))
	now := time.Now()
	secrets := s.secretStore()
	for _, e := range entries {
		dto := ClientCertDTO{ID: e.ID, Host: e.Host, Kind: kindOf(e), CertFile: e.CertFile, KeyFile: e.KeyFile, PFXFile: e.PFXFile,
			Warnings: []CABundleWarningDTO{}}
		pass, err := secrets.Get(appsecrets.ClientCertPassphraseKey(e.ID))
		if err != nil {
			ed := apperr.ToDTO(err)
			dto.Error = &ed
			out = append(out, dto)
			continue
		}
		dto.HasPassphrase = pass != ""
		cert, err := tlsconf.LoadClientCert(e, pass)
		if err != nil {
			ed := apperr.ToDTO(err)
			dto.Error = &ed
			out = append(out, dto)
			continue
		}
		sum := sha256.Sum256(cert.Leaf.Raw)
		dto.Subject = cert.Leaf.Subject.String()
		dto.Issuer = cert.Leaf.Issuer.String()
		dto.NotBefore = cert.Leaf.NotBefore.UTC().Format(time.RFC3339)
		dto.NotAfter = cert.Leaf.NotAfter.UTC().Format(time.RFC3339)
		dto.Expired = now.After(cert.Leaf.NotAfter)
		dto.SHA256 = hex.EncodeToString(sum[:])
		dto.ChainLength = len(cert.Chain)
		for _, w := range cert.Warnings {
			dto.Warnings = append(dto.Warnings, CABundleWarningDTO{Code: w.Code, Message: w.Message, Hint: w.Hint})
		}
		out = append(out, dto)
	}
	return out
}
