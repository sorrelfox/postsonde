import { describe, expect, it } from "vitest";
import table from "../../../internal/core/template/testdata/filters.json.golden?raw";
import fixture from "../../../internal/core/template/testdata/pipes.json?raw";
import { filterDescription, TEMPLATE_FILTERS, parsePipe, splitPipe, templateFilter } from "./templateFilters";

interface PipeCase {
  name: string;
  pipe: boolean;
  input?: string;
  quoted?: boolean;
  filters?: string[];
  keys?: string[];
  error?: string;
}

describe("filter pipes (shared with Go: pipes.json, filters.json.golden)", () => {
  it("has Go's filter table", () => {
    expect(TEMPLATE_FILTERS).toEqual(JSON.parse(table));
    expect(templateFilter("hmacSha256")?.keys).toBe(1);
    expect(templateFilter("nope")).toBeUndefined();
  });

  it("describes every filter from the catalogue, in Go's English", () => {
    for (const f of TEMPLATE_FILTERS) expect(filterDescription(f), f.name).toBe(f.description);
  });

  const cases = (JSON.parse(fixture) as { cases: PipeCase[] }).cases;
  for (const c of cases) {
    it(`reads ${c.name}`, () => {
      const stages = splitPipe(c.name);
      expect(stages !== null).toBe(c.pipe);
      if (stages === null) return;
      const p = parsePipe(stages);
      if (c.error !== undefined) {
        expect(p).toEqual({ error: c.error });
        return;
      }
      if ("error" in p) throw new Error(p.error);
      expect(p.input).toEqual({ text: c.input, quoted: c.quoted });
      expect(p.steps.map((s) => s.name)).toEqual(c.filters);
      expect(p.steps.flatMap((s) => s.keys.filter((k) => !k.quoted).map((k) => k.text))).toEqual(c.keys ?? []);
    });
  }
});
