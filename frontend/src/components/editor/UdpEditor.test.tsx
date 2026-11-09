import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const collection = vi.hoisted(() => ({
  Request: vi.fn<(id: string) => Promise<unknown>>(),
  UpdateRequest: vi.fn<(input: { request: unknown; revision: number }) => Promise<void>>(),
  Flush: vi.fn<(id: string) => Promise<void>>(),
}));
const udp = vi.hoisted(() => ({
  Open: vi.fn<(input: unknown) => Promise<unknown>>(),
  Send: vi.fn<(input: unknown) => Promise<unknown>>(),
  Close: vi.fn<(connId: string) => Promise<void>>(),
  CloseAll: vi.fn(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({
  CollectionService: collection,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/udp", () => ({
  UDPService: udp,
}));

import { resetUdpSessions } from "../../commands/udpSessions";
import type { RequestDTO } from "../../lib/collection";
import { IS_MAC } from "../../lib/shortcuts/keys";
import type { StateEventDTO } from "../../lib/udp";
import { createStores, StoreProvider, type TreeNode } from "../../stores";
import { emit, resetFakeWails, settle } from "../../test/fakeWails";
import { TabBar } from "../tabs/TabBar";
import { TabPanel } from "../tabs/TabPanel";

function request(over: Partial<RequestDTO> = {}): RequestDTO {
  return {
    id: "req_dns",
    kind: "udp",
    name: "DNS",
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
    address: "127.0.0.1:5353",
    sendMode: "",
    lineEnding: "",
    message: "hello",
    udp: { connected: false, ipVersion: "", maxDatagramBytes: null, openTimeoutMs: null },
    subprotocols: [],
    messageFormat: "",
    service: "",
    proto: { files: [], importPaths: [], inherit: false, extra: "" },
    metadata: [],
    bodyExamples: [],
    auth: { type: "", username: "", password: "", prefix: null, token: "", key: "", value: "", in: "", disabled: false, localSecrets: [], extra: "" },
    settings: { timeoutMs: null, followRedirects: null, maxRedirects: null, followOriginalMethod: null, forwardAuthorization: null, verifyTls: null, connectTimeoutMs: null, urlEncoding: "", httpVersion: "", sendCookies: null, storeCookies: null, tls: null, deadlineMs: null, extra: '{"ttl":4}' },
    extra: "",
    ...over,
  };
}

const node = (over: Partial<TreeNode> & { id: string }): TreeNode =>
  ({ kind: "request", parentId: null, name: over.id, path: `${over.id}.json`, fileKind: "udp", readOnly: "", method: "", ...over }) as TreeNode;

function stateEvent(over: Partial<StateEventDTO>): StateEventDTO {
  return {
    connId: "udp_1",
    requestId: "req_dns",
    address: "127.0.0.1:5353",
    seq: 1,
    state: "open",
    at: "2026-10-26T10:00:00.000Z",
    localAddr: "127.0.0.1:50000",
    remoteAddr: "127.0.0.1:5353",
    via: "",
    proxy: null,
    connected: false,
    maxPayload: 65507,
    close: null,
    error: null,
    ...over,
  };
}

async function setup(dto: RequestDTO = request()) {
  collection.Request.mockImplementation(() => Promise.resolve(structuredClone(dto)));
  const stores = createStores();
  const nodes = [node({ id: "req_dns", name: "DNS" })];
  stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
  stores.collection.dispatch({ type: "collection/load", workspaceId: "ws_1", nodes, rootIds: nodes.map((n) => n.id), readOnly: "" });
  stores.tabs.dispatch({ type: "tabs/restore", order: ["req_dns"], activeId: "req_dns" });
  render(
    <StoreProvider stores={stores}>
      <TabBar />
      <TabPanel />
    </StoreProvider>,
  );
  await act(settle);
}

const editor = () => document.querySelector("[data-request-id]") as HTMLElement;
const sent = () => {
  const calls = collection.UpdateRequest.mock.calls;
  return calls[calls.length - 1]?.[0] as { request: RequestDTO; revision: number };
};

async function openSocket(over: Partial<StateEventDTO> = {}) {
  udp.Open.mockResolvedValue({ connId: "udp_1", requestId: "req_dns", address: "127.0.0.1:5353" });
  fireEvent.click(within(editor()).getByRole("button", { name: "Open" }));
  await act(settle);
  act(() => emit("udp:state", stateEvent(over)));
}

beforeEach(() => {
  resetUdpSessions();
  resetFakeWails();
  collection.Request.mockReset();
  collection.UpdateRequest.mockReset().mockResolvedValue(undefined);
  collection.Flush.mockReset().mockResolvedValue(undefined);
  udp.Open.mockReset();
  udp.Send.mockReset();
  udp.Close.mockReset().mockResolvedValue(undefined);
});

describe("UdpEditor", () => {
  it("renders a udp request with its address, options, composer and log", async () => {
    await setup();
    expect(screen.getByRole("tab", { name: "UDP DNS" })).toBeTruthy();
    expect((within(editor()).getByRole("textbox", { name: "Address" }) as HTMLInputElement).value).toBe("127.0.0.1:5353");
    expect((within(editor()).getByRole("textbox", { name: "Datagram" }) as HTMLTextAreaElement).value).toBe("hello");
    expect(within(editor()).getByRole("status").textContent).toBe("Not open");
    const options = within(editor()).getByRole("group", { name: "Socket options" });
    expect((within(options).getByRole("combobox", { name: "Receive from" }) as HTMLSelectElement).value).toBe("any");
    expect((within(options).getByRole("combobox", { name: "IP version" }) as HTMLSelectElement).value).toBe("");
    const sendAs = within(editor()).getByRole("group", { name: "Send as" });
    expect(within(sendAs).getByRole("button", { name: "Text" }).getAttribute("aria-pressed")).toBe("true");
    expect((within(editor()).getByRole("button", { name: "Send" }) as HTMLButtonElement).disabled).toBe(true);
    expect(within(editor()).getByRole("grid", { name: "UDP datagrams" })).toBeTruthy();
    expect(collection.UpdateRequest).not.toHaveBeenCalled();
  });

  it("auto-saves the address, options, mode and datagram, keeping unknown members", async () => {
    await setup();
    fireEvent.change(within(editor()).getByRole("textbox", { name: "Address" }), { target: { value: "{{dns}}:53" } });
    await act(settle);
    const options = within(editor()).getByRole("group", { name: "Socket options" });
    fireEvent.change(within(options).getByRole("combobox", { name: "Receive from" }), { target: { value: "target" } });
    await act(settle);
    fireEvent.change(within(options).getByRole("combobox", { name: "IP version" }), { target: { value: "ipv6" } });
    await act(settle);
    fireEvent.change(within(options).getByRole("spinbutton", { name: "Max datagram" }), { target: { value: "1500" } });
    await act(settle);
    const sendAs = within(editor()).getByRole("group", { name: "Send as" });
    fireEvent.click(within(sendAs).getByRole("button", { name: "Base64" }));
    await act(settle);
    fireEvent.change(within(editor()).getByRole("textbox", { name: "Datagram" }), { target: { value: "aGk=" } });
    await act(settle);
    expect(sent().request).toEqual(
      request({
        address: "{{dns}}:53",
        sendMode: "base64",
        message: "aGk=",
        udp: { connected: true, ipVersion: "ipv6", maxDatagramBytes: 1500, openTimeoutMs: null },
      }),
    );
    // An invalid maximum is marked and not saved.
    const max = within(options).getByRole("spinbutton", { name: "Max datagram" });
    fireEvent.change(max, { target: { value: "70000" } });
    expect(max.getAttribute("aria-invalid")).toBe("true");
    expect(sent().request.udp?.maxDatagramBytes).toBe(1500);
  });

  it("opens with the options, sends with the button and ⌘Enter, and closes", async () => {
    await setup(request({ udp: { connected: true, ipVersion: "ipv4", maxDatagramBytes: 512, openTimeoutMs: 2000 } }));
    await openSocket({ connected: true, maxPayload: 512 });
    expect(udp.Open).toHaveBeenCalledWith({
      requestId: "req_dns",
      address: "127.0.0.1:5353",
      ipVersion: "ipv4",
      connected: true,
      maxDatagramBytes: 512,
      openTimeoutMs: 2000,
    });
    expect(within(editor()).getByRole("status").textContent).toBe(
      "Ready to send to 127.0.0.1:5353 · replies from the target only · 0 datagrams sent · 0 received",
    );
    const options = within(editor()).getByRole("group", { name: "Socket options" });
    expect((within(options).getByRole("combobox", { name: "Receive from" }) as HTMLSelectElement).disabled).toBe(true);

    udp.Send.mockResolvedValue({ bytes: 5 });
    fireEvent.click(within(editor()).getByRole("button", { name: "Send" }));
    await act(settle);
    expect(udp.Send).toHaveBeenLastCalledWith({ connId: "udp_1", mode: "text", text: "hello" });
    fireEvent.keyDown(within(editor()).getByRole("textbox", { name: "Datagram" }), { key: "Enter", ...(IS_MAC ? { metaKey: true } : { ctrlKey: true }) });
    await act(settle);
    expect(udp.Send).toHaveBeenCalledTimes(2);

    act(() =>
      emit("udp:data", {
        connId: "udp_1",
        requestId: "req_dns",
        seq: 3,
        datagrams: [{ direction: "received", at: "2026-10-26T10:00:01.000Z", data: btoa("hello"), size: 5, truncated: false, peer: "127.0.0.1:5353", error: null }],
      }),
    );
    expect(within(editor()).getByRole("status").textContent).toContain("1 received");

    fireEvent.click(within(editor()).getByRole("button", { name: "Close" }));
    await act(settle);
    expect(udp.Close).toHaveBeenCalledWith("udp_1");
  });

  it("checks hex, Base64 and the size while typing", async () => {
    await setup(request({ sendMode: "hex", message: "DE AD" }));
    await openSocket({ maxPayload: 2 });
    const send = () => within(editor()).getByRole("button", { name: "Send" }) as HTMLButtonElement;
    const datagram = () => within(editor()).getByRole("textbox", { name: "Datagram" });
    expect(send().disabled).toBe(false);
    fireEvent.change(datagram(), { target: { value: "DE AD BE" } });
    await act(settle);
    expect(datagram().getAttribute("aria-invalid")).toBe("true");
    expect(send().disabled).toBe(true);
    expect(within(editor()).getByText(/at most 2 B can be sent/)).toBeTruthy();
    fireEvent.change(datagram(), { target: { value: "DE A" } });
    await act(settle);
    expect(send().disabled).toBe(true);
    const sendAs = within(editor()).getByRole("group", { name: "Send as" });
    fireEvent.click(within(sendAs).getByRole("button", { name: "Base64" }));
    await act(settle);
    fireEvent.change(datagram(), { target: { value: "a$" } });
    await act(settle);
    expect(datagram().getAttribute("aria-invalid")).toBe("true");
    fireEvent.change(datagram(), { target: { value: "aGk=" } });
    await act(settle);
    expect(send().disabled).toBe(false);
  });

  it("shows the proxy in the status and a failed open", async () => {
    await setup();
    await openSocket({ via: "socks5h://proxy.lan:1080", remoteAddr: "10.0.0.1:40000" });
    expect(within(editor()).getByRole("status").textContent).toContain("Ready to send to 127.0.0.1:5353 through socks5h://proxy.lan:1080");
    act(() =>
      emit(
        "udp:state",
        stateEvent({
          state: "closed",
          error: {
            kind: "network",
            code: "netx.socks_udp_association_closed",
            message: "The SOCKS proxy at proxy.lan:1080 ended the UDP association.",
            hint: "Start the UDP request again.",
            details: {},
            causes: [],
          },
        }),
      ),
    );
    expect(within(editor()).getByRole("status").textContent).toBe(
      "The SOCKS proxy at proxy.lan:1080 ended the UDP association. Start the UDP request again.",
    );
    expect(within(editor()).getByRole("button", { name: "Open" })).toBeTruthy();
  });
});
