package grpc

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"net"
	"strconv"
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/app/operation"
	"github.com/sorrelfox/postsonde/internal/app/streamcheck"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/fileaccess"
	"github.com/sorrelfox/postsonde/internal/core/grpc/dynamic"
	"github.com/sorrelfox/postsonde/internal/core/grpc/proto/desc"
	"github.com/sorrelfox/postsonde/internal/core/grpc/protojson"
	"github.com/sorrelfox/postsonde/internal/core/grpc/transport"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/netx"
	coresettings "github.com/sorrelfox/postsonde/internal/core/settings"
)

// Error codes of Invoke (docs/grpc.md). Transport errors keep their
// grpctransport.* codes.
const (
	// CodeVariablesUnsupported: {{variables}} in the address, the message
	// or a metadata row, which are not resolved yet.
	CodeVariablesUnsupported = "grpc.variables_unsupported"
	// CodeNoMethod: no service or method chosen.
	CodeNoMethod = "grpc.no_method"
	// CodeNoProto: the setup has no .proto files.
	CodeNoProto = "grpc.no_proto"
	// CodeMethodNotFound: the service or method is not in the loaded files.
	CodeMethodNotFound = "grpc.method_not_found"
	// CodeUnresolvedType: the method's input or output type did not
	// resolve.
	CodeUnresolvedType = "grpc.unresolved_type"
	// CodeCallNotRunning: SendMessage or CloseSend for a call that has
	// ended or never existed.
	CodeCallNotRunning = "grpc.call_not_running"
	// CodeNotClientStreaming: SendMessage or CloseSend for a call whose
	// method does not stream from the client (unary or server streaming).
	CodeNotClientStreaming = "grpc.not_client_streaming"
	// CodeSendClosed: SendMessage after CloseSend.
	CodeSendClosed = "grpc.send_closed"
	// CodeInvalidMessage: the message JSON does not fit the input type.
	CodeInvalidMessage = "grpc.invalid_message"
	// CodeInvalidBinaryMetadata: a "-bin" value that is not Base64.
	CodeInvalidBinaryMetadata = "grpc.invalid_binary_metadata"
	// CodeInvalidSettings: a negative deadline or connect timeout.
	CodeInvalidSettings = "grpc.invalid_settings"
	// CodeUnavailable: the service was built without what calls need.
	CodeUnavailable = "grpc.unavailable"
	// CodeWebClientStreaming: a client-streaming or bidi method called over
	// gRPC-Web, which is half-duplex.
	CodeWebClientStreaming = "grpc.web_client_streaming"
)

// callBatch caps how many call events one data event carries: response
// messages can be large (docs/events.md, Batching).
const callBatch = 64

// clientKey tells transport clients apart: they differ in the settings
// that belong to the connection, not the call.
type clientKey struct {
	insecure bool
	// connect is the connect timeout in ms, -1 for the defaults.
	connect int64
	// roots are the trusted CAs of TLS calls: nil for the system's, or
	// the pool with the custom CA bundles (NET-04).
	roots *x509.CertPool
	// cert is the client certificate of TLS calls (AUTH-30), nil for none.
	cert *httpclient.ClientCert
	// certKey is cert's certificate, which tells the clients apart (the
	// same while the certificate is unchanged).
	certKey *tls.Certificate
}

