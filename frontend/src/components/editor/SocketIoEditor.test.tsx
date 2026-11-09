import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const collection = vi.hoisted(() => ({
  Request: vi.fn<(id: string) => Promise<unknown>>(),
  UpdateRequest: vi.fn<(input: { request: unknown; revision: number }) => Promise<void>>(),
  Flush: vi.fn<(id: string) => Promise<void>>(),
}));
const sio = vi.hoisted(() => ({
  Connect: vi.fn<(input: unknown) => Promise<unknown>>(),
  Emit: vi.fn<(input: unknown) => Promise<unknown>>(),
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
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/socketio", () => ({
  SocketIOService: sio,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/format", () => ({
  FormatService: format,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http", () => ({
  HTTPService: http,
}));
vi.mock(
  "../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/ops",
  async () => (await import("../../test/fakeWails")).fakeOpsBindings,
);

import { resetSioSessions } from "../../commands/sioSessions";
import type { RequestDTO } from "../../lib/collection";
import type { ItemDTO } from "../../lib/socketio";
import { createStores, StoreProvider, type TreeNode } from "../../stores";
import { emit, opsCancel, resetFakeWails, settle } from "../../test/fakeWails";
import { TabBar } from "../tabs/TabBar";
import { TabPanel } from "../tabs/TabPanel";
import { authLooksLikeObject, sioSchemeHint } from "./SocketIoEditor";

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
    id: "req_chat",
    kind: "socketio",
    name: "Chat",
    description: "",
    method: "",
    url: "http://localhost:3000/admin?room=lobby",
    params: [{ name: "room", value: "lobby", description: "", enabled: true, extra: "" }],
    pathParams: [],
    headers: [{ name: "X-Client", value: "postsonde", description: "", enabled: true, extra: "" }],
    body: { mode: "", type: "", text: "", fields: [], parts: [], path: "", contentType: "", extra: "" },
    query: "",
    variables: "",
    operationName: "",
    connectionParams: "",
    schemaFile: "",
    address: "",
    sendMode: "",
    lineEnding: "",
    message: '["hi"]',
    subprotocols: [],
    messageFormat: "",
    path: "/rt/",
    authPayload: '{"token":"t"}',
    transport: "polling",
    event: "chat",
    ack: true,
    savedMessages: [{ name: "Join", text: '["lobby"]', format: "", event: "join", ack: true, extra: "" }],
    service: "",
    proto: { files: [], importPaths: [], inherit: false, extra: "" },
    metadata: [],
    bodyExamples: [],
    auth: { type: "", username: "", password: "", prefix: null, token: "", key: "", value: "", in: "", disabled: false, localSecrets: [], extra: "" },
    settings: { timeoutMs: null, followRedirects: null, maxRedirects: null, followOriginalMethod: null, forwardAuthorization: null, verifyTls: null, connectTimeoutMs: null, urlEncoding: "", httpVersion: "", sendCookies: null, storeCookies: null, tls: null, deadlineMs: null, ackTimeoutMs: 5000, extra: "" },
    extra: "",
    ...over,
  };
}

function item(over: Partial<ItemDTO>): ItemDTO {
  return {
    type: "event",
    at: "2026-09-19T10:00:00.000Z",
    direction: "",
    transport: "",
    url: "",
    sid: "",
    pingIntervalMs: 0,
    pingTimeoutMs: 0,
    maxPayload: 0,
    upgrades: null,
    namespace: "/admin",
    socketId: "",
    event: "",
    args: "",
    ackId: null,
    elapsedMs: 0,
    packetType: "",
    data: "",
    attachments: 0,
    size: 0,
    reason: "",
    error: null,
    tests: null,
    ...over,
  };
}

const node = (over: Partial<TreeNode> & { id: string }): TreeNode =>
  ({ kind: "request", parentId: null, name: over.id, path: `${over.id}.json`, fileKind: "socketio", readOnly: "", method: "", ...over }) as TreeNode;

