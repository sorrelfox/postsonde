package wsserver_test

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/sorrelfox/postsonde/internal/core/errs"
	"github.com/sorrelfox/postsonde/internal/core/servers/wsserver"
)

func TestRuleMatching(t *testing.T) {
	t.Parallel()
	set, err := wsserver.CompileRules([]wsserver.Rule{
		{Enabled: true, Match: "", Pattern: "ping", Reply: "pong"},
		{Enabled: false, Match: wsserver.MatchContains, Pattern: "hello", Reply: "disabled"},
		{Enabled: true, Match: wsserver.MatchContains, Pattern: "hello", Reply: "hi $1", DelayMs: 250},
		{Enabled: true, Match: wsserver.MatchRegex, Pattern: `^get (\d+)/(?P<field>\w+)$`, Reply: `{"id": $1, "field": "${field}", "all": "$0", "none": "$9", "cost": "$$5", "glued": "${1}x"}`},
		{Enabled: true, Match: wsserver.MatchRegex, Pattern: `(a)|(b)`, Reply: "[$1][$2]"},
		{Enabled: true, Match: wsserver.MatchRegex, Pattern: `.*`, Reply: "fallback"},
	})
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		msg   string
		rule  int
		reply string
		delay time.Duration
	}{
		{"ping", 0, "pong", 0},
		// Exact is byte for byte; the next rules still get a chance.
		{"ping ", 5, "fallback", 0},
		{"Ping", 5, "fallback", 0},
		// Contains: the reply is sent as written, $ and all; the disabled rule is skipped.
		{"say hello there", 2, "hi $1", 250 * time.Millisecond},
		{"get 42/name", 3, `{"id": 42, "field": "name", "all": "get 42/name", "none": "", "cost": "$5", "glued": "42x"}`, 0},
		// The first match only; a group that did not take part is empty.
		{"xbxa", 4, "[][b]", 0},
		{"", 5, "fallback", 0},
	}
	for _, tt := range tests {
		got, ok := set.Match([]byte(tt.msg))
		if !ok || got.Rule != tt.rule || string(got.Data) != tt.reply || got.Delay != tt.delay {
			t.Errorf("Match(%q) = %+v %q, %v; want rule %d %q %v", tt.msg, got, got.Data, ok, tt.rule, tt.reply, tt.delay)
		}
	}

	// First match wins, in order.
	set, _ = wsserver.CompileRules([]wsserver.Rule{
		{Enabled: true, Match: wsserver.MatchContains, Pattern: "a", Reply: "first"},
		{Enabled: true, Match: wsserver.MatchExact, Pattern: "abc", Reply: "second"},
	})
	if got, _ := set.Match([]byte("abc")); string(got.Data) != "first" {
		t.Errorf("order = %q", got.Data)
	}
	if _, ok := set.Match([]byte("xyz")); ok {
		t.Error("no rule should match xyz")
	}
	var none *wsserver.RuleSet
	if _, ok := none.Match([]byte("x")); ok {
		t.Error("a nil set matched")
	}
	empty, err := wsserver.CompileRules(nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := empty.Match([]byte("x")); ok {
		t.Error("an empty set matched")
	}
}

func TestRuleValidation(t *testing.T) {
	t.Parallel()
	bad := []struct {
		rules []wsserver.Rule
		index string
		field string
		text  string
	}{
		{[]wsserver.Rule{{Enabled: true, Pattern: "ok"}, {Enabled: true, Match: wsserver.MatchRegex, Pattern: "(unclosed"}}, "1", "pattern", "Rule 2: the regular expression is not valid: missing closing ): `(unclosed`."},
		// A disabled rule is checked too: it is saved.
		{[]wsserver.Rule{{Match: wsserver.MatchRegex, Pattern: "a**"}}, "0", "pattern", "regular expression is not valid"},
		{[]wsserver.Rule{{Match: "glob"}}, "0", "match", `"glob" is not a way to match`},
		{[]wsserver.Rule{{DelayMs: -1}}, "0", "delayMs", "delay"},
		{[]wsserver.Rule{{DelayMs: wsserver.MaxRuleDelayMs + 1}}, "0", "delayMs", "delay"},
		{[]wsserver.Rule{{Reply: "\xff"}}, "0", "reply", "UTF-8"},
		{[]wsserver.Rule{{Pattern: strings.Repeat("a", wsserver.MaxRulePatternBytes+1)}}, "0", "pattern", "longer"},
		{[]wsserver.Rule{{Reply: strings.Repeat("a", wsserver.MaxRuleReplyBytes+1)}}, "0", "reply", "longer"},
		{make([]wsserver.Rule, wsserver.MaxRules+1), "", "", "at most"},
	}
	for _, tt := range bad {
		_, err := wsserver.Normalize(wsserver.Config{Rules: tt.rules})
		e, ok := errors.AsType[*errs.Error](err)
		if !ok || e.Code != wsserver.CodeInvalidConfig || e.Details["field"] != "rules" || e.Details["ruleIndex"] != tt.index ||
			e.Details["ruleField"] != tt.field || !strings.Contains(e.Message, tt.text) {
			t.Errorf("Normalize(%d rules) = %v %+v; want index %q field %q with %q", len(tt.rules), err, e, tt.index, tt.field, tt.text)
		}
	}

	got, err := wsserver.Normalize(wsserver.Config{Rules: []wsserver.Rule{{Enabled: true, Match: " contains ", Pattern: " x ", Reply: " y "}, {Pattern: "z"}}})
	if err != nil {
		t.Fatal(err)
	}
	want := []wsserver.Rule{{Enabled: true, Match: wsserver.MatchContains, Pattern: " x ", Reply: " y "}, {Match: wsserver.MatchExact, Pattern: "z"}}
	if len(got.Rules) != 2 || got.Rules[0] != want[0] || got.Rules[1] != want[1] {
		t.Errorf("normalized rules = %+v", got.Rules)
	}
	if got, _ := wsserver.Normalize(wsserver.Config{}); got.Rules == nil || len(got.Rules) != 0 {
		t.Errorf("no rules = %#v", got.Rules)
	}
}
