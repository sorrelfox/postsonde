import { describe, expect, it } from "vitest";
import type { PartDTO } from "../../lib/collection";
import { toTableRows } from "./keyValueRows";
import { toParts } from "./partRows";

const part = (name: string, over: Partial<PartDTO> = {}): PartDTO => ({
  name,
  value: "",
  file: "",
  contentType: "",
  description: "",
  enabled: true,
  extra: "",
  ...over,
});

describe("partRows", () => {
  it("keeps each part's file, Content-Type and extra by row id through edits, moves and deletes", () => {
    const dtos = [part("a", { value: "1", contentType: "text/csv" }), part("b", { file: "x.png", extra: '{"k":1}' })];
    let n = 0;
    const rows = toTableRows(dtos, [], () => `r${++n}`);
    expect(rows.map((r) => [r.id, r.key, r.value])).toEqual([
      ["r1", "a", "1"],
      ["r2", "b", ""],
    ]);

    const edited = [{ ...rows[1], key: "photo", enabled: false }, { ...rows[0], value: "2" }, { id: "new", key: "c", value: "3", description: "d", enabled: true }];
    expect(toParts(edited, { dtos, rows })).toEqual([
      part("photo", { file: "x.png", extra: '{"k":1}', enabled: false }),
      part("a", { value: "2", contentType: "text/csv" }),
      part("c", { value: "3", description: "d" }),
    ]);
    expect(toParts([rows[1]], { dtos, rows })).toEqual([dtos[1]]);
  });
});
