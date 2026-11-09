import { describe, expect, it, vi } from "vitest";

vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http", () => ({ HTTPService: {} }));

import { foldRows, type DiffRowDTO, type ShownRow } from "./responseDiff";

/** Rows from a pattern: "s" same, "a" added, "r" removed, "c" changed. */
function rows(pattern: string): DiffRowDTO[] {
  const kinds: Record<string, string> = { s: "same", a: "added", r: "removed", c: "changed" };
  return [...pattern].map((k, i) => ({
    kind: kinds[k] ?? "same",
    left: k === "a" ? null : { n: i + 1, text: `l${i}` },
    right: k === "r" ? null : { n: i + 1, text: `r${i}` },
    path: "",
  }));
}

/** Shown rows as a pattern: a row by its index, a fold as [start+count]. */
function shape(shown: ShownRow[]): string {
  return shown.map((s) => (s.kind === "row" ? String(s.index) : `[${s.start}+${s.count}]`)).join(" ");
}

describe("foldRows", () => {
  it("keeps context around changes and folds the rest", () => {
    // 10 same, a change, 10 same.
    const r = rows("ssssssssss" + "c" + "ssssssssss");
    expect(shape(foldRows(r, 3))).toBe("[0+7] 7 8 9 10 11 12 13 [14+7]");
  });

  it("folds only runs that leave two or more lines to fold", () => {
    expect(shape(foldRows(rows("csssssssc"), 3))).toBe("0 1 2 3 4 5 6 7 8");
    expect(shape(foldRows(rows("cssssssssc"), 3))).toBe("0 1 2 3 [4+2] 6 7 8 9");
  });

  it("shows everything when nothing changed but a short text", () => {
    expect(shape(foldRows(rows("s"), 3))).toBe("0");
    expect(shape(foldRows(rows(""), 3))).toBe("");
    expect(shape(foldRows(rows("ssss"), 3))).toBe("[0+4]");
  });

  it("unfolds the folds asked for", () => {
    const r = rows("ssssssssss" + "a" + "ssssssssss");
    expect(shape(foldRows(r, 3, new Set([0])))).toBe("0 1 2 3 4 5 6 7 8 9 10 11 12 13 [14+7]");
  });
});
