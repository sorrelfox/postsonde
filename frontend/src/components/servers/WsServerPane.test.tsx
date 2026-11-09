import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { EditorView } from "@codemirror/view";

const service = vi.hoisted(() => ({
  Types: vi.fn<() => Promise<unknown>>(),
  List: vi.fn<() => Promise<unknown>>(),
  Traffic: vi.fn<(id: string) => Promise<unknown>>(),
  Clients: vi.fn<(id: string) => Promise<unknown>>(),
  UpdateConfig: vi.fn<(id: string, config: unknown) => Promise<unknown>>(),
  SendToClient: vi.fn<(input: unknown) => Promise<unknown>>(),
  Broadcast: vi.fn<(input: unknown) => Promise<unknown>>(),
  DisconnectClient: vi.fn<(input: unknown) => Promise<unknown>>(),
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

import { followServers, toLocalServer } from "../../commands/servers";
import { wsServerUrl } from "../../commands/wsServer";
import { IS_MAC } from "../../lib/shortcuts/keys";
import { SERVERS_CLIENTS_EVENT, type ServerDTO } from "../../lib/servers";
import { TrafficSources } from "../../lib/serverTraffic";
import { createStores, StoreProvider, type Stores } from "../../stores";
import { emit, resetFakeWails, settle } from "../../test/fakeWails";
import { runningDTO, wsClientDTO, wsServerDTO } from "../../test/servers";
import { ServerDetails } from "./ServerView";
import { WS_SETTINGS_DELAY_MS } from "./WsServerPane";

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

const cancellable = <T,>(value: T) => Object.assign(Promise.resolve(value), { cancel: vi.fn() });
const modEnter = { key: "Enter", ...(IS_MAC ? { metaKey: true } : { ctrlKey: true }) };

function storesWith(dto: ServerDTO): Stores {
  const stores = createStores();
  stores.servers.dispatch({ type: "servers/loaded", servers: [toLocalServer(dto)], types: [] });
  return stores;
}

function renderPane(stores: Stores) {
  const view = () => {
    const server = stores.servers.getState().byId.srv_1;
    return (
      <StoreProvider stores={stores}>
        <ServerDetails server={server} />
      </StoreProvider>
    );
  };
  const r = render(view());
  return { rerender: () => r.rerender(view()) };
}

/** Replaces the text of the composer's CodeMirror editor. */
function typeMessage(text: string) {
  const content = screen.getByRole("textbox", { name: "Message" });
  const view = EditorView.findFromDOM(content.closest(".cm-editor") as HTMLElement);
  if (!view) throw new Error("no editor view");
  act(() => view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text } }));
}

beforeEach(() => {
  resetFakeWails();
  for (const fn of Object.values(service)) fn.mockReset();
  format.ValidateJSON.mockReset().mockImplementation(() => cancellable({ valid: true, error: null }));
});

afterEach(() => {
  vi.useRealTimers();
});

