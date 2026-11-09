import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";

const service = vi.hoisted(() => ({
  Types: vi.fn<() => Promise<unknown>>(),
  List: vi.fn<() => Promise<unknown>>(),
  Create: vi.fn<(input: unknown) => Promise<unknown>>(),
  Start: vi.fn<(id: string) => Promise<unknown>>(),
  Stop: vi.fn<(id: string) => Promise<unknown>>(),
  Remove: vi.fn<(id: string) => Promise<unknown>>(),
  Traffic: vi.fn<(id: string) => Promise<unknown>>(),
  ClearTraffic: vi.fn<(id: string) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/servers", () => ({
  ServersService: service,
}));

import { toLocalServer } from "../../commands/servers";
import { TrafficSources } from "../../lib/serverTraffic";
import { SERVERS_TRAFFIC_EVENT } from "../../lib/servers";
import { createStores, StoreProvider, type Stores } from "../../stores";
import { emit, resetFakeWails, settle } from "../../test/fakeWails";
import { runningDTO, serverDTO, trafficDTO } from "../../test/servers";
import { canCopyAddress, formatUptime, serverAddress } from "./format";
import { t } from "../../i18n";
import { ServersPanel } from "./ServersPanel";
import { ServerDetails, ServerTraffic } from "./ServerView";

function storesWith(...dtos: Parameters<typeof toLocalServer>[0][]): Stores {
  const stores = createStores();
  stores.servers.dispatch({ type: "servers/loaded", servers: dtos.map(toLocalServer), types: [] });
  return stores;
}

