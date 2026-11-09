import { describe, expect, it } from "vitest";
import { renderedIndices, visibleRange } from "./useVirtualRows";

describe("visibleRange", () => {
  it("covers the viewport plus overscan, clamped", () => {
    expect(visibleRange(0, 300, 30, 1000, 10)).toEqual({ first: 0, last: 20 });
    expect(visibleRange(3000, 300, 30, 1000, 10)).toEqual({ first: 90, last: 120 });
    expect(visibleRange(29_700, 300, 30, 1000, 10)).toEqual({ first: 980, last: 1000 });
    expect(visibleRange(0, 300, 30, 0, 10)).toEqual({ first: 0, last: 0 });
  });
});

describe("renderedIndices", () => {
  it("adds pinned rows outside the range in order", () => {
    expect(renderedIndices({ first: 5, last: 8 }, 100, [2, 50, 6, -1, 200])).toEqual([2, 5, 6, 7, 50]);
    expect(renderedIndices({ first: 0, last: 3 }, 3)).toEqual([0, 1, 2]);
  });
});
