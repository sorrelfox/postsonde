package requestsecrets

import (
	"maps"
	"reflect"
	"slices"
	"strings"

	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// Secret variables (SEC-04, PRV-09). A variable marked secret in a
// folder's _folder.json or in postsonde.json (the global variables) is
// written with "value": ""; its value goes to the item's side file, keyed
// "var.<name>", next to the item's auth secret:
//
//	<local>/secrets/requests/<folder id>.json      {"values": {"auth.token": …, "var.apiKey": …}}
//	<local>/secrets/requests/<workspace id>.json   {"values": {"var.apiKey": …}}
//
// Values are stored by name, so two secret variables of the same name in
// one file share one value. Unlike an auth secret, a secret variable's
// value is always local, even a {{reference}}: the flag says the value
// never goes to the project.
//
// A value typed into the project file by hand (a plaintext secret) is
// read as it is. The local value wins when there is one (MergeVariables),
// as for an environment's secret variables, so what the editor shows is
// what is sent; the next write of the file moves the file's value to the
// side file (Store.MoveOut), unless a local value is already there, and
// writes "" to the project.

// VarKeyPrefix prefixes the secret variables in a side file's values:
// "var.apiKey".
const VarKeyPrefix = "var."

// SplitVariables separates the values of vars's secret variables from what
// may be written to a project file. It returns a copy of vars with every
// secret value "" and the values for the side file, keyed "var.<name>"
// (nil when there are none). A later variable of the same name wins, as in
// a scope. Variables that are not secret are returned as they are.
func SplitVariables(vars []projectfile.Variable) ([]projectfile.Variable, map[string]string) {
	var values map[string]string
	var out []projectfile.Variable
	for i, v := range vars {
		if !v.Secret || v.Value == "" {
			continue
		}
		if out == nil {
			out = slices.Clone(vars)
		}
		if values == nil {
			values = map[string]string{}
		}
		values[VarKeyPrefix+v.Name] = v.Value
		out[i].Value = ""
	}
	if out == nil {
		return vars, nil
	}
	return out, values
}

// MergeVariables returns vars with the local values of its secret
// variables put in: values["var.<name>"] when there is one, else the value
// the file has (typed into it by hand). vars is not changed.
func MergeVariables(vars []projectfile.Variable, values map[string]string) []projectfile.Variable {
	var out []projectfile.Variable
	for i, v := range vars {
		if !v.Secret {
			continue
		}
		lv, ok := values[VarKeyPrefix+v.Name]
		if !ok || lv == v.Value {
			continue
		}
		if out == nil {
			out = slices.Clone(vars)
		}
		out[i].Value = lv
	}
	if out == nil {
		return vars
	}
	return out
}

// HasSecretVariables reports whether vars has a variable marked secret.
func HasSecretVariables(vars []projectfile.Variable) bool {
	return slices.ContainsFunc(vars, func(v projectfile.Variable) bool { return v.Secret })
}

// SplitFile is Split, SplitProxy and SplitVariables for a whole project
// file: the auth of an HTTP, GraphQL, SSE, WebSocket, Socket.IO or gRPC
// request or a folder, the password of any request's own proxy, and the
// variables of a folder or the workspace file. It returns a copy of f as
// it may be written to the project and the side file's values (nil when
// there are none). Other files, and f without secrets, are returned as
// they are.
func SplitFile(f projectfile.File) (projectfile.File, map[string]string) {
	var values map[string]string
	add := func(m map[string]string) {
		if len(m) == 0 {
			return
		}
		if values == nil {
			values = map[string]string{}
		}
		maps.Copy(values, m)
	}
	var out projectfile.File
	switch r := f.(type) {
	case *projectfile.HTTPRequest:
		c := *r
		var m map[string]string
		c.Auth, m = Split(r.Auth)
		add(m)
		out = &c
	case *projectfile.GraphQLRequest:
		c := *r
		var m map[string]string
		c.Auth, m = Split(r.Auth)
		add(m)
		out = &c
	case *projectfile.SSERequest:
		c := *r
		var m map[string]string
		c.Auth, m = Split(r.Auth)
		add(m)
		out = &c
	case *projectfile.WebSocketRequest:
		c := *r
		var m map[string]string
		c.Auth, m = Split(r.Auth)
		add(m)
		out = &c
	case *projectfile.SocketIORequest:
		c := *r
		var m map[string]string
		c.Auth, m = Split(r.Auth)
		add(m)
		out = &c
	case *projectfile.GRPCRequest:
		c := *r
		var m map[string]string
		c.Auth, m = Split(r.Auth)
		add(m)
		out = &c
	case *projectfile.TCPRequest:
		if proxyUnchanged(r.Settings.Proxy) {
			return f, nil
		}
		c := *r
		out = &c
	case *projectfile.UDPRequest:
		if proxyUnchanged(r.Settings.Proxy) {
			return f, nil
		}
		c := *r
		out = &c
	case *projectfile.Folder:
		c := *r
		var m map[string]string
		c.Auth, m = Split(r.Auth)
		add(m)
		c.Variables, m = SplitVariables(r.Variables)
		add(m)
		return &c, values
	case *projectfile.Workspace:
		c := *r
		var m map[string]string
		c.Variables, m = SplitVariables(r.Variables)
		add(m)
		return &c, values
	default:
		return f, nil
	}
	if p := projectfile.ProxyOf(out); p != nil {
		var m map[string]string
		*p, m = SplitProxy(*p)
		add(m)
	}
	return out, values
}

// proxyUnchanged reports whether SplitProxy leaves p as it is: a request
// of a kind without auth needs no copy then.
func proxyUnchanged(p projectfile.RequestProxy) bool {
	out, values := SplitProxy(p)
	return len(values) == 0 && reflect.DeepEqual(out, p)
}

// MergeFile puts the local values back into f, the inverse of SplitFile:
// its auth secret (Merge), its own proxy's password (MergeProxy) and its
// secret variables (MergeVariables). It returns a copy when something
// changed.
func MergeFile(f projectfile.File, values map[string]string) projectfile.File {
	var out projectfile.File
	switch r := f.(type) {
	case *projectfile.HTTPRequest:
		c := *r
		c.Auth = Merge(r.Auth, values)
		out = &c
	case *projectfile.GraphQLRequest:
		c := *r
		c.Auth = Merge(r.Auth, values)
		out = &c
	case *projectfile.SSERequest:
		c := *r
		c.Auth = Merge(r.Auth, values)
		out = &c
	case *projectfile.WebSocketRequest:
		c := *r
		c.Auth = Merge(r.Auth, values)
		out = &c
	case *projectfile.SocketIORequest:
		c := *r
		c.Auth = Merge(r.Auth, values)
		out = &c
	case *projectfile.GRPCRequest:
		c := *r
		c.Auth = Merge(r.Auth, values)
		out = &c
	case *projectfile.TCPRequest:
		c := *r
		out = &c
	case *projectfile.UDPRequest:
		c := *r
		out = &c
	case *projectfile.Folder:
		c := *r
		c.Auth = Merge(r.Auth, values)
		c.Variables = MergeVariables(r.Variables, values)
		return &c
	case *projectfile.Workspace:
		c := *r
		c.Variables = MergeVariables(r.Variables, values)
		return &c
	default:
		return f
	}
	if p := projectfile.ProxyOf(out); p != nil {
		*p = MergeProxy(*p, values)
	}
	return out
}

// NeedsLocal reports whether f has something its side file may hold: an
// auth secret or a proxy password marked local, or a secret variable.
// Reading the side file of any other file is not needed.
func NeedsLocal(f projectfile.File) bool {
	if p := projectfile.ProxyOf(f); p != nil && p.PasswordIsLocal() {
		return true
	}
	switch r := f.(type) {
	case *projectfile.HTTPRequest:
		return r.Auth.SecretIsLocal()
	case *projectfile.GraphQLRequest:
		return r.Auth.SecretIsLocal()
	case *projectfile.SSERequest:
		return r.Auth.SecretIsLocal()
	case *projectfile.WebSocketRequest:
		return r.Auth.SecretIsLocal()
	case *projectfile.SocketIORequest:
		return r.Auth.SecretIsLocal()
	case *projectfile.GRPCRequest:
		return r.Auth.SecretIsLocal()
	case *projectfile.Folder:
		return r.Auth.SecretIsLocal() || HasSecretVariables(r.Variables)
	case *projectfile.Workspace:
		return HasSecretVariables(r.Variables)
	}
	return false
}

// Update adds values to the side file of item id, keeping the values it
// has under other keys. Nothing is written when values is empty.
func (s *Store) Update(id string, values map[string]string) error {
	if len(values) == 0 {
		return nil
	}
	current, err := s.Read(id)
	if err != nil {
		return err
	}
	maps.Copy(current, values)
	return s.Write(id, current)
}

// MoveOut is the migration of plaintext secrets: it returns f (the file of
// item id) as it may be written to the project, after moving the secrets
// typed into it to id's side file. Every writer of project files calls it
// (the collection's Tree does, see KeepSecretsLocal), so a secret typed
// into a project file by hand leaves the file the next time PostSonde
// writes it, and nothing PostSonde writes to a project holds one.
//
// It never drops a value that is in use: a typed auth secret replaces the
// side file's (the file's own value is what was shown and sent), a secret
// variable's value is stored only when the side file has none for that
// name (the local value wins, MergeVariables). The side file is written
// before f is returned; when it cannot be read or written, MoveOut fails
// and the caller must not write f, so the secret stays where it was.
func (s *Store) MoveOut(id string, f projectfile.File) (projectfile.File, error) {
	out, values := SplitFile(f)
	if len(values) == 0 {
		return out, nil
	}
	current, err := s.Read(id)
	if err != nil {
		return nil, err
	}
	changed := false
	for k, v := range values {
		if _, ok := current[k]; ok && strings.HasPrefix(k, VarKeyPrefix) {
			continue
		}
		if current[k] != v {
			current[k] = v
			changed = true
		}
	}
	if changed {
		if err := s.Write(id, current); err != nil {
			return nil, err
		}
	}
	return out, nil
}