beforeEach(() => {
  resetFakeWails();
  for (const fn of Object.values(service)) fn.mockReset();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("format", () => {
  it("formats uptime", () => {
    expect(formatUptime(-5)).toBe("0s");
    expect(formatUptime(42_900)).toBe("42s");
    expect(formatUptime(187_000)).toBe("3m 07s");
    expect(formatUptime(2 * 3600_000 + 5 * 60_000)).toBe("2h 05m");
    expect(formatUptime(3 * 86400_000 + 4 * 3600_000)).toBe("3d 04h");
  });

  it("shows the bound address, or the defined one", () => {
    expect(serverAddress(toLocalServer(runningDTO()))).toBe("127.0.0.1:52341");
    expect(canCopyAddress(toLocalServer(runningDTO()))).toBe(true);
    expect(serverAddress(toLocalServer(serverDTO({ host: "", port: 8080 })))).toBe("localhost:8080");
    expect(serverAddress(toLocalServer(serverDTO({ host: "::1", port: 0 })))).toBe("[::1]:any port");
    expect(canCopyAddress(toLocalServer(serverDTO()))).toBe(false);
  });
});

describe("ServersPanel", () => {
  it("lists servers with status, address and a ticking uptime", () => {
    vi.useFakeTimers();
    const started = Date.parse("2026-09-16T10:00:00Z");
    vi.setSystemTime(started + 65_000);
    const stores = storesWith(runningDTO(), serverDTO({ id: "srv_2", name: "Stopped one", port: 9000 }));
    render(
      <StoreProvider stores={stores}>
        <ServersPanel label="Servers" />
      </StoreProvider>,
    );
    const list = screen.getByRole("list", { name: "Servers" });
    const rows = within(list).getAllByRole("listitem");
    expect(rows).toHaveLength(2);
    expect(within(rows[0]).getByText("Running")).toBeTruthy();
    expect(within(rows[0]).getByRole("button", { name: "Copy address 127.0.0.1:52341" })).toBeTruthy();
    expect(within(rows[0]).getByLabelText("Uptime 1m 05s")).toBeTruthy();
    expect(within(rows[1]).getByText("localhost:9000")).toBeTruthy();
    expect(within(rows[1]).queryByLabelText(/Uptime/)).toBeNull();

    act(() => {
      vi.advanceTimersByTime(3000);
    });
    expect(within(rows[0]).getByLabelText("Uptime 1m 08s")).toBeTruthy();
  });

  it("lists the open project's saved servers first, with their folder", () => {
    const stores = storesWith(
      serverDTO({ id: "srv_adhoc", name: "Ad hoc" }),
      serverDTO({ id: "srv_saved", name: "Chat server", folder: { workspaceId: "ws_1", folderId: "fld_chat", folderName: "Old name" } }),
      runningDTO({ id: "srv_other", name: "Elsewhere", folder: { workspaceId: "ws_2", folderId: "fld_x", folderName: "Mocks" } }),
    );
    stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
    stores.collection.dispatch({
      type: "collection/load",
      workspaceId: "ws_1",
      nodes: [{ id: "fld_chat", kind: "folder", parentId: null, name: "Chat", path: "chat", fileKind: "", readOnly: "", childIds: [] }],
      rootIds: ["fld_chat"],
    });
    render(
      <StoreProvider stores={stores}>
        <ServersPanel label="Servers" />
      </StoreProvider>,
    );
    const project = screen.getByRole("list", { name: "In this project" });
    expect(within(project).getAllByRole("listitem")).toHaveLength(1);
    // The folder's current name, from the tree.
    expect(within(project).getByText("Chat")).toBeTruthy();
    const session = screen.getByRole("list", { name: "This session" });
    const rows = within(session).getAllByRole("listitem");
    expect(rows).toHaveLength(2);
    expect(rows[0].textContent).toContain("Ad hoc");
    expect(rows[1].textContent).toContain("Elsewhere");
    expect(within(rows[1]).getByText("Mocks (other project)")).toBeTruthy();
  });

  it("starts and stops servers and selects one", async () => {
    const stores = storesWith(runningDTO(), serverDTO({ id: "srv_2", name: "Other" }));
    service.Stop.mockResolvedValue(serverDTO());
    service.Start.mockResolvedValue(runningDTO({ id: "srv_2", name: "Other" }));
    render(
      <StoreProvider stores={stores}>
        <ServersPanel label="Servers" />
      </StoreProvider>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Stop Echo HTTP" }));
    fireEvent.click(screen.getByRole("button", { name: "Start Other" }));
    await act(settle);
    expect(service.Stop).toHaveBeenCalledWith("srv_1");
    expect(service.Start).toHaveBeenCalledWith("srv_2");
    expect(stores.servers.getState().byId.srv_1.state).toBe("stopped");
    expect(stores.servers.getState().byId.srv_2.state).toBe("running");

    fireEvent.click(screen.getByText("Other"));
    expect(stores.servers.getState().selectedId).toBe("srv_2");
  });

  it("creates and starts an Echo server from the inline form, with the LAN warning", async () => {
    const stores = storesWith();
    service.Create.mockResolvedValue(serverDTO({ host: "0.0.0.0", port: 8080, allowLan: true }));
    service.Start.mockResolvedValue(runningDTO({ warning: { code: "listen.all_interfaces_exposed", message: "Everyone can connect." } }));
    render(
      <StoreProvider stores={stores}>
        <ServersPanel label="Servers" />
      </StoreProvider>,
    );
    fireEvent.click(screen.getByRole("button", { name: "New server" }));
    const form = screen.getByRole("form", { name: "New server" });
    fireEvent.click(within(form).getByRole("radio", { name: "Echo HTTP" }));
    fireEvent.change(within(form).getByLabelText("Host"), { target: { value: "0.0.0.0" } });
    fireEvent.change(within(form).getByLabelText("Port"), { target: { value: "8080" } });
    expect(within(form).queryByText(t("servers.lanNote"))).toBeNull();
    fireEvent.click(within(form).getByLabelText(/Allow connections from other devices/));
    expect(within(form).getByText(t("servers.lanNote"))).toBeTruthy();
    fireEvent.submit(form);
    await act(settle);

    expect(service.Create).toHaveBeenCalledWith({ type: "echo_http", name: "", host: "0.0.0.0", port: 8080, allowLan: true, tls: null, config: { mockHttp: null, webSocket: null, webhook: null } });
    expect(service.Start).toHaveBeenCalledWith("srv_1");
    expect(screen.queryByRole("form")).toBeNull();
    expect(stores.servers.getState().selectedId).toBe("srv_1");
    expect(screen.getByRole("img", { name: "Everyone can connect." })).toBeTruthy();
  });

  it("creates a mock server by default, with Go's default routes", async () => {
    const stores = storesWith();
    service.Create.mockResolvedValue(serverDTO({ type: "mock_http", typeLabel: "Mock HTTP", name: "Mock HTTP" }));
    service.Start.mockResolvedValue(runningDTO({ type: "mock_http", typeLabel: "Mock HTTP", name: "Mock HTTP" }));
    render(
      <StoreProvider stores={stores}>
        <ServersPanel label="Servers" />
      </StoreProvider>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Start a mock server" }));
    const form = screen.getByRole("form", { name: "New server" });
    expect((within(form).getByRole("radio", { name: "Mock HTTP" }) as HTMLInputElement).checked).toBe(true);
    expect(within(form).getByText(/Answers with the routes you define/)).toBeTruthy();
    fireEvent.submit(form);
    await act(settle);
    expect(service.Create).toHaveBeenCalledWith({ type: "mock_http", name: "", host: "localhost", port: 0, allowLan: false, tls: null, config: { mockHttp: null, webSocket: null, webhook: null } });
    expect(stores.servers.getState().selectedId).toBe("srv_1");
  });

  it("offers a WebSocket server in the form", async () => {
    const stores = storesWith();
    const ws = runningDTO({ type: "websocket", typeLabel: "WebSocket", name: "WebSocket" });
    service.Create.mockResolvedValue(ws);
    service.Start.mockResolvedValue(ws);
    render(
      <StoreProvider stores={stores}>
        <ServersPanel label="Servers" />
      </StoreProvider>,
    );
    fireEvent.click(screen.getByRole("button", { name: "a WebSocket server" }));
    const form = screen.getByRole("form", { name: "New server" });
    expect((within(form).getByRole("radio", { name: "WebSocket" }) as HTMLInputElement).checked).toBe(true);
    expect(within(form).getByText(/Accepts WebSocket clients/)).toBeTruthy();
    fireEvent.submit(form);
    await act(settle);
    expect(service.Create).toHaveBeenCalledWith({ type: "websocket", name: "", host: "localhost", port: 0, allowLan: false, tls: null, config: { mockHttp: null, webSocket: null, webhook: null } });
  });

  it("keeps the form open with the error when the server cannot be created", async () => {
    const stores = storesWith();
    const err = new Error("bad");
    (err as Error & { cause: unknown }).cause = {
      kind: "invalid_argument",
      code: "listen.invalid_host",
      message: '"example.com" is not an IP address or localhost.',
      hint: "Use 127.0.0.1.",
      details: {},
      causes: [],
    };
    service.Create.mockRejectedValue(err);
    render(
      <StoreProvider stores={stores}>
        <ServersPanel label="Servers" />
      </StoreProvider>,
    );
    fireEvent.click(screen.getByRole("button", { name: "New server" }));
    const form = screen.getByRole("form", { name: "New server" });
    fireEvent.click(within(form).getByRole("radio", { name: "Echo HTTP" }));
    fireEvent.change(within(form).getByLabelText("Port"), { target: { value: "99999" } });
    fireEvent.submit(form);
    expect(within(form).getByRole("alert").textContent).toMatch(/port must be a number/);
    expect(service.Create).not.toHaveBeenCalled();

    fireEvent.change(within(form).getByLabelText("Port"), { target: { value: "" } });
    fireEvent.change(within(form).getByLabelText("Host"), { target: { value: "example.com" } });
    fireEvent.submit(form);
    await act(settle);
    expect(within(form).getByRole("alert").textContent).toBe('"example.com" is not an IP address or localhost. Use 127.0.0.1.');
  });
});

describe("server view", () => {
  it("shows the failure of a server that did not start", () => {
    const stores = storesWith(
      serverDTO({
        state: "failed",
        port: 8080,
        error: { kind: "network", code: "listen.address_in_use", message: "127.0.0.1:8080 is already in use.", hint: "Pick another port.", details: {}, causes: [] },
      }),
    );
    const server = stores.servers.getState().byId.srv_1;
    render(
      <StoreProvider stores={stores}>
        <ServerDetails server={server} />
      </StoreProvider>,
    );
    const alert = screen.getByRole("alert");
    expect(alert.textContent).toContain("127.0.0.1:8080 is already in use.");
    expect(alert.textContent).toContain("Pick another port.");
    expect(screen.getByRole("button", { name: "Start" })).toBeTruthy();
  });

  it("shows traffic events in the selected server's log only", async () => {
    const sources = new TrafficSources({ schedule: () => () => {} });
    const stores = storesWith(runningDTO(), runningDTO({ id: "srv_2", name: "Two" }));
    service.List.mockResolvedValue([runningDTO(), runningDTO({ id: "srv_2", name: "Two" })]);
    service.Types.mockResolvedValue([]);
    service.Traffic.mockResolvedValue([]);
    render(
      <StoreProvider stores={stores}>
        <ServerTraffic serverId="srv_2" sources={sources} />
      </StoreProvider>,
    );
    const { followServers } = await import("../../commands/servers");
    const stop = followServers(stores, sources);
    await act(settle);
    act(() => {
      emit(SERVERS_TRAFFIC_EVENT, {
        events: [trafficDTO({ summary: "GET /for-one" }), trafficDTO({ serverId: "srv_2", summary: "GET /for-two" })],
      });
      sources.get("srv_1").flush();
      sources.get("srv_2").flush();
    });
    const grid = screen.getByRole("grid", { name: "Traffic of Two" });
    expect(within(grid).getByText("GET /for-two")).toBeTruthy();
    expect(within(grid).queryByText("GET /for-one")).toBeNull();

    service.ClearTraffic.mockResolvedValue(undefined);
    fireEvent.click(screen.getByRole("button", { name: "Clear" }));
    expect(service.ClearTraffic).toHaveBeenCalledWith("srv_2");
    stop();
  });
});
