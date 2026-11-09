import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { EditorView } from "@codemirror/view";

const service = vi.hoisted(() => ({
  Types: vi.fn<() => Promise<unknown>>(),
  List: vi.fn<() => Promise<unknown>>(),
  Traffic: vi.fn<(id: string) => Promise<unknown>>(),
  Sockets: vi.fn<(id: string) => Promise<unknown>>(),
  Subscribers: vi.fn<(id: string) => Promise<unknown>>(),
  UpdateConfig: vi.fn<(id: string, config: unknown) => Promise<unknown>>(),
  EmitToSockets: vi.fn<(input: unknown) => Promise<unknown>>(),
  DisconnectSocket: vi.fn<(input: unknown) => Promise<unknown>>(),
  SetSocketRooms: vi.fn<(input: unknown) => Promise<unknown>>(),
}));
const format = vi.hoisted(() => ({
  ValidateJSON: vi.fn<(text: string) => unknown>(),
  BeautifyJSON: vi.fn<(text: string, indent: number) => unknown>(),
  MinifyJSON: vi.fn<(text: string) => unknown>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/servers", () => ({
  ServersService: service,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/format", () => ({
  FormatService: format,
}));

import { followServers, toLocalServer, toSioSocket } from "../../commands/servers";
import { ioSnippet, sioServerUrl } from "../../commands/sioServer";
import { SERVERS_SOCKETS_EVENT, type ServerDTO } from "../../lib/servers";
import { TrafficSources, trafficToMessage } from "../../lib/serverTraffic";
import { createStores, serverKindBadge, StoreProvider, type Stores } from "../../stores";
import { emit, resetFakeWails, settle } from "../../test/fakeWails";
import { runningDTO, sioServerDTO, sioSocketDTO, trafficDTO } from "../../test/servers";
import { ServerDetails } from "./ServerView";
import { SIO_SETTINGS_DELAY_MS } from "./SioServerPane";

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
  function View() {
    return <ServerDetails server={stores.servers.getState().byId.srv_1} />;
  }
  const r = render(
    <StoreProvider stores={stores}>
      <View />
    </StoreProvider>,
  );
  stores.servers.subscribe(() =>
    r.rerender(
      <StoreProvider stores={stores}>
        <View />
      </StoreProvider>,
    ),
  );
}

/** Replaces the text of the composer's CodeMirror editor. */
function typeArgs(text: string) {
  const content = screen.getByRole("textbox", { name: "Event arguments" });
  const view = EditorView.findFromDOM(content.closest(".cm-editor") as HTMLElement);
  if (!view) throw new Error("no editor view");
  act(() => view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text } }));
}

function appError(code: string, message: string, details: Record<string, string> = {}): Error {
  const err = new Error(message);
  (err as Error & { cause: unknown }).cause = { kind: "invalid_argument", code, message, hint: "", details, causes: [] };
  return err;
}

beforeEach(() => {
  resetFakeWails();
  for (const fn of [...Object.values(service), ...Object.values(format)]) fn.mockReset();
  format.ValidateJSON.mockImplementation(() => Object.assign(Promise.resolve({ valid: true, error: null }), { cancel: vi.fn() }));
});

afterEach(() => {
  vi.useRealTimers();
});

