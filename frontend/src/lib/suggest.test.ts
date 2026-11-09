import { describe, expect, it } from "vitest";
import { filterByMatch, matchRank, MAX_SUGGESTIONS } from "./suggest";

describe("matchRank", () => {
  it.each([
    ["Content-Type", "content-type", 0],
    ["Content-Type", "cont", 1],
    ["Content-Type", "", 1],
    ["Accept-Encoding", "enc", 2],
    ["application/json", "json", 2],
    ["max-age=0", "0", 2],
    ["Authorization", "thor", 3],
    ["Content-Type", "ctype", 4],
    ["X-Request-ID", "xrid", 4],
  ] as const)("%s for %j is %d", (candidate, query, rank) => {
    expect(matchRank(candidate, query)).toBe(rank);
  });

  it("is null when the characters are not all there in order", () => {
    expect(matchRank("Accept", "z")).toBeNull();
    expect(matchRank("Accept", "tpa")).toBeNull();
    expect(matchRank("Accept", "accepted")).toBeNull();
    // Out-of-order matches must start at the first character.
    expect(matchRank("Accept-Encoding", "con")).toBeNull();
  });
});

describe("filterByMatch", () => {
  const names = ["Accept", "Accept-Encoding", "Cache-Control", "Content-Encoding", "Encoding-X", "Connection"];

  it("orders by rank, keeping the list order within a rank", () => {
    expect(filterByMatch(names, "enc", (n) => n)).toEqual(["Encoding-X", "Accept-Encoding", "Content-Encoding"]);
    expect(filterByMatch(names, "con", (n) => n)).toEqual(["Content-Encoding", "Connection", "Cache-Control"]);
  });

  it("caps the result", () => {
    const many = Array.from({ length: 30 }, (_, i) => `X-Header-${i}`);
    expect(filterByMatch(many, "x", (n) => n)).toHaveLength(MAX_SUGGESTIONS);
    expect(filterByMatch(many, "x", (n) => n, 3)).toEqual(["X-Header-0", "X-Header-1", "X-Header-2"]);
  });
});
