import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen } from "@testing-library/react";

const workspaceService = vi.hoisted(() => ({
  Current: vi.fn<() => Promise<unknown>>(),
  Recent: vi.fn<() => Promise<unknown>>(),
  Close: vi.fn<() => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("./test/fakeWails")).fakeRuntime);
vi.mock("../bindings/github.com/sorrelfox/postsonde/internal/app/services/workspace", () => ({
  WorkspaceService: workspaceService,
}));
vi.mock("../bindings/github.com/sorrelfox/postsonde/internal/app/services/environments", () => ({
  EnvironmentsService: {
    List: () => Promise.resolve({ environments: [], activeId: "", problems: [], readOnly: false }),
  },
}));
// The console button follows the app log (DBG-06); no Go backend under jsdom.
vi.mock("../bindings/github.com/sorrelfox/postsonde/internal/app/services/console", () => ({
  ConsoleService: {
    List: () => Promise.resolve({ entries: [], capacity: 2000 }),
    Clear: () => Promise.resolve(),
  },
}));
// The network activity indicator follows the connection log (PRV-06).
vi.mock("../bindings/github.com/sorrelfox/postsonde/internal/app/services/network", () => ({
  NetworkService: {
    List: () => Promise.resolve({ conns: [], active: 0, capacity: 500 }),
    Clear: () => Promise.resolve(),
  },
}));
vi.mock("../bindings/github.com/sorrelfox/postsonde/internal/app/services/servers", () => ({
  ServersService: {
    List: () => Promise.resolve([]),
    Types: () => Promise.resolve([{ name: "echo_http", label: "Echo HTTP" }]),
  },
}));

// SystemDemo calls the Go bindings on mount; under jsdom there is no Wails
// backend, so those calls reject after the test ends and fail the run. The
// demo has its own tests; here a stub with the same landmark is enough.
vi.mock("./components/SystemDemo", () => ({
  default: () => <section aria-label="Go bindings demo" />,
}));
import App from "./App";
import { THEME_STORAGE_KEY } from "./styles/theme";
import { resetFakeWails } from "./test/fakeWails";

const shop = {
  id: "ws_1",
  name: "Shop API",
  path: "/Users/zoe/shop-api",
  localDir: "/data/workspaces/ws_1",
  localStatus: "existing",
  previousPath: "",
  newerFormat: false,
  openedAt: "2026-09-16T09:00:00Z",
};

describe("App", () => {
  beforeEach(() => {
    localStorage.clear();
    delete document.documentElement.dataset.theme;
    resetFakeWails();
    workspaceService.Current.mockReset().mockResolvedValue({ workspace: shop });
    workspaceService.Recent.mockReset().mockResolvedValue([]);
    workspaceService.Close.mockReset().mockResolvedValue(undefined);
  });

  it("renders the app shell for the open project", async () => {
    render(<App />);
    expect(await screen.findByRole("navigation", { name: "Sidebar views" })).toBeTruthy();
    expect(screen.getByText("PostSonde")).toBeTruthy();
    expect(screen.getByText("Shop API").closest("[title]")?.getAttribute("title")).toBe(shop.path);
    // The response pane holds the bindings demo; the request pane lazy-loads
    // the editor preview (see App.tsx).
    expect(screen.getByRole("region", { name: "Go bindings demo" })).toBeTruthy();
  });

  it("shows the welcome screen when no project is open, and after closing one", async () => {
    render(<App />);
    fireEvent.click(await screen.findByRole("button", { name: "Close project" }));
    expect(await screen.findByRole("button", { name: /Open folder/ })).toBeTruthy();
    expect(workspaceService.Close).toHaveBeenCalledTimes(1);
    expect(screen.queryByRole("navigation", { name: "Sidebar views" })).toBeNull();
  });

  it("shows the welcome screen when Go cannot be asked", async () => {
    workspaceService.Current.mockRejectedValue(new Error("no backend"));
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    render(<App />);
    expect(await screen.findByRole("button", { name: /Create project/ })).toBeTruthy();
    warn.mockRestore();
  });

  it("starts from the cached theme preference", async () => {
    localStorage.setItem(THEME_STORAGE_KEY, "dark");
    render(<App />);
    expect(document.documentElement.dataset.theme).toBe("dark");
    expect(await screen.findByRole("button", { name: /^Theme: Dark/ })).toBeTruthy();
    await act(async () => {});
  });
});
