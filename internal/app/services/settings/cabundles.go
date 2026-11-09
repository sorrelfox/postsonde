package settings

import (
	"context"
	"path/filepath"
	"slices"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/apperr"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	core "github.com/sorrelfox/postsonde/internal/core/settings"
	"github.com/sorrelfox/postsonde/internal/core/tlsconf"
)

// Custom CA certificates (NET-04): network.caBundles, the PEM files every
// TLS connection trusts on top of the system roots. The settings screen
// lists, adds and removes them with the methods below; the protocol
// services read the list through a tlsconf.Trust. See
// docs/network.md#custom-ca-certificates.

// CodePickUnavailable: AddCABundle cannot show a file dialog (tests,
// headless runs).
const CodePickUnavailable = "settings.pick_unavailable"

// FileDialog describes the native dialog for choosing one file.
type FileDialog struct {
	Title      string
	Message    string
	ButtonText string
	// FilterName and FilterPattern ("*.pem;*.crt") limit the files
	// shown; the user can still choose any file where the OS allows.
	FilterName    string
	FilterPattern string
	// Folder asks for a folder instead of a file (an allowed directory,
	// PRV-10); the filter is then ignored.
	Folder bool
}

// FilePicker shows a native file (or folder) dialog and returns the
// chosen absolute path, or "" when the user cancels. The app layer
// supplies it.
type FilePicker func(ctx context.Context, d FileDialog) (string, error)

// WithFilePicker sets the dialog AddCABundle and AddAllowedDir show.
func WithFilePicker(pick FilePicker) Option {
	return func(s *SettingsService) { s.pick = pick }
}

// CABundleDTO is one configured CA bundle, as read now.
type CABundleDTO struct {
	// Path is the file's absolute path, as stored in the settings.
	Path string `json:"path"`
	// Certificates are the file's certificates, in order; empty when
	// Error is set.
	Certificates []CACertificateDTO `json:"certificates"`
	// Warnings: expired or not yet valid CAs, certificates that are not
	// CAs, a private key in the file.
	Warnings []CABundleWarningDTO `json:"warnings"`
	// Error is why the file cannot be used any more (moved, deleted, no
	// longer PEM): connections then fail until it is fixed or removed.
	// Null when the file is fine.
	Error *apperr.ErrorDTO `json:"error"`
}

// CACertificateDTO describes one certificate of a bundle.
type CACertificateDTO struct {
	Subject string `json:"subject"`
	Issuer  string `json:"issuer"`
	// NotBefore and NotAfter are RFC 3339 times, UTC.
	NotBefore string `json:"notBefore"`
	NotAfter  string `json:"notAfter"`
	// Expired is true when NotAfter has passed.
	Expired bool `json:"expired"`
	// IsCA is false for a certificate that cannot sign others (a
	// server's self-signed certificate, trusted as it is).
	IsCA bool `json:"isCa"`
	// SHA256 is the certificate's fingerprint, lower-case hex.
	SHA256 string `json:"sha256"`
}

// CABundleWarningDTO is a problem that does not stop the bundle from
// being used.
type CABundleWarningDTO struct {
	Code    string `json:"code"`
	Message string `json:"message"`
	Hint    string `json:"hint"`
}

// CABundles returns the configured CA bundles, each read now, in the
// settings' order.
func (s *SettingsService) CABundles(ctx context.Context) ([]CABundleDTO, error) {
	if err := ctx.Err(); err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "Loading the CA certificates was interrupted.")
	}
	return describeBundles(s.store.Settings().Network.CABundles), nil
}

