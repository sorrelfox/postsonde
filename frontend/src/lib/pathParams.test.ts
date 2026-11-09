import { describe, expect, it } from "vitest";
import fixture from "../../../internal/core/request/testdata/path_params.json?raw";
import type { KeyValueDTO } from "./collection";
import { findPathParams, pathParamNames, syncPathParams } from "./pathParams";

interface DetectCase {
  name: string;
  url: string;
  found: string[];
  names: string[];
}

const cases = (JSON.parse(fixture) as { detect: DetectCase[] }).detect;

const row = (name: string, value = "", over: Partial<KeyValueDTO> = {}): KeyValueDTO => ({
  name,
  value,
  description: "",
  enabled: true,
  extra: "",
  ...over,
});

describe("path parameter detection (shared with Go: path_params.json)", () => {
  it("has cases", () => {
    expect(cases.length).toBeGreaterThan(20);
  });

  it.each(cases.map((c) => [c.name, c] as const))("%s", (_name, c) => {
    const spans = findPathParams(c.url);
    expect(spans.map((s) => c.url.slice(s.start, s.end))).toEqual(c.found);
    for (const s of spans) expect(c.url.slice(s.start, s.end).replace(/^[:{]|}$/g, "")).toBe(s.name);
    expect(pathParamNames(c.url)).toEqual(c.names);
  });
});

describe("syncPathParams", () => {
  it("returns the same rows when the names match", () => {
    const rows = [row("id", "1"), row("sub", "x")];
    expect(syncPathParams("/a/:id/{sub}", rows).rows).toBe(rows);
    expect(syncPathParams("/a", null).rows).toBeNull();
    const empty: KeyValueDTO[] = [];
    expect(syncPathParams("/a?x=:id", empty).rows).toBe(empty);
  });

  it("adds rows for new names in URL order, keeping existing rows", () => {
    const id = row("id", "1", { description: "User", extra: '{"x":1}' });
    const { rows, removed } = syncPathParams("/:org/users/:id/:id", [id]);
    expect(rows).toEqual([row("org"), id]);
    expect(rows?.[1]).toBe(id);
    expect(removed).toEqual([]);
  });

  it("removes rows whose names left the URL and restores stashed ones", () => {
    const id = row("id", "42");
    const first = syncPathParams("/users", [id]);
    expect(first.rows).toEqual([]);
    expect(first.removed).toEqual([id]);
    expect(syncPathParams("/users/{id}", first.rows, { id }).rows).toEqual([id]);
  });

  it("prefers the draft's row over the stash, and keeps the first of duplicate names", () => {
    const a = row("id", "draft");
    const b = row("id", "dup");
    const { rows, removed } = syncPathParams("/:id", [a, b], { id: row("id", "stashed") });
    expect(rows).toEqual([a]);
    // A duplicate of a name still in the URL is dropped, not stashed.
    expect(removed).toEqual([]);
  });
});
