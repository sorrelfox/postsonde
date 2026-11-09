package template

import (
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strconv"
	"strings"
	"time"
)

// Fake data (DYN-05): dynamic values that pick from small built-in word
// lists, so requests can carry realistic-looking names, emails and text
// without a faker library. The names are Postman's, so imported
// collections keep working. Every pick uses the Resolver's Rand, so a
// seeded source gives the same values on every run. Host names use the
// reserved .test and example.* domains (RFC 2606): a fake email or URL
// never reaches a real person or server.

// MaxFakeCount bounds the count argument of the word and lorem values.
const MaxFakeCount = 100

// fakeValue is one fake-data dynamic value: its generator and what it
// generates, for hover and completion.
type fakeValue struct {
	fn   dynamicFunc
	desc string
}

var fakeValues = map[string]fakeValue{
	// People.
	"$randomFirstName":  {pickOne(firstNames), "A random first name (Ada)"},
	"$randomLastName":   {pickOne(lastNames), "A random last name (Lovelace)"},
	"$randomFullName":   {fakeFullName, "A random first and last name (Ada Lovelace)"},
	"$randomNamePrefix": {pickOne(namePrefixes), "A random name prefix (Dr.)"},
	"$randomNameSuffix": {pickOne(nameSuffixes), "A random name suffix (Jr.)"},
	"$randomUserName":   {fakeUserName, "A random user name (Ada.Lovelace42)"},
	"$randomJobTitle":   {fakeJobTitle, "A random job title (Senior Data Engineer)"},
	// Internet.
	"$randomEmail":        {fakeEmail, "A random email address at example.com, .org or .net"},
	"$randomExampleEmail": {fakeEmail, "A random email address at example.com, .org or .net"},
	"$randomDomainWord":   {pickOne(domainWords), "A random domain word (acme)"},
	"$randomDomainSuffix": {pickOne(domainSuffixes), "A random reserved domain suffix (test)"},
	"$randomDomainName":   {fakeDomainName, "A random domain name under a reserved suffix (acme.test)"},
	"$randomUrl":          {fakeURL, "A random https URL under a reserved domain (https://acme.test)"},
	"$randomProtocol":     {pickOne([]string{"http", "https"}), "http or https at random"},
	"$randomIP":           {fakeIPv4, "A random IPv4 address"},
	"$randomIPV6":         {fakeIPv6, "A random IPv6 address"},
	"$randomMACAddress":   {fakeMAC, "A random MAC address"},
	"$randomPassword":     {fakePassword, "A random password of letters and digits (length 15 or N)"},
	"$randomSemver":       {fakeSemver, "A random version number (2.14.3)"},
	// Places and phone.
	"$randomCity":           {pickOne(cities), "A random city name"},
	"$randomStreetName":     {fakeStreetName, "A random street name (Maple Avenue)"},
	"$randomStreetAddress":  {fakeStreetAddress, "A random street address (742 Maple Avenue)"},
	"$randomCountry":        {fakeCountry(false), "A random country name"},
	"$randomCountryCode":    {fakeCountry(true), "A random two-letter country code (ISO 3166)"},
	"$randomLatitude":       {fakeCoordinate(90), "A random latitude, -90 to 90"},
	"$randomLongitude":      {fakeCoordinate(180), "A random longitude, -180 to 180"},
	"$randomPhoneNumber":    {fakePhone(false), "A random phone number (555-123-4567)"},
	"$randomPhoneNumberExt": {fakePhone(true), "A random phone number with an extension (555-123-4567 x89)"},
	// Business.
	"$randomCompanyName":   {fakeCompanyName, "A random company name (Lovelace Labs Inc)"},
	"$randomCompanySuffix": {pickOne(companySuffixes), "A random company suffix (LLC)"},
	"$randomPrice":         {fakePrice, "A random price from 1.00 to 1000.00"},
	"$randomCurrencyCode":  {pickOne(currencyCodes), "A random currency code (EUR)"},
	"$randomLocale":        {pickOne(locales), "A random locale (fr-FR)"},
	"$randomAbbreviation":  {pickOne(abbreviations), "A random tech abbreviation (HTTP)"},
	// Colours and dates.
	"$randomColor":      {pickOne(colors), "A random colour name"},
	"$randomHexColor":   {fakeHexColor, "A random hex colour (#1a2b3c)"},
	"$randomWeekday":    {pickOne(weekdays), "A random day of the week"},
	"$randomMonth":      {pickOne(months), "A random month name"},
	"$randomDatePast":   {fakeDate(-365 * 24 * time.Hour), "A random time in the past year, ISO 8601"},
	"$randomDateFuture": {fakeDate(365 * 24 * time.Hour), "A random time in the next year, ISO 8601"},
	"$randomDateRecent": {fakeDate(-24 * time.Hour), "A random time in the past day, ISO 8601"},
	// Words and lorem ipsum.
	"$randomWord":            {pickOne(words), "A random English word"},
	"$randomWords":           {fakeWords(words, " "), "Random English words (3 or N)"},
	"$randomLoremWord":       {pickOne(loremWords), "A random lorem ipsum word"},
	"$randomLoremWords":      {fakeWords(loremWords, " "), "Random lorem ipsum words (3 or N)"},
	"$randomLoremSlug":       {fakeWords(loremWords, "-"), "Lorem ipsum words joined by hyphens (3 or N)"},
	"$randomLoremSentence":   {fakeSentenceValue, "A lorem ipsum sentence"},
	"$randomLoremSentences":  {fakeSentences(" "), "Lorem ipsum sentences (3 or N)"},
	"$randomLoremLines":      {fakeSentences("\n"), "Lorem ipsum sentences, one per line (3 or N)"},
	"$randomLoremParagraph":  {fakeParagraphValue, "A lorem ipsum paragraph"},
	"$randomLoremText":       {fakeParagraphValue, "A lorem ipsum paragraph"},
	"$randomLoremParagraphs": {fakeParagraphs, "Lorem ipsum paragraphs separated by blank lines (3 or N)"},
}

