import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";

const collection = vi.hoisted(() => ({
  ExportPostman: vi.fn<(input: { id: string }) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({ CollectionService: collection }));

import { ConfirmHost } from "../components/common/Dialog";
import { groupNotes, type ExportNoteDTO, type ExportPostmanResultDTO } from "../lib/postmanExport";
import type { TreeNode } from "../stores";
import { createStores } from "../stores";
import { resetFakeWails, settle } from "../test/fakeWails";
import { exportAsPostman, exportSummary, showExportReport } from "./exportPostman";
import { toaster } from "./toasts";

const base = { path: "", readOnly: "" };
const nodes: TreeNode[] = [
  { ...base, id: "api", parentId: null, name: "API", fileKind: "", kind: "folder", childIds: ["list", "ws"] },
  { ...base, id: "list", parentId: "api", name: "List", fileKind: "http", kind: "request", method: "GET" },
  { ...base, id: "ws", parentId: "api", name: "Socket", fileKind: "websocket", kind: "request", method: "" },
];

function stores() {
  const s = createStores();
  s.collection.dispatch({ type: "collection/load", nodes, rootIds: ["api"] });
  return s;
}

const skippedNote: ExportNoteDTO = {
  path: "Socket",
  kind: "WebSocket request",
  code: "postman.export_skipped",
  severity: "warning",
  message: "Postman collections (v2.1) cannot hold a WebSocket request; it is not exported.",
  skipped: true,
};
const notes: ExportNoteDTO[] = [
  { path: "", kind: "collection", code: "postman.export_secret", severity: "info", message: "The secret variable is exported without a value.", skipped: false },
  { path: "List", kind: "request", code: "postman.export_assertions", severity: "warning", message: "The request's assertion is not exported.", skipped: false },
  { path: "List", kind: "request", code: "postman.export_setting", severity: "warning", message: "Postman has no setting for the timeout.", skipped: false },
  skippedNote,
];

function result(over: Partial<ExportPostmanResultDTO> = {}): ExportPostmanResultDTO {
  return { saved: true, path: "/api.json", name: "API", folders: 0, requests: 1, skipped: 1, warnings: 3, notes, ...over };
}

beforeEach(() => {
  resetFakeWails();
  toaster.clear();
  collection.ExportPostman.mockReset();
});

describe("exportSummary", () => {
  it("counts what was written, left out and changed", () => {
    expect(exportSummary(result())).toBe("Exported 1 request as a Postman collection; 1 item left out, 2 warnings.");
    expect(exportSummary(result({ requests: 5, folders: 2, skipped: 0, warnings: 0, notes: [] }))).toBe(
      "Exported 5 requests in 2 folders as a Postman collection.",
    );
  });
});

describe("groupNotes", () => {
  it("groups the notes by item, in order", () => {
    expect(groupNotes(notes).map((g) => [g.path, g.notes.length])).toEqual([
      ["", 1],
      ["List", 2],
      ["Socket", 1],
    ]);
  });
});

describe("exportAsPostman", () => {
  it("exports a folder and offers the report", async () => {
    const s = stores();
    collection.ExportPostman.mockResolvedValueOnce(result());
    await exportAsPostman(s, "api");
    expect(collection.ExportPostman).toHaveBeenCalledWith({ id: "api" });
    const [toast] = toaster.toasts();
    expect(toast?.message).toBe("Exported 1 request as a Postman collection; 1 item left out, 2 warnings.");
    expect(toast?.action?.label).toBe("Show report");
  });

  it("exports the project, says nothing when cancelled and reports failures", async () => {
    const s = stores();
    collection.ExportPostman.mockResolvedValueOnce(result({ saved: false }));
    await exportAsPostman(s, null);
    expect(collection.ExportPostman).toHaveBeenCalledWith({ id: "" });
    expect(toaster.toasts()).toHaveLength(0);

    collection.ExportPostman.mockResolvedValueOnce(result({ skipped: 0, warnings: 0, notes: [] }));
    await exportAsPostman(s, null);
    expect(toaster.toasts()[0]?.action).toBeUndefined();

    collection.ExportPostman.mockRejectedValueOnce({ kind: "io", message: "The disk is full." });
    await exportAsPostman(s, "api");
    expect(toaster.toasts()[1]?.message).toBe("Could not export as a Postman collection: The disk is full.");
  });
});

describe("showExportReport", () => {
  it("lists the notes per item in a dialog with only Close", async () => {
    render(<ConfirmHost />);
    let closed: boolean | undefined;
    act(() => {
      void showExportReport(result()).then((v) => (closed = v));
    });
    const dialog = screen.getByRole("dialog", { name: "Postman export report" });
    const report = within(dialog).getByRole("list", { name: "Export report" });
    expect(within(report).getByText("API")).toBeTruthy();
    expect(within(report).getByText("(not exported)", { exact: false })).toBeTruthy();
    expect(within(report).getByText("Postman has no setting for the timeout.")).toBeTruthy();
    expect(within(dialog).queryByRole("button", { name: "Cancel" })).toBeNull();
    fireEvent.click(dialog.querySelector("[data-dialog-confirm]") as HTMLElement);
    await act(settle);
    expect(closed).toBe(true);
  });
});