// Invoke starts a call of any kind and returns its operation ID at once.
// What happens arrives as grpc:op:data items (CallEventDTO: the request as
// sent, the response headers, each response message, and the status with
// the trailers), then grpc:op:done: "ok" for an OK status, "error" with
// the transport error for any other status or a failure, "cancelled"
// after OpsService.Cancel.
//
// A unary or server-streaming call sends in.Message and half-closes at
// once. A client-streaming or bidi call (GRPC-13, GRPC-14) only opens the
// stream: in.Message is not sent, and the messages are sent one at a time
// with SendMessage and the request stream ended with CloseSend; each
// becomes a "sent" or "half-close" item in order with the received ones
// (GRPC-22).
//
// It fails at once, without starting anything, for input that cannot be
// sent: {{variables}}, no method, a method that is not in the loaded
// .proto files, a message that does not fit the input type, metadata that
// is not valid, and an invalid address.
func (s *GRPCService) Invoke(ctx context.Context, in InvokeInputDTO) (operation.StartedDTO, error) {
	if err := ctx.Err(); err != nil {
		return operation.StartedDTO{}, errs.Wrap(err, errs.KindOf(err), "Starting the call was interrupted.")
	}
	if s.ops == nil || s.dialer == nil {
		return operation.StartedDTO{}, errs.New(errs.Unsupported, "Calling gRPC methods is not available.").WithCode(CodeUnavailable)
	}
	if err := checkVariables(in); err != nil {
		return operation.StartedDTO{}, err
	}
	protocol, compression, err := callProtocol(in)
	if err != nil {
		return operation.StartedDTO{}, err
	}
	pool, m, err := s.findMethod(ctx, in.Files, in.ImportPaths, in.Service, in.Method)
	if err != nil {
		return operation.StartedDTO{}, err
	}
	if protocol.Web() && m.ClientStreaming {
		return operation.StartedDTO{}, errs.New(errs.Unsupported, "gRPC-Web cannot stream from the client, so "+methodKindText(m)+" methods cannot be called over it.").
			WithCode(CodeWebClientStreaming).WithDetail("field", "transport").
			WithHint("Switch the transport to gRPC (HTTP/2) in the Settings tab, or call a unary or server-streaming method.")
	}
	var req []byte
	var reqJSON string
	if !m.ClientStreaming {
		// Streaming calls send their messages with SendMessage.
		if err := checkMessageVariables(in.Message); err != nil {
			return operation.StartedDTO{}, err
		}
		if req, reqJSON, err = encodeRequest(pool, m, in.Message); err != nil {
			return operation.StartedDTO{}, err
		}
	}
	md, err := toMetadata(in.Metadata)
	if err != nil {
		return operation.StartedDTO{}, err
	}
	md, masker, err := s.callAuth(ctx, in, md)
	if err != nil {
		return operation.StartedDTO{}, err
	}
	var deadline time.Duration
	if in.DeadlineMs != nil {
		if *in.DeadlineMs < 0 {
			return operation.StartedDTO{}, invalidSetting("deadlineMs", "The deadline must be 0 (none) or more milliseconds.")
		}
		deadline = time.Duration(*in.DeadlineMs) * time.Millisecond
	}
	key := clientKey{insecure: in.VerifyTLS != nil && !*in.VerifyTLS, connect: -1}
	if in.TLS && key.insecure && s.requireTLS {
		return operation.StartedDTO{}, coresettings.TLSVerificationRequiredError("gRPC")
	}
	if in.ConnectTimeoutMs != nil {
		if *in.ConnectTimeoutMs < 0 {
			return operation.StartedDTO{}, invalidSetting("connectTimeoutMs", "The connect timeout must be 0 (no limit) or more milliseconds.")
		}
		key.connect = *in.ConnectTimeoutMs
	}
	if in.TLS && !key.insecure && s.roots != nil {
		// Plaintext (h2c) calls do not use the CA bundles, so a broken
		// one only stops TLS calls.
		roots, err := s.roots()
		if err != nil {
			return operation.StartedDTO{}, err
		}
		key.roots = roots
	}

	if in.TLS && s.clientCerts != nil {
		cert, err := s.clientCerts(callAddress(strings.TrimSpace(in.Address)))
		if err != nil {
			return operation.StartedDTO{}, err
		}
		if cert != nil && cert.Certificate != nil {
			key.cert, key.certKey = cert, cert.Certificate
		}
	}

	path := "/" + m.Service.FullName + "/" + m.Name
	opts := transport.CallOptions{
		Address:     strings.TrimSpace(in.Address),
		Protocol:    protocol,
		TLS:         in.TLS,
		Method:      path,
		Metadata:    md,
		Timeout:     deadline,
		Compression: compression,
	}
	// The call runs on a context of its own, so Invoke can check the
	// options (and fail the bound call) before the operation starts; the
	// operation's cancellation closes the stream. The connection is dialed
	// at once, so the context is marked as the user's call here, before
	// the operation (and its ID) exists (PRV-05).
	callCtx := netx.WithUserOp(context.Background(), netx.UserOp{Kind: Domain + ".call", ID: in.RequestID})
	stream, err := s.client(key).Invoke(callCtx, opts)
	if err != nil {
		return operation.StartedDTO{}, err
	}
	c := &call{
		stream:          stream,
		pool:            pool,
		method:          m,
		path:            path,
		md:              md,
		masker:          masker,
		req:             req,
		reqJSON:         reqJSON,
		deadlineMs:      deadline.Milliseconds(),
		transport:       string(protocol),
		compression:     compression,
		single:          !m.ServerStreaming,
		clientStreaming: m.ClientStreaming,
		opened:          make(chan struct{}),
		onEnd:           s.forget,
		requestID:       in.RequestID,
		checks:          s.checks,
		log:             streamcheck.NewLog(time.Now()),
	}
	spec := operation.Spec{Domain: Domain, Kind: "call", MaxItems: callBatch}
	started, err := operation.Start(s.ops, spec, c.run)
	if err != nil {
		stream.Close()
		return operation.StartedDTO{}, errs.Wrap(err, errs.Internal, "The call could not be started.")
	}
	s.remember(started.OpID, c)
	s.logger.Debug("grpc call started", "opId", started.OpID, "requestId", in.RequestID, "method", path)
	return started, nil
}

