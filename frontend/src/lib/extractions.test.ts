import { describe, expect, it } from "vitest";
import {
  SOURCES,
  countExtractions,
  describeExtraction,
  describeSource,
  extractionFailed,
  newExtraction,
  scopeInfo,
  sourceInfo,
  summarizeExtractions,
  withScope,
  withSource,
  type ExtractionResultDTO,
} from "./extractions";

describe("extractions (TEST-05)", () => {
  it("offers the sources Go knows, each with or without an expression", () => {
    expect(SOURCES.map((s) => s.id)).toEqual(["jsonpath", "header", "cookie", "regex", "xpath", "status"]);
    expect(sourceInfo("status")?.expr).toBeNull();
    expect(sourceInfo("header")?.expr).toBe("Header name");
    expect(sourceInfo("nope")).toBeUndefined();
  });

  it("reads a missing scope as runtime", () => {
    expect(scopeInfo("")?.id).toBe("runtime");
    expect(scopeInfo("run")?.label).toBe("Run only");
    expect(scopeInfo("suite")).toBeUndefined();
  });

  it("drops what a changed source or scope cannot use", () => {
    const e = newExtraction({ expr: "$.a", variable: "a", persist: true });
    expect(withSource(e, "header").expr).toBe("$.a");
    expect(withSource(e, "status").expr).toBe("");
    expect(withSource(e, "unknown").expr).toBe("$.a");
    expect(withScope(e, "run")).toEqual({ ...e, scope: "run", persist: false });
    expect(withScope(e, "runtime").persist).toBe(true);
  });

  it("describes a rule", () => {
    expect(describeExtraction(newExtraction({ expr: "$.token", variable: "token" }))).toBe("JSONPath $.token into token");
    expect(describeExtraction(newExtraction({ source: "status", expr: "ignored", variable: "code" }))).toBe("Status code into code");
    expect(describeExtraction(newExtraction({ source: "cookie", expr: " sid " }))).toBe("Cookie sid");
    expect(describeSource({ source: "later", expr: "x" })).toBe("later x");
  });

  it("counts enabled rules and sums up results", () => {
    expect(countExtractions([newExtraction(), newExtraction({ enabled: false })])).toBe(1);
    expect(countExtractions(null)).toBe(0);
    const r = (outcome: string) => ({ outcome }) as ExtractionResultDTO;
    const list = [r("set"), r("set"), r("failed"), r("invalid"), r("skipped")];
    expect(summarizeExtractions(list)).toEqual({ total: 5, set: 2, failed: 2, skipped: 1 });
    expect(summarizeExtractions(undefined)).toEqual({ total: 0, set: 0, failed: 0, skipped: 0 });
    expect(list.map(extractionFailed)).toEqual([false, false, true, true, false]);
  });
});
