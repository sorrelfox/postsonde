import type { ReactNode } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, renderHook } from "@testing-library/react";

const service = vi.hoisted(() => ({
  UpdateRequest: vi.fn<(input: unknown) => Promise<void>>(),
  Flush: vi.fn<(id: string) => Promise<void>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({
  CollectionService: service,
}));

import { COLLECTION_SAVE_FAILED_EVENT, COLLECTION_SAVED_EVENT } from "../lib/collection";
import { createStores, StoreProvider } from "../stores";
import { emit, resetFakeWails, settle } from "../test/fakeWails";
import { followAutoSave } from "./autosave";
import { useAutoSave } from "./useAutoSave";

beforeEach(() => {
  resetFakeWails();
  service.UpdateRequest.mockReset().mockResolvedValue(undefined);
  service.Flush.mockReset().mockResolvedValue(undefined);
});

describe("useAutoSave", () => {
  it("edits, reports status and flushes", async () => {
    const stores = createStores();
    stores.drafts.dispatch({ type: "drafts/open", id: "req_1", request: { id: "req_1", url: "/a" } });
    const stop = followAutoSave(stores);
    const wrapper = ({ children }: { children: ReactNode }) => <StoreProvider stores={stores}>{children}</StoreProvider>;
    const { result } = renderHook(() => useAutoSave("req_1"), { wrapper });
    expect(result.current).toMatchObject({ status: "idle", dirty: false, failure: null });

    act(() => result.current.edit({ url: "/b" }));
    expect(result.current).toMatchObject({ status: "saving", dirty: true });
    expect(service.UpdateRequest).toHaveBeenCalledWith({ request: { id: "req_1", url: "/b" }, revision: 1 });

    await act(async () => {
      await settle();
      emit(COLLECTION_SAVE_FAILED_EVENT, {
        workspaceId: "ws_1",
        id: "req_1",
        revision: 1,
        error: { kind: "io", code: "projectfile.save_failed", message: "Disk full.", hint: "", details: {}, causes: [] },
      });
    });
    expect(result.current.status).toBe("error");
    expect(result.current.failure).toMatchObject({ message: "Disk full." });

    await act(() => result.current.flush());
    expect(service.Flush).toHaveBeenCalledWith("req_1");

    act(() => emit(COLLECTION_SAVED_EVENT, { workspaceId: "ws_1", id: "req_1", revision: 1, savedAt: "t" }));
    expect(result.current).toMatchObject({ status: "saved", dirty: false, failure: null });
    stop();
  });
});
