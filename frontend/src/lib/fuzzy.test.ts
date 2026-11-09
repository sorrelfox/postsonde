import { describe, expect, it } from "vitest";
import { fuzzyMatch, isWordStart, normalizeQuery } from "./fuzzy";

const score = (q: string, text: string) => fuzzyMatch(normalizeQuery(q), text)?.score ?? -Infinity;

describe("fuzzyMatch", () => {
  it("matches characters in order, ignoring case", () => {
    expect(fuzzyMatch("gu", "Get user")?.positions).toEqual([0, 4]);
    expect(fuzzyMatch("usr", "Get user")?.positions).toEqual([4, 5, 7]);
    expect(fuzzyMatch("ug", "Get user")).toBeNull();
    expect(fuzzyMatch("x", "Get user")).toBeNull();
    expect(fuzzyMatch("", "anything")).toEqual({ score: 0, positions: [] });
  });

  it("prefers a contiguous occurrence at a word start", () => {
    expect(fuzzyMatch("user", "Superuser / User list")?.positions).toEqual([12, 13, 14, 15]);
    expect(fuzzyMatch("user", "superuser")?.positions).toEqual([5, 6, 7, 8]);
  });

  it("takes the shortest window", () => {
    // "a...b" with a later, tighter "ab".
    expect(fuzzyMatch("ab", "a x x x a b")?.positions).toEqual([8, 10]);
  });

  it("ranks prefixes, word starts and tight matches higher", () => {
    expect(score("get", "Get user")).toBeGreaterThan(score("get", "Forget user"));
    expect(score("cu", "Create user")).toBeGreaterThan(score("cu", "Account"));
    expect(score("lp", "List products")).toBeGreaterThan(score("lp", "Lookup"));
    expect(score("user", "Users")).toBeGreaterThan(score("user", "Users of the admin area"));
  });

  it("knows word starts", () => {
    expect(isWordStart("getUser", 3)).toBe(true);
    expect(isWordStart("get-user", 4)).toBe(true);
    expect(isWordStart("getuser", 3)).toBe(false);
    expect(isWordStart("GET", 1)).toBe(false);
  });

  it("drops spaces from the query", () => {
    expect(normalizeQuery(" Get  User ")).toBe("getuser");
  });
});
