package settings

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"slices"
	"strconv"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/storage/stablejson"
)

// The admin policy file (SET-02): an optional JSON file in a system-wide
// location that a normal user cannot write, which locks settings to fixed
// values. docs/settings.md, "Settings locked by your administrator",
// describes it for admins.
//
//	{
//	  "version": 1,
//	  "locked": {
//	    "network.verifyTls": true,
//	    "network.caBundles": ["/etc/ssl/corp-root.pem"]
//	  },
//	  "requireTlsVerification": true
//	}
//
// "locked" maps a setting key, "<section>.<field>" as in settings.json, to
// the value it always has. Any key the settings file knows can be locked,
// with the same value types; a setting added later is lockable without
// changes here, since the value is checked by the settings file's own
// decoder. "requireTlsVerification" also forbids turning certificate
// verification off per request or connection (HTTP, GraphQL, SSE,
// WebSocket, Socket.IO, gRPC, TCP); it locks network.verifyTls to true.
//
// There is deliberately no environment variable or command-line flag
// pointing elsewhere: either would let a user start PostSonde without the
// policy. Tests pass a path to LoadPolicy instead.

// PolicyFileName is the name of the admin policy file.
const PolicyFileName = "policy.json"

// PolicyVersion is the policy file schema version this build reads.
const PolicyVersion = 1

// Error codes of the admin policy.
const (
	// CodeLocked: a change to a setting the admin policy locks (kind
	// permission_denied). Details: field.
	CodeLocked = "settings.locked"
	// CodePolicyInvalid: the policy file, or one of its entries, cannot
	// be used (kind parse, invalid_argument, unsupported, or the read
	// error's). Details: path, and field for an entry.
	CodePolicyInvalid = "settings.policy_invalid"
	// CodeTLSVerificationRequired: a request or connection turns
	// certificate verification off while the policy requires it (kind
	// permission_denied). Details: protocol.
	CodeTLSVerificationRequired = "settings.tls_verification_required"
)

// KeyVerifyTLS is the setting key requireTlsVerification locks to true.
const KeyVerifyTLS = "network.verifyTls"

// Policy is the admin policy in effect. The zero value is no policy: it
// locks nothing and requires nothing.
type Policy struct {
	// Path is the policy file that was looked for; "" when none was.
	Path string
	// Exists is true when the file was there (even if it could not be
	// used).
	Exists bool
	// Locked are the locks that are applied, by setting key, as compact
	// JSON values. Entries that could not be used are not here; Problems
	// lists them.
	Locked map[string]json.RawMessage
	// RequireTLSVerification forbids turning certificate verification off
	// for a request or connection (TLSVerificationRequired).
	RequireTLSVerification bool
	// Problems are what is wrong with the file: it cannot be read or
	// parsed (then nothing is applied), is from a newer version, or has
	// entries that are not used.
	Problems []*errs.Error
}

// Active reports whether the policy locks or requires anything.
func (p Policy) Active() bool {
	return len(p.Locked) > 0 || p.RequireTLSVerification
}

// IsLocked reports whether the setting key ("network.verifyTls") is
// locked, by itself or with the member it is in ("shortcuts" locks
// "shortcuts.request.send").
func (p Policy) IsLocked(key string) bool {
	for k := range p.Locked {
		if k == key || strings.HasPrefix(key, k+".") {
			return true
		}
	}
	return false
}

// LockedKeys returns the locked setting keys, sorted.
func (p Policy) LockedKeys() []string {
	return slices.Sorted(maps.Keys(p.Locked))
}

// Enforce returns s with every locked setting set to its locked value.
func (p Policy) Enforce(s Settings) Settings {
	if len(p.Locked) == 0 {
		return s
	}
	out, _, err := overlay(s, p.Locked)
	if err != nil {
		// Cannot happen: every lock was checked by LoadPolicy.
		return s
	}
	return out
}

// TLSVerificationRequired returns the error for a request or connection
// of protocol ("HTTP", "WebSocket", …) that turns certificate
// verification off, or nil when the policy allows it (or insecure is
// false).
func (p Policy) TLSVerificationRequired(insecure bool, protocol string) error {
	if !insecure || !p.RequireTLSVerification {
		return nil
	}
	return TLSVerificationRequiredError(protocol)
}

// TLSVerificationRequiredError is the CodeTLSVerificationRequired error
// for protocol.
func TLSVerificationRequiredError(protocol string) *errs.Error {
	return errs.New(errs.PermissionDenied,
		fmt.Sprintf("Your administrator requires TLS certificate verification, so this %s request cannot turn it off.", protocol)).
		WithCode(CodeTLSVerificationRequired).
		WithHint("Turn certificate verification back on in the request's settings. To trust a private CA, add it under Settings, Network, Custom CA certificates.").
		WithDetail("protocol", protocol)
}

// CheckLocked returns the CodeLocked error when the policy locks key, or
// nil. Services call it before asking the user for a new value (a file
// dialog) that could not be saved anyway.
func (p Policy) CheckLocked(key string) error {
	if p.IsLocked(key) {
		return lockedError(key)
	}
	return nil
}

