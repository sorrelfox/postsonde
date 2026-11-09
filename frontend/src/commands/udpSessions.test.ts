import { beforeEach, describe, expect, it, vi } from "vitest";

const udp = vi.hoisted(() => ({
  Open: vi.fn<(input: unknown) => Promise<unknown>>(),
  Send: vi.fn<(input: unknown) => Promise<unknown>>(),
  Close: vi.fn<(connId: string) => Promise<void>>(),
  CloseAll: vi.fn(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/udp", () => ({
  UDPService: udp,
}));

import type { DataEventDTO, DatagramDTO, StateEventDTO } from "../lib/udp";
import { createStores } from "../stores";
import { deferred, emit, listenerCount, resetFakeWails, settle } from "../test/fakeWails";
import { closeTabs } from "./tabs";
import { resetUdpSessions, udpSessions } from "./udpSessions";

function stateEvent(over: Partial<StateEventDTO>): StateEventDTO {
  return {
    connId: "udp_1",
    requestId: "req_dns",
    address: "127.0.0.1:53",
    seq: 1,
    state: "opening",
    at: "2026-10-26T10:00:00.000Z",
    localAddr: "",
    remoteAddr: "",
    via: "",
    proxy: null,
    connected: false,
    maxPayload: 0,
    close: null,
    error: null,
    ...over,
  };
}

function dataEvent(datagrams: DataEventDTO["datagrams"], over: Partial<DataEventDTO> = {}): DataEventDTO {
  return { connId: "udp_1", requestId: "req_dns", seq: 2, datagrams, ...over };
}

const datagram = (over: Partial<DatagramDTO>): DatagramDTO => ({
  direction: "received",
  at: "2026-10-26T10:00:01.000Z",
  data: "",
  size: 0,
  truncated: false,
  peer: "127.0.0.1:53",
  error: null,
  ...over,
});

beforeEach(() => {
  resetUdpSessions();
  resetFakeWails();
  udp.Open.mockReset();
  udp.Send.mockReset();
  udp.Close.mockReset().mockResolvedValue(undefined);
});

describe("UdpSession", () => {
  it("follows the socket's events, the datagrams with their peers, also events before Open resolves", async () => {
    const started = deferred<unknown>();
    udp.Open.mockReturnValue(started.promise);
    const session = udpSessions().get("req_dns");
    const opening = session.open("{{host}}:53", { connected: true, ipVersion: "ipv4", maxDatagramBytes: 1500 });
    expect(udp.Open).toHaveBeenCalledWith({
      requestId: "req_dns",
      address: "{{host}}:53",
      ipVersion: "ipv4",
      connected: true,
      maxDatagramBytes: 1500,
      openTimeoutMs: null,
    });
    expect(session.getState().status).toBe("opening");

    emit("udp:state", stateEvent({ state: "opening" }));
    emit("udp:state", stateEvent({ connId: "udp_old", state: "closed", seq: 9 }));
    emit("udp:state", stateEvent({ state: "open", seq: 2, localAddr: "127.0.0.1:50000", remoteAddr: "127.0.0.1:53", connected: true, maxPayload: 1500 }));
    started.resolve({ connId: "udp_1", requestId: "req_dns", address: "127.0.0.1:53" });
    await opening;
    expect(session.getState()).toMatchObject({ status: "open", connId: "udp_1", address: "127.0.0.1:53", connected: true, maxPayload: 1500 });

    emit(
      "udp:data",
      dataEvent([
        datagram({ direction: "sent", data: btoa("ping"), size: 4 }),
        datagram({ data: btoa("pong"), size: 4, peer: "127.0.0.1:53" }),
        datagram({ data: btoa("\xc3\xa9"), size: 2, peer: "10.0.0.9:5353" }),
        datagram({ data: btoa("cut"), size: 1400, truncated: true, peer: "10.0.0.9:5353" }),
        datagram({
          direction: "note",
          peer: "",
          error: { kind: "network", code: "udpclient.port_unreachable", message: "Nothing listens.", hint: "Is it running?", details: {}, causes: [] },
        }),
      ]),
    );
    expect(session.getState()).toMatchObject({ datagramsSent: 1, datagramsReceived: 3 });
    session.textLog.flush();
    session.hexLog.flush();
    const text = [0, 1, 2, 3, 4, 5, 6].map((seq) => session.textLog.get(seq));
    expect(text.map((m) => [m?.direction, m?.kind, m?.text, m?.badge])).toEqual([
      ["system", "connecting", "Opening a UDP socket to 127.0.0.1:53", undefined],
      ["system", "connected", "Ready to send to 127.0.0.1:53 from 127.0.0.1:50000; replies from the target only", undefined],
      ["sent", "text", "ping", undefined],
      ["received", "text", "pong", "127.0.0.1:53"],
      ["received", "text", "é", "10.0.0.9:5353"],
      ["received", "text", "cut", "10.0.0.9:5353, cut"],
      ["system", "dropped", "Nothing listens. Is it running?", undefined],
    ]);
    expect(text[5]?.size).toBe(1400);
    expect(text[3]?.meta).toEqual({ from: "127.0.0.1:53", truncated: false });
    expect(session.hexLog.get(4)?.kind).toBe("binary");
    expect([...(session.hexLog.get(4)?.data ?? [])]).toEqual([0xc3, 0xa9]);

    emit(
      "udp:state",
      stateEvent({
        state: "closed",
        seq: 4,
        close: { initiatedBy: "local", bytesSent: 4, bytesReceived: 9, datagramsSent: 1, datagramsReceived: 3, notes: 1, durationMs: 1000 },
      }),
    );
    expect(session.getState()).toMatchObject({ status: "closed", error: null });
    session.textLog.flush();
    expect(session.textLog.get(7)?.text).toBe("Closed (1 datagram sent, 3 datagrams received)");
  });

  it("names the proxy and its relay, and shows a proxy that refuses UDP", async () => {
    udp.Open.mockResolvedValue({ connId: "udp_1", requestId: "req_dns", address: "dns.test:53" });
    const session = udpSessions().get("req_dns");
    await session.open("dns.test:53");
    const proxy = {
      scheme: "socks5h",
      version: "SOCKS5",
      proxy: "proxy.lan:1080",
      proxyAddr: "10.0.0.1:1080",
      command: "UDP ASSOCIATE",
      relayAddr: "10.0.0.1:40000",
      auth: "none",
      remoteDns: true,
      reply: 0,
      replyText: "succeeded",
      handshakeMs: 1.5,
    };
    emit("udp:state", stateEvent({ state: "open", address: "dns.test:53", via: "socks5h://proxy.lan:1080", proxy, remoteAddr: "10.0.0.1:40000", localAddr: "127.0.0.1:5000" }));
    expect(session.getState()).toMatchObject({ via: "socks5h://proxy.lan:1080", proxy: { relayAddr: "10.0.0.1:40000" } });
    session.textLog.flush();
    const open = session.textLog.get(0);
    expect(open?.text).toBe("Ready to send to dns.test:53 through socks5h://proxy.lan:1080 (UDP ASSOCIATE, relay 10.0.0.1:40000); replies from any sender");
    expect(open?.meta).toMatchObject({ command: "UDP ASSOCIATE", relay: "10.0.0.1:40000", dns: "remote", reply: "0 succeeded" });
    emit("udp:state", stateEvent({ state: "closed", close: null }));

    await session.open("dns.test:53");
    emit(
      "udp:state",
      stateEvent({
        state: "closed",
        proxy: { ...proxy, reply: 7, replyText: "command not supported", relayAddr: "" },
        error: {
          kind: "network",
          code: "netx.socks_command_not_supported",
          message: "The SOCKS proxy at proxy.lan:1080 does not support UDP ASSOCIATE.",
          hint: "",
          details: {},
          causes: [],
        },
        close: { initiatedBy: "none", bytesSent: 0, bytesReceived: 0, datagramsSent: 0, datagramsReceived: 0, notes: 0, durationMs: 0 },
      }),
    );
    expect(session.getState().error).toMatchObject({ code: "netx.socks_command_not_supported" });
    session.textLog.flush();
    expect(session.textLog.get(2)).toMatchObject({ kind: "error", text: "The SOCKS proxy at proxy.lan:1080 does not support UDP ASSOCIATE." });
    expect(session.textLog.get(2)?.meta).toMatchObject({ reply: "7 command not supported" });
  });

  it("shows input Go refuses, closes while opening and when the tab closes", async () => {
    udp.Open.mockRejectedValueOnce(
      Object.assign(new Error("bad"), {
        cause: { kind: "invalid_argument", code: "udpclient.invalid_address", message: "The address has no port.", hint: "", details: {}, causes: [] },
      }),
    );
    const session = udpSessions().get("req_dns");
    await expect(session.open("localhost")).rejects.toMatchObject({ code: "udpclient.invalid_address" });
    expect(session.getState()).toMatchObject({ status: "idle", error: { message: "The address has no port." } });

    const started = deferred<unknown>();
    udp.Open.mockReturnValue(started.promise);
    const opening = session.open("localhost:53");
    await session.close();
    expect(udp.Close).not.toHaveBeenCalled();
    started.resolve({ connId: "udp_1", requestId: "req_dns", address: "localhost:53" });
    await opening;
    expect(udp.Close).toHaveBeenCalledWith("udp_1");

    udp.Close.mockClear();
    emit("udp:state", stateEvent({ state: "closed", close: null }));
    udp.Open.mockResolvedValue({ connId: "udp_2", requestId: "req_dns", address: "localhost:53" });
    await session.open("localhost:53");
    emit("udp:state", stateEvent({ connId: "udp_2", state: "open" }));
    const stores = createStores();
    stores.tabs.dispatch({ type: "tabs/restore", order: ["req_dns"], activeId: "req_dns" });
    closeTabs(stores, ["req_dns"]);
    await settle();
    expect(udp.Close).toHaveBeenCalledWith("udp_2");
    expect(udpSessions().peek("req_dns")).toBeUndefined();

    resetUdpSessions();
    expect(listenerCount()).toBe(0);
  });

  it("refuses to send without an open socket and passes Go's send errors on", async () => {
    const session = udpSessions().get("req_dns");
    await expect(session.send("text", "x")).rejects.toMatchObject({ code: "udp.not_open" });
    udp.Open.mockResolvedValue({ connId: "udp_1", requestId: "req_dns", address: "localhost:53" });
    await session.open("localhost:53");
    emit("udp:state", stateEvent({ state: "open" }));
    udp.Send.mockRejectedValueOnce(
      Object.assign(new Error("x"), { cause: { kind: "invalid_argument", code: "ws.invalid_base64", message: "bad", hint: "", details: {}, causes: [] } }),
    );
    await expect(session.send("base64", "a$")).rejects.toMatchObject({ code: "ws.invalid_base64" });
    udp.Send.mockResolvedValueOnce({ bytes: 2 });
    await expect(session.send("hex", "abcd")).resolves.toEqual({ bytes: 2 });
    expect(udp.Send).toHaveBeenLastCalledWith({ connId: "udp_1", mode: "hex", text: "abcd" });
  });
});
