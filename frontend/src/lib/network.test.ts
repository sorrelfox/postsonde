import { describe, expect, it, vi } from "vitest";
import { connectionCause, ConnectionFeed, formatBytes, formatConnection, formatDuration, proxyKind, type ConnDTO } from "./network";

vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/network", () => ({ NetworkService: {} }));

function conn(id: number, patch: Partial<ConnDTO> = {}): ConnDTO {
  return {
    id,
    opKind: "http.send",
    opId: `op_${id}`,
    network: "tcp",
    address: "example.com:443",
    localAddr: "",
    remoteAddr: "",
    via: "",
    state: "connecting",
    startedAt: "2026-09-19T10:30:00.123Z",
    connectedAt: "",
    endedAt: "",
    connectMs: 0,
    durationMs: 0,
    bytesIn: 0,
    bytesOut: 0,
    errorCode: "",
    error: "",
    ...patch,
  };
}

const ids = (feed: ConnectionFeed) => feed.getSnapshot().conns.map((c) => `${c.id}:${c.state}`);

describe("ConnectionFeed", () => {
  it("merges events and the list by id, keeping the newer view of each connection", () => {
    const feed = new ConnectionFeed();
    feed.apply([conn(2, { state: "open", bytesIn: 10 })]);
    // The list was taken before that event: its view of 2 is older.
    feed.load({ conns: [conn(1, { state: "closed" }), conn(2, { state: "connecting" })], active: 1, capacity: 10 });
    expect(ids(feed)).toEqual(["1:closed", "2:open"]);
    expect(feed.getSnapshot().active).toBe(1);

    feed.apply([conn(2, { state: "open", bytesIn: 50 })]);
    expect(feed.getSnapshot().conns[1].bytesIn).toBe(50);
    feed.apply([conn(2, { state: "closed", bytesIn: 60 })]);
    expect(ids(feed)).toEqual(["1:closed", "2:closed"]);
    expect(feed.getSnapshot().active).toBe(0);
  });

  it("keeps at most the capacity, dropping ended connections first", () => {
    const feed = new ConnectionFeed(3);
    feed.apply([conn(1, { state: "open" }), conn(2, { state: "closed" }), conn(3, { state: "refused" }), conn(4, { state: "failed" })]);
    expect(ids(feed)).toEqual(["1:open", "3:refused", "4:failed"]);
  });

  it("clears the ended connections and keeps the open ones", () => {
    const feed = new ConnectionFeed();
    feed.apply([conn(1, { state: "open" }), conn(2, { state: "closed" })]);
    const listener = vi.fn();
    feed.subscribe(listener);
    feed.clearEnded();
    expect(ids(feed)).toEqual(["1:open"]);
    expect(listener).toHaveBeenCalledTimes(1);
    feed.clearEnded();
    expect(listener).toHaveBeenCalledTimes(1);
  });
});

describe("formatting", () => {
  it("formats bytes and durations", () => {
    expect(formatBytes(812)).toBe("812 B");
    expect(formatBytes(5120)).toBe("5.0 KB");
    expect(formatBytes(3 * 1024 * 1024)).toBe("3.0 MB");
    expect(formatDuration(42.5)).toBe("43 ms");
    expect(formatDuration(1250)).toBe("1.25 s");
    expect(formatDuration(185_000)).toBe("3 min 5 s");
  });

  it("names the cause, or says there was none", () => {
    expect(connectionCause(conn(1))).toBe("http.send op_1");
    expect(connectionCause(conn(1, { opId: "" }))).toBe("http.send");
    expect(connectionCause(conn(1, { opKind: "", opId: "" }))).toBe("no user action");
  });

  it("names the kind of proxy of a connection (NET-06)", () => {
    expect(proxyKind("socks5h://p:1080")).toBe("SOCKS5");
    expect(proxyKind("socks5://p:1080")).toBe("SOCKS5");
    expect(proxyKind("socks4://p:1080")).toBe("SOCKS4");
    expect(proxyKind("socks4a://p:1080")).toBe("SOCKS4a");
    expect(proxyKind("http://p:3128")).toBe("HTTP");
    expect(proxyKind("https://p:443")).toBe("HTTPS");
    expect(proxyKind("")).toBe("");
  });

  it("copies a connection as one line with host:port only", () => {
    const line = formatConnection(
      conn(7, {
        state: "closed",
        remoteAddr: "93.184.215.14:443",
        localAddr: "192.168.1.20:53211",
        connectMs: 42,
        durationMs: 2000,
        bytesIn: 5120,
        bytesOut: 812,
      }),
    );
    expect(line).toBe(
      "2026-09-19T10:30:00.123Z CLOSED     tcp example.com:443 (http.send op_7) remote=93.184.215.14:443 local=192.168.1.20:53211 connect=42 ms duration=2.00 s in=5120 out=812",
    );
  });
});