// lockedError is the CodeLocked error for key.
func lockedError(key string) *errs.Error {
	return errs.New(errs.PermissionDenied, fmt.Sprintf("The setting %q is set by your administrator and cannot be changed.", key)).
		WithCode(CodeLocked).
		WithHint("Ask your administrator to change the policy file if you need a different value.").
		WithDetail("field", key)
}

// rawPolicy is the policy file's known members.
type rawPolicy struct {
	Version                json.RawMessage `json:"version"`
	Locked                 json.RawMessage `json:"locked"`
	RequireTLSVerification json.RawMessage `json:"requireTlsVerification"`
}

// LoadPolicy reads the policy file at path. A missing file, or an empty
// path, is no policy. It never fails; what cannot be used is in Problems:
//
//   - A file that cannot be read or is not a JSON object applies nothing
//     (PostSonde still starts; the settings screen and the log say why).
//     Only an admin can write the file, so a broken one is an admin's
//     mistake, not a way for a user to get around it.
//   - A file from a newer version applies the entries this version
//     understands, with a problem.
//   - An entry for an unknown setting, with an invalid value, or an
//     unknown top-level member is skipped with a problem; the rest apply.
func LoadPolicy(path string) Policy {
	p := Policy{Path: path}
	if path == "" {
		return p
	}
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return p
	}
	p.Exists = true
	if err != nil {
		p.Problems = append(p.Problems, policyProblem(errs.Wrap(err, errs.KindOf(err),
			"The administrator policy file could not be read, so no setting is locked."), path).
			WithHint("The file must be readable by every user. Ask your administrator to check its permissions."))
		return p
	}
	return parsePolicy(path, data)
}

