import { describe, expect, it } from "vitest";
import type { ImportCountsDTO, ImportItemDTO, ImportReportDTO } from "./collection";
import { importProblems, importRows, itemPath, locationLabel, previewSummary, reportSummary, worstSeverity } from "./importReport";

function item(over: Partial<ImportItemDTO>): ImportItemDTO {
  return {
    kind: "request",
    name: "",
    parent: 0,
    fileKind: "http",
    skippedKind: "",
    method: "GET",
    detail: "",
    status: "new",
    nodeId: "",
    source: { line: 0, column: 0, path: "" },
    diagnostics: [],
    ...over,
  };
}

// Shop / { Users / { List, Skipped }, Me }, env Staging
const ITEMS: ImportItemDTO[] = [
  item({ kind: "folder", name: "Shop", parent: -1 }),
  item({ kind: "folder", name: "Users", parent: 0 }),
  item({ name: "List", parent: 1 }),
  item({ kind: "skipped", name: "Live", parent: 1, status: "skipped" }),
  item({ name: "Me", parent: 0 }),
  item({ kind: "environment", name: "Staging", parent: -1 }),
];

const counts = (over: Partial<ImportCountsDTO>): ImportCountsDTO => ({
  folders: 0,
  requests: 0,
  environments: 0,
  skipped: 0,
  failed: 0,
  lossy: 0,
  errors: 0,
  warnings: 0,
  infos: 0,
  ...over,
});

describe("importRows", () => {
  it("indents items under their folders", () => {
    expect(importRows(ITEMS).map((r) => `${r.item.name}:${r.depth}`)).toEqual(["Shop:0", "Users:1", "List:2", "Live:2", "Me:1", "Staging:0"]);
  });

  it("leaves out the import's own folder when its items go straight into a folder", () => {
    expect(importRows(ITEMS, false).map((r) => `${r.item.name}:${r.depth}`)).toEqual(["Users:0", "List:1", "Live:1", "Me:0", "Staging:0"]);
  });
});

describe("labels", () => {
  it("names an item by its path", () => {
    expect(itemPath(ITEMS, 2)).toBe("Users / List");
    expect(itemPath(ITEMS, 4)).toBe("Me");
  });

  it("says where something is in the file", () => {
    expect(locationLabel({ line: 12, column: 4, path: "command 2" })).toBe("line 12, column 4 · command 2");
    expect(locationLabel({ line: 3, column: 1, path: "" })).toBe("line 3");
    expect(locationLabel({ line: 0, column: 0, path: "item[1]" })).toBe("item[1]");
    expect(locationLabel({ line: 0, column: 0, path: "" })).toBe("");
  });

  it("sums up a preview and a report", () => {
    expect(previewSummary(counts({ folders: 2, requests: 12, environments: 1 }))).toBe("Creates 2 folders, 12 requests and 1 environment");
    expect(previewSummary(counts({ requests: 1, skipped: 2, lossy: 1 }))).toBe("Creates 1 request · 2 skipped · 1 with warnings");
    expect(reportSummary(counts({ folders: 1, requests: 2, failed: 1 }))).toBe("Imported 1 folder and 2 requests. 1 failed.");
    expect(reportSummary(counts({}))).toBe("Imported nothing.");
  });

  it("finds the worst severity", () => {
    const d = (severity: string) => ({ code: "x", severity, message: "", source: { line: 0, column: 0, path: "" } });
    expect(worstSeverity([])).toBeNull();
    expect(worstSeverity([d("info")])).toBe("info");
    expect(worstSeverity([d("info"), d("warning")])).toBe("warning");
    expect(worstSeverity([d("warning"), d("error")])).toBe("error");
  });
});

describe("importProblems", () => {
  it("lists the file's diagnostics, then skipped, failed and lossy items; notes only on request", () => {
    const warn = { code: "w", severity: "warning", message: "Lost.", source: { line: 0, column: 0, path: "" } };
    const info = { code: "i", severity: "info", message: "Note.", source: { line: 0, column: 0, path: "" } };
    const report = {
      fileName: "shop.json",
      diagnostics: [info],
      items: ITEMS.map((i) =>
        i.name === "Me" ? { ...i, diagnostics: [warn] } : i.name === "List" ? { ...i, diagnostics: [info] } : i.kind === "environment" ? { ...i, status: "failed" } : i,
      ),
    } as unknown as ImportReportDTO;
    expect(importProblems(report).map((p) => p.label)).toEqual(["Users / Live", "Me", "Environment “Staging”"]);
    expect(importProblems(report, true).map((p) => p.label)).toEqual(["shop.json", "Users / List", "Users / Live", "Me", "Environment “Staging”"]);
  });
});
