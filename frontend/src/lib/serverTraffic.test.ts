import { describe, expect, it } from "vitest";
import { manualFrames } from "../test/manualFrames";
import { trafficDTO } from "../test/servers";
import type { LogMessage, MessageSource } from "./messageSource";
import { toHexMessage, TrafficSources, trafficToMessage } from "./serverTraffic";

function messages(source: MessageSource): LogMessage[] {
  source.flush(); // one manual frame scheduler is shared by several sources
  const snap = source.getSnapshot();
  const out: LogMessage[] = [];
  for (let seq = snap.firstSeq; seq < snap.nextSeq; seq++) {
    const m = source.get(seq);
    if (m) out.push(m);
  }
  return out;
}

describe("TCP server traffic", () => {
  const tcp = { initiatedBy: "", bytesIn: 0, bytesOut: 0 };

  it("says how a connection closed", () => {
    const m = trafficToMessage(
      trafficDTO({
        direction: "system",
        kind: "connection_close",
        summary: "Closed by the client (5 bytes received, 4 bytes sent)",
        http: null,
        tcp: { initiatedBy: "client", bytesIn: 5, bytesOut: 4 },
      }),
    );
    expect(m.kind).toBe("closed");
    expect(m.meta).toMatchObject({ "closed by": "client", "bytes received": 5, "bytes sent": 4 });
    const lost = trafficToMessage(trafficDTO({ direction: "system", kind: "connection_close", http: null, tcp }));
    expect(lost.meta?.["closed by"]).toBe("nobody (connection lost)");
  });

  it("turns text payloads into bytes for the hex view", () => {
    const text = trafficToMessage(trafficDTO({ kind: "message", text: "hé\n", size: 4, http: null, tcp }));
    const hex = toHexMessage(text);
    expect(hex.kind).toBe("binary");
    expect(hex.text).toBeUndefined();
    expect(Array.from(hex.data ?? [])).toEqual([0x68, 0xc3, 0xa9, 0x0a]);
    const system = trafficToMessage(trafficDTO({ direction: "system", kind: "connection_open", summary: "Connection 1 opened", http: null, tcp }));
    expect(toHexMessage(system)).toBe(system);
  });

  it("keeps a hex view from the first use on, filled from the log", () => {
    const frames = manualFrames();
    const sources = new TrafficSources({ schedule: frames.schedule });
    const msg = (seq: number, text: string) => trafficDTO({ seq, kind: "message", text, size: text.length, http: null, tcp });
    sources.append([trafficDTO({ seq: 1, direction: "system", kind: "started", summary: "Listening", http: null }), msg(2, "hi")]);
    const hex = sources.hex("srv_1");
    expect(sources.hex("srv_1")).toBe(hex);
    sources.append([msg(3, "ok")]);
    frames.frame();
    expect(messages(hex).map((m) => m.text ?? Array.from(m.data ?? []).join(","))).toEqual(["Listening", "104,105", "111,107"]);
    expect(messages(sources.get("srv_1")).map((m) => m.text)).toEqual(["Listening", "hi", "ok"]);

    sources.clear("srv_1");
    expect(messages(hex)).toEqual([]);
    sources.retain([]);
    expect(sources.hex("srv_1")).not.toBe(hex);
  });
});

