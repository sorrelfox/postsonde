// Assertions of HTTP and GraphQL requests (TEST-01…03, TEST-06, TEST-07)
// and of streams (TEST-08: gRPC, WebSocket, Socket.IO, SSE): the targets,
// message scopes and operators the Tests sub-tab offers, as the Go
// evaluator (internal/core/assert, docs/assertions.md) knows them, and the
// summary of the results a send or a stream returns.
import type { AssertionDTO } from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection";
import type { SchemaErrorDTO, TestResultDTO } from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http";
import type { TestResultDTO as StreamTestResultDTO } from "../../bindings/github.com/sorrelfox/postsonde/internal/app/streamcheck";

import { t } from "../i18n";

export type { AssertionDTO, SchemaErrorDTO, StreamTestResultDTO, TestResultDTO };

// Labels and placeholders are looked up when read (getters), so a table
// entry shows in the language in use wherever it is used (UI-10).

/** What part of the response an assertion checks (TEST-02). */
export interface TargetInfo {
  readonly id: string;
  /** In the language in use. */
  readonly label: string;
  /** The target takes an expression (header or cookie name, JSONPath, XPath, regex); its placeholder. */
  readonly expr: string | null;
}

export const TARGETS: readonly TargetInfo[] = [
  {
    id: "status",
    get label() {
      return t("lib.assertions.target.status");
    },
    expr: null,
  },
  {
    id: "header",
    get label() {
      return t("lib.assertions.target.header");
    },
    get expr() {
      return t("lib.assertions.target.headerExpr");
    },
  },
  {
    id: "cookie",
    get label() {
      return t("lib.assertions.target.cookie");
    },
    get expr() {
      return t("lib.assertions.target.cookieExpr");
    },
  },
  // i18n-ignore-start: names of query languages
  { id: "jsonpath", label: "JSONPath", expr: "$.data.id" },
  { id: "xpath", label: "XPath", expr: "//item/@id" },
  // i18n-ignore-end
  {
    id: "regex",
    get label() {
      return t("lib.assertions.target.regex");
    },
    expr: "id=(\\d+)",
  },
  {
    id: "body",
    get label() {
      return t("lib.assertions.target.body");
    },
    expr: null,
  },
  {
    id: "time",
    get label() {
      return t("lib.assertions.target.time");
    },
    expr: null,
  },
  {
    id: "size",
    get label() {
      return t("lib.assertions.target.size");
    },
    expr: null,
  },
];

/** An operator (TEST-03); `expected` false when it takes no expected value. */
export interface OperatorInfo {
  readonly id: string;
  /** In the language in use; symbols (">", "≤") are not text. */
  readonly label: string;
  readonly expected: boolean;
}

export const OPERATORS: readonly OperatorInfo[] = [
  {
    id: "equals",
    get label() {
      return t("lib.assertions.op.equals");
    },
    expected: true,
  },
  {
    id: "not_equals",
    get label() {
      return t("lib.assertions.op.notEquals");
    },
    expected: true,
  },
  {
    id: "contains",
    get label() {
      return t("lib.assertions.op.contains");
    },
    expected: true,
  },
  {
    id: "not_contains",
    get label() {
      return t("lib.assertions.op.notContains");
    },
    expected: true,
  },
  {
    id: "exists",
    get label() {
      return t("lib.assertions.op.exists");
    },
    expected: false,
  },
  {
    id: "not_exists",
    get label() {
      return t("lib.assertions.op.notExists");
    },
    expected: false,
  },
  {
    id: "is_empty",
    get label() {
      return t("lib.assertions.op.isEmpty");
    },
    expected: false,
  },
  { id: "gt", label: ">", expected: true },
  { id: "lt", label: "<", expected: true },
  { id: "gte", label: "≥", expected: true },
  { id: "lte", label: "≤", expected: true },
  {
    id: "matches",
    get label() {
      return t("lib.assertions.op.matches");
    },
    expected: true,
  },
  {
    id: "is_type",
    get label() {
      return t("lib.assertions.op.isType");
    },
    expected: true,
  },
  {
    id: "matches_schema",
    get label() {
      return t("lib.assertions.op.matchesSchema");
    },
    expected: true,
  },
];