func init() {
	for name, v := range fakeValues {
		dynamicFuncs[name] = v.fn
		dynamicDescriptions[name] = v.desc
	}
}

// intRange returns a uniform value in [lo, hi].
func (g *dynamicGen) intRange(lo, hi int) (int, error) {
	v, err := g.intn(big.NewInt(int64(hi - lo + 1)))
	if err != nil {
		return 0, err
	}
	return lo + int(v.Int64()), nil
}

func (g *dynamicGen) pick(list []string) (string, error) {
	i, err := g.intRange(0, len(list)-1)
	if err != nil {
		return "", err
	}
	return list[i], nil
}

// picker picks from several lists in turn, remembering the first error.
type picker struct {
	g   *dynamicGen
	err error
}

func (p *picker) pick(list []string) string {
	if p.err != nil {
		return ""
	}
	s, err := p.g.pick(list)
	p.err = err
	return s
}

func (p *picker) intRange(lo, hi int) int {
	if p.err != nil {
		return 0
	}
	n, err := p.g.intRange(lo, hi)
	p.err = err
	return n
}

func pickOne(list []string) dynamicFunc {
	return func(g *dynamicGen, args []string) (string, error) {
		if err := noArgs(args); err != nil {
			return "", err
		}
		return g.pick(list)
	}
}

// countArg is the optional count of the word and lorem values.
func countArg(args []string, def int) (int, error) {
	switch len(args) {
	case 0:
		return def, nil
	case 1:
		n, err := strconv.Atoi(args[0])
		if err != nil || n < 1 || n > MaxFakeCount {
			return 0, fmt.Errorf("the count must be a whole number from 1 to %d", MaxFakeCount)
		}
		return n, nil
	}
	return 0, errors.New("it takes at most a count")
}

func fakeFullName(g *dynamicGen, args []string) (string, error) {
	if err := noArgs(args); err != nil {
		return "", err
	}
	p := &picker{g: g}
	s := p.pick(firstNames) + " " + p.pick(lastNames)
	return s, p.err
}

