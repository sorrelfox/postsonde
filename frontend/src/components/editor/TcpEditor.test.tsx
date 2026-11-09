import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const collection = vi.hoisted(() => ({
  Request: vi.fn<(id: string) => Promise<unknown>>(),
  UpdateRequest: vi.fn<(input: { request: unknown; revision: number }) => Promise<void>>(),
  Flush: vi.fn<(id: string) => Promise<void>>(),
}));
const tcp = vi.hoisted(() => ({
  Connect: vi.fn<(input: unknown) => Promise<unknown>>(),
  Send: vi.fn<(input: unknown) => Promise<unknown>>(),
  Close: vi.fn<(connId: string) => Promise<void>>(),
  CloseAll: vi.fn(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({
  CollectionService: collection,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/tcp", () => ({
  TCPService: tcp,
}));

import { resetTcpSessions, tcpSessions } from "../../commands/tcpSessions";
import type { RequestDTO } from "../../lib/collection";
import { IS_MAC } from "../../lib/shortcuts/keys";
import type { StateEventDTO } from "../../lib/tcp";
import { createStores, StoreProvider, type Stores, type TreeNode } from "../../stores";
import { emit, resetFakeWails, settle } from "../../test/fakeWails";
import { TabBar } from "../tabs/TabBar";
import { TabPanel } from "../tabs/TabPanel";

function request(over: Partial<RequestDTO> = {}): RequestDTO {
  return {
    id: "req_echo",
    kind: "tcp",
    name: "Echo",
    description: "",
    method: "",
    url: "",
    params: [],
    pathParams: [],
    headers: [],
    body: { mode: "", type: "", text: "", fields: [], parts: [], path: "", contentType: "", extra: "" },
    query: "",
    variables: "",
    operationName: "",
    connectionParams: "",
    schemaFile: "",
    address: "localhost:7000",
    sendMode: "",
    lineEnding: "crlf",
    message: "hello",
    subprotocols: [],
    messageFormat: "",
    service: "",
    proto: { files: [], importPaths: [], inherit: false, extra: "" },
    metadata: [],
    bodyExamples: [],
    auth: { type: "", username: "", password: "", prefix: null, token: "", key: "", value: "", in: "", disabled: false, localSecrets: [], extra: "" },
    settings: { timeoutMs: null, followRedirects: null, maxRedirects: null, followOriginalMethod: null, forwardAuthorization: null, verifyTls: null, connectTimeoutMs: 2500, urlEncoding: "", httpVersion: "", sendCookies: null, storeCookies: null, tls: null, deadlineMs: null, extra: "" },
    extra: '{"x-note":"kept"}',
    ...over,
  };
}

const node = (over: Partial<TreeNode> & { id: string }): TreeNode =>
  ({ kind: "request", parentId: null, name: over.id, path: `${over.id}.json`, fileKind: "tcp", readOnly: "", method: "", ...over }) as TreeNode;

function stateEvent(over: Partial<StateEventDTO>): StateEventDTO {
  return {
    connId: "tcp_1",
    requestId: "req_echo",
    address: "localhost:7000",
    seq: 1,
    state: "open",
    at: "2026-09-16T10:00:00.000Z",
    localAddr: "127.0.0.1:50000",
    remoteAddr: "127.0.0.1:7000",
    tls: null,
    close: null,
    error: null,
    ...over,
  };
}

async function setup(dto: RequestDTO = request()): Promise<Stores> {
  collection.Request.mockImplementation(() => Promise.resolve(structuredClone(dto)));
  const stores = createStores();
  const nodes = [node({ id: "req_echo", name: "Echo" }), node({ id: "other", name: "Other", fileKind: "http", method: "GET" })];
  stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
  stores.collection.dispatch({ type: "collection/load", workspaceId: "ws_1", nodes, rootIds: nodes.map((n) => n.id), readOnly: "" });
  stores.tabs.dispatch({ type: "tabs/restore", order: ["req_echo", "other"], activeId: "req_echo" });
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
const sent = () => {
  const calls = collection.UpdateRequest.mock.calls;
  return calls[calls.length - 1]?.[0] as { request: RequestDTO; revision: number };
};

async function connectAndOpen() {
  tcp.Connect.mockResolvedValue({ connId: "tcp_1", requestId: "req_echo", address: "localhost:7000" });
  fireEvent.click(within(editor()).getByRole("button", { name: "Connect" }));
  await act(settle);
  act(() => emit("tcp:state", stateEvent({ state: "open" })));
}

beforeEach(() => {
  resetTcpSessions();
  resetFakeWails();
  collection.Request.mockReset();
  collection.UpdateRequest.mockReset().mockResolvedValue(undefined);
  collection.Flush.mockReset().mockResolvedValue(undefined);
  tcp.Connect.mockReset();
  tcp.Send.mockReset();
  tcp.Close.mockReset().mockResolvedValue(undefined);
});

describe("TcpEditor", () => {
  it("renders a tcp request with its address, composer and log", async () => {
    await setup();
    expect(screen.getByRole("tab", { name: "TCP Echo" })).toBeTruthy();
    const address = within(editor()).getByRole("textbox", { name: "Address" }) as HTMLInputElement;
    expect(address.value).toBe("localhost:7000");
    expect((within(editor()).getByRole("textbox", { name: "Message" }) as HTMLTextAreaElement).value).toBe("hello");
    expect(within(editor()).getByRole("status").textContent).toBe("Not connected");
    expect((within(editor()).getByRole("combobox", { name: "Line ending" }) as HTMLSelectElement).value).toBe("crlf");
    const sendAs = within(editor()).getByRole("group", { name: "Send as" });
    expect(within(sendAs).getByRole("button", { name: "Text" }).getAttribute("aria-pressed")).toBe("true");
    const showAs = within(editor()).getByRole("group", { name: "Show as" });
    expect(within(showAs).getByRole("button", { name: "Text" }).getAttribute("aria-pressed")).toBe("true");
    expect((within(editor()).getByRole("button", { name: "Send" }) as HTMLButtonElement).disabled).toBe(true);
    expect(within(editor()).getByRole("grid", { name: "TCP messages" })).toBeTruthy();
    expect(collection.UpdateRequest).not.toHaveBeenCalled();
  });

  it("auto-saves the address, mode, line ending and message with the rest of the request", async () => {
    await setup();
    fireEvent.change(within(editor()).getByRole("textbox", { name: "Address" }), { target: { value: "localhost:7001" } });
    expect(sent()).toEqual({ request: request({ address: "localhost:7001" }), revision: 1, baseVersion: 0 });
    await act(settle);
    const sendAs = within(editor()).getByRole("group", { name: "Send as" });
    fireEvent.click(within(sendAs).getByRole("button", { name: "Hex" }));
    await act(settle);
    fireEvent.change(within(editor()).getByRole("textbox", { name: "Message" }), { target: { value: "DE AD" } });
    await act(settle);
    expect(sent().request).toEqual(request({ address: "localhost:7001", sendMode: "hex", message: "DE AD" }));
    // Hex messages are sent as written.
    expect((within(editor()).getByRole("combobox", { name: "Line ending" }) as HTMLSelectElement).disabled).toBe(true);
  });

  it("connects, sends with the button and ⌘Enter, and disconnects", async () => {
    await setup();
    await connectAndOpen();
    expect(tcp.Connect).toHaveBeenCalledWith({
      requestId: "req_echo",
      address: "localhost:7000",
      connectTimeoutMs: 2500,
      tls: false,
      verifyTls: null,
      framing: null,
    });
    expect(within(editor()).getByRole("status").textContent).toBe("Connected to 127.0.0.1:7000 · 0 B sent · 0 B received");

    tcp.Send.mockResolvedValue({ bytes: 7 });
    const send = within(editor()).getByRole("button", { name: "Send" }) as HTMLButtonElement;
    expect(send.disabled).toBe(false);
    fireEvent.click(send);
    await act(settle);
    expect(tcp.Send).toHaveBeenLastCalledWith({ connId: "tcp_1", mode: "text", text: "hello", lineEnding: "crlf" });

    const message = within(editor()).getByRole("textbox", { name: "Message" });
    fireEvent.keyDown(message, { key: "Enter", ...(IS_MAC ? { metaKey: true } : { ctrlKey: true }) });
    await act(settle);
    expect(tcp.Send).toHaveBeenCalledTimes(2);

    fireEvent.click(within(editor()).getByRole("button", { name: "Disconnect" }));
    await act(settle);
    expect(tcp.Close).toHaveBeenCalledWith("tcp_1");
  });

  it("saves TLS and framing, connects with them and sends framed messages", async () => {
    await setup();
    const options = () => within(editor()).getByRole("group", { name: "Connection options" });
    expect(within(options()).queryByRole("checkbox", { name: "Verify certificate" })).toBeNull();

    fireEvent.click(within(editor()).getByRole("button", { name: "TLS" }));
    await act(settle);
    expect(sent().request.settings).toMatchObject({ tls: true, verifyTls: null });
    const verify = within(options()).getByRole("checkbox", { name: "Verify certificate" }) as HTMLInputElement;
    expect(verify.checked).toBe(true);
    fireEvent.click(verify);
    await act(settle);
    expect(sent().request.settings).toMatchObject({ tls: true, verifyTls: false });

    fireEvent.change(within(options()).getByRole("combobox", { name: "Framing" }), { target: { value: "length" } });
    await act(settle);
    expect(sent().request.framing).toEqual({ mode: "length", delimiter: "", prefixBytes: 0, byteOrder: "", maxMessageBytes: null, extra: "" });
    fireEvent.change(within(options()).getByRole("combobox", { name: "Length prefix" }), { target: { value: "2" } });
    await act(settle);
    fireEvent.change(within(options()).getByRole("combobox", { name: "Byte order" }), { target: { value: "little" } });
    await act(settle);
    const max = within(options()).getByRole("spinbutton", { name: "Max message" }) as HTMLInputElement;
    fireEvent.change(max, { target: { value: "0" } });
    await act(settle);
    expect(max.getAttribute("aria-invalid")).toBe("true");
    fireEvent.change(max, { target: { value: "4096" } });
    await act(settle);
    expect(sent().request.framing).toEqual({ mode: "length", delimiter: "", prefixBytes: 2, byteOrder: "little", maxMessageBytes: 4096, extra: "" });
    // The framing ends each message: no line ending.
    expect((within(editor()).getByRole("combobox", { name: "Line ending" }) as HTMLSelectElement).disabled).toBe(true);

    await connectAndOpen();
    expect(tcp.Connect).toHaveBeenCalledWith({
      requestId: "req_echo",
      address: "localhost:7000",
      connectTimeoutMs: 2500,
      tls: true,
      verifyTls: false,
      framing: { mode: "length", delimiter: "", prefixBytes: 2, byteOrder: "little", maxMessageBytes: 4096 },
    });
    // Settings apply when connecting: locked while connected.
    expect((within(editor()).getByRole("button", { name: "TLS" }) as HTMLButtonElement).disabled).toBe(true);
    expect((within(options()).getByRole("combobox", { name: "Framing" }) as HTMLSelectElement).disabled).toBe(true);
    act(() =>
      emit(
        "tcp:state",
        stateEvent({ seq: 2, tls: { version: "TLS 1.3", cipherSuite: "TLS_AES_128_GCM_SHA256", serverName: "localhost", peerSubject: "CN=server" } }),
      ),
    );
    expect(within(editor()).getByRole("status").textContent).toBe("Connected to 127.0.0.1:7000 (TLS 1.3) · 0 B sent · 0 B received");

    // An empty message is a message with a framing.
    fireEvent.change(within(editor()).getByRole("textbox", { name: "Message" }), { target: { value: "" } });
    await act(settle);
    tcp.Send.mockResolvedValue({ bytes: 0 });
    fireEvent.click(within(editor()).getByRole("button", { name: "Send" }));
    await act(settle);
    expect(tcp.Send).toHaveBeenLastCalledWith({ connId: "tcp_1", mode: "text", text: "", lineEnding: "crlf" });
  });

  it("flags invalid hex before sending", async () => {
    await setup(request({ sendMode: "hex", message: "DE AG" }));
    await connectAndOpen();
    const message = within(editor()).getByRole("textbox", { name: "Message" });
    expect(message.getAttribute("aria-invalid")).toBe("true");
    expect(within(editor()).getByText("“G” at position 5 is not a hex digit.")).toBeTruthy();
    expect((within(editor()).getByRole("button", { name: "Send" }) as HTMLButtonElement).disabled).toBe(true);

    fireEvent.change(message, { target: { value: "DE AD" } });
    await act(settle);
    expect(message.getAttribute("aria-invalid")).toBe("false");
    fireEvent.click(within(editor()).getByRole("button", { name: "Send" }));
    await act(settle);
    expect(tcp.Send).toHaveBeenLastCalledWith({ connId: "tcp_1", mode: "hex", text: "DE AD", lineEnding: "crlf" });
  });

  it("shows received data in the log as text or hex, and keeps the connection across tab switches", async () => {
    const stores = await setup();
    await connectAndOpen();
    act(() =>
      emit("tcp:data", {
        connId: "tcp_1",
        requestId: "req_echo",
        seq: 3,
        chunks: [{ direction: "received", at: "2026-09-16T10:00:01.000Z", data: btoa("pong\r\n"), size: 6, incomplete: false }],
      }),
    );
    const session = tcpSessions().get("req_echo");
    act(() => {
      session.textLog.flush();
      session.hexLog.flush();
    });
    const log = () => within(editor()).getByRole("grid", { name: "TCP messages" });
    expect(within(log()).getByText(/pong/)).toBeTruthy();
    expect(within(editor()).getByRole("status").textContent).toContain("6 B received");

    const showAs = within(editor()).getByRole("group", { name: "Show as" });
    fireEvent.click(within(showAs).getByRole("button", { name: "Hex" }));
    expect(within(log()).getByText(/70 6f 6e 67 0d 0a/)).toBeTruthy();

    // Another tab and back: same connection, same log and view.
    act(() => stores.tabs.dispatch({ type: "tabs/activate", id: "other" }));
    await act(settle);
    act(() => stores.tabs.dispatch({ type: "tabs/activate", id: "req_echo" }));
    await act(settle);
    expect(within(editor()).getByRole("button", { name: "Disconnect" })).toBeTruthy();
    expect(within(log()).getByText(/70 6f 6e 67 0d 0a/)).toBeTruthy();
    expect(tcp.Close).not.toHaveBeenCalled();

    // A failure shows in the status line.
    act(() =>
      emit(
        "tcp:state",
        stateEvent({
          state: "closed",
          seq: 4,
          error: { kind: "network", code: "netx.connection_reset", message: "The server reset the connection.", hint: "", details: {}, causes: [] },
          close: { initiatedBy: "remote", bytesSent: 0, bytesReceived: 6, chunksSent: 0, chunksReceived: 1, durationMs: 5 },
        }),
      ),
    );
    expect(within(editor()).getByRole("status").textContent).toBe("The server reset the connection.");
    expect(within(editor()).getByRole("button", { name: "Connect" })).toBeTruthy();
  });
});
