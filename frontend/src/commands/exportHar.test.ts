import { beforeEach, describe, expect, it, vi } from "vitest";

const http = vi.hoisted(() => ({
  ExportHAR: vi.fn<(input: { name: string; requestIds: string[]; includeSecrets: boolean }) => Promise<unknown>>(),
}));
const history = vi.hoisted(() => ({
  ExportHAR: vi.fn<(input: { ids: string[]; requestId: string; title: string }) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http", () => ({ HTTPService: http }));
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/history", () => ({ HistoryService: history }));

import type { TreeNode } from "../stores";
import { createStores } from "../stores";
import { resetFakeWails, settle } from "../test/fakeWails";
import { exportFolderAsHar, exportHistoryAsHar, harRequestIds, hasHistory } from "./exportHar";
import { toaster } from "./toasts";

const base = { path: "", readOnly: "" };
const nodes: TreeNode[] = [
  { ...base, id: "api", parentId: null, name: "API", fileKind: "", kind: "folder", childIds: ["list", "ws", "users", "gql"] },
  { ...base, id: "list", parentId: "api", name: "List", fileKind: "http", kind: "request", method: "GET" },
  { ...base, id: "ws", parentId: "api", name: "Socket", fileKind: "websocket", kind: "request", method: "" },
  { ...base, id: "users", parentId: "api", name: "Users", fileKind: "", kind: "folder", childIds: ["create", "events"] },
  { ...base, id: "create", parentId: "users", name: "Create", fileKind: "http", kind: "request", method: "POST" },
  { ...base, id: "events", parentId: "users", name: "Events", fileKind: "sse", kind: "request", method: "GET" },
  { ...base, id: "gql", parentId: "api", name: "Query", fileKind: "graphql", kind: "request", method: "POST" },
  { ...base, id: "empty", parentId: null, name: "Empty", fileKind: "", kind: "folder", childIds: [] },
];

function stores() {
  const s = createStores();
  s.collection.dispatch({ type: "collection/load", nodes, rootIds: ["api", "empty"] });
  return s;
}

beforeEach(() => {
  resetFakeWails();
  toaster.clear();
  http.ExportHAR.mockReset();
  history.ExportHAR.mockReset();
});

describe("harRequestIds", () => {
  it("lists a folder's HTTP, GraphQL and SSE requests depth first", () => {
    const s = stores();
    expect(harRequestIds(s.collection.getState().nodes, "api")).toEqual(["list", "create", "events", "gql"]);
    expect(hasHistory(s.collection.getState().nodes["gql"])).toBe(true);
    expect(hasHistory(s.collection.getState().nodes["events"])).toBe(false);
    expect(hasHistory(s.collection.getState().nodes["api"])).toBe(false);
  });
});

describe("exportFolderAsHar", () => {
  it("exports masked and offers to export with secrets", async () => {
    const s = stores();
    http.ExportHAR.mockResolvedValueOnce({ saved: true, path: "/x.har", entries: 3, masked: true, skipped: [{ requestId: "events", code: "x", message: "The body file is missing." }] });
    await exportFolderAsHar(s, "api");
    expect(http.ExportHAR).toHaveBeenCalledWith({ name: "API", requestIds: ["list", "create", "events", "gql"], includeSecrets: false });
    const [toast] = toaster.toasts();
    expect(toast?.message).toBe("Exported 3 requests as HAR, with secrets masked. 1 request was left out: The body file is missing.");
    expect(toast?.action?.label).toBe("Export with secrets");

    http.ExportHAR.mockResolvedValueOnce({ saved: true, path: "/x.har", entries: 4, masked: false, skipped: [] });
    toaster.runAction(toast!.id);
    await settle();
    expect(http.ExportHAR).toHaveBeenLastCalledWith(expect.objectContaining({ includeSecrets: true }));
    const all = toaster.toasts();
    expect(all[all.length - 1]?.message).toBe("Exported 4 requests as HAR.");
  });

  it("says nothing when the dialog is cancelled and reports failures", async () => {
    const s = stores();
    http.ExportHAR.mockResolvedValueOnce({ saved: false, path: "", entries: 1, masked: false, skipped: [] });
    await exportFolderAsHar(s, "api");
    expect(toaster.toasts()).toHaveLength(0);
    http.ExportHAR.mockRejectedValueOnce({ kind: "io", message: "The disk is full." });
    await exportFolderAsHar(s, "api");
    expect(toaster.toasts()[0]?.message).toMatch(/^Could not export as HAR/);
    await exportFolderAsHar(s, "empty");
    expect(toaster.toasts()[1]?.message).toBe("Nothing to export: the folder has no HTTP, GraphQL or SSE request.");
    expect(http.ExportHAR).toHaveBeenCalledTimes(2);
  });
});

describe("exportHistoryAsHar", () => {
  it("exports a request's history or all of it", async () => {
    const s = stores();
    history.ExportHAR.mockResolvedValueOnce({ saved: true, path: "/h.har", entries: 1, masked: true });
    await exportHistoryAsHar(s, "list");
    expect(history.ExportHAR).toHaveBeenCalledWith({ ids: [], requestId: "list", title: "List history" });
    expect(toaster.toasts()[0]?.message).toBe("Exported 1 history entry as HAR, with secrets masked.");

    history.ExportHAR.mockRejectedValueOnce({ kind: "invalid_argument", message: "There is no history entry to export." });
    await exportHistoryAsHar(s, null);
    expect(history.ExportHAR).toHaveBeenLastCalledWith({ ids: [], requestId: "", title: "" });
    expect(toaster.toasts()[1]?.message).toBe("Could not export the history: There is no history entry to export.");
  });
});
