import { afterEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";

const service = vi.hoisted(() => ({
  Request: vi.fn<(id: string) => Promise<unknown>>(),
  UpdateRequest: vi.fn<(input: unknown) => Promise<void>>(),
  ResolveConflict: vi.fn<(id: string) => Promise<void>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({
  CollectionService: service,
}));

import { createStores, DELETED_READ_ONLY, StoreProvider, type RequestNode } from "../../stores";
import { settle } from "../../test/fakeWails";
import { ExternalChangeBanner } from "./ExternalChangeBanner";

const health: RequestNode = {
  kind: "request",
  id: "health",
  parentId: null,
  name: "Health",
  path: "health.json",
  fileKind: "http",
  readOnly: "",
  method: "GET",
};

function renderBanner(node: RequestNode = health) {
  const stores = createStores();
  stores.collection.dispatch({ type: "collection/load", nodes: [node], rootIds: [node.id], workspaceId: "ws_1" });
  stores.drafts.dispatch({ type: "drafts/open", id: "health", request: { id: "health", url: "/health" } });
  render(
    <StoreProvider stores={stores}>
      <ExternalChangeBanner id="health" />
    </StoreProvider>,
  );
  return stores;
}

afterEach(() => {
  cleanup();
  vi.clearAllMocks();
});

describe("ExternalChangeBanner", () => {
  it("shows nothing without a conflict", () => {
    renderBanner();
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("offers to keep mine, take theirs or show the changes of a conflict", async () => {
    service.ResolveConflict.mockResolvedValue(undefined);
    service.UpdateRequest.mockResolvedValue(undefined);
    service.Request.mockResolvedValue({ id: "health", url: "/theirs", extra: "" });
    const stores = renderBanner();
    act(() => {
      stores.drafts.dispatch({ type: "drafts/edit", id: "health", patch: { url: "/mine" } });
      stores.drafts.dispatch({ type: "drafts/conflict", id: "health" });
    });
    expect(screen.getByRole("alert").textContent).toContain("changed outside PostSonde");

    fireEvent.click(screen.getByRole("button", { name: "Show changes" }));
    await act(settle);
    const diff = screen.getByLabelText("Differences");
    expect(diff.textContent).toContain('−   "url": "/mine"');
    expect(diff.textContent).toContain('+   "url": "/theirs"');
    fireEvent.keyDown(document.activeElement ?? document.body, { key: "Escape" });

    fireEvent.click(screen.getByRole("button", { name: "Keep mine" }));
    await act(settle);
    expect(service.ResolveConflict).toHaveBeenCalledWith("health");
    expect(service.UpdateRequest).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("says when the conflict is with another window's edit (APP-06)", async () => {
    service.Request.mockResolvedValue({ id: "health", url: "/theirs", extra: "", version: 3 });
    const stores = renderBanner();
    act(() => {
      stores.drafts.dispatch({ type: "drafts/edit", id: "health", patch: { url: "/mine" } });
      stores.drafts.dispatch({ type: "drafts/conflict", id: "health", source: "window" });
    });
    const alert = screen.getByRole("alert");
    expect(alert.textContent).toContain("changed in another PostSonde window");
    expect(alert.getAttribute("data-conflict-source")).toBe("window");

    fireEvent.click(screen.getByRole("button", { name: "Show changes" }));
    await act(settle);
    expect(screen.getByRole("dialog", { name: "Your version and the other window's" })).toBeTruthy();
    const diff = screen.getByLabelText("Differences");
    expect(diff.textContent).toContain('+   "url": "/theirs"');
    // The version is not content: it is not in the comparison.
    expect(diff.textContent).not.toContain("version");
  });

  it("takes theirs", async () => {
    service.ResolveConflict.mockResolvedValue(undefined);
    service.Request.mockResolvedValue({ id: "health", url: "/theirs", extra: "" });
    const stores = renderBanner();
    act(() => {
      stores.drafts.dispatch({ type: "drafts/edit", id: "health", patch: { url: "/mine" } });
      stores.drafts.dispatch({ type: "drafts/conflict", id: "health" });
    });
    fireEvent.click(screen.getByRole("button", { name: "Take theirs" }));
    await act(settle);
    expect(stores.drafts.getState().byId.health).toMatchObject({ dirty: false, request: { url: "/theirs" } });
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("says a request's file was deleted and closes its tab", () => {
    const stores = createStores();
    stores.collection.dispatch({ type: "collection/load", nodes: [], rootIds: [], workspaceId: "ws_1", deleted: [health] });
    stores.tabs.dispatch({ type: "tabs/open", id: "health", preview: false });
    stores.drafts.dispatch({ type: "drafts/open", id: "health", request: { id: "health" } });
    expect(stores.collection.getState().nodes.health?.readOnly).toBe(DELETED_READ_ONLY);
    render(
      <StoreProvider stores={stores}>
        <ExternalChangeBanner id="health" />
      </StoreProvider>,
    );
    expect(screen.getByRole("alert").textContent).toContain("deleted outside PostSonde");
    expect(screen.getByRole("button", { name: "Restore file" })).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Close" }));
    expect(stores.tabs.getState().order).toEqual([]);
  });
});