async function setup(dto: RequestDTO = request()) {
  collection.Request.mockImplementation(() => Promise.resolve(structuredClone(dto)));
  const stores = createStores();
  const nodes = [node({ id: "req_chat", name: "Chat" })];
  stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
  stores.collection.dispatch({ type: "collection/load", workspaceId: "ws_1", nodes, rootIds: ["req_chat"], readOnly: "" });
  stores.tabs.dispatch({ type: "tabs/restore", order: ["req_chat"], activeId: "req_chat" });
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
const button = (name: string) => within(editor()).getByRole("button", { name }) as HTMLButtonElement;
const status = () => within(editor()).getAllByRole("status")[0] as HTMLElement;
const sent = () => {
  const calls = collection.UpdateRequest.mock.calls;
  return calls[calls.length - 1]?.[0] as { request: RequestDTO; revision: number };
};

async function connectAndOpen() {
  sio.Connect.mockResolvedValue({ opId: "op_1" });
  fireEvent.click(button("Connect"));
  await act(settle);
  act(() =>
    emit("sio:op:data", {
      opId: "op_1",
      seq: 1,
      items: [
        item({ type: "open", transport: "polling", url: "http://localhost:3000/rt/", sid: "e1", pingIntervalMs: 25000, pingTimeoutMs: 20000 }),
        item({ type: "connected", transport: "polling", socketId: "s1" }),
      ],
    }),
  );
}

beforeEach(() => {
  resetSioSessions();
  resetFakeWails();
  collection.Request.mockReset();
  collection.UpdateRequest.mockReset().mockResolvedValue(undefined);
  collection.Flush.mockReset().mockResolvedValue(undefined);
  sio.Connect.mockReset();
  sio.Emit.mockReset();
  http.Variables.mockReset().mockImplementation(() => cancellable({ variables: [], environment: "" }));
  format.ValidateJSON.mockReset().mockImplementation(() => cancellable({ valid: true, error: null }));
});

describe("SocketIoEditor", () => {
  it("renders a socketio request, connects with its options, emits and disconnects (SIO-01, SIO-03, SIO-05)", async () => {
    await setup();
    expect(screen.getByRole("tab", { name: "SIO Chat" })).toBeTruthy();
    expect(within(editor()).getByRole("textbox", { name: "URL" }).textContent).toBe("http://localhost:3000/admin?room=lobby");
    const tabs = within(editor()).getByRole("tablist", { name: "Connection options" });
    expect(within(tabs).getAllByRole("tab").map((t) => t.textContent)).toEqual(["Params1", "Headers1", "Auth1", "Options", "Tests", "Settings", "Notes"]);
    expect(status().textContent).toBe("Not connected");
    expect(button("Emit").disabled).toBe(true);

    await connectAndOpen();
    expect(sio.Connect).toHaveBeenCalledWith({
      requestId: "req_chat",
      url: "http://localhost:3000/admin?room=lobby",
      headers: [{ name: "X-Client", value: "postsonde", enabled: true }],
      path: "/rt/",
      namespace: "",
      authPayload: '{"token":"t"}',
      transport: "polling",
      version: "",
      connectTimeoutMs: null,
      ackTimeoutMs: 5000,
      verifyTls: null,
      sendCookies: null,
    });
    expect(status().textContent).toBe("Connected to /admin over long-polling · socket s1 · 0 emitted · 0 received");

    sio.Emit.mockResolvedValue({ ackId: 0, size: 12, attachments: 0 });
    fireEvent.click(button("Emit"));
    await act(settle);
    expect(sio.Emit).toHaveBeenLastCalledWith({ opId: "op_1", event: "chat", args: '["hi"]', ack: true });

    // A saved emit is sent with its own event and ack.
    fireEvent.click(button("Send Join"));
    await act(settle);
    expect(sio.Emit).toHaveBeenLastCalledWith({ opId: "op_1", event: "join", args: '["lobby"]', ack: true });

    fireEvent.click(button("Disconnect"));
    await act(settle);
    expect(opsCancel).toHaveBeenCalledWith("op_1");
    act(() => {
      emit("sio:op:data", { opId: "op_1", seq: 2, items: [item({ type: "closed", reason: "io client disconnect" })] });
      emit("sio:op:done", { opId: "op_1", seq: 3, status: "cancelled", error: null, items: 3, batches: 2, elapsedMs: 10 });
    });
    expect(status().textContent).toBe("Disconnected");
    expect(button("Connect")).toBeTruthy();
  });

  it("has the Auth tab of HTTP requests and sends its credentials in the handshake or the auth payload (AUTH-31)", async () => {
    const base = request();
    await setup(request({ auth: { ...base.auth, type: "bearer", token: "{{token}}" } }));
    fireEvent.click(within(editor()).getByRole("tab", { name: "Auth1" }));
    const section = document.querySelector("[data-auth-type]") as HTMLElement;
    expect(within(section).getByRole("radio", { name: "Bearer" })).toHaveProperty("checked", true);
    const handshake = within(editor()).getByRole("radio", { name: /the handshake/ }) as HTMLInputElement;
    expect(handshake.checked).toBe(true);
    fireEvent.click(within(editor()).getByRole("radio", { name: /the auth payload/ }));
    await act(settle);
    expect(sent().request.authIn).toBe("payload");

    await connectAndOpen();
    expect(sio.Connect).toHaveBeenCalledWith(expect.objectContaining({ authPayload: '{"token":"t"}', authIn: "payload" }));
  });

  it("saves the options, the listen filter, the event row and saved emits", async () => {
    await setup();
    fireEvent.click(within(editor()).getByRole("tab", { name: "Options" }));
    fireEvent.change(within(editor()).getByRole("textbox", { name: "Namespace" }), { target: { value: "/chat" } });
    await act(settle);
    expect(sent().request.namespace).toBe("/chat");
    fireEvent.change(within(editor()).getByRole("combobox", { name: "Transport" }), { target: { value: "upgrade" } });
    await act(settle);
    expect(sent().request.transport).toBe("upgrade");
    expect(within(editor()).getByText(/the server pings/)).toBeTruthy();
    fireEvent.change(within(editor()).getByRole("combobox", { name: "Server version" }), { target: { value: "v2" } });
    await act(settle);
    expect(sent().request.socketIoVersion).toBe("v2");
    expect(within(editor()).getByText(/the client pings/)).toBeTruthy();
    fireEvent.change(within(editor()).getByRole("textbox", { name: "Show events" }), { target: { value: "chat, typing, chat," } });
    await act(settle);
    expect(sent().request.listen).toEqual(["chat", "typing"]);

    fireEvent.change(within(editor()).getByRole("textbox", { name: "Event" }), { target: { value: "typing" } });
    fireEvent.click(within(editor()).getByRole("checkbox", { name: "Ack" }));
    await act(settle);
    expect(sent().request).toMatchObject({ event: "typing", ack: false });

    fireEvent.click(button("Save"));
    await act(settle);
    expect(sent().request.savedMessages).toEqual([
      { name: "Join", text: '["lobby"]', format: "", event: "join", ack: true, extra: "" },
      { name: "Emit 2", text: '["hi"]', format: "", event: "typing", ack: false, extra: "" },
    ]);

    fireEvent.click(within(editor()).getByRole("tab", { name: "Settings" }));
    fireEvent.change(within(editor()).getByRole("spinbutton", { name: /Ack timeout/ }), { target: { value: "" } });
    await act(settle);
    expect(sent().request.settings.ackTimeoutMs).toBeNull();
  });
});

describe("helpers", () => {
  it("hints at schemes Socket.IO cannot use and checks the auth payload's shape", () => {
    expect(sioSchemeHint("")).toBeNull();
    expect(sioSchemeHint("{{base}}/x")).toBeNull();
    expect(sioSchemeHint("https://h")).toBeNull();
    expect(sioSchemeHint("WSS://h")).toBeNull();
    expect(sioSchemeHint("ftp://h")).not.toBeNull();
    expect(authLooksLikeObject("")).toBe(true);
    expect(authLooksLikeObject(' {"a":1}')).toBe(true);
    expect(authLooksLikeObject("[1]")).toBe(false);
  });
});