describe("Socket.IO server view", () => {
  it("builds the URL, the io() line and the tree badge", () => {
    const server = toLocalServer(sioServerDTO());
    expect(sioServerUrl(server)).toBe("http://127.0.0.1:52341");
    expect(sioServerUrl(toLocalServer(sioServerDTO({ state: "stopped", addresses: [] })))).toBeNull();
    expect(ioSnippet("http://127.0.0.1:1", "/socket.io/")).toBe('io("http://127.0.0.1:1").onAny((event, ...args) => console.log(event, args));');
    expect(ioSnippet("http://127.0.0.1:1", "/io/")).toBe('io("http://127.0.0.1:1", { path: "/io/" }).onAny((event, ...args) => console.log(event, args));');
    expect(server.socketIo?.rules[0]).toEqual({ enabled: true, match: "exact", pattern: "ping", ack: '["pong"]', emit: "", emitArgs: "", delayMs: 0 });
    expect(serverKindBadge("socketio")).toBe("SIO");
  });

  it("lists sockets from servers:sockets and loads them for running Socket.IO servers", async () => {
    const stores = createStores();
    service.List.mockResolvedValue([sioServerDTO(), runningDTO({ id: "srv_2" })]);
    service.Types.mockResolvedValue([]);
    service.Traffic.mockResolvedValue([]);
    service.Sockets.mockResolvedValue([sioSocketDTO()]);
    const stop = followServers(stores, new TrafficSources({ schedule: () => () => {} }));
    await settle();
    expect(service.Sockets).toHaveBeenCalledTimes(1);
    expect(service.Sockets).toHaveBeenCalledWith("srv_1");
    expect(stores.servers.getState().sioSockets.srv_1).toEqual([toSioSocket(sioSocketDTO())]);

    emit(SERVERS_SOCKETS_EVENT, { serverId: "srv_1", sockets: [sioSocketDTO(), sioSocketDTO({ id: "1/admin", namespace: "/admin", rooms: ["lobby"] })] });
    expect(stores.servers.getState().sioSockets.srv_1.map((s) => s.rooms)).toEqual([[], ["lobby"]]);
    emit(SERVERS_SOCKETS_EVENT, { serverId: "srv_1", sockets: null });
    expect(stores.servers.getState().sioSockets.srv_1).toEqual([]);
    stop();
  });

  it("shows sockets, emits to one, to all and to a room, sets rooms and disconnects", async () => {
    const stores = storesWith(sioServerDTO());
    stores.servers.dispatch({
      type: "servers/sioSockets",
      id: "srv_1",
      sockets: [sioSocketDTO(), sioSocketDTO({ id: "1/admin", namespace: "/admin", auth: '{"token":"abc"}' })].map(toSioSocket),
    });
    renderPane(stores);

    expect(screen.getByRole("tablist", { name: "Socket.IO server" })).toBeTruthy();
    expect(screen.getByRole("tab", { name: "Sockets2" }).getAttribute("aria-selected")).toBe("true");
    expect(screen.getByText("http://127.0.0.1:52341")).toBeTruthy();
    const rows = within(screen.getByRole("list", { name: "Sockets" })).getAllByRole("listitem");
    expect(rows).toHaveLength(2);
    expect(within(rows[1]).getByText("Client 1 on /admin")).toBeTruthy();
    expect(within(rows[1]).getByText(/auth \{"token":"abc"\}/)).toBeTruthy();
    expect(within(rows[0]).getByText("↓2 ↑1")).toBeTruthy();

    // An event with an ack, to the first socket (the default target).
    fireEvent.change(screen.getByLabelText("Event"), { target: { value: " news " } });
    fireEvent.click(screen.getByLabelText("Ask for an ack"));
    typeArgs('["hi", {"$base64": "AAE="}]');
    service.EmitToSockets.mockResolvedValue({ sent: 1, failed: 0 });
    fireEvent.click(screen.getByRole("button", { name: "Emit to socket 1" }));
    await act(settle);
    expect(service.EmitToSockets).toHaveBeenCalledWith({
      serverId: "srv_1",
      socketId: "1",
      room: "",
      event: "news",
      args: '["hi", {"$base64": "AAE="}]',
      ack: true,
    });
    expect(screen.getByRole("status").textContent).toBe("Emitted to 1 socket.");

    // To all, and to a room once one is typed.
    expect(screen.getByRole("button", { name: "Emit to room" }).hasAttribute("disabled")).toBe(true);
    service.EmitToSockets.mockResolvedValue({ sent: 2, failed: 0 });
    fireEvent.click(screen.getByRole("button", { name: "Emit to all" }));
    await act(settle);
    expect(service.EmitToSockets).toHaveBeenLastCalledWith(expect.objectContaining({ socketId: "", room: "" }));
    expect(screen.getByRole("status").textContent).toBe("Emitted to 2 sockets.");
    fireEvent.change(screen.getByLabelText("Room"), { target: { value: "lobby" } });
    fireEvent.click(screen.getByRole("button", { name: "Emit to room lobby" }));
    await act(settle);
    expect(service.EmitToSockets).toHaveBeenLastCalledWith(expect.objectContaining({ socketId: "", room: "lobby" }));

    // Go's refusal shows in the composer.
    service.EmitToSockets.mockRejectedValue(appError("sioserver.invalid_event", 'The event name "connect" is reserved by Socket.IO.'));
    fireEvent.click(screen.getByRole("button", { name: "Emit to all" }));
    await act(settle);
    expect(screen.getByText('The event name "connect" is reserved by Socket.IO.')).toBeTruthy();

    // The selected socket becomes the target.
    fireEvent.click(within(rows[1]).getByRole("button", { name: "Socket 1 /admin, 127.0.0.1:60001" }));
    expect(stores.servers.getState().sioSelected.srv_1).toBe("1/admin");
    expect(screen.getByRole("button", { name: "Emit to socket 1 /admin" })).toBeTruthy();

    // Rooms.
    service.SetSocketRooms.mockResolvedValue(undefined);
    fireEvent.click(within(rows[0]).getByRole("button", { name: "Rooms…" }));
    const form = screen.getByRole("form", { name: "Rooms of socket 1" });
    fireEvent.change(within(form).getByLabelText("Rooms"), { target: { value: "lobby, vip, " } });
    fireEvent.submit(form);
    await act(settle);
    expect(service.SetSocketRooms).toHaveBeenCalledWith({ serverId: "srv_1", socketId: "1", rooms: ["lobby", "vip"] });

    service.DisconnectSocket.mockResolvedValue(undefined);
    fireEvent.click(within(rows[1]).getByRole("button", { name: "Disconnect" }));
    await act(settle);
    expect(service.DisconnectSocket).toHaveBeenCalledWith({ serverId: "srv_1", socketId: "1/admin", closeClient: false });
    fireEvent.click(within(rows[0]).getByRole("button", { name: "Close client" }));
    await act(settle);
    expect(service.DisconnectSocket).toHaveBeenLastCalledWith({ serverId: "srv_1", socketId: "1", closeClient: true });
  });

  it("does not emit to another socket once the selected one disconnects", async () => {
    const stores = storesWith(sioServerDTO());
    const setSockets = (ids: string[]) =>
      stores.servers.dispatch({
        type: "servers/sioSockets",
        id: "srv_1",
        sockets: ids.map((id) => toSioSocket(sioSocketDTO({ id, clientId: id }))),
      });
    setSockets(["1", "2"]);
    renderPane(stores);
    const rows = within(screen.getByRole("list", { name: "Sockets" })).getAllByRole("listitem");
    fireEvent.click(within(rows[1]).getByRole("button", { name: "Socket 2, 127.0.0.1:60001" }));
    fireEvent.change(screen.getByLabelText("Event"), { target: { value: "news" } });
    typeArgs("[]");
    expect(screen.getByRole("button", { name: "Emit to socket 2" }).hasAttribute("disabled")).toBe(false);

    setSockets(["1"]);
    const button = screen.getByRole("button", { name: "Emit to socket" });
    expect(button.hasAttribute("disabled")).toBe(true);
    expect(button.getAttribute("title")).toBe("The selected socket disconnected: select a socket");
    expect(screen.getByRole("button", { name: "Socket 1, 127.0.0.1:60001" }).getAttribute("aria-pressed")).toBe("false");
    fireEvent.click(button);
    await act(settle);
    expect(service.EmitToSockets).not.toHaveBeenCalled();

    // Selecting a socket again makes it the target.
    fireEvent.click(screen.getByRole("button", { name: "Socket 1, 127.0.0.1:60001" }));
    expect(screen.getByRole("button", { name: "Emit to socket 1" }).hasAttribute("disabled")).toBe(false);
  });

  it("disables emitting without sockets and says how to connect", () => {
    const stores = storesWith(sioServerDTO({ state: "stopped", addresses: [], boundPort: 0 }));
    renderPane(stores);
    expect(screen.getByText("Start the server to accept Socket.IO clients.")).toBeTruthy();
    expect(screen.getByText("No sockets")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Emit to all" }).hasAttribute("disabled")).toBe(true);
  });

  it("sends settings and rules to Go once typing pauses, and shows a rule's problem under it", async () => {
    vi.useFakeTimers();
    const stores = storesWith(sioServerDTO());
    renderPane(stores);
    fireEvent.click(screen.getByRole("tab", { name: "Settings" }));
    service.UpdateConfig.mockImplementation((_id, config) => Promise.resolve(sioServerDTO({ config: config as ServerDTO["config"] })));

    fireEvent.change(screen.getByLabelText("Namespaces"), { target: { value: "/admin, chat" } });
    fireEvent.change(screen.getByLabelText("Required auth"), { target: { value: '{"token": "abc"}' } });
    fireEvent.change(screen.getByLabelText("Transports"), { target: { value: "websocket" } });
    fireEvent.change(screen.getByLabelText("Acks"), { target: { value: "none" } });
    fireEvent.click(screen.getByRole("button", { name: "Add rule" }));
    fireEvent.change(screen.getByLabelText("Rule 2 event name"), { target: { value: "^order:" } });
    fireEvent.change(screen.getByLabelText("Rule 2 match"), { target: { value: "regex" } });
    fireEvent.change(screen.getByLabelText("Rule 2 event to emit"), { target: { value: "updated" } });
    fireEvent.change(screen.getByLabelText("Rule 2 arguments to emit"), { target: { value: '{"at": {{$timestamp}}}' } });
    fireEvent.change(screen.getByLabelText("Rule 2 delay"), { target: { value: "250" } });
    await act(async () => {
      vi.advanceTimersByTime(SIO_SETTINGS_DELAY_MS + 10);
    });
    expect(service.UpdateConfig).toHaveBeenCalledTimes(1);
    expect(service.UpdateConfig).toHaveBeenLastCalledWith("srv_1", {
      mockHttp: null,
      webSocket: null,
      webhook: null,
      socketIo: {
        path: "/socket.io/",
        namespaces: ["/admin", "chat"],
        auth: '{"token": "abc"}',
        transports: "websocket",
        allowOrigin: "",
        pingIntervalMs: 25000,
        pingTimeoutMs: 20000,
        maxPayload: 1000000,
        echo: false,
        ackMode: "none",
        rules: [
          { enabled: true, match: "exact", pattern: "ping", ack: '["pong"]', emit: "", emitArgs: "", delayMs: 0 },
          { enabled: true, match: "regex", pattern: "^order:", ack: "", emit: "updated", emitArgs: '{"at": {{$timestamp}}}', delayMs: 250 },
        ],
      },
    });

    // A bad delay stays in the UI, under its rule.
    fireEvent.change(screen.getByLabelText("Rule 2 delay"), { target: { value: "soon" } });
    await act(async () => {
      vi.advanceTimersByTime(SIO_SETTINGS_DELAY_MS + 10);
    });
    expect(service.UpdateConfig).toHaveBeenCalledTimes(1);
    const rules = screen.getByRole("list", { name: "Auto-reply rules" });
    expect(within(rules).getByRole("alert").textContent).toMatch(/Rule 2: the delay/);

    // Go's refusal names the rule.
    service.UpdateConfig.mockImplementation(() =>
      Promise.reject(appError("sioserver.invalid_config", "Rule 1: the arguments are not valid JSON.", { field: "rules", ruleIndex: "0", ruleField: "ack" })),
    );
    fireEvent.change(screen.getByLabelText("Rule 2 delay"), { target: { value: "" } });
    await act(async () => {
      vi.advanceTimersByTime(SIO_SETTINGS_DELAY_MS + 10);
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(within(rules).getAllByRole("alert")[0].textContent).toMatch(/Rule 1: the arguments/);

    // A heartbeat out of range is refused before Go.
    fireEvent.change(screen.getByLabelText("Ping every"), { target: { value: "5" } });
    expect(screen.getByText(/The ping interval must be/)).toBeTruthy();
  });

  it("shows events with their name, acks with their round trip and how a client ended in the traffic", () => {
    const event = trafficDTO({
      kind: "message",
      summary: "Event “upload” from socket 2/admin (ack 4)",
      text: '["f.bin",{"$base64":"AAE="}]',
      size: 60,
      http: null,
      sio: {
        clientId: "2", session: "S", transport: "websocket", socketId: "2/admin", namespace: "/admin", packet: "BINARY_EVENT", event: "upload",
        ackId: 4, hasAck: true, attachments: 1, rttMs: 0, source: "", reason: "", initiatedBy: "",
      },
    });
    const msg = trafficToMessage(event);
    expect(msg.badge).toBe("upload");
    expect(msg.meta).toMatchObject({ client: "2", socket: "2/admin", namespace: "/admin", packet: "BINARY_EVENT", "ack ID": 4, attachments: 1 });
    const ack = trafficToMessage(
      trafficDTO({
        kind: "message",
        summary: "Ack 0 from socket 2",
        text: '["yes"]',
        http: null,
        sio: {
          clientId: "2", session: "S", transport: "polling", socketId: "2", namespace: "/", packet: "BINARY_ACK", event: "ask",
          ackId: 0, hasAck: true, attachments: 0, rttMs: 12, source: "", reason: "", initiatedBy: "",
        },
      }),
    );
    expect(ack.badge).toBe("ACK");
    expect(ack.meta?.["round trip"]).toBe("12 ms");
    const closed = trafficToMessage(
      trafficDTO({
        kind: "connection_close",
        direction: "system",
        summary: "Client 2 closed: ping timeout",
        http: null,
        sio: {
          clientId: "2", session: "S", transport: "polling", socketId: "", namespace: "", packet: "", event: "",
          ackId: 0, hasAck: false, attachments: 0, rttMs: 0, source: "", reason: "ping timeout", initiatedBy: "",
        },
      }),
    );
    expect(closed.kind).toBe("closed");
    expect(closed.meta?.reason).toBe("ping timeout");
  });
});