func fakeUserName(g *dynamicGen, args []string) (string, error) {
	if err := noArgs(args); err != nil {
		return "", err
	}
	p := &picker{g: g}
	s := p.pick(firstNames) + "." + p.pick(lastNames) + strconv.Itoa(p.intRange(1, 99))
	return s, p.err
}

func fakeJobTitle(g *dynamicGen, args []string) (string, error) {
	if err := noArgs(args); err != nil {
		return "", err
	}
	p := &picker{g: g}
	s := p.pick(jobLevels) + " " + p.pick(jobAreas) + " " + p.pick(jobRoles)
	return s, p.err
}

func fakeEmail(g *dynamicGen, args []string) (string, error) {
	if err := noArgs(args); err != nil {
		return "", err
	}
	p := &picker{g: g}
	s := strings.ToLower(p.pick(firstNames)+"."+p.pick(lastNames)) + "@" + p.pick(emailDomains)
	return s, p.err
}

func fakeDomainName(g *dynamicGen, args []string) (string, error) {
	if err := noArgs(args); err != nil {
		return "", err
	}
	p := &picker{g: g}
	s := p.pick(domainWords) + "." + p.pick(domainSuffixes)
	return s, p.err
}

func fakeURL(g *dynamicGen, args []string) (string, error) {
	d, err := fakeDomainName(g, args)
	if err != nil {
		return "", err
	}
	return "https://" + d, nil
}

func fakeIPv4(g *dynamicGen, args []string) (string, error) {
	if err := noArgs(args); err != nil {
		return "", err
	}
	b, err := g.bytes(4)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d.%d.%d.%d", b[0], b[1], b[2], b[3]), nil
}

func fakeIPv6(g *dynamicGen, args []string) (string, error) {
	if err := noArgs(args); err != nil {
		return "", err
	}
	b, err := g.bytes(16)
	if err != nil {
		return "", err
	}
	h := hex.EncodeToString(b)
	parts := make([]string, 8)
	for i := range parts {
		parts[i] = h[i*4 : i*4+4]
	}
	return strings.Join(parts, ":"), nil
}

func fakeMAC(g *dynamicGen, args []string) (string, error) {
	if err := noArgs(args); err != nil {
		return "", err
	}
	b, err := g.bytes(6)
	if err != nil {
		return "", err
	}
	h := hex.EncodeToString(b)
	parts := make([]string, 6)
	for i := range parts {
		parts[i] = h[i*2 : i*2+2]
	}
	return strings.Join(parts, ":"), nil
}

// DefaultPasswordLength is the length of $randomPassword without an
// argument.
const DefaultPasswordLength = 15

func fakePassword(g *dynamicGen, args []string) (string, error) {
	if len(args) == 0 {
		args = []string{strconv.Itoa(DefaultPasswordLength)}
	}
	return dynRandomString(g, args)
}

func fakeSemver(g *dynamicGen, args []string) (string, error) {
	if err := noArgs(args); err != nil {
		return "", err
	}
	p := &picker{g: g}
	s := fmt.Sprintf("%d.%d.%d", p.intRange(0, 9), p.intRange(0, 20), p.intRange(0, 30))
	return s, p.err
}

func fakeStreetName(g *dynamicGen, args []string) (string, error) {
	if err := noArgs(args); err != nil {
		return "", err
	}
	p := &picker{g: g}
	s := p.pick(streetNames) + " " + p.pick(streetSuffixes)
	return s, p.err
}

func fakeStreetAddress(g *dynamicGen, args []string) (string, error) {
	if err := noArgs(args); err != nil {
		return "", err
	}
	p := &picker{g: g}
	s := strconv.Itoa(p.intRange(1, 9999)) + " " + p.pick(streetNames) + " " + p.pick(streetSuffixes)
	return s, p.err
}

func fakeCountry(code bool) dynamicFunc {
	return func(g *dynamicGen, args []string) (string, error) {
		if err := noArgs(args); err != nil {
			return "", err
		}
		i, err := g.intRange(0, len(countries)-1)
		if err != nil {
			return "", err
		}
		if code {
			return countries[i].code, nil
		}
		return countries[i].name, nil
	}
}

