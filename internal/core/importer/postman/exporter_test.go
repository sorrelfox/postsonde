package postman_test

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/importer"
	"github.com/sorrelfox/postsonde/internal/core/importer/postman"
	"github.com/sorrelfox/postsonde/internal/core/jsonpath"
	"github.com/sorrelfox/postsonde/internal/core/jsonschema"
	"github.com/sorrelfox/postsonde/internal/core/storage/projectfile"
	"github.com/sorrelfox/postsonde/internal/testutil/golden"
)

func ptr[T any](v T) *T { return &v }

func httpReq(name string, r projectfile.HTTPRequest) importer.Item {
	return importer.RequestItem(&importer.Request{Name: name, File: &r})
}

func gqlReq(name string, r projectfile.GraphQLRequest) importer.Item {
	return importer.RequestItem(&importer.Request{Name: name, File: &r})
}

// fullProject uses every feature the export keeps, so the round trip
// through the importer gives it back unchanged.
func fullProject() importer.Folder {
	return importer.Folder{
		Name:        "Shop API",
		Description: "The shop's API.",
		Variables: []projectfile.Variable{
			{Name: "baseUrl", Value: "https://shop.example.com/v1"},
			{Name: "apiKey", Secret: true},
			{Name: "old", Value: "x", Description: "No longer used", Disabled: true},
		},
		Auth: projectfile.Auth{Type: projectfile.AuthBearer, Token: "{{token}}"},
		Items: []importer.Item{
			importer.FolderItem(&importer.Folder{
				Name:        "Users",
				Description: "Accounts.",
				Variables:   []projectfile.Variable{{Name: "userId", Value: "42"}},
				Auth:        projectfile.Auth{Type: projectfile.AuthBasic, Username: "{{user}}", Password: "{{pass}}"},
				Items: []importer.Item{
					httpReq("List users", projectfile.HTTPRequest{
						Method: "GET", URL: "{{baseUrl}}/users?page=1&flag&limit=10#top", Description: "Lists them.",
						Params: []projectfile.KeyValue{
							{Name: "page", Value: "1", Description: "From 1"},
							{Name: "flag"},
							{Name: "limit", Value: "10"},
							{Name: "sort", Value: "name", Disabled: true},
						},
						Headers: []projectfile.KeyValue{
							{Name: "Accept", Value: "application/json"},
							{Name: "X-Debug", Value: "1", Description: "Tracing", Disabled: true},
						},
						Body: projectfile.Body{Mode: projectfile.BodyNone},
						Auth: projectfile.Auth{Type: projectfile.AuthInherit},
						ResponseExamples: []projectfile.ResponseExample{
							{
								Name: "Two users",
								Request: &projectfile.ExampleRequest{
									Method: "GET", URL: "https://shop.example.com/v1/users?page=1",
									Headers: []projectfile.KeyValue{{Name: "Accept", Value: "application/json"}},
								},
								StatusCode: 200, StatusText: "OK",
								Headers: []projectfile.KeyValue{{Name: "Content-Type", Value: "application/json"}},
								Body:    `[{"id":1},{"id":2}]`,
							},
							{Name: "Gone", StatusCode: 410, StatusText: "Gone"},
						},
					}),
					httpReq("Get user", projectfile.HTTPRequest{
						Method: "GET", URL: "{{baseUrl}}/users/:id",
						PathParams: []projectfile.KeyValue{{Name: "id", Value: "{{userId}}", Description: "The user"}},
						Body:       projectfile.Body{Mode: projectfile.BodyNone},
						Auth:       projectfile.Auth{Type: projectfile.AuthNone},
						Settings: projectfile.RequestSettings{
							FollowRedirects: ptr(false), MaxRedirects: ptr(3), FollowOriginalMethod: ptr(true),
							ForwardAuthorization: ptr(true), VerifyTLS: ptr(false), URLEncoding: projectfile.URLEncodingNone,
							HTTPVersion: projectfile.HTTPVersion2, SendCookies: ptr(false), StoreCookies: ptr(false),
						},
					}),
					httpReq("Create user", projectfile.HTTPRequest{
						Method: "POST", URL: "http://localhost:8080/users",
						Body: projectfile.Body{Mode: projectfile.BodyRaw, Type: projectfile.RawJSON, Text: "{\n  \"name\": \"{{$randomFirstName}}\",\n  \"id\": \"{{$guid}}\"\n}"},
						Auth: projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "X-API-Key", Value: "{{apiKey}}"},
						// The example's text is as sent: {{…}} in it stays.
						ResponseExamples: []projectfile.ResponseExample{{
							Name: "Created",
							Request: &projectfile.ExampleRequest{
								Method: "POST", URL: "http://localhost:8080/users",
								Headers: []projectfile.KeyValue{{Name: "X-API-Key", Value: "••••••"}},
								Body:    projectfile.Body{Mode: projectfile.BodyRaw, Type: projectfile.RawJSON, Text: `{"name":"{{literally}}"}`},
							},
							StatusCode: 201, StatusText: "Created",
							Body: `{"id":"u1"}`,
						}},
					}),
					importer.FolderItem(&importer.Folder{
						Name: "Admin",
						Auth: projectfile.Auth{Type: projectfile.AuthDigest, Username: "admin", Password: "{{adminPassword}}"},
						Items: []importer.Item{
							httpReq("Import", projectfile.HTTPRequest{
								Method: "POST", URL: "{{baseUrl}}/admin/import",
								Body: projectfile.Body{Mode: projectfile.BodyMultipart, Parts: []projectfile.Part{
									{Name: "meta", Value: "{\"dry\": true}", ContentType: "application/json", Description: "Options"},
									{Name: "file", File: "data/users.csv"},
									{Name: "note", Value: "later", Disabled: true},
								}},
								Auth: projectfile.Auth{Type: projectfile.AuthInherit},
							}),
							httpReq("Upload", projectfile.HTTPRequest{
								Method: "PUT", URL: "{{baseUrl}}/admin/blob",
								Body: projectfile.Body{Mode: projectfile.BodyFile, Path: "/tmp/blob.bin"},
								Auth: projectfile.Auth{Type: projectfile.AuthInherit},
							}),
							httpReq("Log in", projectfile.HTTPRequest{
								Method: "POST", URL: "{{baseUrl}}/login",
								Body: projectfile.Body{Mode: projectfile.BodyURLEncoded, Fields: []projectfile.KeyValue{
									{Name: "user", Value: "{{user}}"},
									{Name: "remember", Value: "1", Description: "Stay logged in", Disabled: true},
								}},
								Auth: projectfile.Auth{Type: projectfile.AuthInherit},
							}),
						},
					}),
					importer.FolderItem(&importer.Folder{Name: "Empty", Auth: projectfile.Auth{Type: projectfile.AuthInherit}}),
				},
			}),
			gqlReq("Search", projectfile.GraphQLRequest{
				URL: "{{baseUrl}}/graphql", Query: "query Search($q: String!) {\n  search(q: $q) { id }\n}", Variables: "{\"q\": \"shoe\"}",
				Headers: []projectfile.KeyValue{{Name: "X-Client", Value: "postsonde"}},
				Auth:    projectfile.Auth{Type: projectfile.AuthBearer, Token: "{{gqlToken}}"},
			}),
			gqlReq("Schema over GET", projectfile.GraphQLRequest{
				Method: "GET", URL: "https://api.example.com/graphql", Query: "{ __typename }",
				Auth: projectfile.Auth{Type: projectfile.AuthInherit},
			}),
			httpReq("Signed", projectfile.HTTPRequest{
				Method: "GET", URL: "https://api.example.com/photos?size=original",
				Params: []projectfile.KeyValue{{Name: "size", Value: "original"}},
				Body:   projectfile.Body{Mode: projectfile.BodyNone},
				Auth: projectfile.Auth{
					Type: projectfile.AuthOAuth1, ConsumerKey: "ck", ConsumerSecret: "{{consumerSecret}}", AccessToken: "at",
					TokenSecret: "{{tokenSecret}}", SignatureMethod: "HMAC-SHA256", Callback: "https://cb.example.com",
					Verifier: "v", Realm: "photos", Nonce: "n0nce", Timestamp: "1700000000", BodyHash: true,
				},
			}),
			httpReq("Signed form", projectfile.HTTPRequest{
				Method: "POST", URL: "https://api.example.com/status",
				Body: projectfile.Body{Mode: projectfile.BodyURLEncoded, Fields: []projectfile.KeyValue{{Name: "status", Value: "hi"}}},
				Auth: projectfile.Auth{Type: projectfile.AuthOAuth1, ConsumerKey: "ck", ConsumerSecret: "{{cs}}", SignatureMethod: "HMAC-SHA1", In: projectfile.AuthInBody},
			}),
			httpReq("Signed with RSA", projectfile.HTTPRequest{
				Method: "GET", URL: "https://api.example.com/rsa",
				Body: projectfile.Body{Mode: projectfile.BodyNone},
				Auth: projectfile.Auth{Type: projectfile.AuthOAuth1, ConsumerKey: "ck", ConsumerSecret: "{{privateKey}}", AccessToken: "at",
					SignatureMethod: "RSA-SHA1", OmitVersion: true, In: projectfile.APIKeyInQuery},
			}),
			httpReq("JWT", projectfile.HTTPRequest{
				Method: "GET", URL: "https://api.example.com/me",
				Body: projectfile.Body{Mode: projectfile.BodyNone},
				Auth: projectfile.Auth{Type: projectfile.AuthJWT, Algorithm: "HS384", JWTSecret: "{{jwtSecret}}", SecretBase64: true,
					Payload: "{\"sub\": \"{{userId}}\", \"iat\": {{$timestamp}}}", JWTHeader: "{\"kid\": \"k1\"}", Prefix: ptr("JWT")},
			}),
			httpReq("JWT in query", projectfile.HTTPRequest{
				Method: "GET", URL: "https://api.example.com/feed",
				Body: projectfile.Body{Mode: projectfile.BodyNone},
				Auth: projectfile.Auth{Type: projectfile.AuthJWT, Algorithm: "RS256", JWTSecret: "{{pem}}", Payload: "{}", In: projectfile.APIKeyInQuery, Key: "tok"},
			}),
			httpReq("AWS", projectfile.HTTPRequest{
				Method: "GET", URL: "https://iam.amazonaws.com/?Action=ListUsers",
				Params: []projectfile.KeyValue{{Name: "Action", Value: "ListUsers"}},
				Body:   projectfile.Body{Mode: projectfile.BodyNone},
				Auth: projectfile.Auth{Type: projectfile.AuthAWSV4, AccessKey: "AKIDEXAMPLE", SecretKey: "{{awsSecret}}", SessionToken: "{{awsSession}}",
					Region: "us-east-1", Service: "iam"},
			}),
			httpReq("AWS presigned", projectfile.HTTPRequest{
				Method: "GET", URL: "https://bucket.s3.amazonaws.com/key",
				Body: projectfile.Body{Mode: projectfile.BodyNone},
				Auth: projectfile.Auth{Type: projectfile.AuthAWSV4, AccessKey: "{{awsKey}}", Region: "eu-west-1", Service: "s3", In: projectfile.APIKeyInQuery},
			}),
			httpReq("Hawk", projectfile.HTTPRequest{
				Method: "POST", URL: "https://hawk.example.com/resource",
				Body: projectfile.Body{Mode: projectfile.BodyRaw, Type: projectfile.RawText, Text: "hello"},
				Auth: projectfile.Auth{Type: projectfile.AuthHawk, HawkID: "dh37fgj492je", HawkKey: "{{hawkKey}}", HawkAlgorithm: "sha1",
					Ext: "some-app-data", App: "app1", Dlg: "dlg1", PayloadHash: true},
			}),
			httpReq("OAuth 2.0", projectfile.HTTPRequest{
				Method: "GET", URL: "https://api.example.com/orders",
				Body: projectfile.Body{Mode: projectfile.BodyNone},
				Auth: projectfile.Auth{Type: projectfile.AuthOAuth2, GrantType: projectfile.OAuth2ClientCredentials, TokenURL: "{{idp}}/oauth/token",
					ClientID: "shop", ClientSecret: "{{clientSecret}}", ClientAuth: projectfile.OAuth2ClientAuthBody, Scope: "orders:read",
					Audience: "https://api.example.com", In: projectfile.APIKeyInQuery,
					TokenParams: []projectfile.KeyValue{{Name: "tenant", Value: "t1"}, {Name: "debug", Value: "1", Disabled: true}}},
			}),
			httpReq("OAuth 2.0 sign-in", projectfile.HTTPRequest{
				Method: "GET", URL: "https://api.example.com/me",
				Body: projectfile.Body{Mode: projectfile.BodyNone},
				Auth: projectfile.Auth{Type: projectfile.AuthOAuth2, GrantType: projectfile.OAuth2AuthorizationCode, TokenURL: "{{idp}}/oauth/token",
					AuthURL: "{{idp}}/authorize", RedirectURL: "http://127.0.0.1:8765/callback", ClientID: "desk", Scope: "openid"},
			}),
			httpReq("OAuth 2.0 password", projectfile.HTTPRequest{
				Method: "GET", URL: "https://api.example.com/me",
				Body: projectfile.Body{Mode: projectfile.BodyNone},
				Auth: projectfile.Auth{Type: projectfile.AuthOAuth2, GrantType: projectfile.OAuth2Password, TokenURL: "{{idp}}/oauth/token",
					ClientID: "legacy", Username: "{{user}}", Password: "{{password}}", Scope: "openid"},
			}),
			httpReq("OAuth 2.0 implicit", projectfile.HTTPRequest{
				Method: "GET", URL: "https://api.example.com/me",
				Body: projectfile.Body{Mode: projectfile.BodyNone},
				Auth: projectfile.Auth{Type: projectfile.AuthOAuth2, GrantType: projectfile.OAuth2Implicit,
					AuthURL: "{{idp}}/authorize", RedirectURL: "http://localhost:9000/cb", ClientID: "spa", Scope: "openid profile"},
			}),
			httpReq("XML", projectfile.HTTPRequest{
				Method: "POST", URL: "https://api.example.com/xml",
				Body: projectfile.Body{Mode: projectfile.BodyRaw, Type: projectfile.RawXML, Text: "<a>{{x}}</a>"},
				Auth: projectfile.Auth{Type: projectfile.AuthInherit},
			}),
			httpReq("HTML", projectfile.HTTPRequest{
				Method: "POST", URL: "https://api.example.com/html",
				Body: projectfile.Body{Mode: projectfile.BodyRaw, Type: projectfile.RawHTML, Text: "<p>hi</p>"},
				Auth: projectfile.Auth{Type: projectfile.AuthInherit},
			}),
			httpReq("Text", projectfile.HTTPRequest{
				Method: "PURGE", URL: "https://[::1]:8443/cache/{{key}}",
				Body: projectfile.Body{Mode: projectfile.BodyRaw, Type: projectfile.RawText, Text: "all"},
				Auth: projectfile.Auth{Type: projectfile.AuthInherit},
			}),
		},
	}
}