/** The operator that validates against a JSON Schema (TEST-07). */
export const MATCHES_SCHEMA = "matches_schema";

/**
 * Whether the expected value of a matches_schema assertion is the schema
 * itself (JSON starting with "{", or true or false) rather than the path of
 * a schema file, as internal/core/assert.IsInlineSchema decides.
 */
export function isInlineSchema(expected: string): boolean {
  const text = expected.trim();
  return text.startsWith("{") || text === "true" || text === "false";
}

/** Why an inline schema is not JSON, or null when it parses. */
export function inlineSchemaProblem(expected: string): string | null {
  try {
    JSON.parse(expected);
    return null;
  } catch (e) {
    return e instanceof Error ? e.message : String(e);
  }
}

/** An inline schema indented for the multi-line editor, or the text unchanged when it is not JSON. */
export function formatInlineSchema(expected: string): string {
  try {
    return JSON.stringify(JSON.parse(expected), null, 2);
  } catch {
    return expected;
  }
}

/** The type names `is_type` takes. */
export const TYPE_NAMES: readonly string[] = ["string", "number", "boolean", "null", "array", "object"];

/** The number of messages a stream received (TEST-08): a target of stream requests only. */
export const MESSAGE_COUNT: TargetInfo = {
  id: "message_count",
  get label() {
    return t("lib.assertions.target.messageCount");
  },
  expr: null,
};

export function targetInfo(id: string): TargetInfo | undefined {
  return TARGETS.find((target) => target.id === id) ?? (id === MESSAGE_COUNT.id ? MESSAGE_COUNT : undefined);
}

/** Which messages of a stream an assertion reads its target from (TEST-08); "" is the stream as a whole. */
export interface ScopeInfo {
  readonly id: string;
  readonly label: string;
}

export const MESSAGE_SCOPES: readonly ScopeInfo[] = [
  {
    id: "",
    get label() {
      return t("lib.assertions.scope.stream");
    },
  },
  {
    id: "any",
    get label() {
      return t("lib.assertions.scope.any");
    },
  },
  {
    id: "every",
    get label() {
      return t("lib.assertions.scope.every");
    },
  },
  {
    id: "none",
    get label() {
      return t("lib.assertions.scope.none");
    },
  },
  {
    id: "first",
    get label() {
      return t("lib.assertions.scope.first");
    },
  },
  {
    id: "last",
    get label() {
      return t("lib.assertions.scope.last");
    },
  },
  {
    id: "nth",
    get label() {
      return t("lib.assertions.scope.nth");
    },
  },
];

/** The message scope of an assertion: "" for the whole response or stream. */
export function scopeOf(a: Pick<AssertionDTO, "messages">): string {
  return a.messages ?? "";
}

/**
 * The targets a row of a stream request offers: for the stream as a whole
 * the status, headers, time and the message count (no cookies); for its
 * messages the body targets, the size, the time and the message's own
 * header fields.
 */
export function streamTargets(scope: string): readonly TargetInfo[] {
  if (scope === "") return [...TARGETS.filter((x) => x.id !== "cookie"), MESSAGE_COUNT];
  return TARGETS.filter((x) => x.id !== "status" && x.id !== "cookie");
}

/**
 * The assertion with its message scope changed: "nth" starts at message 1,
 * the others drop the number; a target the scope does not offer becomes
 * the body (into a scope) or the message count (out of one).
 */
export function withScope(a: AssertionDTO, scope: string): AssertionDTO {
  const next: AssertionDTO = { ...a, messages: scope, index: scope === "nth" ? Math.max(a.index ?? 0, 1) : 0 };
  if (!streamTargets(scope).some((x) => x.id === a.target)) return withTarget(next, scope === "" ? MESSAGE_COUNT.id : "body");
  return next;
}

/** A scope in words: "Last message", "Message 3"; "" for the whole stream. */
export function scopeLabel(a: Pick<AssertionDTO, "messages" | "index">): string {
  const scope = scopeOf(a);
  if (scope === "") return "";
  if (scope === "nth") return t("lib.assertions.scope.message", { n: a.index ?? 0 });
  return MESSAGE_SCOPES.find((s) => s.id === scope)?.label ?? scope;
}