// fakeCoordinate is a value in [-limit, limit] with 6 decimals.
func fakeCoordinate(limit int) dynamicFunc {
	return func(g *dynamicGen, args []string) (string, error) {
		if err := noArgs(args); err != nil {
			return "", err
		}
		n, err := g.intRange(-limit*1_000_000, limit*1_000_000)
		if err != nil {
			return "", err
		}
		return strconv.FormatFloat(float64(n)/1e6, 'f', 6, 64), nil
	}
}

func fakePhone(ext bool) dynamicFunc {
	return func(g *dynamicGen, args []string) (string, error) {
		if err := noArgs(args); err != nil {
			return "", err
		}
		p := &picker{g: g}
		s := fmt.Sprintf("555-%03d-%04d", p.intRange(0, 999), p.intRange(0, 9999))
		if ext {
			s += fmt.Sprintf(" x%d", p.intRange(10, 99))
		}
		return s, p.err
	}
}

func fakeCompanyName(g *dynamicGen, args []string) (string, error) {
	if err := noArgs(args); err != nil {
		return "", err
	}
	p := &picker{g: g}
	s := p.pick(lastNames) + " " + p.pick(companyWords) + " " + p.pick(companySuffixes)
	return s, p.err
}

func fakePrice(g *dynamicGen, args []string) (string, error) {
	if err := noArgs(args); err != nil {
		return "", err
	}
	cents, err := g.intRange(100, 100_000)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%d.%02d", cents/100, cents%100), nil
}

func fakeHexColor(g *dynamicGen, args []string) (string, error) {
	if err := noArgs(args); err != nil {
		return "", err
	}
	b, err := g.bytes(3)
	if err != nil {
		return "", err
	}
	return "#" + hex.EncodeToString(b), nil
}

// fakeDate is a random time between now and now+span (span may be
// negative), in whole seconds, ISO 8601 in UTC.
func fakeDate(span time.Duration) dynamicFunc {
	return func(g *dynamicGen, args []string) (string, error) {
		if err := noArgs(args); err != nil {
			return "", err
		}
		secs := int(span / time.Second)
		lo, hi := 1, secs
		if secs < 0 {
			lo, hi = secs, -1
		}
		n, err := g.intRange(lo, hi)
		if err != nil {
			return "", err
		}
		t := g.now().UTC().Truncate(time.Second).Add(time.Duration(n) * time.Second)
		return t.Format(isoLayout), nil
	}
}

func fakeWords(list []string, sep string) dynamicFunc {
	return func(g *dynamicGen, args []string) (string, error) {
		n, err := countArg(args, 3)
		if err != nil {
			return "", err
		}
		p := &picker{g: g}
		out := make([]string, n)
		for i := range out {
			out[i] = p.pick(list)
		}
		return strings.Join(out, sep), p.err
	}
}

// sentence is 4 to 10 lorem words, capitalised, with a full stop.
func (p *picker) sentence() string {
	n := p.intRange(4, 10)
	w := make([]string, n)
	for i := range w {
		w[i] = p.pick(loremWords)
	}
	s := strings.Join(w, " ")
	if s == "" {
		return ""
	}
	return strings.ToUpper(s[:1]) + s[1:] + "."
}

// paragraph is 3 to 6 sentences.
func (p *picker) paragraph() string {
	n := p.intRange(3, 6)
	s := make([]string, n)
	for i := range s {
		s[i] = p.sentence()
	}
	return strings.Join(s, " ")
}

func fakeSentenceValue(g *dynamicGen, args []string) (string, error) {
	if err := noArgs(args); err != nil {
		return "", err
	}
	p := &picker{g: g}
	s := p.sentence()
	return s, p.err
}

func fakeSentences(sep string) dynamicFunc {
	return func(g *dynamicGen, args []string) (string, error) {
		n, err := countArg(args, 3)
		if err != nil {
			return "", err
		}
		p := &picker{g: g}
		out := make([]string, n)
		for i := range out {
			out[i] = p.sentence()
		}
		return strings.Join(out, sep), p.err
	}
}

