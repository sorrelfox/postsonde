import { describe, expect, it } from "vitest";
import { httpResponse } from "../test/httpResponse";
import {
  MAX_FILTERED_REQUESTS,
  MAX_RECENT_FILTERS,
  MAX_SAVED_EXPRESSION,
  addRecentFilter,
  canFilter,
  decodeResponseFilters,
  defaultFilterSyntax,
  encodeResponseFilters,
  withRequestFilter,
  type ResponseFilters,
} from "./responseFilter";

describe("response filter helpers (HTTP-36)", () => {
  it("picks the syntax from the body's language, and from its first character for text", () => {
    expect(defaultFilterSyntax(httpResponse({ bodyLanguage: "json" }))).toBe("jsonpath");
    expect(defaultFilterSyntax(httpResponse({ bodyLanguage: "xml" }))).toBe("xpath");
    expect(defaultFilterSyntax(httpResponse({ bodyLanguage: "html" }))).toBe("xpath");
    expect(defaultFilterSyntax(httpResponse({ bodyLanguage: "text", body: "  <a/>" }))).toBe("xpath");
    expect(defaultFilterSyntax(httpResponse({ bodyLanguage: "text", body: "[1]" }))).toBe("jsonpath");
    expect(canFilter(httpResponse({ body: "" }))).toBe(false);
    expect(canFilter(httpResponse({ bodyHex: true }))).toBe(false);
  });

  it("reads only what it understands", () => {
    expect(decodeResponseFilters(null)).toBeNull();
    expect(decodeResponseFilters([])).toBeNull();
    expect(
      decodeResponseFilters({
        req_1: { syntax: "css", expression: 3, recent: [{ syntax: "xpath", expression: "//a" }, { syntax: "x", expression: "b" }, "c"] },
        req_2: "junk",
        "": { expression: "$" },
      }),
    ).toEqual({ req_1: { syntax: null, expression: "", recent: [{ syntax: "xpath", expression: "//a" }] } });
  });

  it("saves little: empty filters and long expressions are left out, the latest requests kept", () => {
    let filters: ResponseFilters = {};
    for (let i = 0; i < MAX_FILTERED_REQUESTS + 5; i++) {
      filters = withRequestFilter(filters, `req_${i}`, { syntax: null, expression: "$", recent: [] });
    }
    filters = withRequestFilter(filters, "req_0", { syntax: "xpath", expression: "x".repeat(MAX_SAVED_EXPRESSION + 1), recent: [] });
    filters = withRequestFilter(filters, "req_empty", { syntax: null, expression: "", recent: [] });
    const saved = encodeResponseFilters(filters);
    expect(Object.keys(saved).length).toBe(MAX_FILTERED_REQUESTS);
    expect(saved.req_0).toEqual({ syntax: "xpath" });
    expect(saved.req_4).toBeUndefined();
    expect(saved.req_empty).toBeUndefined();
  });

  it("keeps the recent expressions newest first, without duplicates", () => {
    let recent = addRecentFilter([], { syntax: "jsonpath", expression: "$.a" });
    recent = addRecentFilter(recent, { syntax: "xpath", expression: "$.a" });
    recent = addRecentFilter(recent, { syntax: "jsonpath", expression: "$.a" });
    expect(recent).toEqual([
      { syntax: "jsonpath", expression: "$.a" },
      { syntax: "xpath", expression: "$.a" },
    ]);
    for (let i = 0; i < 20; i++) recent = addRecentFilter(recent, { syntax: "jsonpath", expression: `$[${i}]` });
    expect(recent.length).toBe(MAX_RECENT_FILTERS);
    expect(recent[0].expression).toBe("$[19]");
  });
});
