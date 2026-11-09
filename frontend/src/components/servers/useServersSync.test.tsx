import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";
import type { ReactNode } from "react";

const service = vi.hoisted(() => ({
  Types: vi.fn<() => Promise<unknown>>(),
  List: vi.fn<() => Promise<unknown>>(),
  SyncProject: vi.fn<() => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/servers", () => ({
  ServersService: service,
}));

import { createStores, StoreProvider, type Stores, type TreeNode } from "../../stores";
import { resetFakeWails } from "../../test/fakeWails";
import { useServersSync } from "./useServersSync";

const folder = (id: string): TreeNode => ({ kind: "folder", id, parentId: null, name: id, path: id, fileKind: "", readOnly: "", childIds: [] });

function load(stores: Stores, workspaceId: string, ids: string[]) {
  stores.collection.dispatch({ type: "collection/loading", workspaceId });
  stores.collection.dispatch({ type: "collection/load", workspaceId, nodes: ids.map(folder), rootIds: ids });
}

beforeEach(() => {
  resetFakeWails();
  vi.useFakeTimers();
  service.Types.mockReset().mockResolvedValue([]);
  service.List.mockReset().mockResolvedValue([]);
  service.SyncProject.mockReset().mockResolvedValue({ workspaceId: "ws_1", problems: [] });
});

afterEach(() => {
  vi.useRealTimers();
});

describe("useServersSync", () => {
  it("syncs the saved servers when a project's tree loads, reloads or gets a folder, never otherwise", async () => {
    const stores = createStores();
    renderHook(() => useServersSync(), {
      wrapper: ({ children }: { children: ReactNode }) => <StoreProvider stores={stores}>{children}</StoreProvider>,
    });
    await act(() => vi.advanceTimersByTimeAsync(500));
    expect(service.SyncProject).not.toHaveBeenCalled();

    act(() => load(stores, "ws_1", ["a"]));
    await act(() => vi.advanceTimersByTimeAsync(500));
    expect(service.SyncProject).toHaveBeenCalledTimes(1);

    // A selection does not sync; a new folder and a reload do.
    act(() => stores.collection.dispatch({ type: "collection/select", id: "a" }));
    await act(() => vi.advanceTimersByTimeAsync(500));
    expect(service.SyncProject).toHaveBeenCalledTimes(1);
    act(() => stores.collection.dispatch({ type: "collection/add", node: folder("b") }));
    await act(() => vi.advanceTimersByTimeAsync(500));
    expect(service.SyncProject).toHaveBeenCalledTimes(2);

    // Another project.
    act(() => load(stores, "ws_2", ["c"]));
    await act(() => vi.advanceTimersByTimeAsync(500));
    expect(service.SyncProject).toHaveBeenCalledTimes(3);
  });
});
