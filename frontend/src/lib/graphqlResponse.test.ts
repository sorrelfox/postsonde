import { Text } from "@codemirror/state";
import { describe, expect, it } from "vitest";
import { httpResponse } from "../test/httpResponse";
import { formatErrorPath, printGraphqlResponse, queryLocationOffset, readGraphqlResponse, readJson, type GraphqlResponse } from "./graphqlResponse";

const read = (body: string, over = {}): GraphqlResponse | null => readGraphqlResponse(httpResponse({ body, ...over }));
const must = (body: string): GraphqlResponse => {
  const r = read(body);
  if (r === null) throw new Error("not a GraphQL response");
  return r;
};

describe("readJson", () => {
  it("keeps literals as received and rejects invalid JSON", () => {
    const node = readJson('{"n": 12345678901234567890, "s": "\\u00e9\\n", "a": [true, null, -1.5e3]}');
    expect(node).toEqual({
      t: "obj",
      members: [
        { key: "n", raw: '"n"', value: { t: "lit", raw: "12345678901234567890" } },
        { key: "s", raw: '"s"', value: { t: "lit", raw: '"\\u00e9\\n"' } },
        {
          key: "a",
          raw: '"a"',
          value: { t: "arr", items: [{ t: "lit", raw: "true" }, { t: "lit", raw: "null" }, { t: "lit", raw: "-1.5e3" }] },
        },
      ],
    });
    for (const bad of ["", "{", '{"a":}', "[1,]", '{"a":1} x', "01", '"a\nb"']) expect(readJson(bad)).toBeNull();
  });
});

describe("readGraphqlResponse", () => {
  it("reads data only", () => {
    const r = must('{"data":{"user":{"name":"Ada"}}}');
    expect(r).toMatchObject({ hasData: true, dataPresent: true, errors: [], hasExtensions: false });
  });

  it("reads errors only, with paths, locations and extensions", () => {
    const r = must(
      '{"errors":[{"message":"Syntax Error: Expected Name","locations":[{"line":2,"column":5},{"line":0,"column":1}]},' +
        '{"message":"Forbidden","path":["user",0,"email"],"extensions":{"code":"FORBIDDEN"}},{"extensions":{"a":1,"b":2}}]}',
    );
    expect(r.hasData).toBe(false);
    expect(r.hasExtensions).toBe(true);
    expect(r.errors).toEqual([
      { message: "Syntax Error: Expected Name", path: null, locations: [{ line: 2, column: 5 }], extensions: "" },
      { message: "Forbidden", path: ["user", 0, "email"], locations: [], extensions: "FORBIDDEN" },
      { message: "", path: null, locations: [], extensions: "a, b" },
    ]);
  });

  it("reads partial data with errors and data: null with errors", () => {
    const partial = must('{"data":{"user":null},"errors":[{"message":"Not found","path":["user"]}]}');
    expect(partial).toMatchObject({ hasData: true, dataPresent: true });
    expect(partial.errors).toHaveLength(1);
    const none = must('{"errors":[{"message":"boom"}],"data":null}');
    expect(none).toMatchObject({ hasData: true, dataPresent: false });
  });

  it("falls back for other JSON and bodies it cannot read whole", () => {
    expect(read('{"user":{"name":"Ada"}}')).toBeNull();
    expect(read("[1,2]")).toBeNull();
    expect(read('{"errors":"nope"}')).toBeNull();
    expect(read('{"data":{}}', { bodyLanguage: "xml" })).toBeNull();
    expect(read('{"data":{}}', { prettyState: "too_large" })).toBeNull();
    expect(read('{"data":{}}', { bodyClipped: true })).toBeNull();
    expect(read('{"data":')).toBeNull();
  });
});

describe("printGraphqlResponse", () => {
  const body = '{"data":{"user":{"id":1,"extensions":{"x":1},"tags":[]}},"errors":[{"message":"a","extensions":{"code":"C"}},{"message":"b"}],"extensions":{"cost":3}}';

  it("indents like Go's beautifier and finds data, errors and each error", () => {
    const printed = printGraphqlResponse(must(body), false);
    const expected = JSON.stringify(JSON.parse(body), null, 2);
    expect(printed.text).toBe(expected);
    const lines = printed.text.split("\n");
    expect(printed.data).toEqual({ from: 2, to: 10 });
    expect(lines[1]).toBe('  "data": {');
    expect(lines[9]).toBe("  },");
    expect(printed.errors?.from).toBe(11);
    expect(lines[10]).toBe('  "errors": [');
    expect(lines[(printed.errors?.to ?? 0) - 1]).toBe("  ],");
    expect(printed.errorLines.map((n) => lines[n - 1])).toEqual(["    {", "    {"]);
  });

  it("hides the response's and the errors' extensions, not those in data", () => {
    const printed = printGraphqlResponse(must(body), true);
    const value = JSON.parse(printed.text) as Record<string, unknown>;
    expect(value).toEqual({
      data: { user: { id: 1, extensions: { x: 1 }, tags: [] } },
      errors: [{ message: "a" }, { message: "b" }],
    });
  });

  it("keeps big numbers and escapes as received", () => {
    const printed = printGraphqlResponse(must('{"data":{"id":12345678901234567890,"s":"\\u00e9"}}'), false);
    expect(printed.text).toBe('{\n  "data": {\n    "id": 12345678901234567890,\n    "s": "\\u00e9"\n  }\n}');
  });
});

describe("queryLocationOffset", () => {
  const doc = Text.of(["query {", "  \u{1F600}x { id }", "}"]);

  it("counts columns in code points from 1", () => {
    expect(queryLocationOffset(doc, { line: 1, column: 1 })).toBe(0);
    expect(queryLocationOffset(doc, { line: 2, column: 3 })).toBe(8 + 2);
    // The emoji is one code point but two UTF-16 units.
    expect(queryLocationOffset(doc, { line: 2, column: 4 })).toBe(8 + 4);
  });

  it("clamps past the end of a line and of the document", () => {
    expect(queryLocationOffset(doc, { line: 1, column: 99 })).toBe(7);
    expect(queryLocationOffset(doc, { line: 9, column: 1 })).toBe(doc.length);
  });
});

describe("formatErrorPath", () => {
  it("joins fields with dots and indexes in brackets", () => {
    expect(formatErrorPath(["user", "friends", 0, "name"])).toBe("user.friends[0].name");
    expect(formatErrorPath([3])).toBe("[3]");
  });
});