describe("trafficToMessage", () => {
  it("maps a request without a body to a received message with the summary as text", () => {
    const m = trafficToMessage(trafficDTO());
    expect(m).toEqual({
      time: Date.parse("2026-09-16T10:00:01.250Z"),
      direction: "received",
      kind: "request",
      badge: "GET",
      size: 0,
      text: "GET /users?id=1",
      meta: { peer: "127.0.0.1:60000", connection: 1, protocol: "HTTP/1.1", Host: "127.0.0.1:52341", Accept: "*/*" },
    });
  });

  it("maps a response with a truncated body to a sent message", () => {
    const m = trafficToMessage(
      trafficDTO({
        direction: "out",
        kind: "response",
        summary: "200 OK",
        text: '{"a":1}',
        size: 70000,
        truncated: true,
        http: {
          method: "GET",
          target: "/x",
          proto: "HTTP/1.1",
          status: 200,
          headers: [
            { name: "Set-Cookie", value: "a=1" },
            { name: "Set-Cookie", value: "b=2" },
          ],
          route: null,
          delayMs: 0,
        },
      }),
    );
    expect(m.direction).toBe("sent");
    expect(m.badge).toBe("200");
    expect(m.text).toBe('{"a":1}');
    expect(m.size).toBe(70000);
    expect(m.meta).toMatchObject({ summary: "200 OK", request: "GET /x", truncated: "first 7 of 70000 bytes", "Set-Cookie": "a=1, b=2" });
  });

  it("adds the mock route, its parameters and the delay", () => {
    const m = trafficToMessage(
      trafficDTO({
        direction: "out",
        kind: "response",
        summary: "200 OK",
        http: {
          method: "GET",
          target: "/users/7/files/a/b",
          proto: "HTTP/1.1",
          status: 200,
          headers: [],
          route: {
            id: "rt_1",
            method: "GET",
            pattern: "/users/:id/files/*rest",
            params: [
              { name: "id", value: "7" },
              { name: "rest", value: "a/b" },
            ],
            wildcard: "a/b",
            response: "not-found",
          },
          delayMs: 250,
        },
      }),
    );
    expect(m.meta).toMatchObject({
      route: "GET /users/:id/files/*rest",
      "params.id": "7",
      "params.rest": "a/b",
      response: "not-found",
      delay: "250 ms",
    });
    expect(Object.keys(m.meta ?? {})).not.toContain("wildcard");
  });

  it("adds the WebSocket client, its messages and how it closed", () => {
    const ws = { clientId: "3", opcode: "", subprotocol: "", closeCode: 0, closeReason: "", initiatedBy: "" };
    const text = trafficToMessage(
      trafficDTO({ kind: "message", summary: "Text message from client 3", text: "hi", size: 2, http: null, connId: 3, ws: { ...ws, opcode: "text" } }),
    );
    expect(text).toMatchObject({ direction: "received", kind: "text", text: "hi", meta: { client: "3", summary: "Text message from client 3" } });
    const binary = trafficToMessage(
      trafficDTO({ kind: "message", direction: "out", binary: true, dataBase64: "AQI=", size: 2, http: null, ws: { ...ws, opcode: "binary" } }),
    );
    expect(binary).toMatchObject({ direction: "sent", kind: "binary", data: new Uint8Array([1, 2]) });
    const upgrade = trafficToMessage(
      trafficDTO({ kind: "response", direction: "out", summary: "101 Switching Protocols (subprotocol chat)", ws: { ...ws, subprotocol: "chat" } }),
    );
    expect(upgrade.meta).toMatchObject({ client: "3", subprotocol: "chat" });
    const closed = trafficToMessage(
      trafficDTO({
        kind: "connection_close",
        direction: "system",
        summary: "Disconnected by the server: 4000 “bye”",
        http: null,
        ws: { ...ws, closeCode: 4000, closeReason: "bye", initiatedBy: "server" },
      }),
    );
    expect(closed).toMatchObject({
      kind: "closed",
      text: "Disconnected by the server: 4000 “bye”",
      meta: { client: "3", "close code": 4000, "close reason": "bye", "closed by": "server" },
    });
    const lost = trafficToMessage(trafficDTO({ kind: "connection_close", direction: "system", http: null, ws: { ...ws, closeCode: 1006 } }));
    expect(lost.meta).toMatchObject({ "closed by": "nobody (connection lost)" });
  });

  it("maps system, connection and binary events", () => {
    const base = { http: null, peer: "", connId: 0 };
    expect(trafficToMessage(trafficDTO({ ...base, direction: "system", kind: "started", summary: "Listening on 127.0.0.1:1" }))).toMatchObject({
      direction: "system",
      kind: "started",
      text: "Listening on 127.0.0.1:1",
    });
    expect(trafficToMessage(trafficDTO({ ...base, direction: "system", kind: "connection_open" })).kind).toBe("connected");
    expect(trafficToMessage(trafficDTO({ ...base, direction: "system", kind: "connection_close" })).kind).toBe("closed");
    const bin = trafficToMessage(trafficDTO({ ...base, kind: "message", binary: true, dataBase64: "/wAQ", size: 3 }));
    expect(bin.kind).toBe("binary");
    expect(bin.text).toBeUndefined();
    expect(Array.from(bin.data ?? [])).toEqual([0xff, 0x00, 0x10]);
    expect(trafficToMessage(trafficDTO({ ...base, kind: "message", text: "hi", size: 2 })).kind).toBe("text");
  });
});

describe("TrafficSources", () => {
  it("appends each server's events to its own source, once, in seq order", () => {
    const frames = manualFrames();
    const sources = new TrafficSources({ schedule: frames.schedule });
    sources.append([
      trafficDTO({ seq: 1 }),
      trafficDTO({ serverId: "srv_2", seq: 1, summary: "POST /b" }),
      trafficDTO({ seq: 2, summary: "GET /2" }),
    ]);
    // Overlap with what was applied (the recent traffic and a live batch).
    sources.append([trafficDTO({ seq: 2, summary: "dup" }), trafficDTO({ seq: 3, summary: "GET /3" })]);
    frames.frame();

    expect(messages(sources.get("srv_1")).map((m) => m.text)).toEqual(["GET /users?id=1", "GET /2", "GET /3"]);
    expect(messages(sources.get("srv_2")).map((m) => m.text)).toEqual(["POST /b"]);
    expect(sources.last("srv_1")).toBe(3);
    expect(sources.get("srv_1")).toBe(sources.get("srv_1"));
  });

  it("clears a log without replaying old events and forgets removed servers", () => {
    const frames = manualFrames();
    const sources = new TrafficSources({ schedule: frames.schedule });
    sources.append([trafficDTO({ seq: 1 }), trafficDTO({ serverId: "srv_2", seq: 4 })]);
    frames.frame();
    sources.clear("srv_1");
    sources.append([trafficDTO({ seq: 1 }), trafficDTO({ seq: 2, summary: "new" })]);
    frames.frame();
    expect(messages(sources.get("srv_1")).map((m) => m.text)).toEqual(["new"]);

    const old = sources.get("srv_2");
    sources.retain(["srv_1"]);
    expect(sources.last("srv_2")).toBe(0);
    expect(sources.get("srv_2")).not.toBe(old);
  });
});
