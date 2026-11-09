import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const history = vi.hoisted(() => ({
  List: vi.fn<(input: Record<string, unknown>) => Promise<unknown>>(),
  Clear: vi.fn<() => Promise<void>>(),
  ClearRequest: vi.fn<(id: string) => Promise<number>>(),
  Delete: vi.fn<(id: string) => Promise<void>>(),
  Restore: vi.fn<(id: string) => Promise<unknown>>(),
}));
const collection = vi.hoisted(() => ({
  Request: vi.fn<(id: string) => Promise<unknown>>(),
  UpdateRequest: vi.fn<(input: unknown) => Promise<unknown>>(),
  CreateScratch: vi.fn<(input: unknown) => Promise<unknown>>(),
  Flush: vi.fn<(id: string) => Promise<void>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/history", () => ({ HistoryService: history }));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({ CollectionService: collection }));

import { toaster } from "../../commands/toasts";
import { DEFAULT_LOCALE, PSEUDO_LOCALE, setLocale } from "../../i18n";
import type { NodeDTO, RequestDTO } from "../../lib/collection";
import type { RestoreDTO } from "../../lib/history";
import type { HistorySummaryDTO } from "../../lib/responseHistory";
import { createStores, StoreProvider, type Stores, type TreeNode } from "../../stores";
import { expectAccessible } from "../../test/a11y";
import { emit, resetFakeWails, settle } from "../../test/fakeWails";
import { englishLeftovers, isPseudo, visibleTexts } from "../../test/pseudoLocale";
import { ConfirmHost } from "../common/Dialog";
import { HistoryPanel } from "./HistoryPanel";

const MASK = "••••••";
const SCRATCH = "req_00000000000000aa";

function summary(id: string, over: Partial<HistorySummaryDTO> = {}): HistorySummaryDTO {
  return {
    id,
    time: new Date().toISOString(),
    kind: "http",
    requestId: "req_1",
    requestName: "Create user",
    environment: "",
    method: "POST",
    url: "https://api.test/users",
    outcome: "ok",
    statusCode: 201,
    status: "201 Created",
    errorCode: "",
    error: "",
    durationMs: 12,
    requestSize: 0,
    responseSize: 2,
    masked: false,
    ...over,
  };
}

const kv = (name: string, value: string) => ({ name, value, description: "", enabled: true, extra: "" });

function restored(over: Partial<RestoreDTO> = {}): RestoreDTO {
  return {
    id: "h_1",
    requestId: "req_1",
    requestName: "Create user",
    kind: "http",
    templated: true,
    method: "PUT",
    url: "{{base}}/users",
    params: [],
    pathParams: [],
    headers: [kv("Authorization", "Bearer "), kv("X-Ref", "{{ref}}")],
    body: { mode: "raw", type: "json", text: '{"a":"{{a}}"}', fields: [], parts: [], path: "", contentType: "", extra: "" },
    query: "",
    variables: "",
    operationName: "",
    hidden: [{ section: "headers", name: "Authorization", reason: "masked" }],
    ...over,
  };
}

function requestDTO(id: string, over: Partial<RequestDTO> = {}): RequestDTO {
  return {
    id,
    kind: "http",
    name: "Create user",
    method: "POST",
    url: "https://old.test",
    params: [],
    pathParams: [],
    headers: [kv("Authorization", "Bearer real-token")],
    body: { mode: "none", type: "", text: "", fields: [], parts: [], path: "", contentType: "", extra: "" },
    ...over,
  } as RequestDTO;
}

const node = (over: Partial<TreeNode> & { id: string }): TreeNode =>
  ({ kind: "request", parentId: null, name: over.id, path: `${over.id}.json`, fileKind: "http", readOnly: "", method: "GET", ...over }) as TreeNode;

function page(entries: HistorySummaryDTO[], total = entries.length, all = total, environments: string[] = []) {
  return { entries, total, all, environments };
}

async function setup(open: string[] = []): Promise<Stores> {
  const stores = createStores();
  stores.workspace.dispatch({
    type: "workspace/setCurrent",
    workspace: { id: "ws_1", name: "Shop", path: "/p/shop", localDir: "/d", localStatus: "existing", previousPath: "", newerFormat: false },
  });
  stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
  stores.collection.dispatch({
    type: "collection/load",
    workspaceId: "ws_1",
    nodes: [node({ id: "req_1", name: "Create user", method: "POST" }), node({ id: "req_2", name: "List users" })],
    rootIds: ["req_1", "req_2"],
  });
  stores.tabs.dispatch({ type: "tabs/restore", order: open, activeId: open[0] ?? null });
  render(
    <StoreProvider stores={stores}>
      <aside aria-label="History">
        <HistoryPanel label="History" />
      </aside>
      <ConfirmHost />
    </StoreProvider>,
  );
  await act(settle);
  return stores;
}

/** The arguments of the last List call. */
const lastList = () => history.List.mock.calls[history.List.mock.calls.length - 1]?.[0] ?? {};
const options = () => [...document.querySelectorAll<HTMLElement>("[role=listbox] [role=option]")];

beforeEach(() => {
  resetFakeWails();
  toaster.clear();
  for (const fn of Object.values(history)) fn.mockReset();
  for (const fn of Object.values(collection)) fn.mockReset();
  history.List.mockResolvedValue(page([summary("h_1"), summary("h_2", { requestId: "req_2", requestName: "List users", method: "GET", statusCode: 404, status: "404 Not Found" })]));
  history.Restore.mockResolvedValue(restored());
  collection.Request.mockImplementation((id) => Promise.resolve(requestDTO(id)));
  collection.UpdateRequest.mockResolvedValue({ version: 2 });
  collection.Flush.mockResolvedValue(undefined);
});

afterEach(() => {
  setLocale(DEFAULT_LOCALE);
  vi.useRealTimers();
});

describe("history panel (NAV-02, NAV-03)", () => {
  it("lists the entries of the open project, grouped by day, and is accessible", async () => {
    await setup();
    expect(history.List).toHaveBeenCalledWith(expect.objectContaining({ text: "", offset: 0, limit: 50 }));
    const list = screen.getByRole("listbox", { name: "History entries" });
    expect(within(list).getByRole("group", { name: "Today" })).toBeTruthy();
    expect(options().map((o) => o.getAttribute("aria-label"))).toEqual([
      expect.stringMatching(/^POST Create user, 201 Created, /),
      expect.stringMatching(/^GET List users, 404 Not Found, /),
    ]);
    expect(screen.getByRole("status").textContent).toBe("2 entries");
    expectAccessible();
    fireEvent.click(screen.getByRole("button", { name: "Show filters" }));
    expectAccessible();
  });

  it("searches in Go after typing stops", async () => {
    await setup();
    vi.useFakeTimers();
    fireEvent.change(screen.getByRole("searchbox", { name: "Search history" }), { target: { value: "users" } });
    expect(lastList().text).toBe("");
    await act(async () => {
      vi.advanceTimersByTime(300);
    });
    vi.useRealTimers();
    await act(settle);
    expect(lastList()).toMatchObject({ text: "users", offset: 0 });
  });

  it("passes each filter to Go", async () => {
    history.List.mockResolvedValue(page([summary("h_1")], 1, 1, ["staging"]));
    await setup(["req_1"]);
    fireEvent.click(screen.getByRole("button", { name: "Show filters" }));
    const filters = screen.getByRole("group", { name: "History filters" });
    const change = async (label: string, value: string) => {
      await act(async () => {
        fireEvent.change(within(filters).getByLabelText(label), { target: { value } });
      });
      await act(settle);
    };
    await change("Method", "DELETE");
    expect(lastList()).toMatchObject({ method: "DELETE" });
    await change("Status", "5xx");
    expect(lastList()).toMatchObject({ status: "5xx" });
    await change("Status", "error");
    expect(lastList()).toMatchObject({ status: "error" });
    await change("Kind", "graphql");
    expect(lastList()).toMatchObject({ kind: "graphql" });
    await change("Environment", "staging");
    expect(lastList()).toMatchObject({ environment: "staging" });
    await change("From", "2026-09-20");
    await change("To", "2026-09-22");
    const { since, until } = lastList() as { since: string; until: string };
    expect(new Date(since).getDate()).toBe(20);
    expect(new Date(until).getDate()).toBe(23);
    await act(async () => {
      fireEvent.click(within(filters).getByRole("checkbox", { name: "Only “Create user”" }));
    });
    await act(settle);
    expect(lastList()).toMatchObject({ requestId: "req_1" });
    await act(async () => {
      fireEvent.click(within(filters).getByRole("checkbox", { name: "Also search headers and bodies" }));
    });
    await act(settle);
    expect(lastList()).toMatchObject({ deep: true });
    expect(screen.getByRole("status").textContent).toBe("1 matching entry of 1");
    await act(async () => {
      fireEvent.click(within(filters).getByRole("button", { name: "Reset filters" }));
    });
    await act(settle);
    expect(lastList()).toMatchObject({ method: "", status: "", kind: "", environment: "", since: "", until: "", requestId: "", deep: false });
  });

  it("pages: Load more asks for the next page", async () => {
    const first = Array.from({ length: 50 }, (_, i) => summary(`h_${i}`));
    history.List.mockResolvedValueOnce(page(first, 120));
    await setup();
    expect(options()).toHaveLength(50);
    history.List.mockResolvedValueOnce(page([summary("h_50"), summary("h_51")], 120));
    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Load more" }));
    });
    await act(settle);
    expect(lastList()).toMatchObject({ offset: 50, limit: 50 });
    expect(options()).toHaveLength(52);
    expect(screen.getByRole("status").textContent).toBe("120 entries");
  });

  it("restores into the request with Enter as an undoable edit, never writing a mask", async () => {
    const stores = await setup();
    const first = options()[0]!;
    act(() => first.focus());
    await act(async () => {
      fireEvent.keyDown(first, { key: "Enter" });
    });
    await act(settle);
    expect(history.Restore).toHaveBeenCalledWith("h_1");
    const draft = stores.drafts.getState().byId.req_1?.request as unknown as RequestDTO;
    expect(draft).toMatchObject({ method: "PUT", url: "{{base}}/users", body: { text: '{"a":"{{a}}"}' } });
    // The masked Authorization keeps the request's own value.
    expect(draft.headers).toEqual([kv("Authorization", "Bearer real-token"), kv("X-Ref", "{{ref}}")]);
    expect(JSON.stringify(draft)).not.toContain(MASK);
    expect(stores.tabs.getState().activeId).toBe("req_1");
    const toast = toaster.toasts()[0]!;
    expect(toast.message).toBe("Restored “Create user” from the history. Not restored, the request keeps its own: header “Authorization”.");
    // Undo takes it back.
    act(() => toaster.runAction(toast.id));
    expect((stores.drafts.getState().byId.req_1?.request as unknown as RequestDTO).url).toBe("https://old.test");
  });

  it("opens an entry as a scratch request from the context menu", async () => {
    history.Restore.mockResolvedValue(restored({ templated: false, url: "https://api.test/users?page=2", headers: [kv("Authorization", "Bearer ")] }));
    collection.CreateScratch.mockResolvedValue({
      nodeId: SCRATCH,
      tree: { workspaceId: "ws_1", path: "/p/shop", readOnly: "", rootIds: [], nodes: [], problems: [], scratch: [{ id: SCRATCH, parentId: null, kind: "request", name: "Create user (history)", path: "", fileKind: "http", method: "GET", childIds: [], readOnly: "" } as NodeDTO] },
    });
    collection.Request.mockImplementation((id) => Promise.resolve(requestDTO(id, { name: "Create user (history)", headers: [], url: "" })));
    const stores = await setup();
    fireEvent.contextMenu(options()[0]!);
    await act(async () => {
      fireEvent.click(screen.getByRole("menuitem", { name: "Open as scratch request" }));
    });
    await act(settle);
    expect(collection.CreateScratch).toHaveBeenCalledWith({ kind: "http", name: "Create user (history)" });
    const draft = stores.drafts.getState().byId[SCRATCH]?.request as unknown as RequestDTO;
    expect(draft).toMatchObject({ url: "https://api.test/users?page=2", params: [expect.objectContaining({ name: "page", value: "2" })] });
    expect(draft.headers).toEqual([kv("Authorization", "Bearer ")]);
    expect(JSON.stringify(draft)).not.toContain(MASK);
    expect(stores.tabs.getState().activeId).toBe(SCRATCH);
    expect(toaster.toasts()[0]?.message).toContain("The history had it only as sent");
  });

  it("offers no restore into a request that is gone", async () => {
    history.List.mockResolvedValue(page([summary("h_9", { requestId: "req_gone", requestName: "Gone" })]));
    await setup();
    fireEvent.contextMenu(options()[0]!);
    expect(screen.getByRole("menuitem", { name: "Restore into the request" }).getAttribute("aria-disabled")).toBe("true");
  });

  it("deletes an entry with the Delete key", async () => {
    history.Delete.mockResolvedValue(undefined);
    await setup();
    const first = options()[0]!;
    act(() => first.focus());
    await act(async () => {
      fireEvent.keyDown(first, { key: "Delete" });
    });
    expect(history.Delete).toHaveBeenCalledWith("h_1");
  });

  it("clears one request's history and all of it, after asking", async () => {
    history.ClearRequest.mockResolvedValue(3);
    history.Clear.mockResolvedValue(undefined);
    await setup(["req_1"]);
    fireEvent.click(screen.getByRole("button", { name: "Clear history" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Clear history of “Create user”…" }));
    let dialog = await screen.findByRole("dialog", { name: "Clear the history of “Create user”?" });
    await act(async () => {
      fireEvent.click(within(dialog).getByRole("button", { name: "Clear history" }));
    });
    expect(history.ClearRequest).toHaveBeenCalledWith("req_1");
    expect(toaster.toasts()[toaster.toasts().length - 1]?.message).toBe("Deleted 3 entries of “Create user”.");

    fireEvent.click(screen.getByRole("button", { name: "Clear history" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Clear all history…" }));
    dialog = await screen.findByRole("dialog", { name: "Clear all history?" });
    expect(dialog.textContent).toContain("All 2 entries of this project’s history are deleted.");
    await act(async () => {
      fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    });
    expect(history.Clear).not.toHaveBeenCalled();
    fireEvent.click(screen.getByRole("button", { name: "Clear history" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Clear all history…" }));
    dialog = await screen.findByRole("dialog", { name: "Clear all history?" });
    await act(async () => {
      fireEvent.click(within(dialog).getByRole("button", { name: "Clear history" }));
    });
    expect(history.Clear).toHaveBeenCalledTimes(1);
  });

  it("reads the list again when Go says the history changed", async () => {
    await setup();
    const calls = history.List.mock.calls.length;
    history.List.mockResolvedValue(page([]));
    vi.useFakeTimers();
    act(() => emit("history:changed", { action: "cleared", id: "", entry: null }));
    await act(async () => {
      vi.advanceTimersByTime(200);
    });
    vi.useRealTimers();
    await act(settle);
    expect(history.List.mock.calls.length).toBe(calls + 1);
    expect(screen.getByText("No history yet")).toBeTruthy();
  });

  it("shows every text from the catalogue (pseudo-locale)", async () => {
    setLocale(PSEUDO_LOCALE);
    await setup(["req_1"]);
    fireEvent.click(screen.getAllByRole("button")[0]!);
    const roots = [document.body];
    expect(englishLeftovers(roots, ["history"])).toEqual([]);
    expect(visibleTexts(roots).some(isPseudo)).toBe(true);
    fireEvent.contextMenu(options()[0]!);
    expect(englishLeftovers([screen.getByRole("menu")], ["history"])).toEqual([]);
  });
});
