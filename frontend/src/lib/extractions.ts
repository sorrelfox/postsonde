// Extraction into variables (TEST-05): the sources and scopes the Extract
// sub-tab offers, as the Go side (internal/core/extract,
// docs/extraction.md) knows them, and the summary of what a send set
// (ResponseDTO.extractions).
import type { ExtractionDTO } from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection";
import type { ExtractionResultDTO } from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http";

import { t } from "../i18n";

export type { ExtractionDTO, ExtractionResultDTO };

// Labels are looked up when read (getters), so a table entry shows in the
// language in use wherever it is used (UI-10).

/** Where a rule takes its value from. */
export interface SourceInfo {
  readonly id: string;
  /** In the language in use. */
  readonly label: string;
  /** The source takes an expression (JSONPath, header name…): its placeholder; null for none. */
  readonly expr: string | null;
}

export const SOURCES: readonly SourceInfo[] = [
  // i18n-ignore-start: names of query languages and example expressions
  { id: "jsonpath", label: "JSONPath", expr: "$.access_token" },
  // i18n-ignore-end
  {
    id: "header",
    get label() {
      return t("lib.extractions.source.header");
    },
    get expr() {
      return t("lib.extractions.source.headerExpr");
    },
  },
  {
    id: "cookie",
    get label() {
      return t("lib.extractions.source.cookie");
    },
    get expr() {
      return t("lib.extractions.source.cookieExpr");
    },
  },
  {
    id: "regex",
    get label() {
      return t("lib.extractions.source.regex");
    },
    expr: "token=(\\w+)",
  },
  // i18n-ignore-start: the name of a query language and an example expression
  { id: "xpath", label: "XPath", expr: "//token" },
  // i18n-ignore-end
  {
    id: "status",
    get label() {
      return t("lib.extractions.source.status");
    },
    expr: null,
  },
];

/** Where a rule's variable is set. */
export interface ScopeInfo {
  readonly id: string;
  readonly label: string;
  /** What the scope means, for a title. */
  readonly title: string;
}

/** The scope a rule has by default: the project's runtime variables. */
export const RUNTIME_SCOPE = "runtime";
/** The scope of the variables of a folder run only. */
export const RUN_SCOPE = "run";

export const SCOPES: readonly ScopeInfo[] = [
  {
    id: RUNTIME_SCOPE,
    get label() {
      return t("lib.extractions.scope.runtime");
    },
    get title() {
      return t("lib.extractions.scope.runtime.title");
    },
  },
  {
    id: RUN_SCOPE,
    get label() {
      return t("lib.extractions.scope.run");
    },
    get title() {
      return t("lib.extractions.scope.run.title");
    },
  },
];

export function sourceInfo(id: string): SourceInfo | undefined {
  return SOURCES.find((s) => s.id === id);
}

export function scopeInfo(id: string): ScopeInfo | undefined {
  return SCOPES.find((s) => s.id === (id === "" ? RUNTIME_SCOPE : id));
}

/** A new rule: a JSONPath into a runtime variable, enabled. */
export function newExtraction(init: Partial<ExtractionDTO> = {}): ExtractionDTO {
  return {
    source: "jsonpath",
    expr: "",
    variable: "",
    scope: RUNTIME_SCOPE,
    secret: false,
    persist: false,
    enabled: true,
    extra: "",
    ...init,
  };
}

/** The rule with its source changed: a source without an expression drops the one typed. */
export function withSource(e: ExtractionDTO, source: string): ExtractionDTO {
  const info = sourceInfo(source);
  return { ...e, source, expr: info !== undefined && info.expr === null ? "" : e.expr };
}

/** The rule with its scope changed: a run variable is never kept, so "persist" goes. */
export function withScope(e: ExtractionDTO, scope: string): ExtractionDTO {
  return { ...e, scope, persist: scope === RUN_SCOPE ? false : e.persist };
}

/** Where a rule reads its value, short: "JSONPath $.token", "Header X-Id", "Status code". */
export function describeSource(e: Pick<ExtractionDTO, "source" | "expr">): string {
  const info = sourceInfo(e.source);
  const label = info?.label ?? e.source;
  const expr = info?.expr === null ? "" : e.expr.trim();
  return expr === "" ? label : t("lib.extractions.describeSource", { source: label, expr });
}

/** A short description of a rule: "JSONPath $.token into token". */
export function describeExtraction(e: Pick<ExtractionDTO, "source" | "expr" | "variable">): string {
  const variable = e.variable.trim();
  const source = describeSource(e);
  return variable === "" ? source : t("lib.extractions.describe", { source, variable });
}

/** The enabled rules, for the Extract sub-tab's count. */
export function countExtractions(list: readonly ExtractionDTO[] | null | undefined): number {
  return (list ?? []).filter((e) => e.enabled).length;
}

/** Whether a rule's result is a failure: the value was not found, or the rule is wrong. */
export function extractionFailed(r: Pick<ExtractionResultDTO, "outcome">): boolean {
  return r.outcome === "failed" || r.outcome === "invalid";
}

/** Counts of a send's extraction results. */
export interface ExtractionSummary {
  readonly total: number;
  readonly set: number;
  /** Failed and invalid. */
  readonly failed: number;
  readonly skipped: number;
}

export function summarizeExtractions(list: readonly ExtractionResultDTO[] | null | undefined): ExtractionSummary {
  let set = 0;
  let failed = 0;
  let skipped = 0;
  for (const r of list ?? []) {
    if (r.outcome === "set") set++;
    else if (r.outcome === "skipped") skipped++;
    else failed++;
  }
  return { total: set + failed + skipped, set, failed, skipped };
}
