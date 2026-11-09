import { describe, expect, it } from "vitest";
import {
  initialWorkspaceState,
  selectWorkspaceName,
  workspaceReducer,
  type RecentWorkspace,
  type WorkspaceAction,
  type WorkspaceInfo,
  type WorkspaceState,
} from "./workspace";

const reduce = (action: WorkspaceAction, state: WorkspaceState = initialWorkspaceState) =>
  workspaceReducer(state, action);

const shop: WorkspaceInfo = {
  id: "ws_1",
  name: "Shop API",
  path: "/p/shop",
  localDir: "/data/workspaces/ws_1",
  localStatus: "new",
  previousPath: "",
  newerFormat: false,
};

const recent: RecentWorkspace[] = [
  { id: "ws_1", name: "Shop API", path: "/p/shop", openedAt: "2026-09-16T09:00:00Z", available: true },
];

describe("workspaceReducer", () => {
  it("starts loading with nothing open", () => {
    expect(initialWorkspaceState).toEqual({ status: "loading", current: null, recent: [] });
    expect(selectWorkspaceName(initialWorkspaceState)).toBeNull();
  });

  it("becomes ready when told what is open", () => {
    const none = reduce({ type: "workspace/setCurrent", workspace: null });
    expect(none).toEqual({ status: "ready", current: null, recent: [] });
    expect(reduce({ type: "workspace/setCurrent", workspace: null }, none)).toBe(none);

    const open = reduce({ type: "workspace/setCurrent", workspace: shop }, none);
    expect(open.current).toEqual(shop);
    expect(selectWorkspaceName(open)).toBe("Shop API");
    expect(reduce({ type: "workspace/setCurrent", workspace: { ...shop } }, open)).toBe(open);
    expect(reduce({ type: "workspace/setCurrent", workspace: { ...shop, name: "Shop" } }, open).current?.name).toBe(
      "Shop",
    );
    expect(reduce({ type: "workspace/setCurrent", workspace: null }, open).current).toBeNull();
  });

  it("sets the recent list, returning the same state when unchanged", () => {
    const withRecent = reduce({ type: "workspace/setRecent", recent });
    expect(withRecent.recent).toEqual(recent);
    expect(withRecent.status).toBe("loading");
    expect(reduce({ type: "workspace/setRecent", recent: recent.map((r) => ({ ...r })) }, withRecent)).toBe(withRecent);
    expect(
      reduce({ type: "workspace/setRecent", recent: [{ ...recent[0], available: false }] }, withRecent).recent[0]
        .available,
    ).toBe(false);
    expect(reduce({ type: "workspace/setRecent", recent: [] }, withRecent).recent).toEqual([]);
  });
});