// callProtocol checks the transport and compression of in and returns
// them with the defaults filled in.
func callProtocol(in InvokeInputDTO) (transport.Protocol, string, error) {
	p := transport.Protocol(strings.TrimSpace(in.Transport))
	switch p {
	case "":
		p = transport.ProtocolGRPC
	case transport.ProtocolGRPC, transport.ProtocolGRPCWeb, transport.ProtocolGRPCWebText:
	default:
		return "", "", invalidSetting("transport", "The transport "+strconv.Quote(in.Transport)+" is not supported; use grpc, grpc-web or grpc-web-text.")
	}
	c := strings.TrimSpace(in.Compression)
	switch c {
	case "":
		c = transport.CompressionIdentity
	case transport.CompressionIdentity, transport.CompressionGzip:
	default:
		return "", "", invalidSetting("compression", "The compression "+strconv.Quote(in.Compression)+" is not supported; use gzip or identity (none).")
	}
	return p, c, nil
}

// methodKindText names a method's call type for messages.
func methodKindText(m *desc.Method) string {
	switch {
	case m.ClientStreaming && m.ServerStreaming:
		return "bidirectional streaming"
	case m.ClientStreaming:
		return "client-streaming"
	case m.ServerStreaming:
		return "server-streaming"
	}
	return "unary"
}

func invalidSetting(field, msg string) error {
	return errs.New(errs.InvalidArgument, msg).WithCode(CodeInvalidSettings).WithDetail("field", field)
}

// checkVariables refuses {{variables}} in the address and the metadata,
// which are not resolved yet. The message is checked where it is sent
// (checkMessageVariables).
func checkVariables(in InvokeInputDTO) error {
	where := ""
	switch {
	case strings.Contains(in.Address, "{{"):
		where = "the address"
	default:
		for _, r := range in.Metadata {
			if r.Enabled && (strings.Contains(r.Name, "{{") || strings.Contains(r.Value, "{{")) {
				where = "the metadata " + strings.TrimSpace(r.Name)
				break
			}
		}
	}
	if where == "" {
		return nil
	}
	return variablesErr(where, "Write the value directly, e.g. localhost:50051.")
}

// checkMessageVariables refuses {{variables}} in a message.
func checkMessageVariables(message string) error {
	if !strings.Contains(message, "{{") {
		return nil
	}
	return variablesErr("the message", "Write the value directly.")
}

func variablesErr(where, hint string) error {
	return errs.New(errs.Unsupported, "Variables in "+where+" are not supported yet.").
		WithCode(CodeVariablesUnsupported).WithHint(hint)
}

