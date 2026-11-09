import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { EditorView } from "@codemirror/view";

const service = vi.hoisted(() => ({
  Types: vi.fn<() => Promise<unknown>>(),
  List: vi.fn<() => Promise<unknown>>(),
  Traffic: vi.fn<(id: string) => Promise<unknown>>(),
  Subscribers: vi.fn<(id: string) => Promise<unknown>>(),
  Peers: vi.fn<(id: string) => Promise<unknown>>(),
  UpdateConfig: vi.fn<(id: string, config: unknown) => Promise<unknown>>(),
  SendDatagram: vi.fn<(input: unknown) => Promise<unknown>>(),
  ClearTraffic: vi.fn<(id: string) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/servers", () => ({
  ServersService: service,
}));

import { followServers, toLocalServer, toUdpPeer } from "../../commands/servers";
import { netcatUdpCommand } from "../../commands/udpServer";
import { SERVERS_PEERS_EVENT, type ServerDTO } from "../../lib/servers";
import { TrafficSources, trafficToMessage } from "../../lib/serverTraffic";
import { createStores, StoreProvider, type Stores } from "../../stores";
import { emit, resetFakeWails, settle } from "../../test/fakeWails";
import { runningDTO, trafficDTO, udpPeerDTO, udpServerDTO } from "../../test/servers";
import { ServerDetails, ServerTraffic } from "./ServerView";
import { UDP_SETTINGS_DELAY_MS } from "./UdpServerPane";

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

function typeMessage(text: string) {
  const content = screen.getByRole("textbox", { name: "Message" });
  const view = EditorView.findFromDOM(content.closest(".cm-editor") as HTMLElement);
  if (!view) throw new Error("no editor view");
  act(() => view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text } }));
}

