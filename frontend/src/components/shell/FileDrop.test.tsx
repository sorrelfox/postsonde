import { useRef } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";

const service = vi.hoisted(() => ({
  Tree: vi.fn<() => Promise<unknown>>(),
  ImportFormats: vi.fn<() => Promise<unknown>>(),
  PickImportFile: vi.fn<() => Promise<string>>(),
  PreviewImport: vi.fn<(input: { path: string; format: string }) => Promise<unknown>>(),
  DetectImport: vi.fn<(input: { paths: string[] }) => Promise<unknown>>(),
  Import: vi.fn<(input: unknown) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({
  CollectionService: service,
}));

import { IMPORT_DROP_TARGET } from "../../commands/fileDrop";
import { toaster } from "../../commands/toasts";
import type { DetectedImportDTO, ImportReportDTO } from "../../lib/collection";
import { COLLECTION_FILES_DROPPED_EVENT } from "../../lib/collection";
import { createStores, StoreProvider, type Stores } from "../../stores";
import { emit, resetFakeWails, settle } from "../../test/fakeWails";
import { DROP_ACTIVE_CLASS, FileDropOverlay, useFileDrop } from "./FileDrop";
import { Sidebar } from "./Sidebar";

const FORMATS = {
  postman: { id: "postman", name: "Postman", extensions: [".json"] },
  har: { id: "har", name: "HAR", extensions: [".har"] },
};

function found(path: string, format: keyof typeof FORMATS | null, code = "import.unknown_format"): DetectedImportDTO {
  const fileName = path.split("/").pop() ?? path;
  if (format !== null) return { path, fileName, format: FORMATS[format], confidence: 100, error: null };
  return {
    path,
    fileName,
    format: { id: "", name: "", extensions: [] },
    confidence: 0,
    error: {
      kind: "unsupported",
      code,
      message: "PostSonde does not recognise the format of this file.",
      hint: "",
      details: {},
      causes: [],
    },
  };
}

function report(path: string, format: keyof typeof FORMATS): ImportReportDTO {
  return {
    format: FORMATS[format],
    path,
    fileName: path.split("/").pop() ?? path,
    digest: "d1",
    intoParent: false,
    items: [
      { kind: "folder", name: "Shop", parent: -1, fileKind: "", skippedKind: "", method: "", detail: "", status: "new", nodeId: "", source: { line: 0, column: 0, path: "" }, diagnostics: [] },
      { kind: "request", name: "List orders", parent: 0, fileKind: "http", skippedKind: "", method: "GET", detail: "https://api.test/orders", status: "new", nodeId: "", source: { line: 0, column: 0, path: "" }, diagnostics: [] },
    ],
    diagnostics: [],
    counts: { folders: 1, requests: 1, environments: 0, skipped: 0, failed: 0, lossy: 0, errors: 0, warnings: 0, infos: 0 },
  };
}

/** The shell's drop wiring: a drop target around the sidebar (which hosts the Import dialog), with the overlay. */
function Harness() {
  const ref = useRef<HTMLDivElement>(null);
  const active = useFileDrop(ref);
  return (
    <div ref={ref} data-testid="shell" data-file-drop-target={IMPORT_DROP_TARGET}>
      <Sidebar id="sidebar" view="collections" hidden={false} />
      <FileDropOverlay active={active} />
    </div>
  );
}

function setup(readOnly = ""): Stores {
  const stores = createStores();
  stores.workspace.dispatch({
    type: "workspace/setCurrent",
    workspace: { id: "ws_1", name: "shop", path: "/p/shop", localDir: "/d", localStatus: "existing", previousPath: "", newerFormat: false },
  });
  stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
  stores.collection.dispatch({ type: "collection/load", workspaceId: "ws_1", nodes: [], rootIds: [], readOnly });
  render(
    <StoreProvider stores={stores}>
      <Harness />
    </StoreProvider>,
  );
  return stores;
}

/** What Wails' runtime does under a file drag: marks the drop target. */
async function dragOver(on: boolean) {
  await act(async () => {
    screen.getByTestId("shell").classList.toggle(DROP_ACTIVE_CLASS, on);
    await settle();
  });
}

/** What Go sends after a drop (internal/app/filedrop.go). */
async function drop(paths: string[], target = IMPORT_DROP_TARGET) {
  await act(async () => {
    emit(COLLECTION_FILES_DROPPED_EVENT, { paths, target });
    await settle();
    await settle();
  });
}

const overlay = () => screen.queryByText("Drop to import");
const toasts = () => toaster.toasts().map((t) => String(t.message));
const dialog = () => screen.getByRole("dialog");

beforeEach(() => {
  resetFakeWails();
  toaster.clear();
  localStorage.clear();
  service.Tree.mockReset().mockResolvedValue({ workspaceId: "ws_1", path: "/p/shop", readOnly: "", rootIds: [], nodes: [], problems: [], scratch: [] });
  service.ImportFormats.mockReset().mockResolvedValue(Object.values(FORMATS));
  service.PickImportFile.mockReset();
  service.Import.mockReset();
  service.DetectImport.mockReset();
  service.PreviewImport.mockReset().mockImplementation(async ({ path, format }) => report(path, format as keyof typeof FORMATS));
});

describe("file drop overlay (IMP-11)", () => {
  it("shows while the runtime marks the shell as the drop target", async () => {
    setup();
    expect(overlay()).toBeNull();
    await dragOver(true);
    expect(overlay()).toBeTruthy();
    expect(screen.getByRole("status").textContent).toMatch(/Nothing is written before you import/);
    await dragOver(false);
    expect(overlay()).toBeNull();
  });

  it("ignores other class changes, and ends on pointer or Escape after a lost drag", async () => {
    setup();
    await act(async () => {
      screen.getByTestId("shell").classList.add("something-else");
      await settle();
    });
    expect(overlay()).toBeNull();

    await dragOver(true);
    fireEvent.pointerMove(window);
    expect(overlay()).toBeNull();
    expect(screen.getByTestId("shell").classList.contains(DROP_ACTIVE_CLASS)).toBe(false);

    await dragOver(true);
    fireEvent.keyDown(window, { key: "a" });
    expect(overlay()).toBeTruthy();
    fireEvent.keyDown(window, { key: "Escape" });
    expect(overlay()).toBeNull();
  });

  it("says so over a read-only project, and a drop there imports nothing", async () => {
    setup("The project folder is read-only.");
    await dragOver(true);
    expect(overlay()).toBeNull();
    expect(screen.getByText("This project is read-only.")).toBeTruthy();
    await drop(["/home/ada/shop.postman_collection.json"]);
    expect(service.DetectImport).not.toHaveBeenCalled();
    expect(toasts()).toEqual(["This project is read-only; files cannot be imported into it."]);
    expect(screen.queryByRole("dialog")).toBeNull();
  });
});

describe("dropped files (IMP-11)", () => {
  it("opens the Import dialog with the dropped file in its detected format", async () => {
    setup();
    const path = "/home/ada/shop.postman_collection.json";
    service.DetectImport.mockResolvedValue([found(path, "postman")]);
    await dragOver(true);
    await drop([path]);
    expect(overlay()).toBeNull();
    expect(service.DetectImport).toHaveBeenCalledWith({ paths: [path] });
    expect(service.PreviewImport).toHaveBeenCalledWith({ path, format: "postman" });
    expect(screen.getByRole("heading", { name: "Import" })).toBeTruthy();
    expect(within(dialog()).getByText("shop.postman_collection.json")).toBeTruthy();
    expect(within(dialog()).getByText(/Postman \(detected\)/)).toBeTruthy();
    expect((within(dialog()).getByRole("combobox", { name: "Format" }) as HTMLSelectElement).value).toBe("postman");
    // Nothing is written until Import.
    expect(service.Import).not.toHaveBeenCalled();
    expect(toasts()).toEqual([]);
  });

  it("imports several files one after another and names the ones it cannot import", async () => {
    setup();
    const paths = ["/home/ada/shop.postman_collection.json", "/home/ada/photo.png", "/home/ada/session.har"];
    service.DetectImport.mockResolvedValue([found(paths[0], "postman"), found(paths[1], null), found(paths[2], "har")]);
    await drop(paths);
    expect(toasts()).toEqual(["photo.png: PostSonde does not recognise the format of this file."]);
    expect(screen.getByRole("heading", { name: "Import (1 of 2)" })).toBeTruthy();
    expect(service.PreviewImport).toHaveBeenLastCalledWith({ path: paths[0], format: "postman" });

    // Cancel moves on to the next file, in a fresh dialog.
    await act(async () => {
      fireEvent.click(within(dialog()).getByRole("button", { name: "Cancel" }));
      await settle();
    });
    expect(screen.getByRole("heading", { name: "Import (2 of 2)" })).toBeTruthy();
    expect(service.PreviewImport).toHaveBeenLastCalledWith({ path: paths[2], format: "har" });
    expect(within(dialog()).getByText(/HAR \(detected\)/)).toBeTruthy();

    await act(async () => {
      fireEvent.click(within(dialog()).getByRole("button", { name: "Cancel" }));
      await settle();
    });
    expect(screen.queryByRole("dialog")).toBeNull();
  });

  it("opens nothing when no file can be imported", async () => {
    setup();
    service.DetectImport.mockResolvedValue([found("/a/photo.png", null), found("/a/notes", null, "import.read_failed")]);
    await drop(["/a/photo.png", "/a/notes"]);
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(toasts()).toEqual(["2 files cannot be imported: photo.png, notes."]);
  });

  it("refuses a drop of too many files with Go's message", async () => {
    setup();
    service.DetectImport.mockRejectedValue(Object.assign(new Error("call failed"), { cause: { kind: "invalid_argument", code: "import.too_many_files", message: "Too many files to import at once.", hint: "Drop at most 20 files at a time.", details: {}, causes: [] } }));
    await drop(Array.from({ length: 21 }, (_, i) => `/a/${i}.json`));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(toasts()).toEqual(["Too many files to import at once. Drop at most 20 files at a time."]);
  });

  it("leaves drops on another drop target alone", async () => {
    setup();
    await drop(["/home/ada/upload.bin"], "body-file");
    expect(service.DetectImport).not.toHaveBeenCalled();
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(toasts()).toEqual([]);
  });
});
