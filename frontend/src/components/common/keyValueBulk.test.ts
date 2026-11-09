import { describe, expect, it } from "vitest";
import type { KeyValueRow } from "./KeyValueTable";
import { formatBulkText, parseBulkText, sameRows } from "./keyValueBulk";

let seq = 0;
const nextId = () => `new${++seq}`;

const rows = (): KeyValueRow[] => [
  { id: "a", enabled: true, key: "Accept", value: "application/json", description: "What we read" },
  { id: "b", enabled: false, key: "X-Debug", value: "1", description: "" },
  { id: "c", enabled: true, key: "X-Empty", value: "", description: "kept" },
  { id: "d", enabled: true, key: "X-Url", value: "http://a.test:8080/x?y=1", description: "" },
  { id: "e", enabled: true, key: "", value: "", description: "a note" },
  { id: "f", enabled: true, key: "X-Pad", value: "  two spaces", description: "" },
  { id: "g", enabled: true, key: "Accept", value: "text/plain", description: "second" },
  { id: "h", enabled: true, key: "X-Tpl", value: "{{token}}", description: "" },
];

describe("bulk edit text (HTTP-08)", () => {
  it("writes one key: value line per row, disabled rows after //", () => {
    expect(formatBulkText(rows())).toBe(
      [
        "Accept: application/json",
        "// X-Debug: 1",
        "X-Empty:",
        "X-Url: http://a.test:8080/x?y=1",
        ":",
        "X-Pad:   two spaces",
        "Accept: text/plain",
        "X-Tpl: {{token}}",
      ].join("\n"),
    );
    expect(formatBulkText([])).toBe("");
  });

  it("round-trips without losing a row, an id or a description", () => {
    const before = rows();
    const after = parseBulkText(formatBulkText(before), before, nextId);
    expect(after).toEqual(before);
    expect(sameRows(after, before)).toBe(true);
  });

  it("reads // and # lines as disabled rows, and skips blank lines", () => {
    const got = parseBulkText("a: 1\n\n  // b: 2\n#c:3\n   \r\nd\ne:f:g\r\n//\n", [], nextId);
    expect(got.map(({ enabled, key, value }) => ({ enabled, key, value }))).toEqual([
      { enabled: true, key: "a", value: "1" },
      { enabled: false, key: "b", value: "2" },
      { enabled: false, key: "c", value: "3" },
      { enabled: true, key: "d", value: "" },
      { enabled: true, key: "e", value: "f:g" },
    ]);
    expect(new Set(got.map((r) => r.id)).size).toBe(5);
  });

  it("keeps ids and descriptions by key when rows are edited, reordered or added", () => {
    const before = rows().slice(0, 3);
    const after = parseBulkText("X-Empty: now set\nAccept: text/html\nX-New: 1", before, () => "fresh");
    expect(after).toEqual([
      { id: "c", enabled: true, key: "X-Empty", value: "now set", description: "kept" },
      { id: "a", enabled: true, key: "Accept", value: "text/html", description: "What we read" },
      { id: "fresh", enabled: true, key: "X-New", value: "1", description: "" },
    ]);
    expect(sameRows(after, before)).toBe(false);
  });

  it("trims the key and takes the value after one space", () => {
    const [row] = parseBulkText("  Key  :  value ", [], nextId);
    expect(row).toMatchObject({ key: "Key", value: " value " });
  });
});
