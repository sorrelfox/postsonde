import { describe, expect, it } from "vitest";
import { diffLines, MAX_DIFF_LINES } from "./lineDiff";

describe("diffLines", () => {
  it("lines up unchanged lines and marks the others", () => {
    const before = ["{", '  "url": "/mine",', '  "method": "GET"', "}"].join("\n");
    const after = ["{", '  "url": "/theirs",', '  "method": "GET",', '  "name": "x"', "}"].join("\n");
    expect(diffLines(before, after)).toEqual([
      { kind: "same", text: "{" },
      { kind: "removed", text: '  "url": "/mine",' },
      { kind: "removed", text: '  "method": "GET"' },
      { kind: "added", text: '  "url": "/theirs",' },
      { kind: "added", text: '  "method": "GET",' },
      { kind: "added", text: '  "name": "x"' },
      { kind: "same", text: "}" },
    ]);
  });

  it("finds nothing to mark in equal texts", () => {
    expect(diffLines("a\nb", "a\nb").every((l) => l.kind === "same")).toBe(true);
  });

  it("handles texts with nothing in common and empty ones", () => {
    expect(diffLines("a", "b")).toEqual([
      { kind: "removed", text: "a" },
      { kind: "added", text: "b" },
    ]);
    expect(diffLines("", "x")).toEqual([
      { kind: "removed", text: "" },
      { kind: "added", text: "x" },
    ]);
  });

  it("does not align texts that are too long", () => {
    const long = Array.from({ length: MAX_DIFF_LINES + 1 }, (_, i) => String(i)).join("\n");
    const lines = diffLines(long, long);
    expect(lines).toHaveLength(2 * (MAX_DIFF_LINES + 1));
    expect(lines[0]?.kind).toBe("removed");
  });
});
