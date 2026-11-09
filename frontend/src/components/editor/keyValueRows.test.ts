import { describe, expect, it } from "vitest";
import type { KeyValueDTO } from "../../lib/collection";
import { toFileRows, toTableRows } from "./keyValueRows";

const dto = (name: string, over: Partial<KeyValueDTO> = {}): KeyValueDTO => ({
  name,
  value: `${name}-value`,
  description: "",
  enabled: true,
  extra: "",
  ...over,
});

describe("keyValueRows", () => {
  it("maps file rows to table rows, reusing ids by position", () => {
    let n = 0;
    const createId = () => `id${++n}`;
    const rows = toTableRows([dto("a", { description: "d" }), dto("b", { enabled: false })], [], createId);
    expect(rows).toEqual([
      { id: "id1", enabled: true, key: "a", value: "a-value", description: "d" },
      { id: "id2", enabled: false, key: "b", value: "b-value", description: "" },
    ]);
    const again = toTableRows([dto("a"), dto("b"), dto("c")], rows, createId);
    expect(again.map((r) => r.id)).toEqual(["id1", "id2", "id3"]);
    expect(toTableRows(null)).toEqual([]);
  });

  it("maps table rows back, keeping each row's extra by id through reorders and deletes", () => {
    const dtos = [dto("a", { extra: '{"x":1}' }), dto("b", { extra: '{"y":2}' })];
    const rows = toTableRows(dtos, [], (() => {
      let n = 0;
      return () => `r${++n}`;
    })());
    const [a, b] = rows;
    if (a === undefined || b === undefined) throw new Error("rows");
    const next = toFileRows(
      [{ ...b, value: "changed" }, { id: "new", enabled: true, key: "c", value: "", description: "" }],
      { dtos, rows },
    );
    expect(next).toEqual([
      { name: "b", value: "changed", description: "", enabled: true, extra: '{"y":2}' },
      { name: "c", value: "", description: "", enabled: true, extra: "" },
    ]);
  });
});