export function operatorInfo(id: string): OperatorInfo | undefined {
  return OPERATORS.find((o) => o.id === id);
}

/** The expected value's placeholder for an assertion. */
export function expectedPlaceholder(a: Pick<AssertionDTO, "target" | "op">): string {
  if (a.op === "is_type") return t("lib.assertions.placeholder.type");
  if (a.op === MATCHES_SCHEMA) return t("lib.assertions.placeholder.schema");
  if (a.op === "matches") return "^regex$";
  switch (a.target) {
    case "status":
      return "200";
    case "time":
      return "500ms";
    case "size":
      return t("lib.assertions.placeholder.bytes");
    default:
      return t("lib.assertions.placeholder.value");
  }
}

/** A new assertion: status code equals 200, enabled. */
export function newAssertion(init: Partial<AssertionDTO> = {}): AssertionDTO {
  return { target: "status", expr: "", op: "equals", expected: "200", enabled: true, extra: "", ...init };
}

/**
 * The assertion with its target changed: a target without an expression
 * drops the one typed, so the file does not keep an unused `expr`.
 */
export function withTarget(a: AssertionDTO, target: string): AssertionDTO {
  const info = targetInfo(target);
  return { ...a, target, expr: info !== undefined && info.expr === null ? "" : a.expr };
}

/** The assertion with its operator changed: one that takes no expected value drops it. */
export function withOperator(a: AssertionDTO, op: string): AssertionDTO {
  const info = operatorInfo(op);
  return { ...a, op, expected: info !== undefined && !info.expected ? "" : a.expected };
}

/** A short description of an assertion: "Status code equals 200", "Header X-Id exists". */
export function describeAssertion(a: Pick<AssertionDTO, "target" | "expr" | "op" | "expected" | "messages" | "index">): string {
  const scope = scopeLabel(a);
  const plain = describePlain(a);
  return scope === "" ? plain : t("lib.assertions.describe.scoped", { scope, assertion: plain });
}

function describePlain(a: Pick<AssertionDTO, "target" | "expr" | "op" | "expected">): string {
  const info = targetInfo(a.target);
  const op = operatorInfo(a.op);
  const target = info?.label ?? a.target;
  const operator = op?.label ?? a.op;
  const expr = a.expr !== "" && (info === undefined || info.expr !== null) ? a.expr : "";
  let expected = "";
  if (a.op === MATCHES_SCHEMA) expected = schemaLabel(a.expected);
  else if (op === undefined || op.expected) expected = a.expected;
  if (expr === "") {
    return expected === "" ? t("lib.assertions.describe.noValue", { target, operator }) : t("lib.assertions.describe", { target, operator, expected });
  }
  return expected === ""
    ? t("lib.assertions.describe.exprNoValue", { target, expr, operator })
    : t("lib.assertions.describe.expr", { target, expr, operator, expected });
}

/** A matches_schema assertion's schema, short: "(inline)" or the file path. */
export function schemaLabel(expected: string): string {
  if (expected.trim() === "") return "";
  return isInlineSchema(expected) ? t("lib.assertions.inlineSchema") : expected.trim();
}

/** The enabled assertions, for the Tests sub-tab's count. */
export function countAssertions(list: readonly AssertionDTO[] | null | undefined): number {
  return (list ?? []).filter((a) => a.enabled).length;
}

/** Counts of a send's results. */
export interface TestSummary {
  readonly total: number;
  readonly passed: number;
  readonly failed: number;
  readonly invalid: number;
}

/** A send's or a stream's result described as its assertion: "Last message: JSONPath $.done equals true". */
export function describeResult(r: TestResultDTO | StreamTestResultDTO): string {
  return "messageIndex" in r ? describeAssertion({ ...r, index: r.messageIndex }) : describeAssertion(r);
}

export function summarizeTests(tests: readonly Pick<TestResultDTO, "outcome">[] | null | undefined): TestSummary {
  let passed = 0;
  let failed = 0;
  let invalid = 0;
  for (const r of tests ?? []) {
    if (r.outcome === "passed") passed++;
    else if (r.outcome === "invalid") invalid++;
    else failed++;
  }
  return { total: passed + failed + invalid, passed, failed, invalid };
}
