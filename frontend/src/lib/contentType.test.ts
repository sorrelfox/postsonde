import { describe, expect, it } from "vitest";
import fixture from "../../../internal/core/request/testdata/content_type.json?raw";
import {
  baseName,
  bodyContentType,
  EXTENSION_MEDIA_TYPES,
  fileMediaType,
  hasContentTypeHeader,
  implicitContentType,
  mediaTypeByExtension,
  rawMediaType,
} from "./contentType";

interface Case {
  name: string;
  body: { mode: string; type?: string; path?: string; contentType?: string };
  headers: { name: string; value: string; enabled: boolean }[];
  want: string;
}

const parsed = JSON.parse(fixture) as { cases: Case[]; extensions: Record<string, string> };
const cases = parsed.cases;

describe("implicit Content-Type (shared with Go: content_type.json)", () => {
  it("has cases", () => {
    expect(cases.length).toBeGreaterThan(10);
  });

  it.each(cases.map((c) => [c.name, c] as const))("%s", (_name, c) => {
    const body = { mode: c.body.mode, type: c.body.type ?? "", path: c.body.path ?? "", contentType: c.body.contentType ?? "" };
    const { value, overridden } = implicitContentType(body, c.headers);
    // Go returns "" when overridden; the editor keeps the value to show it struck through.
    expect(overridden ? "" : value).toBe(c.want);
    if (c.headers.length === 0) expect(bodyContentType(body)).toBe(c.want);
  });

  it("has Go's extension table", () => {
    expect(EXTENSION_MEDIA_TYPES).toEqual(parsed.extensions);
    for (const [ext, type] of Object.entries(parsed.extensions)) {
      expect(mediaTypeByExtension(`dir\\NAME${ext.toUpperCase()}`)).toBe(type);
    }
  });
});

describe("contentType helpers", () => {
  it("knows each raw type", () => {
    expect(rawMediaType("json")).toBe("application/json");
    expect(rawMediaType("csv")).toBe("");
  });

  it("treats a missing header list as no override", () => {
    expect(hasContentTypeHeader(null)).toBe(false);
    expect(implicitContentType({ mode: "urlencoded", type: "" }, null)).toEqual({
      value: "application/x-www-form-urlencoded",
      overridden: false,
    });
  });

  it("guesses file types by extension only", () => {
    expect(baseName("a/b\\c.txt")).toBe("c.txt");
    expect(mediaTypeByExtension("toString")).toBe("");
    expect(mediaTypeByExtension("x.constructor")).toBe("");
    expect(fileMediaType("x.unknown", "")).toBe("application/octet-stream");
    expect(fileMediaType("x.png", "image/apng")).toBe("image/apng");
    expect(bodyContentType({ mode: "file", type: "" })).toBe("application/octet-stream");
  });
});