// findMethod links the setup (cached, as Describe) and finds the method,
// whose input and output types must have resolved.
func (s *GRPCService) findMethod(ctx context.Context, files, importPaths []string, service, method string) (*desc.Pool, *desc.Method, error) {
	if strings.TrimSpace(service) == "" || strings.TrimSpace(method) == "" {
		return nil, nil, errs.New(errs.InvalidArgument, "Choose a service and method to call.").WithCode(CodeNoMethod)
	}
	res, _, extra, err := s.link(ctx, files, importPaths, false)
	if err != nil {
		return nil, nil, err
	}
	// A file or import path outside the allowed directories refuses the
	// call (PRV-10), whatever else linked.
	for _, d := range extra {
		if d.Code == fileaccess.CodeNotAllowed {
			return nil, nil, errs.New(errs.PermissionDenied, d.Message).
				WithCode(fileaccess.CodeNotAllowed).
				WithHint(fileaccess.HintAllow).
				WithDetail("path", d.File)
		}
	}
	if res == nil {
		return nil, nil, errs.New(errs.InvalidArgument, "The request has no .proto files that describe the method.").
			WithCode(CodeNoProto).WithHint("Add .proto files in the Proto tab.")
	}
	name := service + "/" + method
	notFound := func() error {
		return errs.New(errs.NotFound, name+" is not in the loaded .proto files.").
			WithCode(CodeMethodNotFound).WithDetail("method", name).
			WithHint("Check the proto setup and its problems in the Proto tab, or choose another method.")
	}
	svc := res.Pool.FindService(service)
	if svc == nil {
		return nil, nil, notFound()
	}
	m := svc.MethodByName(method)
	if m == nil {
		return nil, nil, notFound()
	}
	if m.Input == nil || m.Output == nil {
		return nil, nil, errs.New(errs.InvalidArgument, "The input or output type of "+name+" is not defined in the loaded .proto files.").
			WithCode(CodeUnresolvedType).WithDetail("method", name).
			WithHint("Fix the problems shown in the Proto tab.")
	}
	return res.Pool, m, nil
}

// encodeRequest parses the message JSON into the input type and returns
// its binary encoding and the canonical JSON of what is sent.
func encodeRequest(pool *desc.Pool, m *desc.Method, text string) ([]byte, string, error) {
	msg, err := parseMessage(pool, m.Input, text)
	if err != nil {
		e := errs.Wrap(err, errs.InvalidArgument, "The message is not a valid "+m.Input.FullName+": "+jsonMessage(err)).
			WithCode(CodeInvalidMessage).WithDetail("type", m.Input.FullName)
		if je, ok := errors.AsType[*protojson.Error](err); ok && je.Offset >= 0 {
			e = e.WithDetail("line", strconv.Itoa(je.Line)).WithDetail("column", strconv.Itoa(je.Column)).
				WithDetail("offset", strconv.Itoa(je.Offset))
		}
		return nil, "", e
	}
	b, err := dynamic.Marshal(msg)
	if err != nil {
		return nil, "", errs.Wrap(err, errs.InvalidArgument, "The message could not be encoded: "+err.Error()).
			WithCode(CodeInvalidMessage).WithDetail("type", m.Input.FullName)
	}
	js, err := (protojson.MarshalOptions{Indent: "  ", Resolver: pool}).Marshal(msg)
	if err != nil {
		// Only shown; the message was valid input.
		js = nil
	}
	return b, string(js), nil
}

// parseMessage parses message JSON as Invoke sends it: blank text is an
// empty message.
func parseMessage(pool *desc.Pool, md *desc.Message, text string) (*dynamic.Message, error) {
	in := []byte(text)
	if strings.TrimSpace(text) == "" {
		in = []byte("{}")
	}
	msg := dynamic.New(md)
	if err := (protojson.UnmarshalOptions{Resolver: pool}).Unmarshal(in, msg); err != nil {
		return nil, err
	}
	return msg, nil
}

