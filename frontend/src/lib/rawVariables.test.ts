import { describe, expect, it } from "vitest";
import { formatRawVariables, parseRawVariables, type RawVariable } from "./rawVariables";
import { SECRET_MASK } from "./templateVariables";

const v = (name: string, over: Partial<RawVariable> = {}): RawVariable => ({
  name,
  value: `${name}-value`,
  description: "",
  secret: false,
  enabled: true,
  localValue: null,
  ...over,
});

// Every member a variable can have, in every combination the table can hold.
const ALL: RawVariable[] = [
  v("baseUrl", { value: "https://api.example" }),
  v("token", { value: "s3cret-token", secret: true }),
  v("emptySecret", { value: "", secret: true }),
  v("user", { value: "team", localValue: "me" }),
  v("emptyLocal", { value: "shared", localValue: "" }),
  v("old", { value: "{{baseUrl}}/v1", enabled: false, description: "Kept for v1" }),
  v("auth", { value: '{"token": "t0k", "users": [{"id": 7}]}' }),
  v("quotes", { value: 'a "quoted" \\ back\nslash\ttab' }),
  v("région", { value: "Zürich · 東京 \\{{literal}}" }),
  v("dup", { value: "first" }),
  v("dup", { value: "second", secret: true }),
  v("", { value: "no name" }),
];

describe("raw variables (ENV-07)", () => {
  it("round-trips with the table", () => {
    const text = formatRawVariables(ALL);
    const parsed = parseRawVariables(text, ALL);
    expect(parsed).toEqual({ ok: true, variables: ALL });
    if (!parsed.ok) return;
    expect(formatRawVariables(parsed.variables)).toBe(text);
  });

  it("writes one variable per line with only the members that differ from the defaults", () => {
    expect(formatRawVariables([])).toBe("[]\n");
    expect(
      formatRawVariables([
        v("host", { value: "localhost" }),
        v("token", { value: "x", secret: true, description: "Personal" }),
        v("user", { value: "team", localValue: "me", enabled: false }),
      ]),
    ).toBe(
      [
        "[",
        '  { "name": "host", "value": "localhost" },',
        `  { "name": "token", "value": "${SECRET_MASK}", "secret": true, "description": "Personal" },`,
        '  { "name": "user", "value": "team", "localValue": "me", "enabled": false }',
        "]",
        "",
      ].join("\n"),
    );
  });

  it("never puts a secret value in the text", () => {
    const text = formatRawVariables(ALL);
    expect(text).not.toContain("s3cret-token");
    expect(text).toContain(SECRET_MASK);
    // A secret without a value on this computer shows "", not the mask.
    expect(text).toContain('{ "name": "emptySecret", "value": "", "secret": true }');
  });

  it("keeps the secret value the mask stands for, by name, also after a reorder or an edit elsewhere", () => {
    const current = [v("token", { value: "s3cret", secret: true }), v("a")];
    const text = `[
      { "name": "a", "value": "changed" },
      { "name": "token", "value": "${SECRET_MASK}", "secret": true, "description": "new" }
    ]`;
    expect(parseRawVariables(text, current)).toEqual({
      ok: true,
      variables: [v("a", { value: "changed" }), v("token", { value: "s3cret", secret: true, description: "new" })],
    });
    // Typing a new value replaces it.
    const typed = parseRawVariables(`[{ "name": "token", "value": "n3w", "secret": true }]`, current);
    expect(typed).toEqual({ ok: true, variables: [v("token", { value: "n3w", secret: true })] });
  });

  it("never takes the mask as a value", () => {
    const current = [v("token", { value: "s3cret", secret: true }), v("plain", { value: "p" })];
    const cases: [string, RegExp][] = [
      // Made not secret: the mask would become its value.
      [`[{ "name": "token", "value": "${SECRET_MASK}" }]`, /is the mask of a secret value/],
      // Renamed: there is no secret value under the new name.
      [`[{ "name": "renamed", "value": "${SECRET_MASK}", "secret": true }]`, /keeps no secret value/],
      // Made secret with the mask typed in.
      [`[{ "name": "plain", "value": "${SECRET_MASK}", "secret": true }]`, /keeps no secret value/],
      [`{ "token": "${SECRET_MASK}" }`, /is the mask of a secret value/],
    ];
    for (const [text, error] of cases) {
      const got = parseRawVariables(text, current);
      expect(got.ok, text).toBe(false);
      if (!got.ok) expect(got.error).toMatch(error);
    }
  });

  it("reads a plain object as variables that are not secret", () => {
    expect(parseRawVariables('{ "host": "localhost", "port": 8080, "auth": { "token": "t" }, "on": true }', [])).toEqual({
      ok: true,
      variables: [
        v("host", { value: "localhost" }),
        v("port", { value: "8080" }),
        v("auth", { value: '{"token":"t"}' }),
        v("on", { value: "true" }),
      ],
    });
  });

  it("defaults the members left out", () => {
    expect(parseRawVariables('[{ "name": "a" }]', [])).toEqual({ ok: true, variables: [v("a", { value: "" })] });
  });

  it("says what is wrong with text it cannot use", () => {
    const cases: [string, RegExp][] = [
      ["[{", /^Not valid JSON: /],
      ['"text"', /Write a list of variables/],
      ["[1]", /Variable 1 is not an object/],
      ['[{ "value": "x" }]', /Variable 1 needs a "name"/],
      ['[{ "name": "a" }, { "name": "b", "secert": true }]', /Variable 2: "secert" is not a member of a variable/],
      ['[{ "name": "a", "value": 1 }]', /"value" must be a string/],
      ['[{ "name": "a", "secret": "yes" }]', /"secret" must be a boolean/],
      ['[{ "name": "a", "enabled": 0 }]', /"enabled" must be a boolean/],
      ['[{ "name": "a", "localValue": 3 }]', /"localValue" must be a string/],
      ['[{ "name": "a", "description": null }]', /"description" must be a string/],
      ['[{ "name": "a", "value": "x", "secret": true, "localValue": "y" }]', /a secret variable has no local value/],
    ];
    for (const [text, error] of cases) {
      const got = parseRawVariables(text, []);
      expect(got.ok, text).toBe(false);
      if (!got.ok) expect(got.error).toMatch(error);
    }
  });
});
