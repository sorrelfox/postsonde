package assert

import (
	"net/http"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

const jsonBody = `{"id":42,"name":"Ada Lovelace","price":9.5,"tags":["a","b",1],"meta":{},"items":[],"ok":true,"none":null,` +
	`"big":12345678901234567890123,"user":{"role":"admin","age":36}}`

const xmlBody = `<order id="7"><item>pen</item><item>ink</item><total>12.50</total><note/></order>`

func testResponse(body string) Response {
	h := http.Header{}
	h.Set("Content-Type", "application/json")
	h.Add("X-Multi", "a")
	h.Add("X-Multi", "b")
	h.Set("X-Empty", "")
	h.Set("X-Num", "42")
	h.Add("Set-Cookie", "sid=abc123; Path=/; HttpOnly")
	h.Add("Set-Cookie", "empty=; Path=/")
	h.Add("Set-Cookie", "n=5")
	h.Add("Set-Cookie", "sid=last; Path=/")
	return Response{Status: 201, Header: h, Body: []byte(body), Time: 120500 * time.Microsecond}
}

type evalCase struct {
	xml     bool // the XML response instead of the JSON one
	a       Assertion
	outcome Outcome
	actual  string
	msg     string
}

func a(t TargetKind, expr string, op Operator, expected string) Assertion {
	return Assertion{Target: t, Expr: expr, Op: op, Expected: expected}
}

// evalCases cover every target with every operator, passing and failing,
// with the messages the user sees.
var evalCases = []evalCase{
	// ---- status ----
	{a: a(TargetStatus, "", OpEquals, "201"), outcome: Passed, actual: "201", msg: "Status code equals 201: it was 201."},
	{a: a(TargetStatus, "", OpEquals, "200"), outcome: Failed, actual: "201", msg: "Expected status code to equal 200, but it was 201."},
	{a: a(TargetStatus, "", OpEquals, "201.0"), outcome: Passed, actual: "201", msg: "Status code equals 201.0: it was 201."},
	{a: a(TargetStatus, "", OpNotEquals, "200"), outcome: Passed, actual: "201", msg: "Status code does not equal 200: it was 201."},
	{a: a(TargetStatus, "", OpNotEquals, "201"), outcome: Failed, actual: "201", msg: "Expected status code to not equal 201, but it was 201."},
	{a: a(TargetStatus, "", OpContains, "20"), outcome: Passed, actual: "201", msg: "Status code contains 20: it was 201."},
	{a: a(TargetStatus, "", OpContains, "5"), outcome: Failed, actual: "201", msg: "Expected status code to contain 5, but it was 201."},
	{a: a(TargetStatus, "", OpNotContains, "5"), outcome: Passed, actual: "201", msg: "Status code does not contain 5: it was 201."},
	{a: a(TargetStatus, "", OpNotContains, "1"), outcome: Failed, actual: "201", msg: "Expected status code to not contain 1, but it was 201."},
	{a: a(TargetStatus, "", OpExists, ""), outcome: Passed, actual: "201", msg: "Status code exists: it was 201."},
	{a: a(TargetStatus, "", OpNotExists, ""), outcome: Failed, actual: "201", msg: "Expected status code to not exist, but it was 201."},
	{a: a(TargetStatus, "", OpIsEmpty, ""), outcome: Failed, actual: "201", msg: "Expected status code to be empty, but it was 201."},
	{a: a(TargetStatus, "", OpGreater, "199"), outcome: Passed, actual: "201", msg: "Status code is greater than 199: it was 201."},
	{a: a(TargetStatus, "", OpGreater, "201"), outcome: Failed, actual: "201", msg: "Expected status code to be greater than 201, but it was 201."},
	{a: a(TargetStatus, "", OpLess, "300"), outcome: Passed, actual: "201", msg: "Status code is less than 300: it was 201."},
	{a: a(TargetStatus, "", OpLess, "201"), outcome: Failed, actual: "201", msg: "Expected status code to be less than 201, but it was 201."},
	{a: a(TargetStatus, "", OpGreaterOrEqual, "201"), outcome: Passed, actual: "201", msg: "Status code is at least 201: it was 201."},
	{a: a(TargetStatus, "", OpGreaterOrEqual, "202"), outcome: Failed, actual: "201", msg: "Expected status code to be at least 202, but it was 201."},
	{a: a(TargetStatus, "", OpLessOrEqual, "201"), outcome: Passed, actual: "201", msg: "Status code is at most 201: it was 201."},
	{a: a(TargetStatus, "", OpLessOrEqual, "200"), outcome: Failed, actual: "201", msg: "Expected status code to be at most 200, but it was 201."},
	{a: a(TargetStatus, "", OpMatches, `^2\d\d$`), outcome: Passed, actual: "201", msg: `Status code matches /^2\d\d$/: it was 201.`},
	{a: a(TargetStatus, "", OpMatches, `^4`), outcome: Failed, actual: "201", msg: "Expected status code to match /^4/, but it was 201."},
	{a: a(TargetStatus, "", OpIsType, "number"), outcome: Passed, actual: "201", msg: "Status code is of type number: it was 201."},
	{a: a(TargetStatus, "", OpIsType, "string"), outcome: Failed, actual: "201", msg: "Expected status code to be of type string, but it was number (201)."},

	// ---- header ----
	{a: a(TargetHeader, "content-type", OpEquals, "application/json"), outcome: Passed, actual: `"application/json"`, msg: `Header content-type equals "application/json": it was "application/json".`},
	{a: a(TargetHeader, "Content-Type", OpEquals, "text/html"), outcome: Failed, actual: `"application/json"`, msg: `Expected header Content-Type to equal "text/html", but it was "application/json".`},
	{a: a(TargetHeader, "X-Multi", OpEquals, "a, b"), outcome: Passed, actual: `"a, b"`, msg: `Header X-Multi equals "a, b": it was "a, b".`},
	{a: a(TargetHeader, "X-Missing", OpEquals, "x"), outcome: Failed, msg: `Expected header X-Missing to equal "x", but it does not exist.`},
	{a: a(TargetHeader, "X-Num", OpNotEquals, "41"), outcome: Passed, actual: `"42"`, msg: `Header X-Num does not equal 41: it was "42".`},
	{a: a(TargetHeader, "X-Num", OpNotEquals, "42"), outcome: Failed, actual: `"42"`, msg: `Expected header X-Num to not equal 42, but it was "42".`},
	{a: a(TargetHeader, "X-Missing", OpNotEquals, "42"), outcome: Failed, msg: `Expected header X-Missing to not equal 42, but it does not exist.`},
	{a: a(TargetHeader, "Content-Type", OpContains, "json"), outcome: Passed, actual: `"application/json"`, msg: `Header Content-Type contains "json": it was "application/json".`},
	{a: a(TargetHeader, "Content-Type", OpContains, "xml"), outcome: Failed, actual: `"application/json"`, msg: `Expected header Content-Type to contain "xml", but it was "application/json".`},
	{a: a(TargetHeader, "Content-Type", OpNotContains, "xml"), outcome: Passed, actual: `"application/json"`, msg: `Header Content-Type does not contain "xml": it was "application/json".`},
	{a: a(TargetHeader, "Content-Type", OpNotContains, "json"), outcome: Failed, actual: `"application/json"`, msg: `Expected header Content-Type to not contain "json", but it was "application/json".`},
	{a: a(TargetHeader, "X-Empty", OpExists, ""), outcome: Passed, actual: `""`, msg: `Header X-Empty exists: it was "".`},
	{a: a(TargetHeader, "X-Missing", OpExists, ""), outcome: Failed, msg: "Expected header X-Missing to exist, but it does not exist."},
	{a: a(TargetHeader, "X-Missing", OpNotExists, ""), outcome: Passed, msg: "Header X-Missing does not exist."},
	{a: a(TargetHeader, "X-Num", OpNotExists, ""), outcome: Failed, actual: `"42"`, msg: `Expected header X-Num to not exist, but it was "42".`},
	{a: a(TargetHeader, "X-Empty", OpIsEmpty, ""), outcome: Passed, actual: `""`, msg: `Header X-Empty is empty: it was "".`},
	{a: a(TargetHeader, "X-Num", OpIsEmpty, ""), outcome: Failed, actual: `"42"`, msg: `Expected header X-Num to be empty, but it was "42".`},
	{a: a(TargetHeader, "X-Num", OpGreater, "41.5"), outcome: Passed, actual: `"42"`, msg: `Header X-Num is greater than 41.5: it was "42".`},
	{a: a(TargetHeader, "X-Num", OpGreater, "42"), outcome: Failed, actual: `"42"`, msg: `Expected header X-Num to be greater than 42, but it was "42".`},
	{a: a(TargetHeader, "Content-Type", OpGreater, "1"), outcome: Failed, actual: `"application/json"`, msg: `Expected header Content-Type to be greater than 1, but it was "application/json".`},
	{a: a(TargetHeader, "X-Num", OpLess, "100"), outcome: Passed, actual: `"42"`, msg: `Header X-Num is less than 100: it was "42".`},
	{a: a(TargetHeader, "X-Num", OpLess, "-1"), outcome: Failed, actual: `"42"`, msg: `Expected header X-Num to be less than -1, but it was "42".`},
	{a: a(TargetHeader, "X-Num", OpGreaterOrEqual, "42"), outcome: Passed, actual: `"42"`, msg: `Header X-Num is at least 42: it was "42".`},
	{a: a(TargetHeader, "X-Num", OpGreaterOrEqual, "4e2"), outcome: Failed, actual: `"42"`, msg: `Expected header X-Num to be at least 4e2, but it was "42".`},
	{a: a(TargetHeader, "X-Num", OpLessOrEqual, "42"), outcome: Passed, actual: `"42"`, msg: `Header X-Num is at most 42: it was "42".`},
	{a: a(TargetHeader, "X-Missing", OpLessOrEqual, "42"), outcome: Failed, msg: `Expected header X-Missing to be at most 42, but it does not exist.`},
	{a: a(TargetHeader, "Content-Type", OpMatches, `^application/(\w+\+)?json`), outcome: Passed, actual: `"application/json"`, msg: `Header Content-Type matches /^application/(\w+\+)?json/: it was "application/json".`},
	{a: a(TargetHeader, "Content-Type", OpMatches, `^text/`), outcome: Failed, actual: `"application/json"`, msg: `Expected header Content-Type to match /^text//, but it was "application/json".`},
	{a: a(TargetHeader, "X-Num", OpIsType, "string"), outcome: Passed, actual: `"42"`, msg: `Header X-Num is of type string: it was "42".`},
	{a: a(TargetHeader, "X-Num", OpIsType, "number"), outcome: Failed, actual: `"42"`, msg: `Expected header X-Num to be of type number, but it was string ("42").`},

	// ---- cookie ----
	{a: a(TargetCookie, "sid", OpEquals, "last"), outcome: Passed, actual: `"last"`, msg: `Cookie sid equals "last": it was "last".`},
	{a: a(TargetCookie, "sid", OpEquals, "abc123"), outcome: Failed, actual: `"last"`, msg: `Expected cookie sid to equal "abc123", but it was "last".`},
	{a: a(TargetCookie, "SID", OpEquals, "last"), outcome: Failed, msg: `Expected cookie SID to equal "last", but it does not exist.`},
	{a: a(TargetCookie, "n", OpNotEquals, "6"), outcome: Passed, actual: `"5"`, msg: `Cookie n does not equal 6: it was "5".`},
	{a: a(TargetCookie, "n", OpNotEquals, "5"), outcome: Failed, actual: `"5"`, msg: `Expected cookie n to not equal 5, but it was "5".`},
	{a: a(TargetCookie, "sid", OpContains, "as"), outcome: Passed, actual: `"last"`, msg: `Cookie sid contains "as": it was "last".`},
	{a: a(TargetCookie, "sid", OpContains, "abc"), outcome: Failed, actual: `"last"`, msg: `Expected cookie sid to contain "abc", but it was "last".`},
	{a: a(TargetCookie, "sid", OpNotContains, "abc"), outcome: Passed, actual: `"last"`, msg: `Cookie sid does not contain "abc": it was "last".`},
	{a: a(TargetCookie, "sid", OpNotContains, "l"), outcome: Failed, actual: `"last"`, msg: `Expected cookie sid to not contain "l", but it was "last".`},
	{a: a(TargetCookie, "empty", OpExists, ""), outcome: Passed, actual: `""`, msg: `Cookie empty exists: it was "".`},
	{a: a(TargetCookie, "nope", OpExists, ""), outcome: Failed, msg: "Expected cookie nope to exist, but it does not exist."},
	{a: a(TargetCookie, "nope", OpNotExists, ""), outcome: Passed, msg: "Cookie nope does not exist."},
	{a: a(TargetCookie, "sid", OpNotExists, ""), outcome: Failed, actual: `"last"`, msg: `Expected cookie sid to not exist, but it was "last".`},
	{a: a(TargetCookie, "empty", OpIsEmpty, ""), outcome: Passed, actual: `""`, msg: `Cookie empty is empty: it was "".`},
	{a: a(TargetCookie, "sid", OpIsEmpty, ""), outcome: Failed, actual: `"last"`, msg: `Expected cookie sid to be empty, but it was "last".`},
	{a: a(TargetCookie, "n", OpGreater, "4"), outcome: Passed, actual: `"5"`, msg: `Cookie n is greater than 4: it was "5".`},
	{a: a(TargetCookie, "n", OpGreater, "5"), outcome: Failed, actual: `"5"`, msg: `Expected cookie n to be greater than 5, but it was "5".`},
	{a: a(TargetCookie, "n", OpLess, "6"), outcome: Passed, actual: `"5"`, msg: `Cookie n is less than 6: it was "5".`},
	{a: a(TargetCookie, "n", OpLess, "5"), outcome: Failed, actual: `"5"`, msg: `Expected cookie n to be less than 5, but it was "5".`},
	{a: a(TargetCookie, "n", OpGreaterOrEqual, "5"), outcome: Passed, actual: `"5"`, msg: `Cookie n is at least 5: it was "5".`},
	{a: a(TargetCookie, "n", OpGreaterOrEqual, "6"), outcome: Failed, actual: `"5"`, msg: `Expected cookie n to be at least 6, but it was "5".`},
	{a: a(TargetCookie, "n", OpLessOrEqual, "5"), outcome: Passed, actual: `"5"`, msg: `Cookie n is at most 5: it was "5".`},
	{a: a(TargetCookie, "sid", OpLessOrEqual, "5"), outcome: Failed, actual: `"last"`, msg: `Expected cookie sid to be at most 5, but it was "last".`},
	{a: a(TargetCookie, "sid", OpMatches, `^[a-z]+$`), outcome: Passed, actual: `"last"`, msg: `Cookie sid matches /^[a-z]+$/: it was "last".`},
	{a: a(TargetCookie, "sid", OpMatches, `\d`), outcome: Failed, actual: `"last"`, msg: `Expected cookie sid to match /\d/, but it was "last".`},
	{a: a(TargetCookie, "sid", OpIsType, "string"), outcome: Passed, actual: `"last"`, msg: `Cookie sid is of type string: it was "last".`},
	{a: a(TargetCookie, "sid", OpIsType, "object"), outcome: Failed, actual: `"last"`, msg: `Expected cookie sid to be of type object, but it was string ("last").`},

	// ---- JSONPath ----
	{a: a(TargetJSONPath, "$.id", OpEquals, "42"), outcome: Passed, actual: "42", msg: "JSONPath $.id equals 42: it was 42."},
	{a: a(TargetJSONPath, "$.id", OpEquals, "42.00"), outcome: Passed, actual: "42", msg: "JSONPath $.id equals 42.00: it was 42."},
	{a: a(TargetJSONPath, "$.id", OpEquals, `"42"`), outcome: Failed, actual: "42", msg: `Expected JSONPath $.id to equal "\"42\"", but it was 42.`},
	{a: a(TargetJSONPath, "$.name", OpEquals, "Ada Lovelace"), outcome: Passed, actual: `"Ada Lovelace"`, msg: `JSONPath $.name equals "Ada Lovelace": it was "Ada Lovelace".`},
	{a: a(TargetJSONPath, "$.name", OpEquals, `"Ada Lovelace"`), outcome: Passed, actual: `"Ada Lovelace"`, msg: `JSONPath $.name equals "\"Ada Lovelace\"": it was "Ada Lovelace".`},
	{a: a(TargetJSONPath, "$.name", OpEquals, "Ada"), outcome: Failed, actual: `"Ada Lovelace"`, msg: `Expected JSONPath $.name to equal "Ada", but it was "Ada Lovelace".`},
	{a: a(TargetJSONPath, "$.ok", OpEquals, "true"), outcome: Passed, actual: "true", msg: "JSONPath $.ok equals true: it was true."},
	{a: a(TargetJSONPath, "$.none", OpEquals, "null"), outcome: Passed, actual: "null", msg: "JSONPath $.none equals null: it was null."},
	{a: a(TargetJSONPath, "$.big", OpEquals, "12345678901234567890123"), outcome: Passed, actual: "12345678901234567890123", msg: "JSONPath $.big equals 12345678901234567890123: it was 12345678901234567890123."},
	{a: a(TargetJSONPath, "$.big", OpEquals, "12345678901234567890124"), outcome: Failed, actual: "12345678901234567890123", msg: "Expected JSONPath $.big to equal 12345678901234567890124, but it was 12345678901234567890123."},
	{a: a(TargetJSONPath, "$.user", OpEquals, `{"age":36,"role":"admin"}`), outcome: Passed, actual: `{"role":"admin","age":36}`, msg: `JSONPath $.user equals "{\"age\":36,\"role\":\"admin\"}": it was {"role":"admin","age":36}.`},
	{a: a(TargetJSONPath, "$.tags", OpEquals, `["a","b",1.0]`), outcome: Passed, actual: `["a","b",1]`, msg: `JSONPath $.tags equals "[\"a\",\"b\",1.0]": it was ["a","b",1].`},
	{a: a(TargetJSONPath, "$.tags", OpEquals, `["b","a",1]`), outcome: Failed, actual: `["a","b",1]`, msg: `Expected JSONPath $.tags to equal "[\"b\",\"a\",1]", but it was ["a","b",1].`},
	{a: a(TargetJSONPath, "$.missing", OpEquals, "1"), outcome: Failed, msg: "Expected JSONPath $.missing to equal 1, but it does not exist."},
	{a: a(TargetJSONPath, "$.id", OpNotEquals, "41"), outcome: Passed, actual: "42", msg: "JSONPath $.id does not equal 41: it was 42."},
	{a: a(TargetJSONPath, "$.user.role", OpNotEquals, "admin"), outcome: Failed, actual: `"admin"`, msg: `Expected JSONPath $.user.role to not equal "admin", but it was "admin".`},
	{a: a(TargetJSONPath, "$.tags", OpContains, "b"), outcome: Passed, actual: `["a","b",1]`, msg: `JSONPath $.tags contains "b": it was ["a","b",1].`},
	{a: a(TargetJSONPath, "$.tags", OpContains, "1"), outcome: Passed, actual: `["a","b",1]`, msg: `JSONPath $.tags contains 1: it was ["a","b",1].`},
	{a: a(TargetJSONPath, "$.user", OpContains, "role"), outcome: Passed, actual: `{"role":"admin","age":36}`, msg: `JSONPath $.user contains "role": it was {"role":"admin","age":36}.`},
	{a: a(TargetJSONPath, "$.name", OpContains, "Love"), outcome: Passed, actual: `"Ada Lovelace"`, msg: `JSONPath $.name contains "Love": it was "Ada Lovelace".`},
	{a: a(TargetJSONPath, "$.tags", OpContains, "c"), outcome: Failed, actual: `["a","b",1]`, msg: `Expected JSONPath $.tags to contain "c", but it was ["a","b",1].`},
	{a: a(TargetJSONPath, "$.user", OpContains, "admin"), outcome: Failed, actual: `{"role":"admin","age":36}`, msg: `Expected JSONPath $.user to contain "admin", but it was {"role":"admin","age":36}.`},
	{a: a(TargetJSONPath, "$.tags", OpNotContains, "c"), outcome: Passed, actual: `["a","b",1]`, msg: `JSONPath $.tags does not contain "c": it was ["a","b",1].`},
	{a: a(TargetJSONPath, "$.tags", OpNotContains, "a"), outcome: Failed, actual: `["a","b",1]`, msg: `Expected JSONPath $.tags to not contain "a", but it was ["a","b",1].`},
	{a: a(TargetJSONPath, "$.none", OpExists, ""), outcome: Passed, actual: "null", msg: "JSONPath $.none exists: it was null."},
	{a: a(TargetJSONPath, "$.missing", OpExists, ""), outcome: Failed, msg: "Expected JSONPath $.missing to exist, but it does not exist."},
	{a: a(TargetJSONPath, "$.items[*]", OpNotExists, ""), outcome: Passed, msg: "JSONPath $.items[*] does not exist."},
	{a: a(TargetJSONPath, "$.tags[0]", OpNotExists, ""), outcome: Failed, actual: `"a"`, msg: `Expected JSONPath $.tags[0] to not exist, but it was "a".`},
	{a: a(TargetJSONPath, "$.items", OpIsEmpty, ""), outcome: Passed, actual: "[]", msg: "JSONPath $.items is empty: it was []."},
	{a: a(TargetJSONPath, "$.meta", OpIsEmpty, ""), outcome: Passed, actual: "{}", msg: "JSONPath $.meta is empty: it was {}."},
	{a: a(TargetJSONPath, "$.none", OpIsEmpty, ""), outcome: Passed, actual: "null", msg: "JSONPath $.none is empty: it was null."},
	{a: a(TargetJSONPath, "$.tags", OpIsEmpty, ""), outcome: Failed, actual: `["a","b",1]`, msg: `Expected JSONPath $.tags to be empty, but it was ["a","b",1].`},
	{a: a(TargetJSONPath, "$.price", OpGreater, "9.49"), outcome: Passed, actual: "9.5", msg: "JSONPath $.price is greater than 9.49: it was 9.5."},
	{a: a(TargetJSONPath, "$.price", OpGreater, "9.5"), outcome: Failed, actual: "9.5", msg: "Expected JSONPath $.price to be greater than 9.5, but it was 9.5."},
	{a: a(TargetJSONPath, "$.name", OpGreater, "1"), outcome: Failed, actual: `"Ada Lovelace"`, msg: `Expected JSONPath $.name to be greater than 1, but it was "Ada Lovelace".`},
	{a: a(TargetJSONPath, "$.price", OpLess, "10"), outcome: Passed, actual: "9.5", msg: "JSONPath $.price is less than 10: it was 9.5."},
	{a: a(TargetJSONPath, "$.big", OpLess, "12345678901234567890123"), outcome: Failed, actual: "12345678901234567890123", msg: "Expected JSONPath $.big to be less than 12345678901234567890123, but it was 12345678901234567890123."},
	{a: a(TargetJSONPath, "$.user.age", OpGreaterOrEqual, "36"), outcome: Passed, actual: "36", msg: "JSONPath $.user.age is at least 36: it was 36."},
	{a: a(TargetJSONPath, "$.user.age", OpGreaterOrEqual, "36.01"), outcome: Failed, actual: "36", msg: "Expected JSONPath $.user.age to be at least 36.01, but it was 36."},
	{a: a(TargetJSONPath, "$.user.age", OpLessOrEqual, "3.6e1"), outcome: Passed, actual: "36", msg: "JSONPath $.user.age is at most 3.6e1: it was 36."},
	{a: a(TargetJSONPath, "$.user.age", OpLessOrEqual, "35"), outcome: Failed, actual: "36", msg: "Expected JSONPath $.user.age to be at most 35, but it was 36."},
	{a: a(TargetJSONPath, "$.name", OpMatches, `^Ada\b`), outcome: Passed, actual: `"Ada Lovelace"`, msg: `JSONPath $.name matches /^Ada\b/: it was "Ada Lovelace".`},
	{a: a(TargetJSONPath, "$.tags", OpMatches, `"b"`), outcome: Passed, actual: `["a","b",1]`, msg: `JSONPath $.tags matches /"b"/: it was ["a","b",1].`},
	{a: a(TargetJSONPath, "$.name", OpMatches, `^Grace`), outcome: Failed, actual: `"Ada Lovelace"`, msg: `Expected JSONPath $.name to match /^Grace/, but it was "Ada Lovelace".`},
	{a: a(TargetJSONPath, "$.tags", OpIsType, "array"), outcome: Passed, actual: `["a","b",1]`, msg: `JSONPath $.tags is of type array: it was ["a","b",1].`},
	{a: a(TargetJSONPath, "$.user", OpIsType, "Object"), outcome: Passed, actual: `{"role":"admin","age":36}`, msg: `JSONPath $.user is of type Object: it was {"role":"admin","age":36}.`},
	{a: a(TargetJSONPath, "$.name", OpIsType, "string"), outcome: Passed, actual: `"Ada Lovelace"`, msg: `JSONPath $.name is of type string: it was "Ada Lovelace".`},
	{a: a(TargetJSONPath, "$.price", OpIsType, "number"), outcome: Passed, actual: "9.5", msg: "JSONPath $.price is of type number: it was 9.5."},
	{a: a(TargetJSONPath, "$.ok", OpIsType, "bool"), outcome: Passed, actual: "true", msg: "JSONPath $.ok is of type bool: it was true."},
	{a: a(TargetJSONPath, "$.none", OpIsType, "null"), outcome: Passed, actual: "null", msg: "JSONPath $.none is of type null: it was null."},
	{a: a(TargetJSONPath, "$", OpIsType, "object"), outcome: Passed, actual: `{"id":42,"name":"Ada Lovelace","price":9.5,"tags":["a","b",1],"meta":{},"items":[],"ok":true,"none":null,"big":12345678901234567890123,"user":{"role":"admin","age":36}}`, msg: `JSONPath $ is of type object: it was {"id":42,"name":"Ada Lovelace","price":9.5,"tags":["a","b",1],"meta":{},"items":[],"ok":true,"none":null,"big":12345678901234567890123,"user":{"role":"admin","age":36}}.`},
	{a: a(TargetJSONPath, "$.id", OpIsType, "string"), outcome: Failed, actual: "42", msg: "Expected JSONPath $.id to be of type string, but it was number (42)."},
	{a: a(TargetJSONPath, "$..role", OpEquals, `["admin"]`), outcome: Passed, actual: `["admin"]`, msg: `JSONPath $..role equals "[\"admin\"]": it was ["admin"].`},
	{a: a(TargetJSONPath, "$.tags[?@ == 'a']", OpContains, "a"), outcome: Passed, actual: `["a"]`, msg: `JSONPath $.tags[?@ == 'a'] contains "a": it was ["a"].`},

	// ---- XPath ----
	{xml: true, a: a(TargetXPath, "/order/total", OpEquals, "12.50"), outcome: Passed, actual: `"12.50"`, msg: `XPath /order/total equals 12.50: it was "12.50".`},
	{xml: true, a: a(TargetXPath, "/order/total", OpEquals, "12.5"), outcome: Failed, actual: `"12.50"`, msg: `Expected XPath /order/total to equal 12.5, but it was "12.50".`},
	{xml: true, a: a(TargetXPath, "count(//item)", OpEquals, "2"), outcome: Passed, actual: "2", msg: "XPath count(//item) equals 2: it was 2."},
	{xml: true, a: a(TargetXPath, "/order/@id", OpNotEquals, "8"), outcome: Passed, actual: `"7"`, msg: `XPath /order/@id does not equal 8: it was "7".`},
	{xml: true, a: a(TargetXPath, "/order/@id", OpNotEquals, "7"), outcome: Failed, actual: `"7"`, msg: `Expected XPath /order/@id to not equal 7, but it was "7".`},
	{xml: true, a: a(TargetXPath, "//item", OpContains, "ink"), outcome: Passed, actual: `["pen","ink"]`, msg: `XPath //item contains "ink": it was ["pen","ink"].`},
	{xml: true, a: a(TargetXPath, "//item", OpContains, "in"), outcome: Failed, actual: `["pen","ink"]`, msg: `Expected XPath //item to contain "in", but it was ["pen","ink"].`},
	{xml: true, a: a(TargetXPath, "//item", OpNotContains, "paper"), outcome: Passed, actual: `["pen","ink"]`, msg: `XPath //item does not contain "paper": it was ["pen","ink"].`},
	{xml: true, a: a(TargetXPath, "//item[1]", OpNotContains, "pe"), outcome: Failed, actual: `"pen"`, msg: `Expected XPath //item[1] to not contain "pe", but it was "pen".`},
	{xml: true, a: a(TargetXPath, "/order/note", OpExists, ""), outcome: Passed, actual: `""`, msg: `XPath /order/note exists: it was "".`},
	{xml: true, a: a(TargetXPath, "/order/customer", OpExists, ""), outcome: Failed, msg: "Expected XPath /order/customer to exist, but it does not exist."},
	{xml: true, a: a(TargetXPath, "/order/customer", OpNotExists, ""), outcome: Passed, msg: "XPath /order/customer does not exist."},
	{xml: true, a: a(TargetXPath, "/order/@id", OpNotExists, ""), outcome: Failed, actual: `"7"`, msg: `Expected XPath /order/@id to not exist, but it was "7".`},
	{xml: true, a: a(TargetXPath, "/order/note", OpIsEmpty, ""), outcome: Passed, actual: `""`, msg: `XPath /order/note is empty: it was "".`},
	{xml: true, a: a(TargetXPath, "//item[2]", OpIsEmpty, ""), outcome: Failed, actual: `"ink"`, msg: `Expected XPath //item[2] to be empty, but it was "ink".`},
	{xml: true, a: a(TargetXPath, "/order/total", OpGreater, "12"), outcome: Passed, actual: `"12.50"`, msg: `XPath /order/total is greater than 12: it was "12.50".`},
	{xml: true, a: a(TargetXPath, "/order/total", OpGreater, "12.5"), outcome: Failed, actual: `"12.50"`, msg: `Expected XPath /order/total to be greater than 12.5, but it was "12.50".`},
	{xml: true, a: a(TargetXPath, "count(//item)", OpLess, "3"), outcome: Passed, actual: "2", msg: "XPath count(//item) is less than 3: it was 2."},
	{xml: true, a: a(TargetXPath, "count(//item)", OpLess, "2"), outcome: Failed, actual: "2", msg: "Expected XPath count(//item) to be less than 2, but it was 2."},
	{xml: true, a: a(TargetXPath, "sum(/order/total)", OpGreaterOrEqual, "12.5"), outcome: Passed, actual: "12.5", msg: "XPath sum(/order/total) is at least 12.5: it was 12.5."},
	{xml: true, a: a(TargetXPath, "sum(/order/total)", OpGreaterOrEqual, "13"), outcome: Failed, actual: "12.5", msg: "Expected XPath sum(/order/total) to be at least 13, but it was 12.5."},
	{xml: true, a: a(TargetXPath, "/order/@id", OpLessOrEqual, "7"), outcome: Passed, actual: `"7"`, msg: `XPath /order/@id is at most 7: it was "7".`},
	{xml: true, a: a(TargetXPath, "/order/@id", OpLessOrEqual, "6"), outcome: Failed, actual: `"7"`, msg: `Expected XPath /order/@id to be at most 6, but it was "7".`},
	{xml: true, a: a(TargetXPath, "//item[1]", OpMatches, `^p`), outcome: Passed, actual: `"pen"`, msg: `XPath //item[1] matches /^p/: it was "pen".`},
	{xml: true, a: a(TargetXPath, "//item[1]", OpMatches, `^i`), outcome: Failed, actual: `"pen"`, msg: `Expected XPath //item[1] to match /^i/, but it was "pen".`},
	{xml: true, a: a(TargetXPath, "//item", OpIsType, "array"), outcome: Passed, actual: `["pen","ink"]`, msg: `XPath //item is of type array: it was ["pen","ink"].`},
	{xml: true, a: a(TargetXPath, "count(//item) = 2", OpIsType, "boolean"), outcome: Passed, actual: "true", msg: "XPath count(//item) = 2 is of type boolean: it was true."},
	{xml: true, a: a(TargetXPath, "/order/total", OpIsType, "number"), outcome: Failed, actual: `"12.50"`, msg: `Expected XPath /order/total to be of type number, but it was string ("12.50").`},
	{xml: true, a: a(TargetXPath, "string(/order/@id)", OpIsType, "string"), outcome: Passed, actual: `"7"`, msg: `XPath string(/order/@id) is of type string: it was "7".`},

	// ---- regex ----
	{a: a(TargetRegex, `"id":(\d+)`, OpEquals, "42"), outcome: Passed, actual: `"42"`, msg: `Body match of /"id":(\d+)/ equals 42: it was "42".`},
	{a: a(TargetRegex, `"id":(\d+)`, OpEquals, "43"), outcome: Failed, actual: `"42"`, msg: `Expected body match of /"id":(\d+)/ to equal 43, but it was "42".`},
	{a: a(TargetRegex, `Ada \w+`, OpNotEquals, "Ada Byron"), outcome: Passed, actual: `"Ada Lovelace"`, msg: `Body match of /Ada \w+/ does not equal "Ada Byron": it was "Ada Lovelace".`},
	{a: a(TargetRegex, `Ada \w+`, OpNotEquals, "Ada Lovelace"), outcome: Failed, actual: `"Ada Lovelace"`, msg: `Expected body match of /Ada \w+/ to not equal "Ada Lovelace", but it was "Ada Lovelace".`},
	{a: a(TargetRegex, `"role":"(\w+)"`, OpContains, "min"), outcome: Passed, actual: `"admin"`, msg: `Body match of /"role":"(\w+)"/ contains "min": it was "admin".`},
	{a: a(TargetRegex, `"role":"(\w+)"`, OpContains, "user"), outcome: Failed, actual: `"admin"`, msg: `Expected body match of /"role":"(\w+)"/ to contain "user", but it was "admin".`},
	{a: a(TargetRegex, `"role":"(\w+)"`, OpNotContains, "user"), outcome: Passed, actual: `"admin"`, msg: `Body match of /"role":"(\w+)"/ does not contain "user": it was "admin".`},
	{a: a(TargetRegex, `"nope":(\d+)`, OpNotContains, "1"), outcome: Failed, msg: `Expected body match of /"nope":(\d+)/ to not contain 1, but it does not exist.`},
	{a: a(TargetRegex, `Lovelace`, OpExists, ""), outcome: Passed, actual: `"Lovelace"`, msg: `Body match of /Lovelace/ exists: it was "Lovelace".`},
	{a: a(TargetRegex, `Hopper`, OpExists, ""), outcome: Failed, msg: "Expected body match of /Hopper/ to exist, but it does not exist."},
	{a: a(TargetRegex, `(?i)error`, OpNotExists, ""), outcome: Passed, msg: "Body match of /(?i)error/ does not exist."},
	{a: a(TargetRegex, `ada`, OpNotExists, ""), outcome: Passed, msg: "Body match of /ada/ does not exist."},
	{a: a(TargetRegex, `(?i)ada`, OpNotExists, ""), outcome: Failed, actual: `"Ada"`, msg: `Expected body match of /(?i)ada/ to not exist, but it was "Ada".`},
	{a: a(TargetRegex, `"meta":\{(.*?)\}`, OpIsEmpty, ""), outcome: Passed, actual: `""`, msg: `Body match of /"meta":\{(.*?)\}/ is empty: it was "".`},
	{a: a(TargetRegex, `"id":(\d+)`, OpIsEmpty, ""), outcome: Failed, actual: `"42"`, msg: `Expected body match of /"id":(\d+)/ to be empty, but it was "42".`},
	{a: a(TargetRegex, `"id":(\d+)`, OpGreater, "41"), outcome: Passed, actual: `"42"`, msg: `Body match of /"id":(\d+)/ is greater than 41: it was "42".`},
	{a: a(TargetRegex, `"id":(\d+)`, OpGreater, "42"), outcome: Failed, actual: `"42"`, msg: `Expected body match of /"id":(\d+)/ to be greater than 42, but it was "42".`},
	{a: a(TargetRegex, `"price":([\d.]+)`, OpLess, "10"), outcome: Passed, actual: `"9.5"`, msg: `Body match of /"price":([\d.]+)/ is less than 10: it was "9.5".`},
	{a: a(TargetRegex, `"price":([\d.]+)`, OpLess, "9"), outcome: Failed, actual: `"9.5"`, msg: `Expected body match of /"price":([\d.]+)/ to be less than 9, but it was "9.5".`},
	{a: a(TargetRegex, `"age":(\d+)`, OpGreaterOrEqual, "18"), outcome: Passed, actual: `"36"`, msg: `Body match of /"age":(\d+)/ is at least 18: it was "36".`},
	{a: a(TargetRegex, `"age":(\d+)`, OpGreaterOrEqual, "40"), outcome: Failed, actual: `"36"`, msg: `Expected body match of /"age":(\d+)/ to be at least 40, but it was "36".`},
	{a: a(TargetRegex, `"age":(\d+)`, OpLessOrEqual, "36"), outcome: Passed, actual: `"36"`, msg: `Body match of /"age":(\d+)/ is at most 36: it was "36".`},
	{a: a(TargetRegex, `"age":(\d+)`, OpLessOrEqual, "30"), outcome: Failed, actual: `"36"`, msg: `Expected body match of /"age":(\d+)/ to be at most 30, but it was "36".`},
	{a: a(TargetRegex, `"name":"([^"]*)"`, OpMatches, `^[A-Z]`), outcome: Passed, actual: `"Ada Lovelace"`, msg: `Body match of /"name":"([^"]*)"/ matches /^[A-Z]/: it was "Ada Lovelace".`},
	{a: a(TargetRegex, `"name":"([^"]*)"`, OpMatches, `^[a-z]`), outcome: Failed, actual: `"Ada Lovelace"`, msg: `Expected body match of /"name":"([^"]*)"/ to match /^[a-z]/, but it was "Ada Lovelace".`},
	{a: a(TargetRegex, `"id":(\d+)`, OpIsType, "string"), outcome: Passed, actual: `"42"`, msg: `Body match of /"id":(\d+)/ is of type string: it was "42".`},
	{a: a(TargetRegex, `"id":(\d+)`, OpIsType, "number"), outcome: Failed, actual: `"42"`, msg: `Expected body match of /"id":(\d+)/ to be of type number, but it was string ("42").`},

	// ---- whole body ----
	{xml: true, a: a(TargetBody, "", OpEquals, xmlBody), outcome: Passed, actual: `"` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `"`, msg: `Body equals "` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `": it was "` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `".`},
	{xml: true, a: a(TargetBody, "", OpEquals, "<order/>"), outcome: Failed, actual: `"` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `"`, msg: `Expected body to equal "<order/>", but it was "` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `".`},
	{xml: true, a: a(TargetBody, "", OpNotEquals, "<order/>"), outcome: Passed, actual: `"` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `"`, msg: `Body does not equal "<order/>": it was "` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `".`},
	{xml: true, a: a(TargetBody, "", OpNotEquals, xmlBody), outcome: Failed, actual: `"` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `"`, msg: `Expected body to not equal "` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `", but it was "` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `".`},
	{xml: true, a: a(TargetBody, "", OpContains, "<item>ink</item>"), outcome: Passed, actual: `"` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `"`, msg: `Body contains "<item>ink</item>": it was "` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `".`},
	{xml: true, a: a(TargetBody, "", OpContains, "error"), outcome: Failed, actual: `"` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `"`, msg: `Expected body to contain "error", but it was "` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `".`},
	{xml: true, a: a(TargetBody, "", OpNotContains, "error"), outcome: Passed, actual: `"` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `"`, msg: `Body does not contain "error": it was "` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `".`},
	{xml: true, a: a(TargetBody, "", OpNotContains, "pen"), outcome: Failed, actual: `"` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `"`, msg: `Expected body to not contain "pen", but it was "` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `".`},
	{xml: true, a: a(TargetBody, "", OpExists, ""), outcome: Passed, actual: `"` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `"`, msg: `Body exists: it was "` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `".`},
	{xml: true, a: a(TargetBody, "", OpNotExists, ""), outcome: Failed, actual: `"` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `"`, msg: `Expected body to not exist, but it was "` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `".`},
	{xml: true, a: a(TargetBody, "", OpIsEmpty, ""), outcome: Failed, actual: `"` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `"`, msg: `Expected body to be empty, but it was "` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `".`},
	{xml: true, a: a(TargetBody, "", OpMatches, `^<order\b`), outcome: Passed, actual: `"` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `"`, msg: `Body matches /^<order\b/: it was "` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `".`},
	{xml: true, a: a(TargetBody, "", OpMatches, `^\{`), outcome: Failed, actual: `"` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `"`, msg: `Expected body to match /^\{/, but it was "` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `".`},
	{xml: true, a: a(TargetBody, "", OpIsType, "string"), outcome: Passed, actual: `"` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `"`, msg: `Body is of type string: it was "` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `".`},
	{xml: true, a: a(TargetBody, "", OpIsType, "object"), outcome: Failed, actual: `"` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `"`, msg: `Expected body to be of type object, but it was string ("` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `").`},
	{xml: true, a: a(TargetBody, "", OpGreater, "1"), outcome: Failed, actual: `"` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `"`, msg: `Expected body to be greater than 1, but it was "` + strings.ReplaceAll(xmlBody, `"`, `\"`) + `".`},

	// ---- response time ----
	{a: a(TargetTime, "", OpEquals, "120.5"), outcome: Passed, actual: "120.5 ms", msg: "Response time equals 120.5 ms: it was 120.5 ms."},
	{a: a(TargetTime, "", OpEquals, "120"), outcome: Failed, actual: "120.5 ms", msg: "Expected response time to equal 120 ms, but it was 120.5 ms."},
	{a: a(TargetTime, "", OpNotEquals, "120"), outcome: Passed, actual: "120.5 ms", msg: "Response time does not equal 120 ms: it was 120.5 ms."},
	{a: a(TargetTime, "", OpNotEquals, "120500µs"), outcome: Failed, actual: "120.5 ms", msg: `Expected response time to not equal "120500µs", but it was 120.5 ms.`},
	{a: a(TargetTime, "", OpContains, "120"), outcome: Passed, actual: "120.5 ms", msg: "Response time contains 120 ms: it was 120.5 ms."},
	{a: a(TargetTime, "", OpContains, "999"), outcome: Failed, actual: "120.5 ms", msg: "Expected response time to contain 999 ms, but it was 120.5 ms."},
	{a: a(TargetTime, "", OpNotContains, "999"), outcome: Passed, actual: "120.5 ms", msg: "Response time does not contain 999 ms: it was 120.5 ms."},
	{a: a(TargetTime, "", OpNotContains, ".5"), outcome: Failed, actual: "120.5 ms", msg: "Expected response time to not contain .5 ms, but it was 120.5 ms."},
	{a: a(TargetTime, "", OpExists, ""), outcome: Passed, actual: "120.5 ms", msg: "Response time exists: it was 120.5 ms."},
	{a: a(TargetTime, "", OpNotExists, ""), outcome: Failed, actual: "120.5 ms", msg: "Expected response time to not exist, but it was 120.5 ms."},
	{a: a(TargetTime, "", OpIsEmpty, ""), outcome: Failed, actual: "120.5 ms", msg: "Expected response time to be empty, but it was 120.5 ms."},
	{a: a(TargetTime, "", OpGreater, "100"), outcome: Passed, actual: "120.5 ms", msg: "Response time is greater than 100 ms: it was 120.5 ms."},
	{a: a(TargetTime, "", OpGreater, "0.5s"), outcome: Failed, actual: "120.5 ms", msg: `Expected response time to be greater than "0.5s", but it was 120.5 ms.`},
	{a: a(TargetTime, "", OpLess, "500ms"), outcome: Passed, actual: "120.5 ms", msg: `Response time is less than "500ms": it was 120.5 ms.`},
	{a: a(TargetTime, "", OpLess, "100"), outcome: Failed, actual: "120.5 ms", msg: "Expected response time to be less than 100 ms, but it was 120.5 ms."},
	{a: a(TargetTime, "", OpGreaterOrEqual, "120.5"), outcome: Passed, actual: "120.5 ms", msg: "Response time is at least 120.5 ms: it was 120.5 ms."},
	{a: a(TargetTime, "", OpGreaterOrEqual, "1m"), outcome: Failed, actual: "120.5 ms", msg: `Expected response time to be at least "1m", but it was 120.5 ms.`},
	{a: a(TargetTime, "", OpLessOrEqual, "1.5s"), outcome: Passed, actual: "120.5 ms", msg: `Response time is at most "1.5s": it was 120.5 ms.`},
	{a: a(TargetTime, "", OpLessOrEqual, "120 ms"), outcome: Failed, actual: "120.5 ms", msg: `Expected response time to be at most "120 ms", but it was 120.5 ms.`},
	{a: a(TargetTime, "", OpMatches, `^120\.`), outcome: Passed, actual: "120.5 ms", msg: `Response time matches /^120\./: it was 120.5 ms.`},
	{a: a(TargetTime, "", OpMatches, `^\d+$`), outcome: Failed, actual: "120.5 ms", msg: `Expected response time to match /^\d+$/, but it was 120.5 ms.`},
	{a: a(TargetTime, "", OpIsType, "number"), outcome: Passed, actual: "120.5 ms", msg: "Response time is of type number: it was 120.5 ms."},
	{a: a(TargetTime, "", OpIsType, "string"), outcome: Failed, actual: "120.5 ms", msg: "Expected response time to be of type string, but it was number (120.5 ms)."},

	// ---- body size ----
	{xml: true, a: a(TargetSize, "", OpEquals, "81"), outcome: Passed, actual: "81 bytes", msg: "Body size equals 81 bytes: it was 81 bytes."},
	{xml: true, a: a(TargetSize, "", OpEquals, "0"), outcome: Failed, actual: "81 bytes", msg: "Expected body size to equal 0 bytes, but it was 81 bytes."},
	{xml: true, a: a(TargetSize, "", OpNotEquals, "0"), outcome: Passed, actual: "81 bytes", msg: "Body size does not equal 0 bytes: it was 81 bytes."},
	{xml: true, a: a(TargetSize, "", OpNotEquals, "81"), outcome: Failed, actual: "81 bytes", msg: "Expected body size to not equal 81 bytes, but it was 81 bytes."},
	{xml: true, a: a(TargetSize, "", OpContains, "8"), outcome: Passed, actual: "81 bytes", msg: "Body size contains 8 bytes: it was 81 bytes."},
	{xml: true, a: a(TargetSize, "", OpContains, "9"), outcome: Failed, actual: "81 bytes", msg: "Expected body size to contain 9 bytes, but it was 81 bytes."},
	{xml: true, a: a(TargetSize, "", OpNotContains, "9"), outcome: Passed, actual: "81 bytes", msg: "Body size does not contain 9 bytes: it was 81 bytes."},
	{xml: true, a: a(TargetSize, "", OpNotContains, "1"), outcome: Failed, actual: "81 bytes", msg: "Expected body size to not contain 1 bytes, but it was 81 bytes."},
	{xml: true, a: a(TargetSize, "", OpExists, ""), outcome: Passed, actual: "81 bytes", msg: "Body size exists: it was 81 bytes."},
	{xml: true, a: a(TargetSize, "", OpNotExists, ""), outcome: Failed, actual: "81 bytes", msg: "Expected body size to not exist, but it was 81 bytes."},
	{xml: true, a: a(TargetSize, "", OpIsEmpty, ""), outcome: Failed, actual: "81 bytes", msg: "Expected body size to be empty, but it was 81 bytes."},
	{xml: true, a: a(TargetSize, "", OpGreater, "0"), outcome: Passed, actual: "81 bytes", msg: "Body size is greater than 0 bytes: it was 81 bytes."},
	{xml: true, a: a(TargetSize, "", OpGreater, "81"), outcome: Failed, actual: "81 bytes", msg: "Expected body size to be greater than 81 bytes, but it was 81 bytes."},
	{xml: true, a: a(TargetSize, "", OpLess, "1024"), outcome: Passed, actual: "81 bytes", msg: "Body size is less than 1024 bytes: it was 81 bytes."},
	{xml: true, a: a(TargetSize, "", OpLess, "10"), outcome: Failed, actual: "81 bytes", msg: "Expected body size to be less than 10 bytes, but it was 81 bytes."},
	{xml: true, a: a(TargetSize, "", OpGreaterOrEqual, "81"), outcome: Passed, actual: "81 bytes", msg: "Body size is at least 81 bytes: it was 81 bytes."},
	{xml: true, a: a(TargetSize, "", OpGreaterOrEqual, "82"), outcome: Failed, actual: "81 bytes", msg: "Expected body size to be at least 82 bytes, but it was 81 bytes."},
	{xml: true, a: a(TargetSize, "", OpLessOrEqual, "81"), outcome: Passed, actual: "81 bytes", msg: "Body size is at most 81 bytes: it was 81 bytes."},
	{xml: true, a: a(TargetSize, "", OpLessOrEqual, "80"), outcome: Failed, actual: "81 bytes", msg: "Expected body size to be at most 80 bytes, but it was 81 bytes."},
	{xml: true, a: a(TargetSize, "", OpMatches, `^\d{2}$`), outcome: Passed, actual: "81 bytes", msg: `Body size matches /^\d{2}$/: it was 81 bytes.`},
	{xml: true, a: a(TargetSize, "", OpMatches, `^\d{3}$`), outcome: Failed, actual: "81 bytes", msg: `Expected body size to match /^\d{3}$/, but it was 81 bytes.`},
	{xml: true, a: a(TargetSize, "", OpIsType, "number"), outcome: Passed, actual: "81 bytes", msg: "Body size is of type number: it was 81 bytes."},
	{xml: true, a: a(TargetSize, "", OpIsType, "array"), outcome: Failed, actual: "81 bytes", msg: "Expected body size to be of type array, but it was number (81 bytes)."},
}

func TestEvaluate(t *testing.T) {
	t.Parallel()
	for _, tc := range evalCases {
		name := string(tc.a.Target) + " " + tc.a.Expr + " " + string(tc.a.Op) + " " + tc.a.Expected
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			body := jsonBody
			if tc.xml {
				body = xmlBody
			}
			got := EvaluateOne(t.Context(), testResponse(body), tc.a)
			want := Result{Assertion: tc.a, Outcome: tc.outcome, Actual: tc.actual, Exists: tc.actual != "", Message: tc.msg}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("\n got %+v\nwant %+v", got, want)
			}
		})
	}
}