// AddCABundle shows the file dialog and adds the chosen PEM file to the
// trusted CA bundles, then returns the bundles as CABundles does. Nothing
// changes when the user cancels or picks a file already in the list.
//
// The file must hold at least one PEM certificate: a file that is not PEM
// or has no certificate is a parse error (code tlsconf.ca_no_certificates),
// one that cannot be read tlsconf.file_unreadable, and so on
// (tlsconf.InspectBundle). An expired CA is only a warning on the bundle.
func (s *SettingsService) AddCABundle(ctx context.Context) ([]CABundleDTO, error) {
	if err := ctx.Err(); err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "Adding a CA certificate was interrupted.")
	}
	if err := s.store.Policy().CheckLocked("network.caBundles"); err != nil {
		return nil, err
	}
	if s.pick == nil {
		return nil, errs.New(errs.Unsupported, "Choosing a file is not available here.").WithCode(CodePickUnavailable)
	}
	path, err := s.pick(ctx, FileDialog{
		Title:         "Add a CA certificate",
		Message:       "Choose a PEM file with one or more CA certificates to trust.",
		ButtonText:    "Add",
		FilterName:    "PEM certificates (*.pem, *.crt, *.cer)",
		FilterPattern: "*.pem;*.crt;*.cer",
	})
	if err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "The file dialog could not be shown.").WithCode(CodePickUnavailable)
	}
	if path == "" {
		return s.CABundles(ctx)
	}
	return s.addCABundle(ctx, path)
}

// TrustCABundle adds the CA certificate file at path, known to the UI
// without a dialog (the local servers' CA, SRV-05), to the trusted CA
// bundles, as AddCABundle does, and returns the bundles as CABundles
// does. A path already in the list changes nothing.
func (s *SettingsService) TrustCABundle(ctx context.Context, path string) ([]CABundleDTO, error) {
	if err := ctx.Err(); err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "Adding a CA certificate was interrupted.")
	}
	return s.addCABundle(ctx, path)
}

// addCABundle validates path and appends it to network.caBundles.
func (s *SettingsService) addCABundle(ctx context.Context, path string) ([]CABundleDTO, error) {
	b, err := tlsconf.InspectBundle(path)
	if err != nil {
		return nil, err
	}
	err = s.editList(func() error {
		list := s.store.Settings().Network.CABundles
		if slices.Contains(list, b.Path) {
			return nil
		}
		if err := s.updateBundles(append(slices.Clone(list), b.Path)); err != nil {
			return err
		}
		s.logger.Info("ca bundle added", "path", b.Path, "certificates", len(b.Certs))
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.CABundles(ctx)
}

// RemoveCABundle removes path from the trusted CA bundles and returns the
// bundles as CABundles does. A path not in the list changes nothing.
func (s *SettingsService) RemoveCABundle(ctx context.Context, path string) ([]CABundleDTO, error) {
	if err := ctx.Err(); err != nil {
		return nil, errs.Wrap(err, errs.KindOf(err), "Removing a CA certificate was interrupted.")
	}
	err := s.editList(func() error {
		list := s.store.Settings().Network.CABundles
		i := slices.Index(list, path)
		if i < 0 {
			return nil
		}
		if err := s.updateBundles(slices.Delete(slices.Clone(list), i, i+1)); err != nil {
			return err
		}
		s.logger.Info("ca bundle removed", "path", path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	return s.CABundles(ctx)
}

// updateBundles saves the list and publishes the change.
func (s *SettingsService) updateBundles(list []string) error {
	_, changed, err := s.store.Update(core.Patch{Network: core.NetworkPatch{CABundles: &list}})
	if err != nil {
		return err
	}
	if changed {
		s.publish(SourceApp)
	}
	return nil
}

// describeBundles inspects each path.
func describeBundles(paths []string) []CABundleDTO {
	out := make([]CABundleDTO, 0, len(paths))
	now := time.Now()
	for _, p := range paths {
		dto := CABundleDTO{Path: p, Certificates: []CACertificateDTO{}, Warnings: []CABundleWarningDTO{}}
		b, err := tlsconf.InspectBundle(filepath.Clean(p))
		if err != nil {
			e := apperr.ToDTO(err)
			dto.Error = &e
			out = append(out, dto)
			continue
		}
		for _, c := range b.Certs {
			dto.Certificates = append(dto.Certificates, CACertificateDTO{
				Subject:   c.Subject,
				Issuer:    c.Issuer,
				NotBefore: c.NotBefore.UTC().Format(time.RFC3339),
				NotAfter:  c.NotAfter.UTC().Format(time.RFC3339),
				Expired:   now.After(c.NotAfter),
				IsCA:      c.IsCA,
				SHA256:    c.SHA256,
			})
		}
		for _, w := range b.Warnings {
			dto.Warnings = append(dto.Warnings, CABundleWarningDTO{Code: w.Code, Message: w.Message, Hint: w.Hint})
		}
		out = append(out, dto)
	}
	return out
}
