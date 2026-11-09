package template

import (
	"regexp"
	"strings"
	"testing"
)

// TestFakeKnownOutputs pins every fake-data value with a random source of
// zero bytes: each pick is the first entry of its list and each number the
// lowest of its range.
func TestFakeKnownOutputs(t *testing.T) {
	t.Parallel()
	sentence := "Lorem lorem lorem lorem."
	paragraph := sentence + " " + sentence + " " + sentence
	tests := []struct{ src, want string }{
		{"{{$randomFirstName}}", "Ada"},
		{"{{$randomLastName}}", "Andersen"},
		{"{{$randomFullName}}", "Ada Andersen"},
		{"{{$randomNamePrefix}}", "Mr."},
		{"{{$randomNameSuffix}}", "Jr."},
		{"{{$randomUserName}}", "Ada.Andersen1"},
		{"{{$randomJobTitle}}", "Junior Data Engineer"},
		{"{{$randomEmail}}", "ada.andersen@example.com"},
		{"{{$randomExampleEmail}}", "ada.andersen@example.com"},
		{"{{$randomDomainWord}}", "acme"},
		{"{{$randomDomainSuffix}}", "test"},
		{"{{$randomDomainName}}", "acme.test"},
		{"{{$randomUrl}}", "https://acme.test"},
		{"{{$randomProtocol}}", "http"},
		{"{{$randomIP}}", "0.0.0.0"},
		{"{{$randomIPV6}}", "0000:0000:0000:0000:0000:0000:0000:0000"},
		{"{{$randomMACAddress}}", "00:00:00:00:00:00"},
		{"{{$randomPassword}}", "AAAAAAAAAAAAAAA"},
		{"{{$randomPassword 4}}", "AAAA"},
		{"{{$randomSemver}}", "0.0.0"},
		{"{{$randomCity}}", "Amsterdam"},
		{"{{$randomStreetName}}", "Maple Street"},
		{"{{$randomStreetAddress}}", "1 Maple Street"},
		{"{{$randomCountry}}", "Argentina"},
		{"{{$randomCountryCode}}", "AR"},
		{"{{$randomLatitude}}", "-90.000000"},
		{"{{$randomLongitude}}", "-180.000000"},
		{"{{$randomPhoneNumber}}", "555-000-0000"},
		{"{{$randomPhoneNumberExt}}", "555-000-0000 x10"},
		{"{{$randomCompanyName}}", "Andersen Labs Inc"},
		{"{{$randomCompanySuffix}}", "Inc"},
		{"{{$randomPrice}}", "1.00"},
		{"{{$randomCurrencyCode}}", "AUD"},
		{"{{$randomLocale}}", "en-US"},
		{"{{$randomAbbreviation}}", "API"},
		{"{{$randomColor}}", "black"},
		{"{{$randomHexColor}}", "#000000"},
		{"{{$randomWeekday}}", "Monday"},
		{"{{$randomMonth}}", "January"},
		{"{{$randomDatePast}}", "2025-09-19T14:05:09.000Z"},
		{"{{$randomDateFuture}}", "2026-09-19T14:05:10.000Z"},
		{"{{$randomDateRecent}}", "2026-09-18T14:05:09.000Z"},
		{"{{$randomWord}}", "anchor"},
		{"{{$randomWords}}", "anchor anchor anchor"},
		{"{{$randomWords 2}}", "anchor anchor"},
		{"{{$randomLoremWord}}", "lorem"},
		{"{{$randomLoremWords}}", "lorem lorem lorem"},
		{"{{$randomLoremWords 1}}", "lorem"},
		{"{{$randomLoremSlug}}", "lorem-lorem-lorem"},
		{"{{$randomLoremSentence}}", sentence},
		{"{{$randomLoremSentences 2}}", sentence + " " + sentence},
		{"{{$randomLoremSentences}}", sentence + " " + sentence + " " + sentence},
		{"{{$randomLoremLines 2}}", sentence + "\n" + sentence},
		{"{{$randomLoremParagraph}}", paragraph},
		{"{{$randomLoremText}}", paragraph},
		{"{{$randomLoremParagraphs 2}}", paragraph + "\n\n" + paragraph},
	}
	seen := map[string]bool{}
	for _, tt := range tests {
		seen[strings.Fields(strings.Trim(tt.src, "{}"))[0]] = true
		t.Run(tt.src, func(t *testing.T) {
			t.Parallel()
			r := Resolver{Now: dynResolver().Now, Rand: zeroReader{}}
			if got := renderOne(t, r, tt.src); got != tt.want {
				t.Errorf("Render(%q) = %q, want %q", tt.src, got, tt.want)
			}
		})
	}
	for name := range fakeValues {
		if !seen[name] {
			t.Errorf("%s has no known-output test", name)
		}
	}
}