describe("WebSocket server view", () => {
  it("builds the URL clients connect to", () => {
    expect(wsServerUrl(toLocalServer(wsServerDTO()))).toBe(["ws", "127.0.0.1:52341/ws"].join("://"));
    const tls = { certificate: "generated", certFile: "", keyFile: "", pfxFile: "", hasPassphrase: false };
    expect(wsServerUrl(toLocalServer(wsServerDTO({ tls })))).toBe(["wss", "127.0.0.1:52341/ws"].join("://"));
    const any = wsServerDTO({ config: { mockHttp: null, webSocket: { path: "*", subprotocols: [], echo: false, maxMessageSize: 0, rules: null }, webhook: null } });
    expect(wsServerUrl(toLocalServer(any))).toBe(["ws", "127.0.0.1:52341/"].join("://"));
    expect(wsServerUrl(toLocalServer(wsServerDTO({ state: "stopped", addresses: [] })))).toBeNull();
  });

  it("lists clients from servers:clients and loads them for running servers", async () => {
    const stores = createStores();
    service.List.mockResolvedValue([wsServerDTO(), runningDTO({ id: "srv_2" })]);
    service.Types.mockResolvedValue([]);
    service.Traffic.mockResolvedValue([]);
    service.Clients.mockResolvedValue([wsClientDTO()]);
    const stop = followServers(stores, new TrafficSources({ schedule: () => () => {} }));
    await settle();
    expect(service.Clients).toHaveBeenCalledTimes(1);
    expect(service.Clients).toHaveBeenCalledWith("srv_1");
    expect(stores.servers.getState().wsClients.srv_1).toHaveLength(1);

    emit(SERVERS_CLIENTS_EVENT, { serverId: "srv_1", clients: [wsClientDTO(), wsClientDTO({ id: "2", peer: "127.0.0.1:60002" })] });
    expect(stores.servers.getState().wsClients.srv_1.map((c) => c.id)).toEqual(["1", "2"]);
    emit(SERVERS_CLIENTS_EVENT, { serverId: "srv_1", clients: null });
    expect(stores.servers.getState().wsClients.srv_1).toEqual([]);
    stop();
  });

  it("shows the clients, sends to the selected client or to all, and disconnects one", async () => {
    const stores = storesWith(wsServerDTO());
    stores.servers.dispatch({
      type: "servers/wsClients",
      id: "srv_1",
      clients: [wsClientDTO(), wsClientDTO({ id: "2", peer: "127.0.0.1:60002", subprotocol: "" })].map((c) => ({
        ...c,
        connectedAt: Date.parse(c.connectedAt),
      })),
    });
    renderPane(stores);

    expect(screen.getByRole("tablist", { name: "WebSocket server" })).toBeTruthy();
    expect(screen.getByRole("tab", { name: "Clients2" }).getAttribute("aria-selected")).toBe("true");
    expect(screen.getByText(["ws", "127.0.0.1:52341/ws"].join("://"))).toBeTruthy();
    const list = screen.getByRole("list", { name: "Clients" });
    const rows = within(list).getAllByRole("listitem");
    expect(rows).toHaveLength(2);
    expect(within(rows[0]).getByText("127.0.0.1:60001 · /ws · subprotocol chat")).toBeTruthy();
    expect(within(rows[0]).getByText("↓2 ↑1")).toBeTruthy();

    // The first client is the target until another one is selected.
    expect(screen.getByRole("button", { name: "Send to client 1" }).hasAttribute("disabled")).toBe(true); // no text yet
    fireEvent.click(within(rows[1]).getByRole("button", { name: "Client 2, 127.0.0.1:60002" }));
    expect(stores.servers.getState().wsSelectedClient.srv_1).toBe("2");

    typeMessage("hello");
    expect(stores.servers.getState().wsComposer.srv_1).toEqual({ format: "text", message: "hello" });
    service.SendToClient.mockResolvedValue(undefined);
    fireEvent.click(screen.getByRole("button", { name: "Send to client 2" }));
    await act(settle);
    expect(service.SendToClient).toHaveBeenCalledWith({ serverId: "srv_1", clientId: "2", message: { binary: false, text: "hello", dataBase64: "" } });

    // ⌘Enter sends to the selected client too.
    fireEvent.keyDown(screen.getByRole("textbox", { name: "Message" }), modEnter);
    await act(settle);
    expect(service.SendToClient).toHaveBeenCalledTimes(2);

    service.Broadcast.mockResolvedValue({ sent: 2, failed: 0 });
    fireEvent.click(screen.getByRole("button", { name: "Send to all" }));
    await act(settle);
    expect(service.Broadcast).toHaveBeenCalledWith({ serverId: "srv_1", message: { binary: false, text: "hello", dataBase64: "" } });
    expect(screen.getByRole("status").textContent).toBe("Sent to 2 clients.");

    // A failed send is shown in the composer.
    const err = new Error("gone");
    (err as Error & { cause: unknown }).cause = { kind: "not_found", code: "wsserver.client_not_found", message: "This client is no longer connected.", hint: "", details: {}, causes: [] };
    service.SendToClient.mockRejectedValue(err);
    fireEvent.click(screen.getByRole("button", { name: "Send to client 2" }));
    await act(settle);
    expect(screen.getByText("This client is no longer connected.")).toBeTruthy();

    // Disconnect with a code and reason.
    fireEvent.click(within(rows[0]).getByRole("button", { name: "Disconnect…" }));
    const form = screen.getByRole("form", { name: "Disconnect client 1" });
    fireEvent.change(within(form).getByLabelText("Close code"), { target: { value: "4000" } });
    fireEvent.change(within(form).getByLabelText("Close reason"), { target: { value: "bye" } });
    service.DisconnectClient.mockResolvedValue(undefined);
    fireEvent.submit(form);
    await act(settle);
    expect(service.DisconnectClient).toHaveBeenCalledWith({ serverId: "srv_1", clientId: "1", code: 4000, reason: "bye" });
    expect(screen.queryByRole("form", { name: "Disconnect client 1" })).toBeNull();

    // An invalid code is refused before Go; Go's refusal is shown.
    fireEvent.click(within(rows[1]).getByRole("button", { name: "Disconnect…" }));
    const form2 = screen.getByRole("form", { name: "Disconnect client 2" });
    fireEvent.change(within(form2).getByLabelText("Close code"), { target: { value: "abc" } });
    fireEvent.submit(form2);
    expect(within(form2).getByRole("alert").textContent).toMatch(/whole number/);
    const bad = new Error("bad");
    (bad as Error & { cause: unknown }).cause = { kind: "invalid_argument", code: "wsframe.invalid_argument", message: "1006 cannot be sent.", hint: "", details: {}, causes: [] };
    service.DisconnectClient.mockRejectedValue(bad);
    fireEvent.change(within(form2).getByLabelText("Close code"), { target: { value: "1006" } });
    fireEvent.submit(form2);
    await act(settle);
    expect(within(form2).getByRole("alert").textContent).toBe("1006 cannot be sent.");
  });

  it("does not send to another client once the selected one disconnects", async () => {
    const stores = storesWith(wsServerDTO());
    const setClients = (ids: string[]) =>
      act(() =>
        stores.servers.dispatch({
          type: "servers/wsClients",
          id: "srv_1",
          clients: ids.map((id) => wsClientDTO({ id })).map((c) => ({ ...c, connectedAt: Date.parse(c.connectedAt) })),
        }),
      );
    setClients(["1", "2"]);
    renderPane(stores);
    const rows = within(screen.getByRole("list", { name: "Clients" })).getAllByRole("listitem");
    fireEvent.click(within(rows[1]).getByRole("button", { name: /^Client 2,/ }));
    typeMessage("hello");
    expect(screen.getByRole("button", { name: "Send to client 2" }).hasAttribute("disabled")).toBe(false);

    setClients(["1"]);
    const button = screen.getByRole("button", { name: "Send to client" });
    expect(button.hasAttribute("disabled")).toBe(true);
    expect(button.getAttribute("title")).toBe("The selected client disconnected: select a client");
    fireEvent.keyDown(screen.getByRole("textbox", { name: "Message" }), modEnter);
    fireEvent.click(button);
    await act(settle);
    expect(service.SendToClient).not.toHaveBeenCalled();

    // Selecting a client again makes it the target.
    fireEvent.click(screen.getByRole("button", { name: /^Client 1,/ }));
    expect(screen.getByRole("button", { name: "Send to client 1" }).hasAttribute("disabled")).toBe(false);
  });

  it("disables sending without clients and says how to connect", () => {
    const stores = storesWith(wsServerDTO({ state: "stopped", addresses: [], boundPort: 0 }));
    renderPane(stores);
    expect(screen.getByText("Start the server to accept WebSocket connections.")).toBeTruthy();
    expect(screen.getByText("No clients connected")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Send to client" }).hasAttribute("disabled")).toBe(true);
    expect(screen.getByRole("button", { name: "Send to all" }).hasAttribute("disabled")).toBe(true);
  });

  it("sends settings edits to Go once typing pauses", async () => {
    vi.useFakeTimers();
    const stores = storesWith(wsServerDTO());
    renderPane(stores);
    fireEvent.click(screen.getByRole("tab", { name: "Settings" }));
    const path = screen.getByLabelText("Path") as HTMLInputElement;
    expect(path.value).toBe("/ws");
    expect((screen.getByLabelText("Subprotocols") as HTMLInputElement).value).toBe("chat");

    service.UpdateConfig.mockImplementation((_id, config) =>
      Promise.resolve(wsServerDTO({ config: config as ServerDTO["config"] })),
    );
    fireEvent.change(path, { target: { value: "/socket" } });
    fireEvent.change(screen.getByLabelText("Subprotocols"), { target: { value: "a, b ," } });
    fireEvent.click(screen.getByLabelText("Echo messages no rule answers back to the client that sent them"));
    expect(service.UpdateConfig).not.toHaveBeenCalled();
    await act(async () => {
      vi.advanceTimersByTime(WS_SETTINGS_DELAY_MS + 10);
    });
    expect(service.UpdateConfig).toHaveBeenCalledTimes(1);
    expect(service.UpdateConfig).toHaveBeenCalledWith("srv_1", {
      mockHttp: null,
      webSocket: { path: "/socket", subprotocols: ["a", "b"], echo: true, maxMessageSize: 0, rules: [] },
      webhook: null,
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(stores.servers.getState().byId.srv_1.webSocket?.path).toBe("/socket");

    // Any path, and a bad size is shown instead of sent.
    fireEvent.click(screen.getByLabelText("Any path"));
    fireEvent.change(screen.getByLabelText("Largest message"), { target: { value: "-1" } });
    await act(async () => {
      vi.advanceTimersByTime(WS_SETTINGS_DELAY_MS + 10);
    });
    expect(service.UpdateConfig).toHaveBeenCalledTimes(1);
    expect(screen.getByRole("alert").textContent).toMatch(/whole number of bytes/);
    fireEvent.change(screen.getByLabelText("Largest message"), { target: { value: "1024" } });
    await act(async () => {
      vi.advanceTimersByTime(WS_SETTINGS_DELAY_MS + 10);
    });
    expect(service.UpdateConfig).toHaveBeenLastCalledWith("srv_1", {
      mockHttp: null,
      webSocket: { path: "*", subprotocols: ["a", "b"], echo: true, maxMessageSize: 1024, rules: [] },
      webhook: null,
    });
  });

  it("edits the auto-reply rules: add, change, reorder, remove", async () => {
    vi.useFakeTimers();
    const rule = { enabled: true, match: "exact", pattern: "ping", reply: "pong", delayMs: 0 };
    const stores = storesWith(
      wsServerDTO({
        config: { mockHttp: null, webSocket: { path: "/ws", subprotocols: [], echo: true, maxMessageSize: 0, rules: [rule] }, webhook: null },
      }),
    );
    renderPane(stores);
    fireEvent.click(screen.getByRole("tab", { name: "Settings" }));
    expect((screen.getByLabelText("Rule 1 pattern") as HTMLInputElement).value).toBe("ping");
    expect((screen.getByLabelText("Rule 1 reply") as HTMLTextAreaElement).value).toBe("pong");
    expect(screen.getByRole("button", { name: "Move rule 1 up" }).hasAttribute("disabled")).toBe(true);

    service.UpdateConfig.mockImplementation((_id, config) => Promise.resolve(wsServerDTO({ config: config as ServerDTO["config"] })));
    fireEvent.click(screen.getByRole("button", { name: "Add rule" }));
    fireEvent.change(screen.getByLabelText("Rule 2 match"), { target: { value: "regex" } });
    fireEvent.change(screen.getByLabelText("Rule 2 pattern"), { target: { value: "^get (\\d+)$" } });
    fireEvent.change(screen.getByLabelText("Rule 2 reply"), { target: { value: '{"id": $1}' } });
    fireEvent.change(screen.getByLabelText("Rule 2 delay"), { target: { value: "250" } });
    fireEvent.click(screen.getByRole("button", { name: "Move rule 2 up" }));
    fireEvent.click(screen.getByLabelText("Rule 2 enabled"));
    await act(async () => {
      vi.advanceTimersByTime(WS_SETTINGS_DELAY_MS + 10);
    });
    expect(service.UpdateConfig).toHaveBeenCalledTimes(1);
    expect(service.UpdateConfig).toHaveBeenLastCalledWith("srv_1", {
      mockHttp: null,
      webSocket: {
        path: "/ws",
        subprotocols: [],
        echo: true,
        maxMessageSize: 0,
        rules: [
          { enabled: true, match: "regex", pattern: "^get (\\d+)$", reply: '{"id": $1}', delayMs: 250 },
          { enabled: false, match: "exact", pattern: "ping", reply: "pong", delayMs: 0 },
        ],
      },
      webhook: null,
    });
    // The moved row keeps what was typed into it.
    expect((screen.getByLabelText("Rule 1 pattern") as HTMLInputElement).value).toBe("^get (\\d+)$");

    // A bad delay is shown under its rule instead of being sent.
    fireEvent.change(screen.getByLabelText("Rule 1 delay"), { target: { value: "soon" } });
    await act(async () => {
      vi.advanceTimersByTime(WS_SETTINGS_DELAY_MS + 10);
    });
    expect(service.UpdateConfig).toHaveBeenCalledTimes(1);
    const rules = screen.getByRole("list", { name: "Auto-reply rules" });
    const first = within(rules).getAllByRole("listitem")[0];
    expect(within(first).getByRole("alert").textContent).toMatch(/Rule 1: the delay/);

    // Go refuses a regular expression: the error is shown under that rule.
    const err = new Error("rejected");
    (err as Error & { cause: unknown }).cause = {
      kind: "invalid_argument",
      code: "wsserver.invalid_config",
      message: "Rule 1: the regular expression is not valid: missing closing ): `(`.",
      hint: "",
      details: { field: "rules", ruleIndex: "0", ruleField: "pattern" },
      causes: [],
    };
    service.UpdateConfig.mockImplementation(() => Promise.reject(err));
    fireEvent.change(screen.getByLabelText("Rule 1 delay"), { target: { value: "" } });
    fireEvent.change(screen.getByLabelText("Rule 1 pattern"), { target: { value: "(" } });
    await act(async () => {
      vi.advanceTimersByTime(WS_SETTINGS_DELAY_MS + 10);
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(within(first).getByRole("alert").textContent).toMatch(/regular expression is not valid/);
    expect(screen.getByLabelText("Rule 1 pattern").getAttribute("aria-invalid")).toBe("true");
    expect(screen.getAllByRole("alert")).toHaveLength(1);

    // Removing the rules sends an empty list.
    service.UpdateConfig.mockImplementation((_id, config) => Promise.resolve(wsServerDTO({ config: config as ServerDTO["config"] })));
    fireEvent.click(screen.getByRole("button", { name: "Remove rule 2" }));
    fireEvent.click(screen.getByRole("button", { name: "Remove rule 1" }));
    expect(screen.queryByRole("list", { name: "Auto-reply rules" })).toBeNull();
    await act(async () => {
      vi.advanceTimersByTime(WS_SETTINGS_DELAY_MS + 10);
    });
    expect(service.UpdateConfig).toHaveBeenLastCalledWith("srv_1", {
      mockHttp: null,
      webSocket: { path: "/ws", subprotocols: [], echo: true, maxMessageSize: 0, rules: [] },
      webhook: null,
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(screen.queryByRole("alert")).toBeNull();
  });
});