// Every target is combined with every operator, and every operator both
// passes and fails somewhere.
func TestEvaluateCoversEveryCombination(t *testing.T) {
	t.Parallel()
	targets := []TargetKind{TargetStatus, TargetHeader, TargetCookie, TargetJSONPath, TargetXPath, TargetRegex, TargetBody, TargetTime, TargetSize}
	ops := []Operator{OpEquals, OpNotEquals, OpContains, OpNotContains, OpExists, OpNotExists, OpIsEmpty,
		OpGreater, OpLess, OpGreaterOrEqual, OpLessOrEqual, OpMatches, OpIsType}
	seen := map[[2]string]map[Outcome]bool{}
	for _, tc := range evalCases {
		k := [2]string{string(tc.a.Target), string(tc.a.Op)}
		if seen[k] == nil {
			seen[k] = map[Outcome]bool{}
		}
		seen[k][tc.outcome] = true
	}
	// Combinations that cannot pass (a status code always exists and is
	// never empty) or cannot fail need only the possible outcome.
	onlyFail := func(tg TargetKind, op Operator) bool {
		always := tg == TargetStatus || tg == TargetBody || tg == TargetTime || tg == TargetSize
		return always && op == OpNotExists || (tg == TargetStatus || tg == TargetTime || tg == TargetSize) && op == OpIsEmpty ||
			tg == TargetBody && (op == OpGreater || op == OpIsEmpty)
	}
	onlyPass := func(tg TargetKind, op Operator) bool {
		return (tg == TargetStatus || tg == TargetBody || tg == TargetTime || tg == TargetSize) && op == OpExists
	}
	for _, tg := range targets {
		for _, op := range ops {
			got := seen[[2]string{string(tg), string(op)}]
			if tg == TargetBody && slices.Contains([]Operator{OpLess, OpGreaterOrEqual, OpLessOrEqual}, op) {
				continue // numeric comparisons of a text body are covered by gt
			}
			if !onlyFail(tg, op) && !got[Passed] {
				t.Errorf("%s %s: no passing case", tg, op)
			}
			if !onlyPass(tg, op) && !got[Failed] {
				t.Errorf("%s %s: no failing case", tg, op)
			}
		}
	}
}

