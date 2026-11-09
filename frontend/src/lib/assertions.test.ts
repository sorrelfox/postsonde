import { describe, expect, it } from "vitest";
import {
  countAssertions,
  describeAssertion,
  expectedPlaceholder,
  formatInlineSchema,
  inlineSchemaProblem,
  isInlineSchema,
  newAssertion,
  OPERATORS,
  summarizeTests,
  TARGETS,
  withOperator,
  withTarget,
  type TestResultDTO,
} from "./assertions";

describe("assertions", () => {
  it("lists the evaluator's targets and operators (TEST-02, TEST-03)", () => {
    expect(TARGETS.map((t) => t.id)).toEqual(["status", "header", "cookie", "jsonpath", "xpath", "regex", "body", "time", "size"]);
    expect(OPERATORS.map((o) => o.id)).toEqual([
      "equals", "not_equals", "contains", "not_contains", "exists", "not_exists", "is_empty",
      "gt", "lt", "gte", "lte", "matches", "is_type", "matches_schema",
    ]);
  });

  it("drops the expression and the expected value a new target or operator does not take", () => {
    const a = newAssertion({ target: "header", expr: "X-Id", expected: "7" });
    expect(withTarget(a, "jsonpath").expr).toBe("X-Id");
    expect(withTarget(a, "status").expr).toBe("");
    expect(withOperator(a, "exists").expected).toBe("");
    expect(withOperator(a, "contains").expected).toBe("7");
    // Unknown ones are kept as they are.
    expect(withTarget(a, "grpc_status").expr).toBe("X-Id");
  });

  it("describes an assertion in words", () => {
    expect(describeAssertion(newAssertion())).toBe("Status code equals 200");
    expect(describeAssertion({ target: "header", expr: "X-Id", op: "exists", expected: "" })).toBe("Header X-Id exists");
    expect(describeAssertion({ target: "time", expr: "", op: "lt", expected: "500ms" })).toBe("Response time < 500ms");
    expect(describeAssertion({ target: "mystery", expr: "a", op: "odd", expected: "b" })).toBe("mystery a odd b");
    expect(describeAssertion({ target: "body", expr: "", op: "matches_schema", expected: "schemas/user.json" })).toBe("Body matches schema schemas/user.json");
    expect(describeAssertion({ target: "jsonpath", expr: "$.user", op: "matches_schema", expected: '{"type":"object"}' })).toBe(
      "JSONPath $.user matches schema (inline)",
    );
  });

  it("tells inline schemas from schema files (TEST-07)", () => {
    expect(isInlineSchema('  {"type":"object"}')).toBe(true);
    expect(isInlineSchema("true")).toBe(true);
    expect(isInlineSchema(" false ")).toBe(true);
    expect(isInlineSchema("schemas/user.json")).toBe(false);
    expect(isInlineSchema("")).toBe(false);
    expect(inlineSchemaProblem('{"type":"object"}')).toBeNull();
    expect(inlineSchemaProblem('{"type":')).not.toBeNull();
    expect(formatInlineSchema('{"type":"object","required":["id"]}')).toBe('{\n  "type": "object",\n  "required": [\n    "id"\n  ]\n}');
    expect(formatInlineSchema('{"type":')).toBe('{"type":');
    expect(expectedPlaceholder({ target: "body", op: "matches_schema" })).toBe("schemas/user.json or { … }");
  });

  it("counts enabled assertions and results by outcome", () => {
    expect(countAssertions([newAssertion(), newAssertion({ enabled: false })])).toBe(1);
    expect(countAssertions(undefined)).toBe(0);
    const r = (outcome: string) => ({ outcome }) as TestResultDTO;
    expect(summarizeTests([r("passed"), r("failed"), r("invalid"), r("passed")])).toEqual({ total: 4, passed: 2, failed: 1, invalid: 1 });
    expect(summarizeTests(null)).toEqual({ total: 0, passed: 0, failed: 0, invalid: 0 });
  });
});
