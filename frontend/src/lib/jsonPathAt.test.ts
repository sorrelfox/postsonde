import { describe, expect, it } from "vitest";
import { expectedOf, jsonValueAt, nameSegment } from "./jsonPathAt";

const doc = `{
  "id": 42,
  "name": "Ada \\"L\\"",
  "tags": ["a", "b"],
  "nested": { "x-y": null, "ok": true },
  "list": [{ "n": 1.5e3 }]
}`;

function at(needle: string, shift = 0) {
  const i = doc.indexOf(needle);
  if (i < 0) throw new Error(needle);
  return jsonValueAt(doc, i + shift);
}

describe("jsonValueAt", () => {
  it("finds the innermost value and its JSONPath", () => {
    expect(at("42")).toMatchObject({ path: "$.id", kind: "number", text: "42" });
    expect(at('"Ada', 2)).toMatchObject({ path: "$.name", kind: "string" });
    expect(at('"b"')).toMatchObject({ path: "$.tags[1]", text: '"b"' });
    expect(at("null")).toMatchObject({ path: "$.nested['x-y']", kind: "null" });
    expect(at("true")).toMatchObject({ path: "$.nested.ok", kind: "boolean" });
    expect(at("1.5e3")).toMatchObject({ path: "$.list[0].n", kind: "number", text: "1.5e3" });
  });

  it("takes a member's name as its value, and a container between its values", () => {
    expect(at('"tags"', 1)).toMatchObject({ path: "$.tags", kind: "array" });
    expect(at(', "b"')).toMatchObject({ path: "$.tags", kind: "array" });
    expect(jsonValueAt(doc, 0)).toMatchObject({ path: "$", kind: "object" });
  });

  it("gives up on text that is not JSON", () => {
    expect(jsonValueAt("{ nope }", 3)).toBeNull();
    expect(jsonValueAt('{"a": 1} trailing', 6)).toBeNull();
    expect(jsonValueAt("", 0)).toBeNull();
  });

  it("writes names that are not identifiers in brackets", () => {
    expect(nameSegment("ok_1")).toBe(".ok_1");
    expect(nameSegment("it's")).toBe("['it\\'s']");
    expect(nameSegment("a\\b")).toBe("['a\\\\b']");
  });

  it("gives the expected value of an equals assertion", () => {
    expect(expectedOf(at('"Ada', 2)!)).toBe('Ada "L"');
    expect(expectedOf(at("42")!)).toBe("42");
    expect(expectedOf(at('"tags"', 1)!)).toBe('["a","b"]');
  });
});