// parsePolicy decodes a policy file's content (path is for messages).
func parsePolicy(path string, data []byte) Policy {
	p := Policy{Path: path, Exists: true}
	broken := func(cause error) Policy {
		if cause == nil {
			cause = errors.New("settings: the policy file is not a JSON object")
		}
		p.Problems = append(p.Problems, policyProblem(errs.Wrap(cause, errs.Parse,
			"The administrator policy file is not a valid JSON object, so no setting is locked."), path).
			WithHint("Ask your administrator to fix "+PolicyFileName+"."))
		return p
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil || top == nil {
		return broken(err)
	}
	var head rawPolicy
	extra, err := stablejson.SplitUnknown(data, &head)
	if err != nil {
		return broken(err)
	}

	version, problem := parseVersion(head.Version)
	if problem != nil {
		p.Problems = append(p.Problems, policyEntryProblem(path, "version", problem.Message))
	}
	if version > PolicyVersion {
		p.Problems = append(p.Problems, policyProblem(errs.New(errs.Unsupported,
			fmt.Sprintf("The administrator policy file is from a newer PostSonde (policy version %d; this version supports %d). The entries this version understands are applied.", version, PolicyVersion)), path).
			WithDetail("version", strconv.Itoa(version)))
	}
	for _, key := range slices.Sorted(maps.Keys(extra)) {
		p.Problems = append(p.Problems, policyEntryProblem(path, key, fmt.Sprintf("The administrator policy member %q is not known to this version of PostSonde and is ignored.", key)))
	}

	if !isAbsent(head.RequireTLSVerification) {
		if err := json.Unmarshal(head.RequireTLSVerification, &p.RequireTLSVerification); err != nil {
			p.Problems = append(p.Problems, policyEntryProblem(path, "requireTlsVerification",
				fmt.Sprintf("The administrator policy member %q must be true or false; it is ignored.", "requireTlsVerification")))
		}
	}

	var locked map[string]json.RawMessage
	if !isAbsent(head.Locked) {
		if err := json.Unmarshal(head.Locked, &locked); err != nil {
			p.Problems = append(p.Problems, policyEntryProblem(path, "locked",
				`The administrator policy member "locked" must be an object of setting keys and values; no setting is locked by it.`))
			locked = nil
		}
	}
	if p.RequireTLSVerification {
		if v, ok := locked[KeyVerifyTLS]; ok && !bytes.Equal(bytes.TrimSpace(v), []byte("true")) {
			p.Problems = append(p.Problems, policyEntryProblem(path, KeyVerifyTLS,
				fmt.Sprintf("The administrator policy locks %q to %s but also requires TLS verification; it is locked to true.", KeyVerifyTLS, compactJSON(v))))
		}
		if locked == nil {
			locked = map[string]json.RawMessage{}
		}
		locked[KeyVerifyTLS] = json.RawMessage("true")
	}

	for _, key := range slices.Sorted(maps.Keys(locked)) {
		value := locked[key]
		if msg := checkLock(key, value); msg != "" {
			p.Problems = append(p.Problems, policyEntryProblem(path, key, msg))
			continue
		}
		if p.Locked == nil {
			p.Locked = map[string]json.RawMessage{}
		}
		p.Locked[key] = json.RawMessage(compactJSON(value))
	}
	return p
}

// checkLock returns why the lock of key to value cannot be used, or "".
// The value is decoded by the settings file's own decoder, so any setting
// it knows can be locked: a member of a section ("network.verifyTls"), a
// top-level member ("shortcuts"), or one entry of a map member
// ("shortcuts.request.send").
func checkLock(key string, value json.RawMessage) string {
	section, field, dotted := strings.Cut(key, ".")
	if section == "" || (dotted && field == "") || key == "version" {
		return fmt.Sprintf(`The administrator policy key %q is not a setting key (such as %q); it is ignored.`, key, KeyVerifyTLS)
	}
	if isAbsent(value) {
		return fmt.Sprintf("The administrator policy locks %q to null; it is ignored.", key)
	}
	_, report, err := overlay(Defaults(), map[string]json.RawMessage{key: value})
	if err != nil {
		return fmt.Sprintf("The administrator policy value of %q cannot be used; it is ignored.", key)
	}
	_, unknownTop := report.Unknown[RootSection][section]
	_, unknownField := report.Unknown[section][field]
	if unknownTop || (dotted && unknownField) {
		return fmt.Sprintf("The administrator policy locks %q, which is not a setting of this version of PostSonde; it is ignored.", key)
	}
	for _, pr := range report.Problems {
		if f := pr.Details["field"]; f == key || f == section || strings.HasPrefix(f, key+".") {
			return fmt.Sprintf("The administrator policy value of %q is invalid (expected %s); it is ignored.", key, pr.Details["expected"])
		}
	}
	return ""
}

// overlay returns s with the members in values set, by setting key: a
// top-level member ("shortcuts") or a member of an object member
// ("network.verifyTls"; "shortcuts.request.send", split at the first
// dot). Top-level members are set first, so a lock of one entry wins over
// a lock of its whole member. A nil value removes the member, so it has
// its default. The result is decoded as the settings file is, with its
// report.
func overlay(s Settings, values map[string]json.RawMessage) (Settings, Report, error) {
	data, err := Marshal(s, nil)
	if err != nil {
		return s, Report{}, err
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return s, Report{}, err
	}
	keys := slices.Sorted(maps.Keys(values))
	for _, key := range keys {
		if strings.Contains(key, ".") {
			continue
		}
		if v := values[key]; v == nil {
			delete(top, key)
		} else {
			top[key] = v
		}
	}
	for _, key := range keys {
		section, field, dotted := strings.Cut(key, ".")
		if !dotted {
			continue
		}
		m := map[string]json.RawMessage{}
		if raw := top[section]; !isAbsent(raw) {
			if err := json.Unmarshal(raw, &m); err != nil {
				return s, Report{}, err
			}
		}
		if v := values[key]; v == nil {
			delete(m, field)
		} else {
			m[field] = v
		}
		raw, err := json.Marshal(m)
		if err != nil {
			return s, Report{}, err
		}
		top[section] = raw
	}
	if data, err = json.Marshal(top); err != nil {
		return s, Report{}, err
	}
	return parse("", data)
}

// fieldValues returns the members of s at the setting keys, as compact
// JSON; a member the file would leave out is nil.
func fieldValues(s Settings, keys []string) (map[string]json.RawMessage, error) {
	data, err := Marshal(s, nil)
	if err != nil {
		return nil, err
	}
	var top map[string]json.RawMessage
	if err := json.Unmarshal(data, &top); err != nil {
		return nil, err
	}
	out := make(map[string]json.RawMessage, len(keys))
	for _, key := range keys {
		out[key] = nil
		section, field, dotted := strings.Cut(key, ".")
		if !dotted {
			if v, ok := top[key]; ok {
				out[key] = json.RawMessage(compactJSON(v))
			}
			continue
		}
		var m map[string]json.RawMessage
		if raw := top[section]; !isAbsent(raw) {
			if err := json.Unmarshal(raw, &m); err != nil {
				return nil, err
			}
		}
		if v, ok := m[field]; ok {
			out[key] = json.RawMessage(compactJSON(v))
		}
	}
	return out, nil
}

// checkLocks returns the CodeLocked error for the first locked key whose
// value in next is not the locked one, or nil.
func (p Policy) checkLocks(next Settings) error {
	for _, key := range p.LockedKeys() {
		want, _, err := overlay(next, map[string]json.RawMessage{key: p.Locked[key]})
		if err != nil {
			return err
		}
		if !want.Equal(next) {
			return lockedError(key)
		}
	}
	return nil
}

func policyProblem(e *errs.Error, path string) *errs.Error {
	return e.WithCode(CodePolicyInvalid).WithDetail("path", path)
}

func policyEntryProblem(path, field, msg string) *errs.Error {
	return errs.New(errs.InvalidArgument, msg).
		WithCode(CodePolicyInvalid).
		WithHint("Ask your administrator to fix "+PolicyFileName+".").
		WithDetail("path", path).
		WithDetail("field", field)
}
