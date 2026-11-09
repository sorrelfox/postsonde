package snippet

import (
	"slices"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// CodeUnknownLanguage: no generator has the language ID asked for (kind
// invalid_argument). Details: language.
const CodeUnknownLanguage = "snippet.unknown_language"

// Language is one snippet target of the snippet panel (GEN-12): a pure
// "Request → string" generator and what the panel shows of it. Adding a
// language is adding an entry to languages: the service lists it, the
// panel offers it, and masking and the templated view come with the
// model, so the generator only writes code. docs/snippets.md.
type Language struct {
	// ID names the language in the API and in the UI's message keys
	// ("curl").
	ID string
	// Name is the language's name, shown when the UI has no text of its
	// own for ID.
	Name string
	// Syntax is how the UI highlights the code: "shell" (a POSIX shell),
	// or "text" for none.
	Syntax string
	// Generate writes r as code. It must be pure: it reads no file, sends
	// nothing and writes only what r holds (so a masked r gives masked
	// code). A value the language cannot hold fails with an *errs.Error
	// (CodeNulByte, CodeBodyTooLarge…).
	Generate func(r Request) (string, error)
}

// LanguageCurl is the ID of the POSIX shell curl command (GEN-01), the
// default language.
const LanguageCurl = "curl"

// languages are the generators, in the order the panel lists them.
var languages = []Language{
	{ID: LanguageCurl, Name: "cURL", Syntax: "shell", Generate: Curl},
	{ID: "curlCmd", Name: "cURL (cmd)", Syntax: "text", Generate: CurlCmd},
	{ID: "curlPowerShell", Name: "cURL (PowerShell)", Syntax: "text", Generate: CurlPowerShell},
	{ID: "httpie", Name: "HTTPie", Syntax: "shell", Generate: HTTPie},
	{ID: "wget", Name: "wget", Syntax: "shell", Generate: Wget},
	{ID: "http", Name: "Raw HTTP", Syntax: "text", Generate: RawHTTP},
	{ID: "fetch", Name: "JavaScript fetch", Syntax: "text", Generate: Fetch},
	{ID: "python", Name: "Python requests", Syntax: "text", Generate: Python},
	{ID: "go", Name: "Go net/http", Syntax: "text", Generate: Go},
	{ID: "java", Name: "Java HttpClient", Syntax: "text", Generate: Java},
	{ID: "csharp", Name: "C# HttpClient", Syntax: "text", Generate: CSharp},
}

// Languages returns the languages, in the order the panel lists them.
func Languages() []Language { return slices.Clone(languages) }

// LanguageByID returns the language named id; "" is LanguageCurl. It
// fails with CodeUnknownLanguage.
func LanguageByID(id string) (Language, error) {
	return byID(languages, id, LanguageCurl, func(l Language) string { return l.ID })
}

// GRPCLanguage is a snippet target of the gRPC editor's Code tab
// (GEN-10): a Language over a GRPCRequest.
type GRPCLanguage struct {
	ID       string
	Name     string
	Syntax   string
	Generate func(r GRPCRequest) (string, error)
}

// LanguageGrpcurl is the ID of the grpcurl command, the gRPC default.
const LanguageGrpcurl = "grpcurl"

var grpcLanguages = []GRPCLanguage{
	{ID: LanguageGrpcurl, Name: "grpcurl", Syntax: "shell", Generate: Grpcurl},
}

// GRPCLanguages returns the gRPC languages, in the order the panel lists
// them.
func GRPCLanguages() []GRPCLanguage { return slices.Clone(grpcLanguages) }

// GRPCLanguageByID returns the gRPC language named id; "" is
// LanguageGrpcurl. It fails with CodeUnknownLanguage.
func GRPCLanguageByID(id string) (GRPCLanguage, error) {
	return byID(grpcLanguages, id, LanguageGrpcurl, func(l GRPCLanguage) string { return l.ID })
}

// WebSocketLanguage is a snippet target of the WebSocket editor's Code
// tab (GEN-11): a Language over a WebSocketRequest.
type WebSocketLanguage struct {
	ID       string
	Name     string
	Syntax   string
	Generate func(r WebSocketRequest) (string, error)
}

// LanguageWebsocat is the ID of the websocat command, the WebSocket
// default.
const LanguageWebsocat = "websocat"

var webSocketLanguages = []WebSocketLanguage{
	{ID: LanguageWebsocat, Name: "websocat", Syntax: "shell", Generate: Websocat},
	{ID: "wscat", Name: "wscat", Syntax: "shell", Generate: Wscat},
}

// WebSocketLanguages returns the WebSocket languages, in the order the
// panel lists them.
func WebSocketLanguages() []WebSocketLanguage { return slices.Clone(webSocketLanguages) }

// WebSocketLanguageByID returns the WebSocket language named id; "" is
// LanguageWebsocat. It fails with CodeUnknownLanguage.
func WebSocketLanguageByID(id string) (WebSocketLanguage, error) {
	return byID(webSocketLanguages, id, LanguageWebsocat, func(l WebSocketLanguage) string { return l.ID })
}

// byID finds the language of list named id ("" is def).
func byID[L any](list []L, id, def string, idOf func(L) string) (L, error) {
	if id == "" {
		id = def
	}
	for _, l := range list {
		if idOf(l) == id {
			return l, nil
		}
	}
	var zero L
	return zero, errs.New(errs.InvalidArgument, "This version of PostSonde cannot write code in this language.").
		WithCode(CodeUnknownLanguage).
		WithDetail("language", id)
}