// jsonMessage is a protojson error without its "protojson: " prefix, with
// the position.
func jsonMessage(err error) string {
	if je, ok := errors.AsType[*protojson.Error](err); ok {
		if je.Offset >= 0 {
			return je.Message + " (line " + strconv.Itoa(je.Line) + ", column " + strconv.Itoa(je.Column) + ")"
		}
		return je.Message
	}
	return err.Error()
}

// toMetadata turns the enabled rows into metadata: names trimmed and in
// lower case (gRPC keys are case-insensitive and HTTP/2 sends them in
// lower case), "-bin" values decoded from Base64. The transport checks the
// rest.
func toMetadata(rows []MetadataRowDTO) (transport.Metadata, error) {
	var md transport.Metadata
	for i, r := range rows {
		key := strings.ToLower(strings.TrimSpace(r.Name))
		if !r.Enabled || key == "" {
			continue
		}
		value := r.Value
		if transport.IsBinaryKey(key) {
			b, ok := decodeBase64(value)
			if !ok {
				return nil, errs.New(errs.InvalidArgument, "The value of "+key+" is not Base64. Values of -bin keys are binary and are entered as Base64.").
					WithCode(CodeInvalidBinaryMetadata).WithDetail("key", key).WithDetail("row", strconv.Itoa(i)).
					WithHint("Enter the bytes in Base64, e.g. AAEC for the bytes 00 01 02.")
			}
			value = string(b)
		}
		md = append(md, transport.MDEntry{Key: key, Value: value})
	}
	return md, nil
}

// decodeBase64 accepts standard and URL-safe Base64, padded or not, with
// whitespace anywhere.
func decodeBase64(s string) ([]byte, bool) {
	s = strings.Join(strings.Fields(s), "")
	for _, enc := range []*base64.Encoding{base64.StdEncoding, base64.RawStdEncoding, base64.URLEncoding, base64.RawURLEncoding} {
		if b, err := enc.DecodeString(s); err == nil {
			return b, true
		}
	}
	return nil, false
}

// callAddress is the host:port a TLS call to address connects to: port
// 443 when address has none.
func callAddress(address string) string {
	if _, _, err := net.SplitHostPort(address); err == nil {
		return address
	}
	return net.JoinHostPort(strings.Trim(address, "[]"), "443")
}

// maxClients bounds the cached transport clients; past it the idle ones
// are closed and the cache starts over (client certificates edited over
// and over).
const maxClients = 32

// client returns the transport client for key, built on first use.
func (s *GRPCService) client(key clientKey) *transport.Client {
	s.clientsMu.Lock()
	defer s.clientsMu.Unlock()
	lookup := key
	lookup.cert = nil // the pointer to the description changes per call
	if c, ok := s.clients[lookup]; ok {
		return c
	}
	if len(s.clients) >= maxClients {
		for k, c := range s.clients {
			c.CloseIdleConnections()
			delete(s.clients, k)
		}
	}
	// Clients of an older CA pool are dropped (NET-04). Those of h2c
	// calls (roots nil) stay: they do not depend on it.
	for k, c := range s.clients {
		if k.insecure == key.insecure && k.connect == key.connect && k.roots != nil && k.roots != key.roots {
			c.CloseIdleConnections()
			delete(s.clients, k)
		}
	}
	d := s.dialer
	o := transport.Options{Transport: httpclient.TransportOptions{InsecureSkipVerify: key.insecure, RootCAs: key.roots, ClientCertificate: key.cert}}
	switch {
	case key.connect > 0:
		limit := time.Duration(key.connect) * time.Millisecond
		next := d
		d = netx.DialerFunc(func(ctx context.Context, network, address string) (net.Conn, error) {
			ctx, cancel := context.WithTimeout(ctx, limit)
			defer cancel()
			return next.DialContext(ctx, network, address)
		})
		o.Transport.TLSHandshakeTimeout = limit
	case key.connect == 0:
		o.Transport.TLSHandshakeTimeout = -1
	}
	c := transport.New(d, o)
	s.clients[lookup] = c
	return c
}
