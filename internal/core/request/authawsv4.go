package request

// authawsv4.go maps an awsv4 auth (AUTH-17): the fields are rendered with
// the rest of the auth, and the request is signed by package awssigv4
// once it is complete (HTTPBuild.signAWSV4), since the signature covers
// the method, the URL as sent, the headers and the body. See
// docs/auth.md#aws-signature-v4-auth-17.

import (
	"strings"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/awssigv4"
	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/httpclient"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
)

// maxAWSExpires is the longest a presigned URL can be valid: 7 days.
const maxAWSExpires = 7 * 24 * 60 * 60

// awsV4Pending is an awsv4 auth rendered and checked, waiting for the
// finished request to sign.
type awsV4Pending struct {
	// in is "header" or "query".
	in                                                  string
	accessKey, secretKey, sessionToken, region, service string
	unsigned                                            bool
	expires                                             int64
	now                                                 func() time.Time
	// omit are the request's settings.omitHeaders.
	omit []string
}

func (m authMapper) awsv4() (AuthResult, error) {
	a := m.auth
	in := a.In
	if in == "" {
		in = projectfile.APIKeyInHeader
	}
	if in != projectfile.APIKeyInHeader && in != projectfile.APIKeyInQuery {
		return AuthResult{}, m.detail(errs.New(errs.Unsupported, "This version of PostSonde cannot send an AWS signature there.").
			WithCode(CodeAuthUnsupported).
			WithHint("Send it in the Authorization header or the query string."), "in")
	}
	v, err := m.fields(
		"accessKey", a.AccessKey, "secretKey", a.SecretKey, "sessionToken", a.SessionToken,
		"region", a.Region, "service", a.Service,
	)
	if err != nil {
		return AuthResult{}, err
	}
	accessKey, secretKey, token, region, service := v[0], v[1], v[2], v[3], v[4]
	if accessKey == "" {
		return AuthResult{}, nil
	}
	for _, f := range []struct{ name, label, value string }{
		{"accessKey", "access key", accessKey}, {"secretKey", "secret access key", secretKey},
		{"sessionToken", "session token", token}, {"region", "region", region}, {"service", "service", service},
	} {
		if hasControl(f.value) {
			return AuthResult{}, m.invalid(f.name, "The AWS "+f.label+" contains a line break or another control character.",
				"Remove control characters from the "+f.label+", or from the variables it uses.")
		}
	}
	for _, f := range []struct{ name, label, example string }{{"region", "region", "us-east-1"}, {"service", "service", "execute-api, s3"}} {
		value := region
		if f.name == "service" {
			value = service
		}
		if value == "" || strings.ContainsAny(value, "/ ") {
			return AuthResult{}, m.invalid(f.name, "The AWS "+f.label+" is empty or not a "+f.label+" name.",
				"Enter the "+f.label+" the request is signed for, e.g. "+f.example+".")
		}
	}
	expires := a.Expires
	if expires == 0 {
		expires = projectfile.DefaultAWSExpires
	}
	if in == projectfile.APIKeyInQuery && (expires < 1 || expires > maxAWSExpires) {
		return AuthResult{}, m.invalid("expires", "A presigned AWS URL is valid for 1 second to 7 days.",
			"Set the expiry between 1 and 604800 seconds.")
	}
	p := &awsV4Pending{
		in: in, accessKey: accessKey, secretKey: secretKey, sessionToken: token,
		region: region, service: service, unsigned: a.UnsignedPayload, expires: expires,
		now: m.opts.Now,
	}
	return AuthResult{awsv4: p, Secrets: nonEmpty([]string{secretKey, token, encodeComponent(token)})}, nil
}

// awsUnsignedHeaders are headers that are not signed: added or changed on
// the way (User-Agent, Expect, X-Amzn-Trace-Id, hop-by-hop fields), or the
// signature itself. Content-Length is computed by the client.
var awsUnsignedHeaders = map[string]bool{
	"authorization": true, "user-agent": true, "expect": true, "x-amzn-trace-id": true,
	"connection": true, "keep-alive": true, "proxy-authorization": true, "te": true,
	"transfer-encoding": true, "upgrade": true, "content-length": true,
}