// TestFakeShapes draws each value many times from a seeded source and
// checks its shape, that it varies, and that the same seed repeats it.
func TestFakeShapes(t *testing.T) {
	t.Parallel()
	word := `[A-Za-zÀ-ÿ]+`
	iso := `\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.000Z`
	tests := []struct {
		src, re string
		varies  bool
	}{
		{"{{$randomFullName}}", `^` + word + ` ` + word + `$`, true},
		{"{{$randomUserName}}", `^` + word + `\.` + word + `\d{1,2}$`, true},
		{"{{$randomEmail}}", `^[a-z]+\.[a-z]+@example\.(com|org|net)$`, true},
		{"{{$randomUrl}}", `^https://[a-z]+\.(test|example|invalid)$`, true},
		{"{{$randomIP}}", `^\d{1,3}\.\d{1,3}\.\d{1,3}\.\d{1,3}$`, true},
		{"{{$randomIPV6}}", `^[0-9a-f]{4}(:[0-9a-f]{4}){7}$`, true},
		{"{{$randomMACAddress}}", `^[0-9a-f]{2}(:[0-9a-f]{2}){5}$`, true},
		{"{{$randomPassword}}", `^[A-Za-z0-9]{15}$`, true},
		{"{{$randomSemver}}", `^\d\.\d{1,2}\.\d{1,2}$`, true},
		{"{{$randomStreetAddress}}", `^\d{1,4} [A-Za-z]+ [A-Za-z]+$`, true},
		{"{{$randomCountryCode}}", `^[A-Z]{2}$`, true},
		{"{{$randomLatitude}}", `^-?\d{1,2}\.\d{6}$`, true},
		{"{{$randomLongitude}}", `^-?\d{1,3}\.\d{6}$`, true},
		{"{{$randomPhoneNumber}}", `^555-\d{3}-\d{4}$`, true},
		{"{{$randomPrice}}", `^\d{1,4}\.\d{2}$`, true},
		{"{{$randomHexColor}}", `^#[0-9a-f]{6}$`, true},
		{"{{$randomDatePast}}", `^` + iso + `$`, true},
		{"{{$randomLoremSentence}}", `^[A-Z][a-z]*( [a-z]+){3,9}\.$`, true},
		{"{{$randomLoremSlug 4}}", `^[a-z]+(-[a-z]+){3}$`, true},
		{"{{$randomWords 5}}", `^[a-z]+( [a-z]+){4}$`, true},
		{"{{$randomProtocol}}", `^https?$`, true},
	}
	for _, tt := range tests {
		t.Run(tt.src, func(t *testing.T) {
			t.Parallel()
			re := regexp.MustCompile(tt.re)
			r := dynResolver()
			distinct := map[string]bool{}
			for range 100 {
				v := renderOne(t, r, tt.src)
				if !re.MatchString(v) {
					t.Fatalf("%s = %q, want %s", tt.src, v, tt.re)
				}
				distinct[v] = true
			}
			if tt.varies && len(distinct) < 2 {
				t.Errorf("%s never varied: %v", tt.src, distinct)
			}
			if a, b := renderOne(t, dynResolver(), tt.src), renderOne(t, dynResolver(), tt.src); a != b {
				t.Errorf("the same seed gave %q and %q", a, b)
			}
		})
	}

	// Dates stay in their range around the clock.
	r := dynResolver()
	for range 200 {
		past, future, recent := renderOne(t, r, "{{$randomDatePast}}"), renderOne(t, r, "{{$randomDateFuture}}"), renderOne(t, r, "{{$randomDateRecent}}")
		now := fixedNow.UTC().Format(isoLayout)
		if past >= now || past < "2025-09-19T14:05:09.000Z" || future <= now || future > "2027-09-19T14:05:09.000Z" || recent >= now || recent < "2026-09-18T14:05:09.000Z" {
			t.Fatalf("past %s, future %s, recent %s around %s", past, future, recent, now)
		}
	}

	// Every list entry is reachable.
	seen := map[string]bool{}
	for range 2000 {
		seen[renderOne(t, r, "{{$randomCity}}")] = true
	}
	if len(seen) != len(cities) {
		t.Errorf("$randomCity gave %d of %d cities", len(seen), len(cities))
	}
}

func TestFakeInvalid(t *testing.T) {
	t.Parallel()
	tests := []struct{ src, msg string }{
		{"{{$randomFirstName x}}", "it takes no arguments"},
		{"{{$randomEmail a b}}", "it takes no arguments"},
		{"{{$randomWords 0}}", "the count must be a whole number from 1 to 100"},
		{"{{$randomLoremParagraphs 101}}", "the count must be a whole number from 1 to 100"},
		{"{{$randomLoremLines x}}", "the count must be a whole number from 1 to 100"},
		{"{{$randomLoremSentences 1 2}}", "it takes at most a count"},
		{"{{$randomPassword 0}}", "the length must be a whole number from 1 to 4096"},
	}
	for _, tt := range tests {
		res := dynResolver().Render(tt.src)
		if len(res.Problems) != 1 || res.Problems[0].Kind != ProblemInvalidDynamic || res.Problems[0].Message != tt.msg {
			t.Errorf("Render(%q) problems = %+v, want %q", tt.src, res.Problems, tt.msg)
		}
	}
	r := Resolver{Rand: failingReader{}}
	for name := range fakeValues {
		res := r.Render("{{" + name + "}}")
		if len(res.Problems) != 1 || res.Problems[0].Message != "no random data is available" {
			t.Errorf("%s with a failing source: %+v", name, res.Problems)
		}
	}
}

// TestFakeNotSecret: generated values never count as secret, but a hash of
// a secret does even when salted with a fake value.
func TestFakeNotSecret(t *testing.T) {
	t.Parallel()
	res := dynResolver().Render("{{$randomEmail}} {{$randomLoremWords 2 | base64}}")
	if res.Err() != nil || res.Secret {
		t.Errorf("err %v, secret %v", res.Err(), res.Secret)
	}
	res = dynResolver(Chain{MapScope{Label: "s", Variables: map[string]Variable{"pw": {Value: "x", Secret: true}}}}...).
		Render("{{$randomUserName | hmacSha256 pw}}")
	if res.Err() != nil || !res.Secret {
		t.Errorf("err %v, secret %v", res.Err(), res.Secret)
	}
}
