import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { EditorView } from "@codemirror/view";

const service = vi.hoisted(() => ({
  Types: vi.fn<() => Promise<unknown>>(),
  List: vi.fn<() => Promise<unknown>>(),
  Traffic: vi.fn<(id: string) => Promise<unknown>>(),
  Clients: vi.fn<(id: string) => Promise<unknown>>(),
  Connections: vi.fn<(id: string) => Promise<unknown>>(),
  SendToConnection: vi.fn<(input: unknown) => Promise<unknown>>(),
  CloseConnection: vi.fn<(input: unknown) => Promise<unknown>>(),
  ClearTraffic: vi.fn<(id: string) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/servers", () => ({
  ServersService: service,
}));

import { followServers, toLocalServer, toTcpConnection } from "../../commands/servers";
import { netcatCommand, tcpServerAddress } from "../../commands/tcpServer";
import { IS_MAC } from "../../lib/shortcuts/keys";
import { SERVERS_CONNECTIONS_EVENT, type ServerDTO } from "../../lib/servers";
import { TrafficSources } from "../../lib/serverTraffic";
import { createStores, StoreProvider, type Stores } from "../../stores";
import { emit, resetFakeWails, settle } from "../../test/fakeWails";
import { runningDTO, tcpConnectionDTO, tcpServerDTO, trafficDTO } from "../../test/servers";
import { ServerDetails, ServerTraffic } from "./ServerView";

// jsdom has no layout: CodeMirror measures ranges for decorations.
for (const name of ["getClientRects", "getBoundingClientRect"] as const) {
  if (typeof Range.prototype[name] !== "function") {
    Object.defineProperty(Range.prototype, name, {
      configurable: true,
      value:
        name === "getClientRects" ? () => [] : () => ({ left: 0, right: 0, top: 0, bottom: 0, width: 0, height: 0 }),
    });
  }
}

const modEnter = { key: "Enter", ...(IS_MAC ? { metaKey: true } : { ctrlKey: true }) };

function storesWith(dto: ServerDTO): Stores {
  const stores = createStores();
  stores.servers.dispatch({ type: "servers/loaded", servers: [toLocalServer(dto)], types: [] });
  return stores;
}

function renderPane(stores: Stores) {
  const server = stores.servers.getState().byId.srv_1;
  render(
    <StoreProvider stores={stores}>
      <ServerDetails server={server} />
    </StoreProvider>,
  );
}

/** Replaces the text of the composer's CodeMirror editor. */
function typeMessage(text: string) {
  const content = screen.getByRole("textbox", { name: "Message" });
  const view = EditorView.findFromDOM(content.closest(".cm-editor") as HTMLElement);
  if (!view) throw new Error("no editor view");
  act(() => view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text } }));
}

function appError(code: string, message: string): Error {
  const err = new Error(message);
  (err as Error & { cause: unknown }).cause = { kind: "not_found", code, message, hint: "", details: {}, causes: [] };
  return err;
}

beforeEach(() => {
  resetFakeWails();
  for (const fn of Object.values(service)) fn.mockReset();
});