// canonical renders a folder tree as JSON for comparisons: request files
// as PostSonde writes them, without IDs; diagnostics and places left out.
func canonical(t *testing.T, f *importer.Folder) string {
	t.Helper()
	var folder func(f *importer.Folder) map[string]any
	folder = func(f *importer.Folder) map[string]any {
		var items []any
		for _, it := range f.Items {
			switch {
			case it.Folder != nil:
				items = append(items, folder(it.Folder))
			case it.Request != nil:
				items = append(items, map[string]any{"request": it.Request.Name, "file": json.RawMessage(marshal(t, it.Request.File))})
			default:
				items = append(items, map[string]any{"skipped": it.Skipped.Name})
			}
		}
		auth, err := json.Marshal(f.Auth)
		if err != nil {
			t.Fatal(err)
		}
		return map[string]any{
			"folder": f.Name, "description": f.Description, "variables": f.Variables, "headers": f.Headers,
			"auth": json.RawMessage(auth), "items": items,
		}
	}
	data, err := json.MarshalIndent(folder(f), "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func export(t *testing.T, f importer.Folder) *postman.ExportResult {
	t.Helper()
	res, err := postman.Export(f, postman.ExportOptions{ID: "fld_0123456789abcdef"})
	if err != nil {
		t.Fatal(err)
	}
	validate(t, res.Data)
	return res
}

// validate checks a collection against the Postman Collection v2.1.0
// schema (testdata/collection-v2.1.0.schema.json).
func validate(t *testing.T, data []byte) {
	t.Helper()
	for _, e := range schemaErrors(t, data) {
		t.Errorf("not a valid Postman v2.1 collection: %s", e)
	}
}

func schemaErrors(t *testing.T, data []byte) []jsonschema.Error {
	t.Helper()
	schemaData, err := os.ReadFile(filepath.Join("testdata", "collection-v2.1.0.schema.json"))
	if err != nil {
		t.Fatal(err)
	}
	s, err := jsonschema.CompileJSON(schemaData, jsonschema.Options{})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := jsonpath.Decode(data)
	if err != nil {
		t.Fatalf("the export is not JSON: %v", err)
	}
	res, err := s.Validate(context.Background(), doc, jsonschema.DefaultLimits)
	if err != nil {
		t.Fatal(err)
	}
	return res.Errors
}

// TestSchemaRejects: the test schema tells a collection from what is not.
func TestSchemaRejects(t *testing.T) {
	for _, doc := range []string{
		`{"info": {"name": "x"}, "item": []}`,
		`{"info": {"name": "x", "schema": "s"}, "item": [{"name": "neither request nor items"}]}`,
		`{"info": {"name": "x", "schema": "s"}, "item": [], "auth": {"type": "magic"}}`,
		`{"info": {"name": "x", "schema": "s"}, "item": [{"request": {"header": [{"key": "a"}]}}]}`,
	} {
		if len(schemaErrors(t, []byte(doc))) == 0 {
			t.Errorf("accepted %s", doc)
		}
	}
}

func TestExportRoundTrip(t *testing.T) {
	in := fullProject()
	res := export(t, in)
	if res.Folders != 3 || res.Requests != 23 || res.Skipped != 0 {
		t.Errorf("folders %d, requests %d, skipped %d", res.Folders, res.Requests, res.Skipped)
	}
	for _, n := range res.Notes {
		if n.Severity == importer.SeverityWarning {
			t.Errorf("unexpected warning %s at %v: %s", n.Code, n.Path, n.Message)
		}
	}
	golden.Assert(t, "export/full.postman_collection.json", res.Data)

	m := parse(t, importer.Source{Name: "Shop API.postman_collection.json", Data: res.Data})
	if m.Name != in.Name {
		t.Errorf("name %q", m.Name)
	}
	if got, want := canonical(t, &m.Root), canonical(t, &in); got != want {
		t.Errorf("the round trip changed the collection:\n%s", lineDiff(want, got))
	}
}

// TestExportIDStable: the same folder gets the same _postman_id.
func TestExportIDStable(t *testing.T) {
	f := importer.Folder{Name: "A"}
	id := func(opts postman.ExportOptions) string {
		res, err := postman.Export(f, opts)
		if err != nil {
			t.Fatal(err)
		}
		var c struct {
			Info struct {
				ID string `json:"_postman_id"`
			} `json:"info"`
		}
		if err := json.Unmarshal(res.Data, &c); err != nil {
			t.Fatal(err)
		}
		return c.Info.ID
	}
	a, b := id(postman.ExportOptions{ID: "fld_1"}), id(postman.ExportOptions{ID: "fld_1"})
	if a != b || len(a) != 36 || a[14] != '5' {
		t.Errorf("ids %q, %q", a, b)
	}
	if c := id(postman.ExportOptions{ID: "fld_2"}); c == a {
		t.Error("two folders got the same id")
	}
	if r := id(postman.ExportOptions{}); r == a || len(r) != 36 || r[14] != '4' {
		t.Errorf("random id %q", r)
	}
}

// lossyProject uses what Postman cannot hold, or holds differently.
func lossyProject() importer.Folder {
	return importer.Folder{
		Name:        "Lossy",
		Diagnostics: []importer.Diagnostic{{Code: "test.note", Severity: importer.SeverityInfo, Message: "A note of the caller."}},
		Variables:   []projectfile.Variable{{Name: "password", Secret: true}, {Name: "typed", Value: "s3cr3t-var", Secret: true}},
		Headers:     []projectfile.KeyValue{{Name: "X-Tenant", Value: "acme"}, {Name: "X-Off", Value: "1", Disabled: true}},
		Items: []importer.Item{
			importer.FolderItem(&importer.Folder{
				Name:    "Inner",
				Headers: []projectfile.KeyValue{{Name: "X-Tenant", Value: "inner"}, {Name: "X-Trace", Value: "{{$uuid}}"}},
				Auth:    projectfile.Auth{Type: projectfile.AuthNetrc},
				Items: []importer.Item{
					httpReq("Own tenant", projectfile.HTTPRequest{
						Method: "POST", URL: "{{baseUrl}}/files/{fileId}?stamp={{$timestampMs}}",
						Headers: []projectfile.KeyValue{{Name: "x-trace", Value: "mine"}},
						Body:    projectfile.Body{Mode: projectfile.BodyRaw, Type: projectfile.RawYAML, Text: "a: {{ secret | base64 }}"},
						BodyExamples: []projectfile.BodyExample{
							{Name: "Active", Active: true},
							{Name: "Other", Body: projectfile.Body{Mode: projectfile.BodyRaw, Text: "x"}},
						},
						Auth: projectfile.Auth{Type: projectfile.AuthBearer, Prefix: ptr("Token"), LocalSecrets: []string{"token"}},
						Settings: projectfile.RequestSettings{
							TimeoutMs: ptr(int64(5000)), URLEncoding: projectfile.URLEncodingRFC3986, SendCookies: ptr(false),
							OmitHeaders: []string{"Content-Type"},
						},
						Assertions:  []projectfile.Assertion{{Target: "status", Op: "eq", Expected: "200"}},
						Extractions: []projectfile.Extraction{{Source: "jsonpath", Expr: "$.id", Variable: "fileId"}, {Source: "status", Variable: "code"}},
					}),
					httpReq("Typed secret", projectfile.HTTPRequest{
						Method: "GET", URL: "https://x.example.com",
						Headers: []projectfile.KeyValue{{Name: "X-Who", Value: "{{$prompt Name}}"}},
						Body:    projectfile.Body{Mode: projectfile.BodyFile, Path: "body.bin", ContentType: "application/octet-stream"},
						Auth:    projectfile.Auth{Type: projectfile.AuthBasic, Username: "u", Password: "hunter2-literal"},
						ResponseExamples: []projectfile.ResponseExample{
							{Name: "Picture", SavedAt: "2026-09-23T10:15:02Z", StatusCode: 200, Body: "iVBORw0KGgo=", BodyEncoding: projectfile.ExampleBodyBase64},
							{Name: "Big", StatusCode: 200, Body: "[1,2", BodyClipped: true, BodySize: 100000,
								Request: &projectfile.ExampleRequest{Method: "GET", URL: "https://x.example.com",
									Headers: []projectfile.KeyValue{{Name: "Authorization", Value: "Bearer example-token-literal"}}}},
						},
					}),
					httpReq("Turned off", projectfile.HTTPRequest{
						Method: "GET", URL: "https://x.example.com",
						Auth: projectfile.Auth{Type: projectfile.AuthBasic, Username: "u", Password: "{{p}}", Disabled: true},
					}),
					httpReq("JWT extras", projectfile.HTTPRequest{
						Method: "GET", URL: "https://x.example.com",
						Auth: projectfile.Auth{Type: projectfile.AuthJWT, Algorithm: "ES256", KeyFile: "keys/ec.pem", Key: "X-JWT",
							IssuedAt: true, ExpiresIn: 60, LocalSecrets: []string{"secret"}},
					}),
					httpReq("AWS extras", projectfile.HTTPRequest{
						Method: "PUT", URL: "https://bucket.s3.amazonaws.com/key",
						Auth: projectfile.Auth{Type: projectfile.AuthAWSV4, AccessKey: "AKID", SecretKey: "aws-literal-secret", Region: "us-east-1",
							Service: "s3", UnsignedPayload: true, In: projectfile.APIKeyInQuery, Expires: 60, LocalSecrets: []string{"sessionToken"}},
					}),
					httpReq("Hawk typed key", projectfile.HTTPRequest{
						Method: "GET", URL: "https://hawk.example.com",
						Auth: projectfile.Auth{Type: projectfile.AuthHawk, HawkID: "id", LocalSecrets: []string{"hawkKey"}},
					}),
					httpReq("OAuth 2.0 extras", projectfile.HTTPRequest{
						Method: "GET", URL: "https://x.example.com",
						Auth: projectfile.Auth{Type: projectfile.AuthOAuth2, GrantType: projectfile.OAuth2Password, TokenURL: "https://idp.example.com/token", ClientID: "app",
							ClientSecret: "oauth2-literal-secret", Key: "X-Token", Username: "u", Password: "oauth2-literal-password", IDToken: true, Issuer: "https://idp.example.com"},
					}),
					httpReq("OAuth in body of a GET", projectfile.HTTPRequest{
						Method: "GET", URL: "https://x.example.com",
						Auth: projectfile.Auth{Type: projectfile.AuthOAuth1, ConsumerKey: "ck", In: projectfile.AuthInBody,
							ConsumerSecret: "", TokenSecret: "", LocalSecrets: []string{"consumerSecret", "tokenSecret"}},
					}),
				},
			}),
			gqlReq("GraphQL extras", projectfile.GraphQLRequest{
				URL: "https://api.example.com/graphql", Query: "query A { a } query B { b }", OperationName: "B",
				Extensions: "{\"persistedQuery\": {}}", Files: []projectfile.Part{{Name: "variables.file", File: "a.png"}},
				Auth:     projectfile.Auth{Type: projectfile.AuthAPIKey, Key: "k", LocalSecrets: []string{"value"}},
				Settings: projectfile.RequestSettings{GraphQLTransport: &projectfile.GraphQLTransport{Subscription: projectfile.GraphQLTransportSSE}, GraphQLSchemaRefresh: &projectfile.GraphQLSchemaRefresh{OnSend: true}},
			}),
			importer.RequestItem(&importer.Request{Name: "Socket", File: &projectfile.WebSocketRequest{URL: "wss://x.example.com"}}),
			importer.RequestItem(&importer.Request{Name: "Call", File: &projectfile.GRPCRequest{Address: "localhost:50051"}}),
			importer.RequestItem(&importer.Request{Name: "Raw", File: &projectfile.TCPRequest{Address: "localhost:7"}}),
			importer.RequestItem(&importer.Request{Name: "Chat", File: &projectfile.SocketIORequest{URL: "http://x.example.com"}}),
			importer.RequestItem(&importer.Request{Name: "Stream", File: &projectfile.SSERequest{URL: "http://x.example.com/events"}}),
			importer.SkippedItem(&importer.Skipped{Kind: "unreadable file", Name: "broken.json", Reason: "The file could not be read."}),
		},
	}
}

func TestExportLossy(t *testing.T) {
	res := export(t, lossyProject())
	if res.Folders != 1 || res.Requests != 9 || res.Skipped != 6 {
		t.Errorf("folders %d, requests %d, skipped %d", res.Folders, res.Requests, res.Skipped)
	}
	for _, secret := range []string{"s3cr3t-var", "hunter2-literal", "aws-literal-secret", "example-token-literal", "oauth2-literal-secret", "oauth2-literal-password"} {
		if bytes.Contains(res.Data, []byte(secret)) {
			t.Errorf("the export holds the secret %q", secret)
		}
	}
	var report bytes.Buffer
	for _, n := range res.Notes {
		skipped := ""
		if n.Skipped {
			skipped = " (skipped)"
		}
		report.WriteString(strings.Join(n.Path, " / ") + " [" + n.Kind + "]" + skipped + " " + string(n.Severity) + " " + n.Code + ": " + n.Message + "\n")
	}
	golden.Assert(t, "export/lossy.postman_collection.json", res.Data)
	golden.Assert(t, "export/lossy.report", report.Bytes())

	// The collection still imports: the folder headers are the requests'.
	m := parse(t, importer.Source{Name: "lossy.json", Data: res.Data})
	inner := m.Root.Items[0].Folder
	h := inner.Items[0].Request.File.(*projectfile.HTTPRequest).Headers
	var names []string
	for _, r := range h {
		names = append(names, r.Name+"="+r.Value)
	}
	want := []string{"X-Tenant=inner", "x-trace=mine", "Content-Type=application/yaml"}
	if !slices.Equal(names, want) {
		t.Errorf("headers %v, want %v", names, want)
	}
}

// lineDiff shows the first differing line of two texts.
func lineDiff(want, got string) string {
	w, g := strings.Split(want, "\n"), strings.Split(got, "\n")
	for i := 0; i < max(len(w), len(g)); i++ {
		var a, b string
		if i < len(w) {
			a = w[i]
		}
		if i < len(g) {
			b = g[i]
		}
		if a != b {
			lo := max(0, i-3)
			return "line " + itoa(i+1) + ":\nwant: " + a + "\ngot:  " + b + "\ncontext (want):\n" + strings.Join(w[lo:min(len(w), i+3)], "\n")
		}
	}
	return ""
}

// Postman's Hawk user and fixed nonce and timestamp have no PostSonde
// equivalent: the import says so.
func TestImportHawkLost(t *testing.T) {
	data := `{"info": {"name": "H", "schema": "` + postman.SchemaV21 + `"}, "item": [{"name": "r", "request": {"method": "GET", "url": "https://x",
		"auth": {"type": "hawk", "hawk": [{"key": "authId", "value": "id"}, {"key": "user", "value": "bob"}, {"key": "nonce", "value": "n"},
		{"key": "includePayloadHash", "value": true}, {"key": "addAuthDataToQuery", "value": true}]}}}]}`
	m := parse(t, importer.Source{Name: "h.json", Data: []byte(data)})
	r := m.Root.Items[0].Request
	a := r.File.(*projectfile.HTTPRequest).Auth
	if a.Type != projectfile.AuthHawk || a.HawkID != "id" || !a.PayloadHash || len(r.Diagnostics) != 1 ||
		r.Diagnostics[0].Code != postman.CodeAuthChanged || !strings.Contains(r.Diagnostics[0].Message, "user, nonce") {
		t.Errorf("auth = %+v, diagnostics %+v", a, r.Diagnostics)
	}
}
