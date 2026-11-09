import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";

const collection = vi.hoisted(() => ({
  ExportFolderZip: vi.fn<(input: { id: string }) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({ CollectionService: collection }));

import { ConfirmHost } from "../components/common/Dialog";
import type { ExportNoteDTO } from "../lib/postmanExport";
import type { ExportZipResultDTO } from "../lib/zipExport";
import type { TreeNode } from "../stores";
import { createStores } from "../stores";
import { resetFakeWails, settle } from "../test/fakeWails";
import { exportAsZip, showZipExportReport, zipExportSummary } from "./exportZip";
import { toaster } from "./toasts";

const base = { path: "", readOnly: "" };
const nodes: TreeNode[] = [
  { ...base, id: "api", parentId: null, name: "API", fileKind: "", kind: "folder", childIds: ["list"] },
  { ...base, id: "list", parentId: "api", name: "List", fileKind: "http", kind: "request", method: "GET" },
];

function stores() {
  const s = createStores();
  s.collection.dispatch({ type: "collection/load", nodes, rootIds: ["api"] });
  return s;
}

const notes: ExportNoteDTO[] = [
  { path: "", kind: "folder", code: "projectzip.export_secret", severity: "info", message: "A secret typed into the file was left out of the zip.", skipped: false },
  {
    path: "Stream",
    kind: "request of a newer PostSonde",
    code: "projectzip.export_skipped",
    severity: "warning",
    message: "The request is of a kind this version of PostSonde does not know, so it is not exported.",
    skipped: true,
  },
];

function result(over: Partial<ExportZipResultDTO> = {}): ExportZipResultDTO {
  return { saved: true, path: "/api.zip", name: "API", folders: 1, requests: 1, skipped: 1, secretsRemoved: 1, warnings: 1, notes, ...over };
}

beforeEach(() => {
  resetFakeWails();
  toaster.clear();
  collection.ExportFolderZip.mockReset();
});

describe("zipExportSummary", () => {
  it("says secrets are not included and counts what was left out", () => {
    expect(zipExportSummary(result())).toBe("Exported 1 request as a zip, without secrets; 1 secret typed into a file left out, 1 item left out.");
    expect(zipExportSummary(result({ requests: 5, folders: 3, skipped: 0, secretsRemoved: 0, warnings: 0, notes: [] }))).toBe(
      "Exported 5 requests in 3 folders as a zip, without secrets.",
    );
    expect(zipExportSummary(result({ skipped: 0, secretsRemoved: 2, warnings: 1, notes: [] }))).toBe(
      "Exported 1 request as a zip, without secrets; 2 secrets typed into files left out, 1 warning.",
    );
  });
});

describe("exportAsZip", () => {
  it("exports a folder and offers the report", async () => {
    collection.ExportFolderZip.mockResolvedValueOnce(result());
    await exportAsZip(stores(), "api");
    expect(collection.ExportFolderZip).toHaveBeenCalledWith({ id: "api" });
    const [toast] = toaster.toasts();
    expect(toast?.message).toBe("Exported 1 request as a zip, without secrets; 1 secret typed into a file left out, 1 item left out.");
    expect(toast?.action?.label).toBe("Show report");
  });

  it("says nothing when cancelled and reports failures", async () => {
    const s = stores();
    collection.ExportFolderZip.mockResolvedValueOnce(result({ saved: false }));
    await exportAsZip(s, "api");
    expect(toaster.toasts()).toHaveLength(0);

    collection.ExportFolderZip.mockResolvedValueOnce(result({ skipped: 0, secretsRemoved: 0, warnings: 0, notes: [] }));
    await exportAsZip(s, "api");
    expect(toaster.toasts()[0]?.action).toBeUndefined();

    collection.ExportFolderZip.mockRejectedValueOnce({ kind: "invalid_argument", message: "The folder is too large for a zip PostSonde can import." });
    await exportAsZip(s, "api");
    expect(toaster.toasts()[1]?.message).toBe("Could not export as a zip: The folder is too large for a zip PostSonde can import.");
  });
});

describe("showZipExportReport", () => {
  it("lists the notes per item", async () => {
    render(<ConfirmHost />);
    act(() => {
      void showZipExportReport(result());
    });
    const dialog = screen.getByRole("dialog", { name: "Zip export report" });
    const report = within(dialog).getByRole("list", { name: "Export report" });
    expect(within(report).getByText("API")).toBeTruthy();
    expect(within(report).getByText("(not exported)", { exact: false })).toBeTruthy();
    fireEvent.click(dialog.querySelector("[data-dialog-confirm]") as HTMLElement);
    await act(settle);
  });
});
