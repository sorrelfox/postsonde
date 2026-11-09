import { act, fireEvent, render, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const service = vi.hoisted(() => ({
  Folder: vi.fn<(id: string) => Promise<unknown>>(),
  UpdateFolder: vi.fn<(input: { folder: unknown; revision: number }) => Promise<void>>(),
  UpdateRequest: vi.fn<(input: { request: unknown; revision: number }) => Promise<void>>(),
  Flush: vi.fn<(id: string) => Promise<void>>(),
  InheritedAuth: vi.fn<(id: string) => unknown>(),
  InheritedSettings: vi.fn<(id: string) => unknown>(),
}));
const http = vi.hoisted(() => ({
  Variables: vi.fn<(id: string) => unknown>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({
  CollectionService: service,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http", () => ({
  HTTPService: http,
}));

import { EditorView } from "@codemirror/view";
import { confirmQueue } from "../../commands/dialogs";
import { inheritedRows } from "../../commands/folderSettings";
import { undoRequestEdit } from "../../commands/undoRedo";
import type { AuthDTO, FolderDTO, InheritedFolderDTO, KeyValueDTO } from "../../lib/collection";
import { createStores, StoreProvider, type Stores, type TreeNode } from "../../stores";
import { fakeRuntime, resetFakeWails, settle } from "../../test/fakeWails";
import { TabPanel } from "../tabs/TabPanel";

const cancellable = <T,>(value: T) => Object.assign(Promise.resolve(value), { cancel: vi.fn() });

const row = (name: string, value: string, over: Partial<KeyValueDTO> = {}): KeyValueDTO => ({
  name,
  value,
  description: "",
  enabled: true,
  extra: "",
  ...over,
});

const auth = (over: Partial<AuthDTO> = {}): AuthDTO => ({
  type: "inherit",
  username: "",
  password: "",
  prefix: null,
  token: "",
  key: "",
  value: "",
  in: "",
  disabled: false,
  localSecrets: [],
  extra: "",
  ...over,
});

function folder(over: Partial<FolderDTO> = {}): FolderDTO {
  return {
    id: "fld_admins",
    kind: "folder",
    name: "Admins",
    description: "",
    auth: auth(),
    headers: [row("Accept", "application/xml", { extra: '{"x-note":"kept"}' })],
    variables: [row("role", "admin")],
    ...over,
  };
}

const inherited: InheritedFolderDTO[] = [
  {
    folderId: "fld_users",
    folderName: "Users",
    headers: [row("X-Team", "core"), row("Accept", "application/json"), row("X-Off", "no", { enabled: false })],
    variables: [row("role", "user"), row("region", "eu")],
    unreadable: false,
  },
];

const NODES = [
  { id: "fld_users", kind: "folder", parentId: null, name: "Users", path: "users", fileKind: "", readOnly: "", childIds: ["fld_admins"] },
  { id: "fld_admins", kind: "folder", parentId: "fld_users", name: "Admins", path: "users/admins", fileKind: "", readOnly: "", childIds: [] },
] as unknown as TreeNode[];

async function setup(dto: FolderDTO = folder(), opts: { readOnly?: string; subTab?: string } = {}): Promise<Stores> {
  service.Folder.mockImplementation(() => Promise.resolve(structuredClone(dto)));
  const stores = createStores();
  const nodes = NODES.map((n) => (n.id === "fld_admins" ? { ...n, readOnly: opts.readOnly ?? "" } : n));
  stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
  stores.collection.dispatch({ type: "collection/load", workspaceId: "ws_1", nodes, rootIds: ["fld_users"] });
  stores.tabs.dispatch({ type: "tabs/restore", order: ["fld_admins"], activeId: "fld_admins" });
  if (opts.subTab !== undefined) stores.drafts.dispatch({ type: "drafts/setSubTab", id: "fld_admins", subTab: opts.subTab });
  render(
    <StoreProvider stores={stores}>
      <TabPanel />
    </StoreProvider>,
  );
  await act(settle);
  return stores;
}

const editor = () => document.querySelector("[data-folder-id]") as HTMLElement;
/** The folder of the last UpdateFolder call. */
const sent = (): FolderDTO => {
  const calls = service.UpdateFolder.mock.calls;
  return calls[calls.length - 1]?.[0].folder as FolderDTO;
};

beforeEach(() => {
  resetFakeWails();
  service.Folder.mockReset();
  service.UpdateFolder.mockReset().mockResolvedValue(undefined);
  service.UpdateRequest.mockReset().mockResolvedValue(undefined);
  service.Flush.mockReset().mockResolvedValue(undefined);
  service.InheritedAuth.mockReset().mockImplementation(() =>
    cancellable({ type: "bearer", disabled: false, folderId: "fld_users", folderName: "Users" }),
  );
  service.InheritedSettings.mockReset().mockImplementation(() => cancellable({ folders: inherited }));
  http.Variables.mockReset().mockImplementation(() => cancellable({ variables: [], environment: "" }));
});

describe("FolderEditor", () => {
  it("shows the folder's name, path and Auth, Headers, Variables and Server tabs", async () => {
    await setup();
    expect(service.Folder).toHaveBeenCalledWith("fld_admins");
    expect(editor().textContent).toContain("DIR");
    expect(within(editor()).getByRole("heading", { name: "Admins" })).toBeTruthy();
    expect(editor().textContent).toContain("users/admins");
    const tabs = within(editor()).getAllByRole("tab");
    expect(tabs.map((t) => t.textContent)).toEqual(["Auth", "Headers1", "Variables1", "Server", "Notes", "Run"]);
    expect(tabs[0]?.getAttribute("aria-selected")).toBe("true");
    // The folder's variables resolve its chips (the folder's own ID).
    expect(http.Variables).toHaveBeenCalledWith("fld_admins");
  });

  it("edits the folder's auth like a request's, with the folder above as the inherited one", async () => {
    await setup();
    const section = document.querySelector("[data-auth-type]") as HTMLElement;
    expect(service.InheritedAuth).toHaveBeenCalledWith("fld_admins");
    expect(within(section).getByRole("status").textContent).toContain("Bearer token, inherited from “Users”");
    fireEvent.click(within(section).getByRole("radio", { name: "None" }));
    await act(settle);
    expect(sent().auth.type).toBe("none");
    expect(service.UpdateRequest).not.toHaveBeenCalled();
    expect(editor().textContent).toContain("send no credentials");
  });

  it("saves header rows, keeping their unknown members, and lists the inherited ones", async () => {
    await setup(folder(), { subTab: "headers" });
    const list = within(editor()).getByRole("list", { name: "Inherited headers" });
    const items = within(list).getAllByRole("listitem");
    expect(items.map((li) => [li.textContent?.slice(0, 30), li.hasAttribute("data-overridden")])).toEqual([
      [expect.stringContaining("X-Team"), false],
      [expect.stringContaining("Accept"), true],
    ]);

    const grid = within(editor()).getByRole("grid", { name: "Folder headers" });
    fireEvent.change(within(grid).getByDisplayValue("application/xml"), { target: { value: "text/plain" } });
    await act(settle);
    expect(sent()).toEqual({ ...folder(), headers: [row("Accept", "text/plain", { extra: '{"x-note":"kept"}' })] });
    expect(service.UpdateFolder.mock.calls[0]?.[0].revision).toBe(1);

    // Disabling the row no longer overrides the parent's Accept.
    fireEvent.click(within(grid).getAllByRole("checkbox")[0] as HTMLElement);
    await act(settle);
    expect(within(list).getAllByRole("listitem").some((li) => li.hasAttribute("data-overridden"))).toBe(false);
  });

  it("saves variables and reloads the chips after an edit", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    try {
      await setup(folder(), { subTab: "variables" });
      const list = within(editor()).getByRole("list", { name: "Inherited variables" });
      expect(within(list).getAllByRole("listitem").map((li) => li.hasAttribute("data-overridden"))).toEqual([true, false]);
      expect(http.Variables).toHaveBeenCalledTimes(1);

      const grid = within(editor()).getByRole("grid", { name: "Folder variables" });
      fireEvent.change(within(grid).getByDisplayValue("admin"), { target: { value: "owner" } });
      await act(settle);
      expect(sent().variables).toEqual([row("role", "owner")]);
      await act(async () => {
        await vi.advanceTimersByTimeAsync(400);
      });
      expect(http.Variables).toHaveBeenCalledTimes(2);
    } finally {
      vi.useRealTimers();
    }
  });

  it("marks a variable secret: masked, the flag saved in its extra members (PRV-09)", async () => {
    await setup(folder({ variables: [row("role", "admin", { extra: '{"x-note":"kept"}' })] }), { subTab: "variables" });
    const panel = within(editor());
    expect(panel.getByRole("tabpanel").textContent).toContain("stored only on this computer");
    const grid = panel.getByRole("grid", { name: "Folder variables" });
    expect((within(grid).getByDisplayValue("admin") as HTMLInputElement).type).toBe("text");

    const lock = within(grid).getByRole("button", { name: "Secret, row 1" });
    expect(lock.getAttribute("aria-pressed")).toBe("false");
    fireEvent.click(lock);
    await act(settle);
    expect(sent().variables).toEqual([row("role", "admin", { extra: '{"x-note":"kept","secret":true}' })]);
    expect((within(grid).getByDisplayValue("admin") as HTMLInputElement).type).toBe("password");
    expect(within(grid).getByRole("button", { name: "Secret, row 1" }).getAttribute("aria-pressed")).toBe("true");

    // The eye shows the value; the lock makes it plain again.
    fireEvent.click(within(grid).getByRole("button", { name: "Show value, row 1" }));
    expect((within(grid).getByDisplayValue("admin") as HTMLInputElement).type).toBe("text");
    fireEvent.click(within(grid).getByRole("button", { name: "Secret, row 1" }));
    await act(settle);
    expect(sent().variables).toEqual([row("role", "admin", { extra: '{"x-note":"kept"}' })]);
    expect(within(grid).queryByRole("button", { name: /value, row 1/ })).toBeNull();
  });

  it("shows inherited secret variables masked", async () => {
    service.InheritedSettings.mockImplementation(() =>
      cancellable({
        folders: [{ ...inherited[0], variables: [row("apiKey", "", { extra: '{"secret":true}' }), row("region", "eu")] }],
      }),
    );
    await setup(folder(), { subTab: "variables" });
    const items = within(within(editor()).getByRole("list", { name: "Inherited variables" })).getAllByRole("listitem");
    expect(items[0]?.hasAttribute("data-secret")).toBe(true);
    expect(items[0]?.textContent).toContain("••••••");
    expect(items[1]?.textContent).toContain("eu");
  });

  it("undoes an edit through the same history as a request", async () => {
    const stores = await setup(folder(), { subTab: "headers" });
    const grid = within(editor()).getByRole("grid", { name: "Folder headers" });
    const input = within(grid).getByDisplayValue("application/xml");
    fireEvent.change(input, { target: { value: "text/plain" } });
    await act(settle);
    // The panel marks the folder for the undo keys (commands/undoRedo.ts).
    expect(input.closest("[data-edit-history]")?.getAttribute("data-edit-history")).toBe("fld_admins");
    act(() => {
      expect(undoRequestEdit(stores, "fld_admins")).toBe(true);
    });
    await act(settle);
    expect(sent().headers).toEqual(folder().headers);
    expect(stores.drafts.getState().byId.fld_admins?.dirty).toBe(true);
  });

  it("is read-only when the folder cannot be written", async () => {
    await setup(folder(), { readOnly: "newer_version", subTab: "headers" });
    expect(within(editor()).getByRole("note").textContent).toContain("newer version");
    const grid = within(editor()).getByRole("grid", { name: "Folder headers" });
    expect((within(grid).getByDisplayValue("application/xml") as HTMLInputElement).readOnly).toBe(true);
  });

  it("shows why the settings could not be opened, and retries", async () => {
    service.Folder.mockRejectedValueOnce({ kind: "unsupported", code: "collection.read_only", message: "The folder's settings could not be read." });
    const stores = createStores();
    stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
    stores.collection.dispatch({ type: "collection/load", workspaceId: "ws_1", nodes: NODES, rootIds: ["fld_users"] });
    stores.tabs.dispatch({ type: "tabs/restore", order: ["fld_admins"], activeId: "fld_admins" });
    render(
      <StoreProvider stores={stores}>
        <TabPanel />
      </StoreProvider>,
    );
    await act(settle);
    expect(document.body.textContent).toContain("could not be opened");
    service.Folder.mockImplementation(() => Promise.resolve(folder()));
    fireEvent.click(within(document.body).getByRole("button", { name: "Try again" }));
    await act(settle);
    expect(editor()).not.toBeNull();
  });
});

describe("FolderEditor notes (COL-07)", () => {
  let detach: () => void = () => {};
  beforeEach(() => {
    detach = confirmQueue.attach();
    return () => detach();
  });

  it("previews the folder's Markdown notes and opens a link only after confirming", async () => {
    await setup(
      folder({ description: "# About\n\nSee [the docs](https://docs.example/users).\n\n![diagram](https://img.example/d.png)" }),
      { subTab: "notes" },
    );
    const panel = editor().querySelector("[data-notes-mode]") as HTMLElement;
    expect(panel.getAttribute("data-notes-mode")).toBe("preview");
    expect(within(panel).getByRole("heading", { name: "About" })).toBeTruthy();
    expect(panel.querySelectorAll("img, iframe, link, script, object, embed")).toHaveLength(0);
    expect(panel.textContent).toContain("diagram");

    fireEvent.click(within(panel).getByRole("link", { name: "the docs" }));
    await act(settle);
    const asked = confirmQueue.current();
    expect(asked).toMatchObject({ title: "Open this link in your browser?", message: "https://docs.example/users" });
    expect(fakeRuntime.Browser.OpenURL).not.toHaveBeenCalled();
    await act(async () => {
      confirmQueue.settle(asked?.id ?? 0, true);
      await settle();
    });
    expect(fakeRuntime.Browser.OpenURL).toHaveBeenCalledWith("https://docs.example/users");

    // Cancelling opens nothing.
    fireEvent.click(within(panel).getByRole("link", { name: "the docs" }));
    await act(settle);
    await act(async () => {
      confirmQueue.settle(confirmQueue.current()?.id ?? 0, false);
      await settle();
    });
    expect(fakeRuntime.Browser.OpenURL).toHaveBeenCalledTimes(1);
  });

  it("edits the notes in a Markdown editor and auto-saves them as the folder's description", async () => {
    await setup(folder(), { subTab: "notes" });
    const panel = editor().querySelector("[data-notes-mode]") as HTMLElement;
    // No notes yet: the editor is shown.
    expect(panel.getAttribute("data-notes-mode")).toBe("edit");
    const view = EditorView.findFromDOM(within(editor()).getByRole("textbox", { name: "Notes" })) as EditorView;
    act(() => view.dispatch({ changes: { from: 0, insert: "## Admins\n\n- [x] set up" }, userEvent: "input.type" }));
    await act(settle);
    expect(sent()).toEqual({ ...folder(), description: "## Admins\n\n- [x] set up" });
    expect(service.UpdateRequest).not.toHaveBeenCalled();

    fireEvent.click(within(panel).getByRole("button", { name: "Preview" }));
    expect(within(panel).getByRole("heading", { name: "Admins" })).toBeTruthy();
    expect((within(panel).getByRole("checkbox") as HTMLInputElement).checked).toBe(true);
  });

  it("shows read-only notes without an editable editor", async () => {
    await setup(folder({ description: "Kept." }), { subTab: "notes", readOnly: "newer_version" });
    const panel = editor().querySelector("[data-notes-mode]") as HTMLElement;
    expect(panel.textContent).toContain("Kept.");
    fireEvent.click(within(panel).getByRole("button", { name: "Edit" }));
    const field = within(editor()).getByRole("textbox", { name: "Notes" });
    expect(field.getAttribute("contenteditable")).toBe("false");
  });
});

describe("inheritedRows", () => {
  const folders: InheritedFolderDTO[] = [
    { folderId: "a", folderName: "A", headers: [row("X-Env", "a"), row("X-A", "1")], variables: [row("v", "a")], unreadable: false },
    { folderId: "b", folderName: "B", headers: [row("x-env", "b")], variables: [row("V", "b")], unreadable: false },
  ];

  it("marks a header a nearer folder or the item overrides, ignoring case", () => {
    expect(inheritedRows(folders, "headers", null).map((r) => [r.folderId, r.name, r.overridden])).toEqual([
      ["a", "X-Env", true],
      ["a", "X-A", false],
      ["b", "x-env", false],
    ]);
    expect(inheritedRows(folders, "headers", [row("X-ENV", "own")]).every((r) => r.name === "X-A" || r.overridden)).toBe(true);
  });

  it("compares variable names exactly", () => {
    expect(inheritedRows(folders, "variables", [row("v", "own")]).map((r) => r.overridden)).toEqual([true, false]);
  });
});
