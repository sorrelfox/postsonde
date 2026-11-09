import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { EditorView } from "@codemirror/view";
import { beforeEach, describe, expect, it, vi } from "vitest";

const collection = vi.hoisted(() => ({
  Request: vi.fn<(id: string) => Promise<unknown>>(),
  UpdateRequest: vi.fn<(input: { request: unknown; revision: number }) => Promise<void>>(),
  Flush: vi.fn<(id: string) => Promise<void>>(),
}));
const ws = vi.hoisted(() => ({
  Connect: vi.fn<(input: unknown) => Promise<unknown>>(),
  Send: vi.fn<(input: unknown) => Promise<unknown>>(),
  Close: vi.fn<(input: unknown) => Promise<void>>(),
  Ping: vi.fn<(input: unknown) => Promise<unknown>>(),
  CloseAll: vi.fn(),
  Snippet: vi.fn<(input: { connect: unknown; language: string; mode: string; revealSecrets: boolean }) => unknown>(),
  SnippetLanguages: vi.fn<() => unknown>(),
}));
const http = vi.hoisted(() => ({
  Variables: vi.fn<(id: string) => Promise<unknown>>(),
}));
const format = vi.hoisted(() => ({
  ValidateJSON: vi.fn<(text: string) => unknown>(),
  BeautifyJSON: vi.fn<(text: string, indent: number) => unknown>(),
  MinifyJSON: vi.fn<(text: string) => unknown>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({
  CollectionService: collection,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/websocket", () => ({
  WebSocketService: ws,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/format", () => ({
  FormatService: format,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http", () => ({
  HTTPService: http,
}));

import { confirmQueue } from "../../commands/dialogs";
import { JSON_CHECK_DELAY_MS } from "../../commands/useJsonCheck";
import { resetWsSessions, wsSessions } from "../../commands/wsSessions";
import type { RequestDTO } from "../../lib/collection";
import { IS_MAC } from "../../lib/shortcuts/keys";
import type { VariableDTO } from "../../lib/templateVariables";
import { connectInput, WS_FILE_MAX_BYTES, type SavedMessageDTO, type StateEventDTO } from "../../lib/websocket";
import { createStores, StoreProvider, type Stores, type TreeNode } from "../../stores";
import { emit, resetFakeWails, settle } from "../../test/fakeWails";
import { TabBar } from "../tabs/TabBar";
import { TabPanel } from "../tabs/TabPanel";
import { moveItem, nextMessageName } from "./SavedMessages";
import { schemeHint, subprotocolProblem } from "./WebSocketEditor";

// jsdom has no layout: CodeMirror measures ranges when it draws the cursor.
for (const name of ["getClientRects", "getBoundingClientRect"] as const) {
  if (typeof Range.prototype[name] !== "function") {
    Object.defineProperty(Range.prototype, name, {
      configurable: true,
      value: name === "getClientRects" ? () => [] : () => ({ left: 0, right: 0, top: 0, bottom: 0, width: 0, height: 0 }),
    });
  }
}

const cancellable = <T,>(value: T) => Object.assign(Promise.resolve(value), { cancel: vi.fn() });

function request(over: Partial<RequestDTO> = {}): RequestDTO {
  return {
    id: "req_feed",
    kind: "websocket",
    name: "Feed",
    description: "",
    method: "",
    url: "ws://localhost:8080/feed?room=lobby",
    params: [{ name: "room", value: "lobby", description: "", enabled: true, extra: "" }],
    pathParams: [],
    headers: [
      { name: "X-Client", value: "postsonde", description: "", enabled: true, extra: "" },
      { name: "X-Off", value: "1", description: "", enabled: false, extra: "" },
    ],
    body: { mode: "", type: "", text: "", fields: [], parts: [], path: "", contentType: "", extra: "" },
    query: "",
    variables: "",
    operationName: "",
    connectionParams: "",
    schemaFile: "",
    address: "",
    sendMode: "",
    lineEnding: "",
    message: "hello",
    subprotocols: ["chat.v1"],
    messageFormat: "",
    service: "",
    proto: { files: [], importPaths: [], inherit: false, extra: "" },
    metadata: [],
    bodyExamples: [],
    auth: { type: "", username: "", password: "", prefix: null, token: "", key: "", value: "", in: "", disabled: false, localSecrets: [], extra: "" },
    settings: { timeoutMs: null, followRedirects: null, maxRedirects: null, followOriginalMethod: null, forwardAuthorization: null, verifyTls: null, connectTimeoutMs: 4000, urlEncoding: "", httpVersion: "", sendCookies: null, storeCookies: null, tls: null, deadlineMs: null, extra: "" },
    extra: '{"x-note":"kept"}',
    ...over,
  };
}

const node = (over: Partial<TreeNode> & { id: string }): TreeNode =>
  ({ kind: "request", parentId: null, name: over.id, path: `${over.id}.json`, fileKind: "websocket", readOnly: "", method: "", ...over }) as TreeNode;

function stateEvent(over: Partial<StateEventDTO>): StateEventDTO {
  return {
    connId: "ws_1",
    requestId: "req_feed",
    url: "ws://localhost:8080/feed?room=lobby",
    seq: 1,
    attempt: 1,
    state: "open",
    at: "2026-09-16T10:00:00.000Z",
    handshake: null,
    close: null,
    error: null,
    reconnect: null,
    tests: null,
    ...over,
  };
}

async function setup(dto: RequestDTO = request()): Promise<Stores> {
  collection.Request.mockImplementation(() => Promise.resolve(structuredClone(dto)));
  const stores = createStores();
  const nodes = [node({ id: "req_feed", name: "Feed" }), node({ id: "other", name: "Other", fileKind: "http", method: "GET" })];
  stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
  stores.collection.dispatch({ type: "collection/load", workspaceId: "ws_1", nodes, rootIds: nodes.map((n) => n.id), readOnly: "" });
  stores.tabs.dispatch({ type: "tabs/restore", order: ["req_feed", "other"], activeId: "req_feed" });
  render(
    <StoreProvider stores={stores}>
      <TabBar />
      <TabPanel />
    </StoreProvider>,
  );
  await act(settle);
  return stores;
}

const editor = () => document.querySelector("[data-request-id]") as HTMLElement;
const viewOf = (label: string): EditorView => {
  const content = within(editor()).getByRole("textbox", { name: label });
  const found = EditorView.findFromDOM(content.closest(".cm-editor") as HTMLElement);
  if (!found) throw new Error(`no editor view for ${label}`);
  return found;
};
const sent = () => {
  const calls = collection.UpdateRequest.mock.calls;
  return calls[calls.length - 1]?.[0] as { request: RequestDTO; revision: number };
};
const button = (name: string) => within(editor()).getByRole("button", { name }) as HTMLButtonElement;
const status = () => within(editor()).getByRole("status");
const waitForCheck = () => act(() => new Promise<void>((r) => setTimeout(r, JSON_CHECK_DELAY_MS + 30)));
const modEnter = { key: "Enter", ...(IS_MAC ? { metaKey: true } : { ctrlKey: true }) };

async function connectAndOpen() {
  ws.Connect.mockResolvedValue({ connId: "ws_1", requestId: "req_feed", url: "ws://localhost:8080/feed?room=lobby" });
  fireEvent.click(button("Connect"));
  await act(settle);
  act(() =>
    emit(
      "ws:state",
      stateEvent({
        state: "open",
        handshake: {
          url: "ws://localhost:8080/feed?room=lobby",
          requestHeaders: [],
          response: { statusCode: 101, status: "101 Switching Protocols", headers: [], body: "", bodyTruncated: false },
          subprotocol: "chat.v1",
          localAddr: "127.0.0.1:50000",
          remoteAddr: "127.0.0.1:8080",
          tlsVersion: "",
          durationMs: 3,
        },
      }),
    ),
  );
}

beforeEach(() => {
  resetWsSessions();
  resetFakeWails();
  collection.Request.mockReset();
  collection.UpdateRequest.mockReset().mockResolvedValue(undefined);
  collection.Flush.mockReset().mockResolvedValue(undefined);
  ws.Connect.mockReset();
  ws.Send.mockReset();
  ws.Close.mockReset().mockResolvedValue(undefined);
  ws.Ping.mockReset();
  http.Variables.mockReset().mockImplementation(() => cancellable({ variables: [], environment: "" }));
  format.ValidateJSON.mockReset().mockImplementation(() => cancellable({ valid: true, error: null }));
  format.BeautifyJSON.mockReset();
});

describe("WebSocketEditor", () => {
  it("writes the handshake Connect would make as a websocat or wscat command in the Code tab", async () => {
    ws.SnippetLanguages.mockReset().mockImplementation(() =>
      cancellable([
        { id: "websocat", name: "websocat", syntax: "shell" },
        { id: "wscat", name: "wscat", syntax: "shell" },
      ]),
    );
    ws.Snippet.mockReset().mockImplementation((input) =>
      cancellable({ language: input.language, mode: input.mode, code: `${input.language} ws://localhost:8080/feed?room=lobby`, masked: false, revealed: false, unresolved: [] }),
    );
    await setup();
    fireEvent.click(within(within(editor()).getByRole("tablist", { name: "Handshake options" })).getByRole("tab", { name: /Code/ }));
    await act(settle);
    expect(ws.Snippet).toHaveBeenLastCalledWith({ connect: connectInput(request()), language: "websocat", mode: "resolved", revealSecrets: false });
    expect(within(editor()).getByLabelText("websocat code").textContent).toBe("websocat ws://localhost:8080/feed?room=lobby");
    fireEvent.change(within(editor()).getByRole("combobox", { name: "Language" }), { target: { value: "wscat" } });
    await act(settle);
    expect(ws.Snippet).toHaveBeenLastCalledWith({ connect: connectInput(request()), language: "wscat", mode: "resolved", revealSecrets: false });
    expect(within(editor()).getByLabelText("wscat code").textContent).toBe("wscat ws://localhost:8080/feed?room=lobby");
  });

  it("renders a websocket request with its URL, handshake tabs, log and composer", async () => {
    await setup();
    expect(screen.getByRole("tab", { name: "WS Feed" })).toBeTruthy();
    expect(within(editor()).getByRole("textbox", { name: "URL" }).textContent).toBe("ws://localhost:8080/feed?room=lobby");
    expect(within(editor()).getByRole("textbox", { name: "Message" }).textContent).toBe("hello");
    expect(status().textContent).toBe("Not connected");
    const tabs = within(editor()).getByRole("tablist", { name: "Handshake options" });
    expect(within(tabs).getAllByRole("tab").map((t) => t.textContent)).toEqual(["Params1", "Headers1", "Auth", "Subprotocols1", "Tests", "Settings", "Notes", "Code"]);
    const formatGroup = within(editor()).getByRole("group", { name: "Message format" });
    expect(within(formatGroup).getByRole("button", { name: "Text" }).getAttribute("aria-pressed")).toBe("true");
    expect(within(editor()).queryByRole("button", { name: "Beautify" })).toBeNull();
    expect(button("Send").disabled).toBe(true);
    expect(button("Connect").disabled).toBe(false);
    expect(within(editor()).getByRole("grid", { name: "WebSocket messages" })).toBeTruthy();
    expect(collection.UpdateRequest).not.toHaveBeenCalled();
  });

  it("auto-saves the URL with its params, subprotocols, settings and message format", async () => {
    await setup();
    const url = viewOf("URL");
    act(() => url.dispatch({ changes: { from: url.state.doc.length, insert: "&debug=1" }, userEvent: "input.type" }));
    await act(settle);
    expect(sent().request).toEqual(
      request({
        url: "ws://localhost:8080/feed?room=lobby&debug=1",
        params: [
          { name: "room", value: "lobby", description: "", enabled: true, extra: "" },
          { name: "debug", value: "1", description: "", enabled: true, extra: "" },
        ],
      }),
    );

    fireEvent.click(within(editor()).getByRole("tab", { name: /Subprotocols/ }));
    fireEvent.click(button("Add subprotocol"));
    await act(settle);
    const second = within(editor()).getByRole("textbox", { name: "Subprotocol 2" }) as HTMLInputElement;
    fireEvent.change(second, { target: { value: "chat v2" } });
    await act(settle);
    expect(sent().request.subprotocols).toEqual(["chat.v1", "chat v2"]);
    expect(second.getAttribute("aria-invalid")).toBe("true");
    fireEvent.click(button("Remove subprotocol 1"));
    await act(settle);
    expect(sent().request.subprotocols).toEqual(["chat v2"]);

    fireEvent.click(within(editor()).getByRole("tab", { name: "Settings" }));
    fireEvent.click(within(editor()).getByRole("checkbox", { name: /Use the project's cookies/ }));
    fireEvent.change(within(editor()).getByRole("spinbutton", { name: /Connect timeout/ }), { target: { value: "" } });
    await act(settle);
    expect(sent().request.settings).toEqual({ ...request().settings, sendCookies: false, connectTimeoutMs: null });

    fireEvent.click(within(within(editor()).getByRole("group", { name: "Message format" })).getByRole("button", { name: "JSON" }));
    await act(settle);
    expect(sent().request).toMatchObject({ messageFormat: "json", extra: '{"x-note":"kept"}', url: "ws://localhost:8080/feed?room=lobby&debug=1" });
  });

  it("connects with the handshake options, sends with the button and ⌘Enter, and closes with a code", async () => {
    await setup();
    await connectAndOpen();
    expect(ws.Connect).toHaveBeenCalledWith({
      requestId: "req_feed",
      url: "ws://localhost:8080/feed?room=lobby",
      headers: [
        { name: "X-Client", value: "postsonde", enabled: true },
        { name: "X-Off", value: "1", enabled: false },
      ],
      subprotocols: ["chat.v1"],
      connectTimeoutMs: 4000,
      verifyTls: null,
      sendCookies: null,
    });
    expect(status().textContent).toBe("Connected to ws://localhost:8080/feed?room=lobby · subprotocol chat.v1 · 0 sent · 0 received");

    ws.Send.mockResolvedValue({ bytes: 5 });
    expect(button("Send").disabled).toBe(false);
    fireEvent.click(button("Send"));
    await act(settle);
    expect(ws.Send).toHaveBeenLastCalledWith({ connId: "ws_1", text: "hello" });

    const message = within(editor()).getByRole("textbox", { name: "Message" });
    fireEvent.keyDown(message, modEnter);
    await act(settle);
    expect(ws.Send).toHaveBeenCalledTimes(2);
    // ⌘Enter did not insert a line in the message.
    expect(viewOf("Message").state.doc.toString()).toBe("hello");

    fireEvent.change(within(editor()).getByRole("textbox", { name: "Close code" }), { target: { value: "4000" } });
    fireEvent.change(within(editor()).getByRole("textbox", { name: "Close reason" }), { target: { value: "bye" } });
    fireEvent.click(button("Disconnect"));
    await act(settle);
    expect(ws.Close).toHaveBeenCalledWith({ connId: "ws_1", code: 4000, reason: "bye" });
  });

  it("saves keepalive and reconnect settings, pings, hides pings in the log and shows reconnects (WS-09, WS-10)", async () => {
    await setup();
    fireEvent.click(within(editor()).getByRole("tab", { name: "Settings" }));
    const spin = (name: RegExp) => within(editor()).getByRole("spinbutton", { name }) as HTMLInputElement;
    expect(spin(/Pong timeout/).disabled).toBe(true);
    expect(spin(/Reconnect attempts/).disabled).toBe(true);
    fireEvent.change(spin(/Keepalive ping every/), { target: { value: "15000" } });
    await act(settle);
    fireEvent.change(spin(/Pong timeout/), { target: { value: "0" } });
    await act(settle);
    fireEvent.click(within(editor()).getByRole("checkbox", { name: /Reconnect automatically/ }));
    await act(settle);
    fireEvent.change(spin(/Reconnect attempts/), { target: { value: "3" } });
    await act(settle);
    fireEvent.change(spin(/First reconnect after/), { target: { value: "500" } });
    await act(settle);
    fireEvent.change(spin(/Longest wait/), { target: { value: "-1" } }); // refused
    await act(settle);
    expect(sent().request.settings).toEqual({
      ...request().settings,
      pingIntervalMs: 15000,
      pongTimeoutMs: 0,
      autoReconnect: true,
      reconnectMaxAttempts: 3,
      reconnectDelayMs: 500,
    });

    await connectAndOpen();
    expect(ws.Connect).toHaveBeenCalledWith(
      expect.objectContaining({ pingIntervalMs: 15000, pongTimeoutMs: 0, autoReconnect: true, reconnectMaxAttempts: 3, reconnectDelayMs: 500 }),
    );
    expect(ws.Connect.mock.calls[0]?.[0]).not.toHaveProperty("reconnectMaxDelayMs");

    ws.Ping.mockResolvedValue({ bytes: 3 });
    fireEvent.change(within(editor()).getByRole("textbox", { name: "Ping payload" }), { target: { value: "abc" } });
    fireEvent.click(button("Ping"));
    await act(settle);
    expect(ws.Ping).toHaveBeenCalledWith({ connId: "ws_1", payload: "abc" });
    act(() =>
      emit("ws:message", {
        connId: "ws_1",
        requestId: "req_feed",
        seq: 3,
        messages: [
          { direction: "sent", opcode: "ping", at: "2026-09-16T10:00:01.000Z", text: "abc", data: btoa("abc"), size: 3 },
          { direction: "received", opcode: "pong", at: "2026-09-16T10:00:01.004Z", text: "abc", data: btoa("abc"), size: 3, rttMs: 4.2 },
        ],
      }),
    );
    act(() => wsSessions().get("req_feed").log.flush());
    const log = () => within(editor()).getByRole("grid", { name: "WebSocket messages" });
    expect(within(log()).getAllByText("abc")).toHaveLength(2);
    expect(within(log()).getByText("pong")).toBeTruthy();
    expect(status().textContent).toBe("Connected to ws://localhost:8080/feed?room=lobby · subprotocol chat.v1 · 0 sent · 0 received · last pong 4.20 ms");
    const toggles = within(editor()).getByRole("group", { name: "Show kinds" });
    const pings = within(toggles).getByRole("button", { name: "Pings" });
    expect(pings.getAttribute("aria-pressed")).toBe("true");
    fireEvent.click(pings);
    expect(pings.getAttribute("aria-pressed")).toBe("false");
    expect(within(log()).queryByText("abc")).toBeNull();
    fireEvent.click(pings);
    expect(within(log()).getAllByText("abc")).toHaveLength(2);

    ws.Ping.mockRejectedValueOnce(
      Object.assign(new Error("x"), { cause: { kind: "invalid_argument", code: "ws.ping_too_long", message: "A ping payload can be at most 125 bytes.", hint: "", details: {}, causes: [] } }),
    );
    fireEvent.click(button("Ping"));
    await act(settle);
    expect(status().textContent).toBe("A ping payload can be at most 125 bytes.");

    act(() =>
      emit(
        "ws:state",
        stateEvent({
          state: "closed",
          seq: 4,
          error: { kind: "timeout", code: "wsclient.pong_timeout", message: "The server did not answer a keepalive ping within 10 s; the connection was closed.", hint: "", details: {}, causes: [] },
          close: {
            code: 1006,
            reason: "",
            wasClean: false,
            initiatedBy: "local",
            sent: null,
            received: null,
            messagesSent: 0,
            messagesReceived: 0,
            bytesSent: 0,
            bytesReceived: 0,
            durationMs: 1000,
          },
          reconnect: { attempt: 1, maxAttempts: 3, delayMs: 500, stopped: "" },
        }),
      ),
    );
    expect(status().textContent).toBe(
      "The server did not answer a keepalive ping within 10 s; the connection was closed. Reconnecting in 500 ms (attempt 1 of 3)…",
    );
    expect(button("Ping").disabled).toBe(true);
    act(() => wsSessions().get("req_feed").log.flush());
    expect(within(log()).getByText("Reconnecting in 500 ms (attempt 1 of 3)")).toBeTruthy();
    fireEvent.click(button("Cancel"));
    await act(settle);
    expect(ws.Close).toHaveBeenCalledWith({ connId: "ws_1", code: 1000, reason: "" });
    act(() => emit("ws:state", stateEvent({ state: "closed", seq: 5, reconnect: { attempt: 1, maxAttempts: 3, delayMs: 0, stopped: "manual" } })));
    expect(button("Connect")).toBeTruthy();
  });

  it("checks a JSON message, blocks sending it while invalid, and beautifies it", async () => {
    format.ValidateJSON.mockImplementation((text) =>
      cancellable(
        text === '{"a":}'
          ? { valid: false, error: { offset: 5, byteOffset: 5, line: 1, column: 6, message: "Invalid character '}'" } }
          : { valid: true, error: null },
      ),
    );
    await setup(request({ messageFormat: "json", message: '{"a":}' }));
    await connectAndOpen();
    await waitForCheck();
    expect(format.ValidateJSON).toHaveBeenCalledWith('{"a":}');
    expect(within(editor()).getByText("Invalid character '}'")).toBeTruthy();
    expect(button("Line 1, column 6")).toBeTruthy();
    expect(button("Send").disabled).toBe(true);
    expect(editor().querySelector(".cm-errorMarker")).not.toBeNull();

    const view = viewOf("Message");
    act(() => view.dispatch({ changes: { from: 5, insert: "1" }, userEvent: "input.type" }));
    await waitForCheck();
    expect(within(editor()).queryByText("Invalid character '}'")).toBeNull();
    expect(button("Send").disabled).toBe(false);

    format.BeautifyJSON.mockImplementation(() => cancellable({ valid: true, text: '{\n  "a": 1\n}', error: null }));
    fireEvent.click(button("Beautify"));
    await act(settle);
    expect(format.BeautifyJSON).toHaveBeenCalledWith('{"a":1}', 2);
    expect(sent().request.message).toBe('{\n  "a": 1\n}');
    expect(view.state.doc.toString()).toBe('{\n  "a": 1\n}');

    ws.Send.mockResolvedValue({ bytes: 12 });
    fireEvent.click(button("Send"));
    await act(settle);
    expect(ws.Send).toHaveBeenLastCalledWith({ connId: "ws_1", text: '{\n  "a": 1\n}' });
  });

  it("shows sent and received messages in the log, keeps the connection across tabs, and shows the server's close", async () => {
    const stores = await setup();
    await connectAndOpen();
    act(() =>
      emit("ws:message", {
        connId: "ws_1",
        requestId: "req_feed",
        seq: 3,
        messages: [
          { direction: "sent", opcode: "text", at: "2026-09-16T10:00:01.000Z", text: "hello", data: "", size: 5 },
          { direction: "received", opcode: "text", at: "2026-09-16T10:00:01.001Z", text: "echo hello", data: "", size: 10 },
          { direction: "received", opcode: "binary", at: "2026-09-16T10:00:01.002Z", text: "", data: btoa("\x00\xffb"), size: 3 },
        ],
      }),
    );
    act(() => wsSessions().get("req_feed").log.flush());
    const log = () => within(editor()).getByRole("grid", { name: "WebSocket messages" });
    expect(within(log()).getByText("echo hello")).toBeTruthy();
    expect(within(log()).getByText(/00 ff 62/)).toBeTruthy();
    expect(status().textContent).toContain("1 sent · 2 received");

    act(() => stores.tabs.dispatch({ type: "tabs/activate", id: "other" }));
    await act(settle);
    act(() => stores.tabs.dispatch({ type: "tabs/activate", id: "req_feed" }));
    await act(settle);
    expect(button("Disconnect")).toBeTruthy();
    expect(within(log()).getByText("echo hello")).toBeTruthy();
    expect(ws.Close).not.toHaveBeenCalled();

    act(() =>
      emit(
        "ws:state",
        stateEvent({
          state: "closed",
          seq: 5,
          close: {
            code: 4001,
            reason: "server bye",
            wasClean: true,
            initiatedBy: "remote",
            sent: { code: 4001, reason: "" },
            received: { code: 4001, reason: "server bye" },
            messagesSent: 1,
            messagesReceived: 2,
            bytesSent: 5,
            bytesReceived: 13,
            durationMs: 1000,
          },
        }),
      ),
    );
    expect(status().textContent).toBe("Closed by the server: 4001 “server bye”");
    expect(button("Connect")).toBeTruthy();
  });

  it("saves, renames, reorders, loads, updates, deletes and sends saved messages (WS-07)", async () => {
    const saved = (name: string, text: string, format = ""): SavedMessageDTO => ({ name, text, format, extra: "" });
    await setup(request({ savedMessages: [saved("Join", '{"join":"{{room}}"}', "json"), saved("Ping", "ping")] }));
    const panel = () => within(editor()).getByRole("region", { name: "Saved messages" });
    const names = () => within(panel()).getAllByRole("textbox").map((el) => (el as HTMLInputElement).value);
    expect(names()).toEqual(["Join", "Ping"]);
    expect(within(panel()).getByText("JSON")).toBeTruthy();
    // Not connected: nothing can be sent.
    expect((within(panel()).getByRole("button", { name: "Send Join" }) as HTMLButtonElement).disabled).toBe(true);

    // Save stores the composer's message under a new name, focused to rename it.
    fireEvent.click(within(panel()).getByRole("button", { name: "Save" }));
    await act(settle);
    expect(sent().request.savedMessages).toEqual([saved("Join", '{"join":"{{room}}"}', "json"), saved("Ping", "ping"), saved("Message 3", "hello", "text")]);
    const third = within(panel()).getByRole("textbox", { name: "Name of saved message 3" }) as HTMLInputElement;
    expect(document.activeElement).toBe(third);
    fireEvent.change(third, { target: { value: "Hello" } });
    await act(settle);
    expect(sent().request.savedMessages?.[2]?.name).toBe("Hello");

    fireEvent.click(within(panel()).getByRole("button", { name: "Move Hello up" }));
    await act(settle);
    expect(names()).toEqual(["Join", "Hello", "Ping"]);
    fireEvent.click(within(panel()).getByRole("button", { name: "Delete Ping" }));
    await act(settle);
    expect(sent().request.savedMessages?.map((m) => m.name)).toEqual(["Join", "Hello"]);

    // Load puts a message and its format into the composer; Update writes the composer back.
    fireEvent.click(within(panel()).getByRole("button", { name: "Load Join into the composer" }));
    await act(settle);
    expect(sent().request).toMatchObject({ message: '{"join":"{{room}}"}', messageFormat: "json" });
    expect(viewOf("Message").state.doc.toString()).toBe('{"join":"{{room}}"}');
    expect(within(panel()).queryByRole("button", { name: "Update" })).toBeNull();
    const view = viewOf("Message");
    act(() => view.dispatch({ changes: { from: view.state.doc.length - 1, insert: ',"x":1' }, userEvent: "input.type" }));
    await act(settle);
    fireEvent.click(within(panel()).getByRole("button", { name: "Update" }));
    await act(settle);
    expect(sent().request.savedMessages?.[0]).toEqual(saved("Join", '{"join":"{{room}}","x":1}', "json"));

    // One click sends it once connected; Go resolves its variables.
    await connectAndOpen();
    ws.Send.mockResolvedValue({ bytes: 10 });
    fireEvent.click(within(panel()).getByRole("button", { name: "Send Hello" }));
    await act(settle);
    expect(ws.Send).toHaveBeenLastCalledWith({ connId: "ws_1", text: "hello" });
    ws.Send.mockRejectedValueOnce(
      Object.assign(new Error("x"), { cause: { kind: "network", code: "wsframe.write_failed", message: "write failed", hint: "", details: {}, causes: [] } }),
    );
    fireEvent.click(within(panel()).getByRole("button", { name: "Send Join" }));
    await act(settle);
    expect(within(panel()).getByRole("alert").textContent).toBe("Join: write failed");
  });

  it("draws {{variables}} as chips and asks before connecting with undefined ones (WS-11)", async () => {
    const variable = (name: string): VariableDTO => ({ name, value: `${name}-value`, secret: false, resolved: true, scope: "Environment: dev", source: "environment" });
    http.Variables.mockImplementation(() => cancellable({ variables: [variable("host"), variable("room")], environment: "dev" }));
    await setup(request({ url: "ws://{{host}}/feed", params: [], message: '{"room":"{{room}}","id":"{{id}}"}' }));
    expect(http.Variables).toHaveBeenCalledWith("req_feed");
    const chips = (label: string) =>
      [...within(editor()).getByRole("textbox", { name: label }).querySelectorAll(".cm-templateVariable")].map((el) => [
        el.textContent,
        el.hasAttribute("data-unresolved"),
      ]);
    expect(chips("URL")).toEqual([["{{host}}", false]]);
    expect(chips("Message")).toEqual([
      ["{{room}}", false],
      ["{{id}}", true],
    ]);

    const detach = confirmQueue.attach();
    try {
      ws.Connect.mockRejectedValueOnce(
        Object.assign(new Error("refused"), {
          cause: { kind: "invalid_argument", code: "template.unresolved", message: "{{host}} is not defined.", hint: "", details: { names: "host" }, causes: [] },
        }),
      );
      fireEvent.click(button("Connect"));
      await act(settle);
      await act(settle);
      const asked = confirmQueue.current();
      expect(asked).toMatchObject({ title: "Connect with an undefined variable?" });
      act(() => confirmQueue.settle(asked!.id, false));
      await act(settle);
      expect(status().textContent).toBe("Not connected");
      expect(button("Connect")).toBeTruthy();
    } finally {
      detach();
    }
  });

  it("sends binary messages typed as hex, Base64 or loaded from a file, and shows them as hex or Base64 (WS-04, WS-06)", async () => {
    const saved = (name: string, text: string, format = ""): SavedMessageDTO => ({ name, text, format, extra: "" });
    await setup(request({ messageFormat: "hex", message: "de ad b", savedMessages: [saved("Blob", "3q2+7w==", "base64")] }));
    const formatGroup = within(editor()).getByRole("group", { name: "Message format" });
    expect(within(formatGroup).getAllByRole("button").map((b) => b.textContent)).toEqual(["Text", "JSON", "Hex", "Base64"]);
    expect(within(formatGroup).getByRole("button", { name: "Hex" }).getAttribute("aria-pressed")).toBe("true");
    await connectAndOpen();
    // Bad hex is marked and blocks sending.
    expect(button("The hex digit at position 7 has no partner: bytes are pairs of digits.")).toBeTruthy();
    expect(button("Send").disabled).toBe(true);

    const view = viewOf("Message");
    act(() => view.dispatch({ changes: { from: view.state.doc.length, insert: "e" }, userEvent: "input.type" }));
    await act(settle);
    expect(within(editor()).getByText("3 bytes")).toBeTruthy();
    ws.Send.mockResolvedValue({ bytes: 3 });
    fireEvent.click(button("Send"));
    await act(settle);
    expect(ws.Send).toHaveBeenLastCalledWith({ connId: "ws_1", text: "de ad be", format: "hex" });

    // Saved messages keep their format and are sent with it.
    const panel = within(editor()).getByRole("region", { name: "Saved messages" });
    expect(within(panel).getByText("B64")).toBeTruthy();
    fireEvent.click(within(panel).getByRole("button", { name: "Send Blob" }));
    await act(settle);
    expect(ws.Send).toHaveBeenLastCalledWith({ connId: "ws_1", text: "3q2+7w==", format: "base64" });

    // A file goes into the composer as Base64; one too big is refused.
    const fileInput = within(editor()).getByLabelText("File to send") as HTMLInputElement;
    fireEvent.change(fileInput, { target: { files: [new File([Uint8Array.of(0, 1, 2, 255)], "blob.bin")] } });
    await act(settle);
    await act(settle);
    expect(sent().request).toMatchObject({ message: "AAEC/w==", messageFormat: "base64" });
    expect(within(formatGroup).getByRole("button", { name: "Base64" }).getAttribute("aria-pressed")).toBe("true");
    fireEvent.change(fileInput, { target: { files: [new File([new Uint8Array(WS_FILE_MAX_BYTES + 1)], "big.bin")] } });
    await act(settle);
    expect(within(editor()).getByRole("alert").textContent).toContain("big.bin is 1.0 MB");
    expect(sent().request.message).toBe("AAEC/w==");

    // Binary messages in the log: hex, or Base64 for the whole log, and per message in the detail pane.
    act(() =>
      emit("ws:message", {
        connId: "ws_1",
        requestId: "req_feed",
        seq: 3,
        messages: [{ direction: "received", opcode: "binary", at: "2026-09-16T10:00:01.002Z", text: "", data: btoa("\x00\xffb"), size: 3 }],
      }),
    );
    act(() => wsSessions().get("req_feed").log.flush());
    const log = () => within(editor()).getByRole("grid", { name: "WebSocket messages" });
    expect(within(log()).getByText("00 ff 62")).toBeTruthy();
    fireEvent.click(within(within(editor()).getByRole("group", { name: "Show binary messages as" })).getByRole("button", { name: "Base64" }));
    expect(within(log()).getByText("AP9i")).toBeTruthy();
    fireEvent.click(within(log()).getByText("AP9i"));
    const detail = within(editor()).getByRole("region", { name: /^Message \d+$/ });
    const payload = () => within(detail).getByLabelText("Payload").textContent;
    expect(payload()).toBe("AP9i");
    fireEvent.click(within(within(detail).getByRole("group", { name: "Show payload as" })).getByRole("button", { name: "Hexdump" }));
    expect(payload()).toContain("00 ff 62");
    expect(payload()).toContain("|..b|");
  });

  it("names, moves and formats saved messages", () => {
    const m = (name: string): SavedMessageDTO => ({ name, text: "", format: "", extra: "" });
    expect(nextMessageName([])).toBe("Message 1");
    expect(nextMessageName([m("Message 2"), m("x")])).toBe("Message 3");
    expect(nextMessageName([m("Message 3"), m("x")])).toBe("Message 4");
    expect(moveItem(["a", "b", "c"], 2, 0)).toEqual(["c", "a", "b"]);
  });

  it("hints at the scheme and checks subprotocols", () => {
    expect(schemeHint("ws://a")).toBeNull();
    expect(schemeHint("WSS://a")).toBeNull();
    expect(schemeHint("{{base}}/socket")).toBeNull();
    expect(schemeHint("https://a")).toContain("wss://");
    expect(schemeHint("localhost:8080")).toContain("ws:// or wss://");
    expect(subprotocolProblem(["a", "b c"], 1)).toContain("no spaces");
    expect(subprotocolProblem(["a", "a"], 1)).toBe("Listed twice.");
    expect(subprotocolProblem(["a", ""], 1)).toBeNull();
  });
});
