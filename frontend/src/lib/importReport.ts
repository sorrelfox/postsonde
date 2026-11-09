// Pure helpers of the Import dialog (IMP-10, docs/import.md, docs/frontend.md
// "Import"): the preview's and the report's items as indented rows, the
// one-line summaries, where a diagnostic is in the file, and the items the
// report lists as problems.

import { t } from "../i18n";
import type { ImportCountsDTO, ImportDiagnosticDTO, ImportItemDTO, ImportLocationDTO, ImportReportDTO } from "./collection";

/** An item of a preview or report as a row of the tree. */
export interface ImportRow {
  /** The item's index in `items`. */
  readonly index: number;
  readonly item: ImportItemDTO;
  /** 0 for the import's own folder and the environments, 1 for its items, … */
  readonly depth: number;
}

/** The items as rows, in order, with their depth; `withRoot: false` leaves out the import's own folder and lifts its items one level. */
export function importRows(items: readonly ImportItemDTO[], withRoot = true): ImportRow[] {
  const depths: number[] = [];
  const rows: ImportRow[] = [];
  items.forEach((item, index) => {
    const depth = item.parent < 0 || item.parent >= index ? 0 : (depths[item.parent] ?? 0) + 1;
    depths[index] = depth;
    if (!withRoot && index === 0 && item.kind === "folder") return;
    const shown = !withRoot && item.kind !== "environment" ? Math.max(0, depth - 1) : depth;
    rows.push({ index, item, depth: shown });
  });
  return rows;
}

/** The environment items. */
export function environmentItems(items: readonly ImportItemDTO[]): number {
  return items.filter((i) => i.kind === "environment").length;
}

/** "Users / List users": the names of the folders above an item (the import's own folder left out) and its own. */
export function itemPath(items: readonly ImportItemDTO[], index: number): string {
  const names: string[] = [];
  for (let i = index, guard = 0; i > 0 && i < items.length && guard < 1000; guard++) {
    names.unshift(items[i].name);
    const parent = items[i].parent;
    if (parent < 0 || parent >= i) break;
    i = parent;
  }
  return names.join(" / ");
}

/** "line 12, column 4 · item[0]": where something is in the file; "" when unknown. */
export function locationLabel(source: ImportLocationDTO): string {
  const line =
    source.line <= 0
      ? ""
      : source.column > 1
        ? t("lib.importReport.lineColumn", { line: source.line, column: source.column })
        : t("lib.importReport.line", { line: source.line });
  if (line === "") return source.path;
  return source.path === "" ? line : t("lib.importReport.location", { line, path: source.path });
}

/** The worst severity of `diagnostics`, or null for none. */
export function worstSeverity(diagnostics: readonly ImportDiagnosticDTO[]): "error" | "warning" | "info" | null {
  let worst: "error" | "warning" | "info" | null = null;
  for (const d of diagnostics) {
    if (d.severity === "error") return "error";
    if (d.severity === "warning") worst = "warning";
    else if (worst === null) worst = "info";
  }
  return worst;
}

/** "a, b and c", in the language in use. */
function joinList(parts: readonly string[]): string {
  if (parts.length <= 1) return parts.join("");
  const head = parts.slice(0, -1).reduce((list, item) => t("lib.importReport.list.item", { list, item }));
  return t("lib.importReport.list.last", { list: head, last: parts[parts.length - 1] });
}

/** Joins the problems part of a summary: "1 skipped · 3 with warnings". */
function joinProblems(parts: readonly string[]): string {
  return parts.length === 0 ? "" : parts.reduce((list, item) => t("lib.importReport.problems.item", { list, item }));
}

/** What would be, or was, created: "2 folders, 12 requests and 1 environment". */
export function createdLabel(c: ImportCountsDTO): string {
  const parts: string[] = [];
  if (c.folders > 0) parts.push(t("lib.importReport.folders", { count: c.folders }));
  if (c.requests > 0) parts.push(t("lib.importReport.requests", { count: c.requests }));
  if (c.environments > 0) parts.push(t("lib.importReport.environments", { count: c.environments }));
  return parts.length === 0 ? t("lib.importReport.nothing") : joinList(parts);
}

/** The problems part of a summary: "1 skipped · 3 with warnings · 1 failed"; "" when there are none. */
export function problemsLabel(c: ImportCountsDTO): string {
  const parts: string[] = [];
  if (c.skipped > 0) parts.push(t("lib.importReport.skipped", { count: c.skipped }));
  if (c.lossy > 0) parts.push(t("lib.importReport.lossy", { count: c.lossy }));
  if (c.failed > 0) parts.push(t("lib.importReport.failed", { count: c.failed }));
  return joinProblems(parts);
}

/** The preview's summary: "Creates 2 folders and 12 requests · 1 skipped". */
export function previewSummary(c: ImportCountsDTO): string {
  const problems = problemsLabel(c);
  const created = createdLabel(c);
  return problems === "" ? t("lib.importReport.preview", { created }) : t("lib.importReport.previewProblems", { created, problems });
}

/** The report's summary: "Imported 2 folders and 12 requests. 1 skipped · 1 failed." */
export function reportSummary(c: ImportCountsDTO): string {
  const problems = problemsLabel(c);
  const created = createdLabel(c);
  return problems === "" ? t("lib.importReport.report", { created }) : t("lib.importReport.reportProblems", { created, problems });
}

/** The report's problems: the items skipped, failed or with a warning or error, and the file's own diagnostics. */
export interface ImportProblem {
  /** The item's index, or -1 for the file. */
  readonly index: number;
  /** "Users / List users", "Staging", or the file's name. */
  readonly label: string;
  readonly status: string;
  readonly diagnostics: readonly ImportDiagnosticDTO[];
}

/** The items worth listing in a report, the file's diagnostics first. Info-only items are left out unless `withInfo`. */
export function importProblems(report: ImportReportDTO, withInfo = false): ImportProblem[] {
  const out: ImportProblem[] = [];
  const fileDiagnostics = (report.diagnostics ?? []).filter((d) => withInfo || d.severity !== "info");
  if (fileDiagnostics.length > 0) out.push({ index: -1, label: report.fileName, status: "", diagnostics: fileDiagnostics });
  const items = report.items ?? [];
  items.forEach((item, index) => {
    const diagnostics = (item.diagnostics ?? []).filter((d) => withInfo || d.severity !== "info");
    if (diagnostics.length === 0 && item.status !== "skipped" && item.status !== "failed") return;
    const label =
      index === 0
        ? item.name || t("lib.importReport.topLevel")
        : item.kind === "environment"
          ? t("lib.importReport.environment", { name: item.name })
          : itemPath(items, index);
    out.push({ index, label, status: item.status, diagnostics });
  });
  return out;
}
