package template

import (
	"encoding/json"
	"errors"
	"slices"
	"testing"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

func TestParsePrompt(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		want    Prompt
		notOne  bool
		invalid bool
	}{
		{name: "$prompt", want: Prompt{Label: DefaultPromptLabel}},
		{name: "$prompt Token", want: Prompt{Label: "Token"}},
		{name: `$prompt "API token"`, want: Prompt{Label: "API token"}},
		{name: `$prompt "API token" secret`, want: Prompt{Label: "API token", Secret: true}},
		{name: `$prompt "User id" 42`, want: Prompt{Label: "User id", Default: "42"}},
		{name: `$prompt Pass "hunter 2" secret`, want: Prompt{Label: "Pass", Default: "hunter 2", Secret: true}},
		{name: `$prompt Pass secret "x"`, want: Prompt{Label: "Pass", Default: "x", Secret: true}},
		{name: `$prompt Word "secret"`, want: Prompt{Label: "Word", Default: "secret"}},
		{name: "$prompt\tToken", want: Prompt{Label: "Token"}},
		{name: "$promptly", notOne: true},
		{name: "$uuid", notOne: true},
		{name: "prompt", notOne: true},
		{name: `$prompt ""`, invalid: true},
		{name: `$prompt "open`, invalid: true},
		{name: `$prompt a b c`, invalid: true},
		{name: `$prompt a secret secret`, invalid: true},
	}
	for _, tt := range tests {
		p, ok, err := ParsePrompt(tt.name)
		switch {
		case tt.notOne:
			if ok {
				t.Errorf("ParsePrompt(%q) ok, want not a prompt", tt.name)
			}
		case tt.invalid:
			if !ok || err == nil {
				t.Errorf("ParsePrompt(%q) = ok %v err %v, want an error", tt.name, ok, err)
			}
			if IsDynamic(tt.name) {
				t.Errorf("IsDynamic(%q) = true", tt.name)
			}
		default:
			if !ok || err != nil || p != tt.want {
				t.Errorf("ParsePrompt(%q) = %+v, %v, %v; want %+v", tt.name, p, ok, err, tt.want)
			}
			if !IsDynamic(tt.name) {
				t.Errorf("IsDynamic(%q) = false", tt.name)
			}
		}
	}
	if !slices.Contains(DynamicNames(), PromptName) || DynamicDescription(PromptName) == "" {
		t.Error("$prompt is not listed with the dynamic values")
	}
}

func TestPromptAnswered(t *testing.T) {
	t.Parallel()
	r := Resolver{Prompts: map[string]string{"API token": "t0k", "Id": "7"}}
	res := r.Render(`Bearer {{$prompt "API token" secret}} id={{ $prompt Id 1 }} again={{$prompt Id}}`)
	if err := res.Err(); err != nil {
		t.Fatal(err)
	}
	if want := "Bearer t0k id=7 again=7"; res.Text != want {
		t.Errorf("Text = %q, want %q", res.Text, want)
	}
	if !res.Secret || !res.Refs[0].Secret || res.Refs[1].Secret {
		t.Errorf("secrecy: result %v, refs %v %v", res.Secret, res.Refs[0].Secret, res.Refs[1].Secret)
	}
	if res.Refs[0].Scope != PromptScope {
		t.Errorf("Scope = %q", res.Refs[0].Scope)
	}

	// Inside a variable's value and as a pipe's input.
	r.Scopes = Chain{Vars("Env", map[string]string{"auth": `{{$prompt "API token" secret}}`})}
	if got := renderOne(t, r, "{{auth}} {{ $prompt Id | base64 }}"); got != "t0k Nw==" {
		t.Errorf("got %q", got)
	}

	// A scope that defines the name wins, as for every dynamic value.
	r.Scopes = Chain{Vars("Env", map[string]string{"$prompt Id": "scope"})}
	if got := renderOne(t, r, "{{$prompt Id}}"); got != "scope" {
		t.Errorf("got %q", got)
	}
}

func TestPromptUnanswered(t *testing.T) {
	t.Parallel()
	r := Resolver{Scopes: Chain{Vars("Env", map[string]string{"auth": `{{$prompt "API token" secret}}`})}}
	src := `{{auth}} {{$prompt Id 1}} {{$prompt "API token"}} {{$prompt Id}}`
	res := r.Render(src)
	if want := `{{$prompt "API token" secret}} {{$prompt Id 1}} {{$prompt "API token"}} {{$prompt Id}}`; res.Text != want {
		t.Errorf("Text = %q, want the prompts as written", res.Text)
	}
	got := PromptsOf(res.Problems)
	want := []Prompt{{Label: "API token", Secret: true}, {Label: "Id", Default: "1"}}
	if !slices.Equal(got, want) {
		t.Errorf("PromptsOf = %+v, want %+v", got, want)
	}
	err := res.Err()
	e, ok := errors.AsType[*errs.Error](err)
	if !ok || e.Code != CodePrompt || errs.KindOf(err) != errs.InvalidArgument {
		t.Fatalf("Err() = %v, want %s", err, CodePrompt)
	}
	var detail []Prompt
	if err := json.Unmarshal([]byte(e.Details["prompts"]), &detail); err != nil || !slices.Equal(detail, want) {
		t.Errorf("details.prompts = %q (%v), want %+v", e.Details["prompts"], err, want)
	}
	if e.Message != `2 values are asked for when the request is sent: "API token", "Id".` {
		t.Errorf("Message = %q", e.Message)
	}

	// Undefined names keep their priority in Err.
	res = Resolver{}.Render("{{$prompt}} {{nope}}")
	if e, _ := errors.AsType[*errs.Error](res.Err()); e == nil || e.Code != CodeUnresolved {
		t.Errorf("Err() = %v, want %s", res.Err(), CodeUnresolved)
	}

	// Invalid arguments are an invalid dynamic value.
	res = Resolver{}.Render("{{$prompt a b c}}")
	if e, _ := errors.AsType[*errs.Error](res.Err()); e == nil || e.Code != CodeInvalidDynamic {
		t.Errorf("Err() = %v, want %s", res.Err(), CodeInvalidDynamic)
	}
}
