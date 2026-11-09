package validate

import (
	"slices"
	"testing"
)

// Cases from graphql-js's suggestionList-test.ts and naturalCompare-test.ts.
func TestSuggestionList(t *testing.T) {
	for _, tc := range []struct {
		input   string
		options []string
		want    []string
	}{
		{"", nil, nil},
		{"", []string{"a"}, []string{"a"}},
		{"input", nil, nil},
		{"greenish", []string{"green"}, []string{"green"}},
		{"green", []string{"greenish"}, []string{"greenish"}},
		{"verylongstring", []string{"VERYLONGSTRING"}, []string{"VERYLONGSTRING"}},
		{"VERYLONGSTRING", []string{"verylongstring"}, []string{"verylongstring"}},
		{"VERYLONGSTRING", []string{"VeryLongString"}, []string{"VeryLongString"}},
		{"aaaa", []string{"aaab"}, []string{"aaab"}},
		{"aaaa", []string{"aabb"}, []string{"aabb"}},
		{"aaaa", []string{"abbb"}, nil},
		{"ab", []string{"ca"}, nil},
		{"agr", []string{"arg"}, []string{"arg"}},
		{"214365879", []string{"123456789"}, []string{"123456789"}},
		{"abc", []string{"a", "ab", "abc"}, []string{"abc", "ab", "a"}},
		{"a", []string{"az", "ax", "ay"}, []string{"ax", "ay", "az"}},
		{"boo", []string{"moo", "foo", "zoo"}, []string{"foo", "moo", "zoo"}},
		{"abc", []string{"a1", "a12", "a2"}, []string{"a1", "a2", "a12"}},
		{"héllo", []string{"hello", "HÉLLO"}, []string{"HÉLLO", "hello"}},
	} {
		got := SuggestionList(tc.input, tc.options)
		if !slices.Equal(got, tc.want) {
			t.Errorf("SuggestionList(%q, %q) = %q, want %q", tc.input, tc.options, got, tc.want)
		}
	}
}

func TestSuggestCapsAtFive(t *testing.T) {
	got := suggest("a", []string{"a1", "a2", "a3", "a4", "a5", "a6"})
	if want := []string{"a1", "a2", "a3", "a4", "a5"}; !slices.Equal(got, want) {
		t.Errorf("got %q, want %q", got, want)
	}
	if got := suggest("zzz", []string{"a"}); got != nil {
		t.Errorf("no match = %#v, want nil", got)
	}
}

func TestNaturalCompare(t *testing.T) {
	for _, tc := range []struct {
		a, b string
		sign int
	}{
		{"", "", 0},
		{"a", "a", 0},
		{"a", "b", -1},
		{"", "a", -1},
		{"a", "ab", -1},
		{"0", "0", 0},
		{"1", "2", -1},
		{"2", "10", -1},
		{"a2", "a10", -1},
		{"a10", "a2", 1},
		{"a02", "a1", -1}, // "0" ends the number: 0 < 1
		{"a" + "99999999999999999999999", "a" + "99999999999999999999998", 0},
		{"1a", "1b", -1},
	} {
		got := naturalCompare(tc.a, tc.b)
		if sign(got) != tc.sign {
			t.Errorf("naturalCompare(%q, %q) = %d, want sign %d", tc.a, tc.b, got, tc.sign)
		}
	}
}

func sign(n int) int {
	switch {
	case n < 0:
		return -1
	case n > 0:
		return 1
	}
	return 0
}

func TestDidYouMean(t *testing.T) {
	for _, tc := range []struct {
		sub  string
		s    []string
		want string
	}{
		{"", nil, ""},
		{"", []string{"a"}, ` Did you mean "a"?`},
		{"", []string{"a", "b"}, ` Did you mean "a" or "b"?`},
		{"", []string{"a", "b", "c"}, ` Did you mean "a", "b", or "c"?`},
		{"", []string{"a", "b", "c", "d", "e", "f"}, ` Did you mean "a", "b", "c", "d", or "e"?`},
		{"the enum value", []string{"A"}, ` Did you mean the enum value "A"?`},
	} {
		if got := didYouMean(tc.sub, tc.s); got != tc.want {
			t.Errorf("didYouMean(%q, %q) = %q, want %q", tc.sub, tc.s, got, tc.want)
		}
	}
}