func TestEvaluateInvalid(t *testing.T) {
	t.Parallel()
	tests := []struct {
		a         Assertion
		code, msg string
	}{
		{a(TargetStatus, "", "between", "1"), CodeUnknownOperator, `The operator "between" is not known.`},
		{a("trailer", "", OpExists, ""), CodeUnknownTarget, `The target "trailer" is not known.`},
		{a(TargetHeader, " ", OpExists, ""), CodeMissingExpr, "The header name is missing."},
		{a(TargetCookie, "", OpExists, ""), CodeMissingExpr, "The cookie name is missing."},
		{a(TargetJSONPath, "", OpExists, ""), CodeMissingExpr, "The JSONPath is missing."},
		{a(TargetXPath, "", OpExists, ""), CodeMissingExpr, "The XPath is missing."},
		{a(TargetRegex, "", OpExists, ""), CodeMissingExpr, "The regular expression is missing."},
		{a(TargetJSONPath, "$.a[", OpExists, ""), "jsonpath.invalid_query", ""},
		{a(TargetXPath, "//a[", OpExists, ""), "xpath.invalid_query", ""},
		{a(TargetRegex, "(a", OpExists, ""), CodeInvalidRegex, "The regular expression \"(a\" is not valid: missing closing ): `(a`."},
		{a(TargetStatus, "", OpMatches, "[z-a]"), CodeInvalidRegex, "The regular expression \"[z-a]\" is not valid: invalid character class range: `z-a`."},
		{a(TargetStatus, "", OpGreater, "two hundred"), CodeInvalidNumber, `The expected value "two hundred" is not a number.`},
		{a(TargetSize, "", OpLess, "1KB"), CodeInvalidNumber, `The expected value "1KB" is not a number.`},
		{a(TargetHeader, "X-Missing", OpLess, "x"), CodeInvalidNumber, `The expected value "x" is not a number.`},
		{a(TargetTime, "", OpLess, "fast"), CodeInvalidNumber, `The expected value "fast" is not a number.`},
		{a(TargetStatus, "", OpIsType, "integer"), CodeInvalidType, `The type "integer" is not one of string, number, boolean, null, array or object.`},
	}
	for _, tt := range tests {
		t.Run(string(tt.a.Target)+" "+tt.a.Expr+" "+string(tt.a.Op), func(t *testing.T) {
			t.Parallel()
			r := EvaluateOne(t.Context(), testResponse(jsonBody), tt.a)
			if r.Outcome != Invalid || r.Code != tt.code || r.Actual != "" || r.Exists {
				t.Errorf("got %+v, want invalid with code %s", r, tt.code)
			}
			if tt.msg != "" && r.Message != tt.msg {
				t.Errorf("message = %q, want %q", r.Message, tt.msg)
			}
			if r.Message == "" {
				t.Error("no message")
			}
		})
	}
}