func fakeParagraphValue(g *dynamicGen, args []string) (string, error) {
	if err := noArgs(args); err != nil {
		return "", err
	}
	p := &picker{g: g}
	s := p.paragraph()
	return s, p.err
}

func fakeParagraphs(g *dynamicGen, args []string) (string, error) {
	n, err := countArg(args, 3)
	if err != nil {
		return "", err
	}
	p := &picker{g: g}
	out := make([]string, n)
	for i := range out {
		out[i] = p.paragraph()
	}
	return strings.Join(out, "\n\n"), p.err
}

// The word lists. Small on purpose: enough variety for test data.

var firstNames = []string{
	"Ada", "Alan", "Alice", "Amara", "Anton", "Beatrice", "Boris", "Carmen",
	"Chen", "Clara", "Daniel", "Dmitri", "Elena", "Emil", "Fatima", "Felix",
	"Grace", "Hana", "Hugo", "Ines", "Ivan", "James", "Julia", "Kenji",
	"Lars", "Leila", "Linus", "Maria", "Mateo", "Mira", "Nadia", "Noah",
	"Olga", "Omar", "Priya", "Rosa", "Sofia", "Tariq", "Yuki", "Zoe",
}

var lastNames = []string{
	"Andersen", "Babbage", "Becker", "Costa", "Dubois", "Fischer", "Garcia",
	"Hopper", "Ivanova", "Jensen", "Kowalski", "Lamarr", "Lovelace", "Meyer",
	"Moreau", "Nakamura", "Novak", "Okafor", "Petrov", "Quinn", "Rossi",
	"Santos", "Schmidt", "Silva", "Tanaka", "Turing", "Varga", "Weber",
	"Wright", "Zhang",
}

var namePrefixes = []string{"Mr.", "Mrs.", "Ms.", "Miss", "Dr.", "Prof."}

var nameSuffixes = []string{"Jr.", "Sr.", "I", "II", "III", "IV", "V", "MD", "PhD"}

var jobLevels = []string{"Junior", "Senior", "Lead", "Principal", "Chief", "Associate", "Staff"}

var jobAreas = []string{
	"Data", "Security", "Product", "Marketing", "Operations", "Research",
	"Infrastructure", "Quality", "Sales", "Design",
}

var jobRoles = []string{
	"Engineer", "Analyst", "Manager", "Designer", "Architect", "Consultant",
	"Specialist", "Developer", "Strategist", "Administrator",
}

var emailDomains = []string{"example.com", "example.org", "example.net"}

var domainWords = []string{
	"acme", "globex", "initech", "umbrella", "hooli", "stark", "wayne",
	"cyberdyne", "soylent", "tyrell", "wonka", "vandelay", "oscorp", "aperture",
}

// domainSuffixes are reserved names (RFC 2606, RFC 6761) that never
// resolve to real hosts.
var domainSuffixes = []string{"test", "example", "invalid"}

var cities = []string{
	"Amsterdam", "Athens", "Bangkok", "Berlin", "Bogotá", "Cairo", "Cape Town",
	"Chicago", "Dublin", "Hanoi", "Helsinki", "Istanbul", "Lagos", "Lima",
	"Lisbon", "London", "Madrid", "Melbourne", "Montreal", "Mumbai", "Nairobi",
	"Osaka", "Oslo", "Paris", "Prague", "Seoul", "Singapore", "Tallinn",
	"Toronto", "Vienna",
}

var streetNames = []string{
	"Maple", "Oak", "Cedar", "Pine", "Elm", "Willow", "Birch", "Chestnut",
	"Lake", "Hill", "River", "Park", "Church", "Mill", "Station", "Harbor",
}

var streetSuffixes = []string{"Street", "Avenue", "Road", "Lane", "Boulevard", "Way", "Drive", "Court"}