describe("TCP server view", () => {
  it("gives the address and a netcat command", () => {
    const address = tcpServerAddress(toLocalServer(tcpServerDTO()));
    expect(address).toBe("127.0.0.1:52341");
    expect(netcatCommand("127.0.0.1:52341")).toBe("nc 127.0.0.1 52341");
    expect(netcatCommand("[::1]:7000")).toBe("nc ::1 7000");
    expect(netcatCommand("127.0.0.1:52341", true)).toBe("openssl s_client -connect 127.0.0.1:52341");
    expect(tcpServerAddress(toLocalServer(tcpServerDTO({ state: "stopped", addresses: [] })))).toBeNull();
  });

  it("lists connections from servers:connections and loads them for running TCP servers", async () => {
    const stores = createStores();
    service.List.mockResolvedValue([tcpServerDTO(), runningDTO({ id: "srv_2" })]);
    service.Types.mockResolvedValue([]);
    service.Traffic.mockResolvedValue([]);
    service.Connections.mockResolvedValue([tcpConnectionDTO()]);
    const stop = followServers(stores, new TrafficSources({ schedule: () => () => {} }));
    await settle();
    expect(service.Connections).toHaveBeenCalledTimes(1);
    expect(service.Connections).toHaveBeenCalledWith("srv_1");
    expect(service.Clients).not.toHaveBeenCalled();
    expect(stores.servers.getState().tcpConnections.srv_1).toEqual([toTcpConnection(tcpConnectionDTO())]);

    emit(SERVERS_CONNECTIONS_EVENT, {
      serverId: "srv_1",
      connections: [tcpConnectionDTO(), tcpConnectionDTO({ id: "2", connId: 2, peer: "127.0.0.1:60002" })],
    });
    expect(stores.servers.getState().tcpConnections.srv_1.map((c) => c.id)).toEqual(["1", "2"]);
    emit(SERVERS_CONNECTIONS_EVENT, { serverId: "srv_1", connections: null });
    expect(stores.servers.getState().tcpConnections.srv_1).toEqual([]);

    // A stopped server has no connections.
    emit(SERVERS_CONNECTIONS_EVENT, { serverId: "srv_1", connections: [tcpConnectionDTO()] });
    stores.servers.dispatch({ type: "servers/set", servers: [toLocalServer(tcpServerDTO({ state: "stopped", addresses: [] }))] });
    expect(stores.servers.getState().tcpConnections.srv_1).toBeUndefined();
    stop();
  });

  it("shows the connections, sends text and hex to the selected one, and closes one", async () => {
    const stores = storesWith(tcpServerDTO());
    stores.servers.dispatch({
      type: "servers/tcpConnections",
      id: "srv_1",
      connections: [tcpConnectionDTO(), tcpConnectionDTO({ id: "2", connId: 2, peer: "127.0.0.1:60002" })].map(toTcpConnection),
    });
    renderPane(stores);

    expect(screen.getByRole("tablist", { name: "TCP server" })).toBeTruthy();
    expect(screen.getByRole("tab", { name: "Connections2" }).getAttribute("aria-selected")).toBe("true");
    expect(screen.getByText("nc 127.0.0.1 52341")).toBeTruthy();
    const rows = within(screen.getByRole("list", { name: "Connections" })).getAllByRole("listitem");
    expect(rows).toHaveLength(2);
    expect(within(rows[0]).getByText("↓12 B ↑4 B")).toBeTruthy();

    // The first connection is the target until another one is selected.
    expect(screen.getByRole("button", { name: "Send to connection 1" }).hasAttribute("disabled")).toBe(true); // no text yet
    fireEvent.click(within(rows[1]).getByRole("button", { name: "Connection 2, 127.0.0.1:60002" }));
    expect(stores.servers.getState().tcpSelectedConnection.srv_1).toBe("2");

    // Text with a line ending.
    typeMessage("hello");
    fireEvent.change(screen.getByLabelText("Line ending"), { target: { value: "crlf" } });
    expect(stores.servers.getState().tcpComposer.srv_1).toEqual({ format: "text", message: "hello", lineEnding: "crlf" });
    service.SendToConnection.mockResolvedValue({ bytes: 7 });
    fireEvent.click(screen.getByRole("button", { name: "Send to connection 2" }));
    await act(settle);
    expect(service.SendToConnection).toHaveBeenCalledWith({
      serverId: "srv_1",
      connectionId: "2",
      mode: "text",
      text: "hello",
      lineEnding: "crlf",
    });
    expect(screen.getByRole("status").textContent).toBe("Sent 7 bytes to connection 2.");

    // Hex: checked while typing, sent with ⌘Enter; the line ending does not apply.
    fireEvent.click(screen.getByRole("button", { name: "Hex" }));
    expect((screen.getByLabelText("Line ending") as HTMLSelectElement).disabled).toBe(true);
    typeMessage("DE AD B");
    expect(screen.getByText(/has no partner/)).toBeTruthy();
    expect(screen.getByRole("button", { name: "Send to connection 2" }).hasAttribute("disabled")).toBe(true);
    typeMessage("DE AD BE EF");
    service.SendToConnection.mockResolvedValue({ bytes: 4 });
    fireEvent.keyDown(screen.getByRole("textbox", { name: "Message" }), modEnter);
    await act(settle);
    expect(service.SendToConnection).toHaveBeenLastCalledWith({
      serverId: "srv_1",
      connectionId: "2",
      mode: "hex",
      text: "DE AD BE EF",
      lineEnding: "crlf",
    });

    // A failed send is shown in the composer.
    service.SendToConnection.mockRejectedValue(appError("tcpserver.connection_not_found", "This connection is closed."));
    fireEvent.click(screen.getByRole("button", { name: "Send to connection 2" }));
    await act(settle);
    expect(screen.getByText("This connection is closed.")).toBeTruthy();

    // Close a connection; a refusal is shown on its row.
    service.CloseConnection.mockResolvedValue(undefined);
    fireEvent.click(within(rows[0]).getByRole("button", { name: "Close" }));
    await act(settle);
    expect(service.CloseConnection).toHaveBeenCalledWith({ serverId: "srv_1", connectionId: "1" });
    service.CloseConnection.mockRejectedValue(appError("tcpserver.connection_not_found", "This connection is closed."));
    fireEvent.click(within(rows[1]).getByRole("button", { name: "Close" }));
    await act(settle);
    expect(within(rows[1]).getByRole("alert").textContent).toBe("This connection is closed.");
  });

  it("disables sending without connections and says how to connect", () => {
    const stores = storesWith(tcpServerDTO({ state: "stopped", addresses: [], boundPort: 0 }));
    renderPane(stores);
    expect(screen.getByText("Start the server to accept TCP connections.")).toBeTruthy();
    expect(screen.getByText("No open connections")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Send to connection" }).hasAttribute("disabled")).toBe(true);
  });

  it("shows the traffic as text or hex", async () => {
    const sources = new TrafficSources({ schedule: () => () => {} });
    const stores = storesWith(tcpServerDTO());
    render(
      <StoreProvider stores={stores}>
        <ServerTraffic serverId="srv_1" sources={sources} />
      </StoreProvider>,
    );
    const received = (text: string, seq: number) =>
      trafficDTO({ seq, kind: "message", summary: "", text, size: text.length, http: null, tcp: { initiatedBy: "", bytesIn: 0, bytesOut: 0 } });
    act(() => {
      sources.append([received("hi\n", 1)]);
      sources.get("srv_1").flush();
    });
    const grid = () => screen.getByRole("grid", { name: "Traffic of TCP" });
    expect(within(grid()).getByText("hi")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Hex" }));
    expect(stores.servers.getState().tcpView.srv_1).toBe("hex");
    act(() => sources.hex("srv_1").flush());
    expect(within(grid()).getByText("68 69 0a")).toBeTruthy();

    // Later traffic reaches both views.
    act(() => {
      sources.append([received("ok", 2)]);
      sources.get("srv_1").flush();
      sources.hex("srv_1").flush();
    });
    expect(within(grid()).getByText("6f 6b")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Text" }));
    expect(within(grid()).getByText("ok")).toBeTruthy();
  });
});