// signAWSV4 signs b's request as it is now with the pending awsv4 auth,
// if there is one: in the header, X-Amz-Date, X-Amz-Security-Token (with
// a session token), X-Amz-Content-Sha256 (S3, or an unsigned payload) and
// Authorization are added; in the query, the X-Amz-* parameters of a
// presigned URL. It runs once, as the last step of a build.
func (b *HTTPBuild) signAWSV4() error {
	p := b.awsv4
	if p == nil {
		return nil
	}
	b.awsv4 = nil
	r := &b.Request
	s3 := p.service == awssigv4.ServiceS3
	now := time.Now
	if p.now != nil {
		now = p.now
	}
	t := now().UTC().Truncate(time.Second)
	query := p.in == projectfile.APIKeyInQuery

	// A typed X-Amz-Date row is signed as it is, so the scope uses its
	// date.
	dateRow, hasDate := headerRow(r.Headers, awssigv4.HeaderDate)
	if hasDate && !query {
		d, err := time.Parse(awssigv4.TimeFormat, strings.TrimSpace(dateRow))
		if err != nil {
			return errs.New(errs.InvalidArgument, "The X-Amz-Date header is not a time like 20150830T123600Z.").
				WithCode(CodeAuthInvalid).
				WithHint("Correct or disable the X-Amz-Date header; PostSonde adds one with the time of the send.").
				WithDetail(DetailAuthType, projectfile.AuthAWSV4).
				WithDetail("header", awssigv4.HeaderDate)
		}
		t = d
	}
	scope := awssigv4.Scope{Time: t, Region: p.region, Service: p.service}

	// The payload hash: a typed X-Amz-Content-Sha256 row, UNSIGNED-PAYLOAD,
	// or the body's SHA-256.
	payload, hasPayloadRow := headerRow(r.Headers, awssigv4.HeaderContentSHA256)
	payload = strings.TrimSpace(payload)
	if !hasPayloadRow {
		switch {
		case p.unsigned || (query && s3):
			payload = awssigv4.UnsignedPayload
		default:
			var err error
			if payload, err = hashBody(r.Body); err != nil {
				return errs.Wrap(err, errs.KindOf(err), "The body could not be read to sign it.").
					WithHint("Check the body, or sign the request with an unsigned payload.").
					WithDetail(DetailAuthType, projectfile.AuthAWSV4)
			}
		}
	}

	target, err := sentURL(r)
	if err != nil {
		return err
	}
	host, hasHost := headerRow(r.Headers, "Host")
	if !hasHost {
		host = target.Authority
	}
	signed := []awssigv4.Header{{Name: "host", Value: host}}
	for _, h := range r.Headers {
		if !h.Enabled || strings.EqualFold(h.Name, "Host") || awsUnsignedHeaders[strings.ToLower(h.Name)] {
			continue
		}
		signed = append(signed, awssigv4.Header{Name: h.Name, Value: h.Value})
	}
	if !HasContentType(r.Headers) && r.Body != nil && r.Body.ContentType() != "" {
		signed = append(signed, awssigv4.Header{Name: "Content-Type", Value: r.Body.ContentType()})
	}

	if query {
		params := awssigv4.PresignParams(p.accessKey, scope, p.expires, awssigv4.SignedHeaders(signed), p.sessionToken)
		qp := make([]QueryParam, len(params))
		for i, x := range params {
			qp[i] = QueryParam{Name: x.Name, Value: x.Value}
		}
		r.URL = WithAuthQuery(r.URL, qp)
		if target, err = sentURL(r); err != nil {
			return err
		}
		res := awssigv4.Sign(awssigv4.Request{Method: r.Method, Path: target.Path, Query: target.Query, Headers: signed, PayloadHash: payload}, p.secretKey, scope)
		r.URL = WithAuthQuery(r.URL, []QueryParam{{Name: awssigv4.QuerySignature, Value: res.Signature}})
		b.AWSV4 = &res
		b.AuthSecrets = append(b.AuthSecrets, res.Signature)
		return nil
	}

	// The headers the signature adds are signed too.
	var added []httpclient.Header
	add := func(name, value string) {
		if _, typed := headerRow(r.Headers, name); typed {
			return
		}
		added = append(added, httpclient.Header{Name: name, Value: value, Enabled: true})
		signed = append(signed, awssigv4.Header{Name: name, Value: value})
	}
	add(awssigv4.HeaderDate, t.Format(awssigv4.TimeFormat))
	if p.sessionToken != "" {
		add(awssigv4.HeaderSecurityToken, p.sessionToken)
	}
	if s3 || p.unsigned {
		add(awssigv4.HeaderContentSHA256, payload)
	}
	res := awssigv4.Sign(awssigv4.Request{Method: r.Method, Path: target.Path, Query: target.Query, Headers: signed, PayloadHash: payload}, p.secretKey, scope)
	b.AWSV4 = &res
	b.AuthSecrets = append(b.AuthSecrets, res.Signature)
	value := res.AuthorizationHeader(p.accessKey)
	if b.authorizationWins(value, p.omit) {
		return nil
	}
	for _, h := range added {
		b.addAuthHeader(h.Name, h.Value)
	}
	b.addAuthHeader(AuthorizationHeader, value)
	return nil
}

// hashBody is the hex SHA-256 of body (of nothing when nil), read as it
// is sent.
func hashBody(body httpclient.Body) (string, error) {
	if body == nil {
		return awssigv4.PayloadHash(nil)
	}
	rc, _, err := body.Open()
	if err != nil {
		return "", err
	}
	defer rc.Close()
	return awssigv4.PayloadHash(rc)
}
