package wsserver

import (
	"bytes"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/sorrelfox/postsonde/internal/core/errs"
)

// Auto-reply rules (SSRV-02): a text message a client sends is matched
// against the rules in order; the first enabled rule that matches sends
// its reply to that client. When no rule matches (and for every binary
// message), Echo sends the message back. See docs/servers.md.

// How a rule matches a text message (Rule.Match).
const (
	// MatchExact matches a message equal to the pattern, byte for byte.
	MatchExact = "exact"
	// MatchContains matches a message that contains the pattern.
	MatchContains = "contains"
	// MatchRegex matches a message in which the pattern, a Go (RE2)
	// regular expression, finds a match; anchor it with ^ and $ to match
	// the whole message. The reply can use its capture groups.
	MatchRegex = "regex"
)

// Limits of the rules.
const (
	// MaxRules is the most rules a server has.
	MaxRules = 100
	// MaxRulePatternBytes is the longest pattern.
	MaxRulePatternBytes = 64 << 10
	// MaxRuleReplyBytes is the longest reply as written.
	MaxRuleReplyBytes = 1 << 20
	// MaxRuleDelayMs is the longest delay of a reply (one minute).
	MaxRuleDelayMs = 60_000
)

// Rule is an auto-reply rule of a WebSocket server.
type Rule struct {
	// Enabled rules are matched; disabled ones are kept but skipped.
	Enabled bool
	// Match is MatchExact (the default for ""), MatchContains or
	// MatchRegex.
	Match string
	// Pattern is the text, or the regular expression, to match.
	Pattern string
	// Reply is the text message sent back. With MatchRegex, $1, ${1},
	// $name and ${name} are replaced by the capture groups of the first
	// match ($0 is the whole match, $$ a literal $; a group that did not
	// take part or does not exist is empty). With the other modes it is
	// sent as written.
	Reply string
	// DelayMs holds the reply back, in milliseconds (0 to
	// MaxRuleDelayMs). Messages keep being read meanwhile.
	DelayMs int
}

// RuleSet is the compiled enabled rules of a configuration.
type RuleSet struct {
	rules []compiledRule
}

type compiledRule struct {
	index int // in Config.Rules
	rule  Rule
	re    *regexp.Regexp
}

// AutoReply is the reply the rules give to a message.
type AutoReply struct {
	// Rule is the index of the rule that matched, in Config.Rules.
	Rule int
	// Data is the text to send.
	Data []byte
	// Delay is how long to wait before sending it.
	Delay time.Duration
}

// CompileRules checks rules (as Normalize does) and compiles the enabled
// ones. A nil or empty list gives a RuleSet that matches nothing.
func CompileRules(rules []Rule) (*RuleSet, error) {
	set := &RuleSet{}
	if len(rules) > MaxRules {
		return nil, invalidConfig("rules", fmt.Sprintf("A WebSocket server has at most %d auto-reply rules.", MaxRules))
	}
	for i, r := range rules {
		c, err := compileRule(i, r)
		if err != nil {
			return nil, err
		}
		if r.Enabled {
			set.rules = append(set.rules, c)
		}
	}
	return set, nil
}

// normalizeRule returns r with its default match mode, or an error naming
// the rule and the field.
func normalizeRule(i int, r Rule) (Rule, error) {
	r.Match = strings.TrimSpace(r.Match)
	if r.Match == "" {
		r.Match = MatchExact
	}
	switch r.Match {
	case MatchExact, MatchContains, MatchRegex:
	default:
		return Rule{}, invalidRule(i, "match", fmt.Sprintf("Rule %d: %q is not a way to match; use exact, contains or regex.", i+1, r.Match))
	}
	if len(r.Pattern) > MaxRulePatternBytes {
		return Rule{}, invalidRule(i, "pattern", fmt.Sprintf("Rule %d: the pattern is longer than %d KiB.", i+1, MaxRulePatternBytes>>10))
	}
	if !utf8.ValidString(r.Pattern) {
		return Rule{}, invalidRule(i, "pattern", fmt.Sprintf("Rule %d: the pattern is not valid UTF-8.", i+1))
	}
	if len(r.Reply) > MaxRuleReplyBytes {
		return Rule{}, invalidRule(i, "reply", fmt.Sprintf("Rule %d: the reply is longer than %d MiB.", i+1, MaxRuleReplyBytes>>20))
	}
	if !utf8.ValidString(r.Reply) {
		return Rule{}, invalidRule(i, "reply", fmt.Sprintf("Rule %d: the reply is not valid UTF-8.", i+1))
	}
	if r.DelayMs < 0 || r.DelayMs > MaxRuleDelayMs {
		return Rule{}, invalidRule(i, "delayMs", fmt.Sprintf("Rule %d: the delay must be from 0 to %d ms.", i+1, MaxRuleDelayMs))
	}
	return r, nil
}

func compileRule(i int, r Rule) (compiledRule, error) {
	r, err := normalizeRule(i, r)
	if err != nil {
		return compiledRule{}, err
	}
	c := compiledRule{index: i, rule: r}
	if r.Match == MatchRegex {
		re, err := regexp.Compile(r.Pattern)
		if err != nil {
			return compiledRule{}, invalidRule(i, "pattern", fmt.Sprintf("Rule %d: the regular expression is not valid: %s.", i+1, regexErrorText(err)))
		}
		c.re = re
	}
	return c, nil
}

// regexErrorText is the part of a regexp error after "error parsing
// regexp: ", which names the problem and the offending text.
func regexErrorText(err error) string {
	return strings.TrimPrefix(err.Error(), "error parsing regexp: ")
}

// Match returns the reply of the first enabled rule that matches the text
// message msg.
func (s *RuleSet) Match(msg []byte) (AutoReply, bool) {
	if s == nil {
		return AutoReply{}, false
	}
	for _, c := range s.rules {
		var data []byte
		switch c.rule.Match {
		case MatchExact:
			if string(msg) != c.rule.Pattern {
				continue
			}
			data = []byte(c.rule.Reply)
		case MatchContains:
			if !bytes.Contains(msg, []byte(c.rule.Pattern)) {
				continue
			}
			data = []byte(c.rule.Reply)
		case MatchRegex:
			m := c.re.FindSubmatchIndex(msg)
			if m == nil {
				continue
			}
			data = c.re.Expand([]byte{}, []byte(c.rule.Reply), msg, m)
		}
		return AutoReply{Rule: c.index, Data: data, Delay: time.Duration(c.rule.DelayMs) * time.Millisecond}, true
	}
	return AutoReply{}, false
}

func invalidRule(i int, field, msg string) *errs.Error {
	return invalidConfig("rules", msg).WithDetail("ruleIndex", strconv.Itoa(i)).WithDetail("ruleField", field)
}