// A body in the wrong format fails the assertion: it is the response
// that is wrong, not the assertion.
func TestEvaluateWrongBodyFormat(t *testing.T) {
	t.Parallel()
	res := Evaluate(t.Context(), testResponse(xmlBody), []Assertion{
		a(TargetJSONPath, "$.id", OpExists, ""),
		a(TargetJSONPath, "$.id", OpEquals, "1"),
	})
	for _, r := range res {
		if r.Outcome != Failed || !strings.HasSuffix(r.Message, "but the body is not valid JSON.") {
			t.Errorf("got %+v", r)
		}
	}
	if got := res[1].Message; got != "Expected JSONPath $.id to equal 1, but the body is not valid JSON." {
		t.Errorf("message = %q", got)
	}
	r := EvaluateOne(t.Context(), testResponse(jsonBody), a(TargetXPath, "/a", OpExists, ""))
	if r.Outcome != Failed || r.Message != "Expected XPath /a to exist, but the body is not valid XML." {
		t.Errorf("got %+v", r)
	}
}

func TestEvaluateSizeAndTruncation(t *testing.T) {
	t.Parallel()
	resp := testResponse(strings.Repeat("é", 300))
	resp.Size = 1 << 20
	r := EvaluateOne(t.Context(), resp, a(TargetSize, "", OpEquals, "1048576"))
	if r.Outcome != Passed || r.Actual != "1048576 bytes" {
		t.Errorf("Size override: %+v", r)
	}
	r = EvaluateOne(t.Context(), resp, a(TargetBody, "", OpEquals, "x"))
	want := `"` + strings.Repeat("é", 100) + `…"`
	if r.Outcome != Failed || r.Actual != want {
		t.Errorf("long body shown as %q, want %q", r.Actual, want)
	}

	empty := Response{Status: 204}
	r = EvaluateOne(t.Context(), empty, a(TargetBody, "", OpIsEmpty, ""))
	if r.Outcome != Passed || r.Message != `Body is empty: it was "".` {
		t.Errorf("empty body: %+v", r)
	}
	r = EvaluateOne(t.Context(), empty, a(TargetHeader, "X", OpNotExists, ""))
	if r.Outcome != Passed {
		t.Errorf("nil header: %+v", r)
	}
}

func TestEvaluateParsesBodyOnceAndKeepsOrder(t *testing.T) {
	t.Parallel()
	list := []Assertion{
		a(TargetStatus, "", OpEquals, "201"),
		a(TargetJSONPath, "$.id", OpEquals, "42"),
		a(TargetJSONPath, "$.name", OpEquals, "nope"),
		a(TargetStatus, "", "bogus", ""),
	}
	res := Evaluate(t.Context(), testResponse(jsonBody), list)
	got := make([]Outcome, len(res))
	for i, r := range res {
		got[i] = r.Outcome
		if r.Assertion != list[i] {
			t.Errorf("result %d is for %+v", i, r.Assertion)
		}
	}
	if want := []Outcome{Passed, Passed, Failed, Invalid}; !slices.Equal(got, want) {
		t.Errorf("outcomes = %v, want %v", got, want)
	}
}