var countries = []struct{ code, name string }{
	{"AR", "Argentina"}, {"AU", "Australia"}, {"AT", "Austria"}, {"BR", "Brazil"},
	{"CA", "Canada"}, {"CL", "Chile"}, {"CN", "China"}, {"CZ", "Czechia"},
	{"DK", "Denmark"}, {"EG", "Egypt"}, {"EE", "Estonia"}, {"FI", "Finland"},
	{"FR", "France"}, {"DE", "Germany"}, {"GR", "Greece"}, {"IN", "India"},
	{"IE", "Ireland"}, {"IT", "Italy"}, {"JP", "Japan"}, {"KE", "Kenya"},
	{"MX", "Mexico"}, {"NL", "Netherlands"}, {"NZ", "New Zealand"}, {"NG", "Nigeria"},
	{"NO", "Norway"}, {"PE", "Peru"}, {"PL", "Poland"}, {"PT", "Portugal"},
	{"KR", "South Korea"}, {"ES", "Spain"}, {"SE", "Sweden"}, {"CH", "Switzerland"},
	{"TH", "Thailand"}, {"TR", "Türkiye"}, {"GB", "United Kingdom"}, {"US", "United States"},
	{"VN", "Vietnam"},
}

var companyWords = []string{"Labs", "Systems", "Industries", "Solutions", "Works", "Dynamics", "Partners", "Group"}

var companySuffixes = []string{"Inc", "LLC", "Ltd", "GmbH", "SA", "AG", "Co", "BV"}

var currencyCodes = []string{
	"AUD", "BRL", "CAD", "CHF", "CNY", "CZK", "DKK", "EUR", "GBP", "INR",
	"JPY", "KRW", "MXN", "NOK", "NZD", "PLN", "SEK", "SGD", "USD", "ZAR",
}

var locales = []string{
	"en-US", "en-GB", "de-DE", "fr-FR", "es-ES", "it-IT", "pt-BR", "nl-NL",
	"sv-SE", "pl-PL", "cs-CZ", "ja-JP", "ko-KR", "zh-CN", "ru-RU", "tr-TR",
}

var abbreviations = []string{
	"API", "CSS", "CSV", "DNS", "FTP", "GB", "HTML", "HTTP", "JSON", "JWT",
	"PCI", "REST", "RSS", "SDK", "SMTP", "SQL", "SSL", "TCP", "UDP", "XML",
}

var colors = []string{
	"black", "blue", "brown", "cyan", "gold", "gray", "green", "indigo",
	"lavender", "lime", "magenta", "maroon", "navy", "olive", "orange",
	"pink", "purple", "red", "silver", "teal", "turquoise", "violet",
	"white", "yellow",
}

var weekdays = []string{"Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday", "Sunday"}

var months = []string{
	"January", "February", "March", "April", "May", "June", "July",
	"August", "September", "October", "November", "December",
}

var words = []string{
	"anchor", "apple", "arrow", "autumn", "bamboo", "beacon", "breeze",
	"bridge", "candle", "canyon", "cloud", "comet", "copper", "coral",
	"crystal", "desert", "echo", "ember", "falcon", "feather", "forest",
	"garden", "glacier", "harbor", "horizon", "island", "jungle", "lantern",
	"meadow", "meteor", "mirror", "mountain", "nebula", "ocean", "orbit",
	"pebble", "pepper", "planet", "prism", "quartz", "rainbow", "river",
	"rocket", "saddle", "shadow", "signal", "silver", "spark", "summit",
	"thunder", "timber", "tunnel", "valley", "velvet", "violet", "voyage",
	"walnut", "willow", "winter", "zephyr",
}

var loremWords = []string{
	"lorem", "ipsum", "dolor", "sit", "amet", "consectetur", "adipiscing",
	"elit", "sed", "do", "eiusmod", "tempor", "incididunt", "ut", "labore",
	"et", "dolore", "magna", "aliqua", "enim", "ad", "minim", "veniam",
	"quis", "nostrud", "exercitation", "ullamco", "laboris", "nisi",
	"aliquip", "ex", "ea", "commodo", "consequat", "duis", "aute", "irure",
	"in", "reprehenderit", "voluptate", "velit", "esse", "cillum", "fugiat",
	"nulla", "pariatur", "excepteur", "sint", "occaecat", "cupidatat", "non",
	"proident", "sunt", "culpa", "qui", "officia", "deserunt", "mollit",
	"anim", "id", "est", "laborum",
}
