import { describe, expect, it } from "vitest";
import { suggestHeaderNames, suggestHeaderValues } from "./headerSuggestions";
import { t } from "../i18n";
import { findHeader, HTTP_HEADERS } from "./httpHeaders";

const TOKEN = /^[!#$%&'*+\-.^_`|~0-9A-Za-z]+$/;

describe("HTTP_HEADERS", () => {
  it("is sorted by name and has no duplicate names, in any case", () => {
    const lower = HTTP_HEADERS.map((h) => h.name.toLowerCase());
    expect(lower).toEqual([...lower].sort());
    expect(new Set(lower).size).toBe(lower.length);
  });

  it("has valid names, descriptions and unique values", () => {
    for (const h of HTTP_HEADERS) {
      expect(h.name, h.name).toMatch(TOKEN);
      expect(t(h.descriptionKey).trim(), h.name).not.toBe("");
      const values = (h.values ?? []).map((v) => v.value);
      expect(new Set(values).size, h.name).toBe(values.length);
      for (const v of values) expect(v, h.name).not.toMatch(/[\r\n\0]/);
    }
  });

  it("has the common standard and non-standard headers", () => {
    for (const name of ["Accept", "Authorization", "Content-Type", "Cache-Control", "User-Agent", "X-Request-ID", "X-Forwarded-For"]) {
      expect(findHeader(name)?.name).toBe(name);
    }
  });

  it("notes the headers the HTTP client sets or handles itself", () => {
    for (const name of ["Host", "Content-Length", "Transfer-Encoding", "Connection", "User-Agent", "Content-Type"]) {
      expect(findHeader(name)?.clientKey, name).toBeTruthy();
    }
    expect(findHeader("Accept")?.clientKey).toBeUndefined();
  });

  it("finds headers case-insensitively", () => {
    expect(findHeader("content-TYPE")?.name).toBe("Content-Type");
    expect(findHeader(" accept ")?.name).toBe("Accept");
    expect(findHeader("X-Unknown")).toBeUndefined();
  });
});

describe("suggestHeaderNames", () => {
  it("matches case-insensitively and inserts the canonical spelling", () => {
    const items = suggestHeaderNames("content-t");
    expect(items[0]).toEqual({
      value: "Content-Type",
      detail: "Media type of the request body",
      note: expect.stringContaining("body type"),
    });
  });

  it("puts prefixes first and caps the list", () => {
    const items = suggestHeaderNames("acc");
    expect(items.slice(0, 5).map((s) => s.value)).toEqual([
      "Accept",
      "Accept-Charset",
      "Accept-Encoding",
      "Accept-Language",
      "Accept-Patch",
    ]);
    expect(suggestHeaderNames("e").length).toBe(8);
    expect(suggestHeaderNames("x-req").map((s) => s.value)).toEqual(["X-Request-ID", "X-Requested-With"]);
  });

  it("marks Host with what the client does", () => {
    expect(suggestHeaderNames("host")[0]?.note).toMatch(/URL/);
  });
});

describe("suggestHeaderValues", () => {
  it("suggests values for the header, whatever its case", () => {
    expect(suggestHeaderValues("json", "content-type").map((s) => s.value)).toEqual([
      "application/json",
      "application/graphql-response+json",
      "application/ld+json",
      "application/merge-patch+json",
      "application/json-patch+json",
      "application/problem+json",
      "application/vnd.api+json",
      "application/json; charset=utf-8",
    ]);
    expect(suggestHeaderValues("", "Authorization").map((s) => s.value)).toContain("Bearer ");
  });

  it("suggests nothing for unknown headers or headers without values", () => {
    expect(suggestHeaderValues("a", "X-Custom")).toEqual([]);
    expect(suggestHeaderValues("", "Host")).toEqual([]);
    expect(suggestHeaderValues("app", "")).toEqual([]);
  });

  it("completes the last item of a list header and skips items already there", () => {
    const items = suggestHeaderValues("gzip, ", "Accept-Encoding");
    expect(items.map((s) => s.label)).toEqual(["br", "deflate", "zstd", "identity"]);
    expect(items[0]).toEqual({ value: "gzip, br", label: "br", detail: "Brotli" });
    expect(suggestHeaderValues("gzip,de", "accept-encoding").map((s) => s.value)).toEqual(["gzip,deflate", "gzip,identity"]);
    expect(suggestHeaderValues("no-cache, max", "Cache-Control").map((s) => s.value)).toEqual([
      "no-cache, max-age=0",
      "no-cache, max-age=3600",
      "no-cache, max-stale",
    ]);
  });

  it("matches a whole non-list value", () => {
    expect(suggestHeaderValues("text/plain; ch", "Content-Type").map((s) => s.value)).toEqual(["text/plain; charset=utf-8"]);
  });
});