beforeEach(() => {
  resetFakeWails();
  for (const fn of Object.values(service)) fn.mockReset();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("UDP server view", () => {
  it("gives a netcat command in UDP mode", () => {
    expect(netcatUdpCommand("127.0.0.1:9125")).toBe("nc -u 127.0.0.1 9125");
    expect(netcatUdpCommand("[::1]:9125")).toBe("nc -u ::1 9125");
  });

  it("lists peers from servers:peers and loads them for running UDP servers", async () => {
    const stores = createStores();
    service.List.mockResolvedValue([udpServerDTO(), runningDTO({ id: "srv_2" })]);
    service.Types.mockResolvedValue([]);
    service.Traffic.mockResolvedValue([]);
    service.Peers.mockResolvedValue([udpPeerDTO()]);
    const stop = followServers(stores, new TrafficSources({ schedule: () => () => {} }));
    await settle();
    expect(service.Peers).toHaveBeenCalledWith("srv_1");
    expect(service.Subscribers).not.toHaveBeenCalled();
    expect(stores.servers.getState().udpPeers.srv_1).toEqual([toUdpPeer(udpPeerDTO())]);
    emit(SERVERS_PEERS_EVENT, { serverId: "srv_1", peers: [udpPeerDTO({ addr: "127.0.0.1:60002" }), udpPeerDTO()] });
    expect(stores.servers.getState().udpPeers.srv_1.map((p) => p.addr)).toEqual(["127.0.0.1:60002", "127.0.0.1:60001"]);
    stop();
  });

  it("shows the peers and sends text or hex to the selected one", async () => {
    const stores = storesWith(udpServerDTO());
    stores.servers.dispatch({
      type: "servers/udpPeers",
      id: "srv_1",
      peers: [udpPeerDTO(), udpPeerDTO({ addr: "127.0.0.1:60002" })].map(toUdpPeer),
    });
    renderPane(stores);
    expect(screen.getByRole("tablist", { name: "UDP server" })).toBeTruthy();
    expect(screen.getByText("nc -u 127.0.0.1 52341")).toBeTruthy();
    const rows = within(screen.getByRole("list", { name: "Peers" })).getAllByRole("listitem");
    expect(rows).toHaveLength(2);
    expect(within(rows[0]).getByText("↓2 ↑2")).toBeTruthy();

    fireEvent.click(within(rows[1]).getByRole("button", { name: "Peer 127.0.0.1:60002" }));
    typeMessage("ping");
    fireEvent.change(screen.getByLabelText("Line ending"), { target: { value: "lf" } });
    service.SendDatagram.mockResolvedValue({ bytes: 5 });
    fireEvent.click(screen.getByRole("button", { name: "Send to 127.0.0.1:60002" }));
    await act(settle);
    expect(service.SendDatagram).toHaveBeenCalledWith({ serverId: "srv_1", peer: "127.0.0.1:60002", mode: "text", text: "ping", lineEnding: "lf" });
    expect(screen.getByRole("status").textContent).toBe("Sent a datagram of 5 bytes to 127.0.0.1:60002.");

    fireEvent.click(screen.getByRole("button", { name: "Hex" }));
    typeMessage("06");
    service.SendDatagram.mockResolvedValue({ bytes: 1 });
    fireEvent.click(screen.getByRole("button", { name: "Send to 127.0.0.1:60002" }));
    await act(settle);
    expect(service.SendDatagram).toHaveBeenLastCalledWith(expect.objectContaining({ mode: "hex", text: "06" }));
    expect(screen.getByRole("status").textContent).toBe("Sent a datagram of 1 byte to 127.0.0.1:60002.");
  });

  it("has no peers before a datagram and no TLS", () => {
    const stores = storesWith(udpServerDTO());
    renderPane(stores);
    expect(screen.getByText("No peers yet")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Send to peer" }).hasAttribute("disabled")).toBe(true);
    fireEvent.click(screen.getByRole("tab", { name: "Server" }));
    expect(screen.getByText("Not for UDP")).toBeTruthy();
    expect(screen.queryByText(/Serve over TLS/)).toBeNull();
  });

  it("sends reply edits once typing pauses and checks hex first", async () => {
    vi.useFakeTimers();
    const stores = storesWith(udpServerDTO());
    renderPane(stores);
    fireEvent.click(screen.getByRole("tab", { name: "Replies" }));
    service.UpdateConfig.mockImplementation((_id, config) => Promise.resolve(udpServerDTO({ config: config as ServerDTO["config"] })));
    fireEvent.click(screen.getByLabelText("Send a fixed reply to the sender of every datagram"));
    fireEvent.change(screen.getByLabelText("Reply as"), { target: { value: "hex" } });
    fireEvent.change(screen.getByLabelText("Fixed reply"), { target: { value: "DE AD B" } });
    await act(async () => {
      vi.advanceTimersByTime(UDP_SETTINGS_DELAY_MS + 10);
    });
    expect(service.UpdateConfig).not.toHaveBeenCalled();
    expect(screen.getByRole("alert").textContent).toMatch(/not valid hex/);

    fireEvent.change(screen.getByLabelText("Fixed reply"), { target: { value: "DE AD BE EF" } });
    await act(async () => {
      vi.advanceTimersByTime(UDP_SETTINGS_DELAY_MS + 10);
    });
    expect(service.UpdateConfig).toHaveBeenCalledWith("srv_1", {
      mockHttp: null,
      webSocket: null,
      webhook: null,
      udp: { echo: true, fixedReply: true, reply: "DE AD BE EF", replyFormat: "hex" },
    });
  });

  it("shows datagrams as text or hex, with the reply kind", () => {
    const sources = new TrafficSources({ schedule: () => () => {} });
    const stores = storesWith(udpServerDTO());
    render(
      <StoreProvider stores={stores}>
        <ServerTraffic serverId="srv_1" sources={sources} />
      </StoreProvider>,
    );
    const echo = trafficDTO({
      direction: "out",
      kind: "message",
      connId: 0,
      summary: "2 bytes to 127.0.0.1:60000 (echo)",
      text: "hi",
      size: 2,
      http: null,
      udp: { local: "127.0.0.1:52341", reply: "echo" },
    });
    expect(trafficToMessage(echo).meta).toEqual({
      summary: "2 bytes to 127.0.0.1:60000 (echo)",
      peer: "127.0.0.1:60000",
      local: "127.0.0.1:52341",
      reply: "echo",
    });
    act(() => {
      sources.append([echo]);
      sources.get("srv_1").flush();
    });
    fireEvent.click(screen.getByRole("button", { name: "Hex" }));
    act(() => sources.hex("srv_1").flush());
    expect(within(screen.getByRole("grid", { name: "Traffic of UDP" })).getByText("68 69")).toBeTruthy();
  });
});
