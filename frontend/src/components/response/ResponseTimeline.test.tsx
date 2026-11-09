import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import type { ConnInfoDTO, HopTimingDTO, PhaseDTO, SentRequestDTO, TimelineDTO, TimelineHopDTO } from "../../lib/http";
import { httpResponse } from "../../test/httpResponse";
import { ResponseTimeline, formatOffsetMs } from "./ResponseTimeline";
import { ResponseView } from "./ResponseView";

function sent(over: Partial<SentRequestDTO> = {}): SentRequestDTO {
  return {
    method: "POST",
    url: "https://api.test/start",
    proto: "HTTP/1.1",
    target: "/start",
    headers: [],
    head: "POST /start HTTP/1.1\r\nHost: api.test\r\nAuthorization: Bearer ••••••\r\nContent-Length: 7\r\n\r\n",
    body: '{"a":1}',
    bodyHex: false,
    bodyShown: 7,
    bodyClipped: false,
    bodySize: 7,
    bodyChunked: false,
    ...over,
  };
}

function phase(name: string, startMs: number, durationMs: number): PhaseDTO {
  return { phase: name, observed: true, startMs, durationMs, skipped: "" };
}

function skipped(name: string, why: string): PhaseDTO {
  return { phase: name, observed: false, startMs: 0, durationMs: 0, skipped: why };
}

/** A new TLS connection: every phase but the download of a redirect. */
const newTiming: HopTimingDTO = {
  totalMs: 12.5,
  phases: [
    phase("dns", 0.1, 1),
    phase("connect", 1.1, 2),
    phase("tls", 3.1, 4.5),
    phase("request_write", 7.7, 0.2),
    phase("wait", 7.9, 4),
    skipped("download", "not_final"),
  ],
};

/** The same connection, reused. */
const reusedTiming: HopTimingDTO = {
  totalMs: 20,
  phases: [
    skipped("dns", "reused"),
    skipped("connect", "reused"),
    skipped("tls", "reused"),
    phase("request_write", 0.5, 0.25),
    phase("wait", 0.75, 15),
    phase("download", 15.75, 4),
  ],
};

const newConn: ConnInfoDTO = {
  reused: false,
  wasIdle: false,
  idleMs: 0,
  localAddr: "192.0.2.10:50000",
  remoteAddr: "203.0.113.5:443",
  protocol: "h2",
  via: "",
  proxy: null,
  tls: {
    version: "TLS 1.3",
    cipherSuite: "TLS_AES_128_GCM_SHA256",
    serverName: "api.test",
    alpn: "h2",
    resumed: false,
    certificates: [
      {
        subject: "CN=api.test",
        issuer: "CN=Test CA",
        notBefore: "2020-01-01T00:00:00Z",
        notAfter: "2020-06-01T00:00:00Z",
        dnsNames: ["api.test", "www.api.test"],
        ipAddresses: ["203.0.113.5"],
        validity: "expired",
        selfSigned: false,
      },
      {
        subject: "CN=Test CA",
        issuer: "CN=Test Root",
        notBefore: "2020-01-01T00:00:00Z",
        notAfter: "2999-01-01T00:00:00Z",
        dnsNames: [],
        ipAddresses: [],
        validity: "valid",
        selfSigned: false,
      },
    ],
  },
};

const timeline: TimelineDTO = {
  masked: true,
  hops: [
    {
      kind: "redirect",
      request: sent(),
      startMs: 0,
      responseProto: "HTTP/1.1",
      statusCode: 307,
      status: "307 Temporary Redirect",
      responseHead: "HTTP/1.1 307 Temporary Redirect\r\nLocation: /end\r\n\r\n",
      timing: newTiming,
      conn: newConn,
    },
    {
      kind: "final",
      request: sent({
        target: "/end",
        head: "POST /end HTTP/1.1\r\nHost: api.test\r\n\r\n",
        body: "00 01",
        bodyHex: true,
        bodyShown: 2048,
        bodySize: 4096,
        bodyClipped: true,
      }),
      startMs: 12.5,
      responseProto: "HTTP/1.1",
      statusCode: 200,
      status: "200 OK",
      responseHead: "HTTP/1.1 200 OK\r\nContent-Type: text/plain\r\n\r\n",
      timing: reusedTiming,
      conn: { ...newConn, reused: true, wasIdle: true, idleMs: 1500 },
    },
  ],
  events: [
    { atMs: 0.125, name: "get_conn", detail: "api.test:443" },
    { atMs: 12.5, name: "redirect", detail: "307 → api.test" },
    { atMs: 20, name: "something_new", detail: "" },
  ],
};

describe("ResponseTimeline (DBG-01)", () => {
  it("opens from the response's Timeline tab", () => {
    render(<ResponseView response={httpResponse({ timeline })} />);
    fireEvent.click(screen.getByRole("tab", { name: "Timeline" }));
    expect(screen.getByRole("region", { name: "Redirect 1 of 2" })).toBeTruthy();
    expect(screen.getByRole("region", { name: "Final request (2 of 2)" })).toBeTruthy();
  });

  it("names a hop answered with an auth challenge (AUTH-06)", () => {
    const [first, final] = timeline.hops ?? [];
    const auth: TimelineDTO = {
      ...timeline,
      hops: [{ ...first, kind: "auth", statusCode: 401, status: "401 Unauthorized" }, final],
      events: [{ atMs: 3, name: "auth_challenge", detail: "401 Digest SHA-256, qop auth" }],
    };
    render(<ResponseTimeline timeline={auth} />);
    expect(screen.getByRole("region", { name: "Auth challenge 1 of 2" })).toBeTruthy();
    expect(screen.getByText("Auth challenge answered")).toBeTruthy();
  });

  it("shows an OAuth 2.0 token request with its masked response body (AUTH-33)", () => {
    const [first, final] = timeline.hops ?? [];
    const token: TimelineDTO = {
      ...timeline,
      hops: [
        {
          ...first,
          kind: "token",
          statusCode: 200,
          status: "200 OK",
          responseHead: "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n",
          responseBody: '{"access_token":"••••••","expires_in":3600}',
        },
        final,
      ],
      events: [{ atMs: 3, name: "oauth2_token", detail: "client_credentials from https://idp.test/token: 200, expires in 3600 s" }],
    };
    render(<ResponseTimeline timeline={token} />);
    const hop = screen.getByRole("region", { name: "Token request 1 of 2" });
    expect(hop.querySelector("[data-wire=response]")?.textContent).toBe(
      'HTTP/1.1 200 OK\nContent-Type: application/json\n\n{"access_token":"••••••","expires_in":3600}',
    );
    expect(screen.getByText("OAuth 2.0 token received")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Copy token request 1 of 2" })).toBeTruthy();
  });

  it("shows each hop's request as sent and its response head", () => {
    render(<ResponseTimeline timeline={timeline} />);
    const requests = document.querySelectorAll("[data-wire=request]");
    expect(requests[0].textContent).toBe(
      'POST /start HTTP/1.1\nHost: api.test\nAuthorization: Bearer ••••••\nContent-Length: 7\n\n{"a":1}',
    );
    const responses = document.querySelectorAll("[data-wire=response]");
    expect(responses[0].textContent).toBe("HTTP/1.1 307 Temporary Redirect\nLocation: /end");
    expect(responses[1].textContent).toBe("HTTP/1.1 200 OK\nContent-Type: text/plain");
    expect(screen.getByText("Secret values are masked as ••••••.")).toBeTruthy();
    expect(screen.getByText("The body is not UTF-8 text, so its bytes are shown.")).toBeTruthy();
    expect(screen.getByText(/Only the first 2(\.0)? KB of the 4(\.0)? KB body are shown/)).toBeTruthy();
    expect(screen.getByText("The response body is in the Body tab.")).toBeTruthy();
  });

  it("lists the events with readable names, unknown ones as they are", () => {
    render(<ResponseTimeline timeline={timeline} />);
    const rows = screen.getByRole("table", { name: "Timeline events" }).querySelectorAll("tbody tr");
    expect([...rows].map((r) => r.textContent)).toEqual([
      "0.125 msGet connectionapi.test:443",
      "12.5 msRedirect307 → api.test",
      "20.0 mssomething_new",
    ]);
  });

  it("shows each hop's phases as a waterfall (DBG-03)", () => {
    render(<ResponseTimeline timeline={timeline} />);
    const [first, final] = screen.getAllByRole("table", { name: "Phase timings" });
    const rows = (t: HTMLElement) => [...t.querySelectorAll("tbody tr")].map((r) => r.textContent);
    expect(rows(first)).toEqual([
      "DNS lookup1.000 ms",
      "TCP connect2.000 ms",
      "TLS handshake4.500 ms",
      "Request sent0.200 ms",
      "Waiting (TTFB)4.000 ms",
      "Content download—",
      "Total12.5 ms",
    ]);
    expect(rows(final)).toEqual([
      "DNS lookupreused",
      "TCP connectreused",
      "TLS handshakereused",
      "Request sent0.250 ms",
      "Waiting (TTFB)15.0 ms",
      "Content download4.000 ms",
      "Total20.0 ms",
    ]);
    // Bars sit on the hop's span: TLS starts at 3.1 of 12.5 ms.
    const tls = first.querySelector<HTMLElement>("[data-phase=tls] span");
    expect(tls?.style.left).toBe("24.8%");
    expect(tls?.style.width).toBe("36%");
    expect(final.querySelector("[data-phase=dns] span")).toBeNull();
    expect(screen.getByRole("region", { name: "Final request (2 of 2)" }).textContent).toContain("at 12.5 ms, took 20.0 ms");
  });

  it("shows the connection and the TLS session (DBG-04, DBG-05)", () => {
    render(<ResponseTimeline timeline={timeline} />);
    const [first, final] = screen.getAllByLabelText("Connection");
    const pairs = (dl: HTMLElement) =>
      [...dl.querySelectorAll("dt")].map((dt) => `${dt.textContent}: ${dt.nextElementSibling?.textContent}`);
    expect(pairs(first)).toEqual([
      "Connection: New",
      "Protocol: HTTP/2 (h2)",
      "Remote address: 203.0.113.5:443",
      "Local address: 192.0.2.10:50000",
      "TLS: TLS 1.3, TLS_AES_128_GCM_SHA256",
      "ALPN: h2",
      "Server name (SNI): api.test",
    ]);
    expect(pairs(final)[0]).toBe("Connection: Reused, idle 1.50 s");
    const leaf = screen.getAllByRole("group", { name: "Server certificate" })[0];
    expect(pairs(leaf.querySelector("dl") as HTMLElement)).toEqual([
      "Subject: CN=api.test",
      "Issuer: CN=Test CA",
      "Valid: 2020-01-01 to 2020-06-01 (expired)",
      "Names (SAN): api.test, www.api.test, 203.0.113.5",
    ]);
    const ca = screen.getAllByRole("group", { name: "Chain certificate 2" })[0];
    expect(ca.textContent).toContain("2020-01-01 to 2999-01-01");
    expect(ca.textContent).not.toContain("expired");
  });

  it("marks a certificate by its validity at the request and as self-signed", () => {
    const [first] = timeline.hops ?? [];
    const tls = newConn.tls!;
    const [leafCert, caCert] = tls.certificates ?? [];
    const certs = [
      // Expired when sent, though its dates would pass now: Go's validity wins.
      { ...leafCert, notAfter: "2999-01-01T00:00:00Z", validity: "expired", selfSigned: true },
      { ...caCert, validity: "not_yet_valid", selfSigned: true },
      // Saved before Go recorded the validity: judged from the dates.
      { ...caCert, notAfter: "2020-06-01T00:00:00Z", validity: undefined as unknown as string },
    ];
    const hop = { ...first, conn: { ...newConn, tls: { ...tls, certificates: certs } } };
    render(<ResponseTimeline timeline={{ ...timeline, hops: [hop] }} />);
    const leaf = screen.getByRole("group", { name: "Server certificate" });
    expect(leaf.textContent).toContain("2020-01-01 to 2999-01-01 (expired) (self-signed)");
    const bad = [...leaf.querySelectorAll("span")].filter((s) => s.className !== "");
    expect(bad.map((s) => s.textContent?.trim())).toEqual(["(expired)", "(self-signed)"]);
    const root = screen.getByRole("group", { name: "Chain certificate 2" });
    expect(root.textContent).toContain("(not yet valid) (self-signed)");
    // A self-signed root is expected: not flagged as a problem.
    const rootSelf = [...root.querySelectorAll("span")].find((s) => s.textContent?.includes("self-signed"));
    expect(rootSelf?.className ?? "").toBe("");
    expect(screen.getByRole("group", { name: "Chain certificate 3" }).textContent).toContain("(expired)");
  });

  it("leaves out the timing and connection of a timeline saved without them", () => {
    const [first] = timeline.hops ?? [];
    const old = { ...first, timing: undefined, conn: undefined } as unknown as TimelineHopDTO;
    render(<ResponseTimeline timeline={{ ...timeline, hops: [old] }} />);
    expect(screen.queryByRole("table", { name: "Phase timings" })).toBeNull();
    expect(screen.queryByLabelText("Connection")).toBeNull();
    expect(document.querySelector("[data-wire=request]")).not.toBeNull();
  });

  it("shows the hop through a proxy with its own phase (SOCKS-12)", () => {
    const [, final] = timeline.hops ?? [];
    const proxied: TimelineHopDTO = {
      ...final,
      timing: {
        totalMs: 20,
        phases: [phase("dns", 0, 0.5), phase("connect", 0.5, 1), phase("proxy", 1.5, 2), phase("tls", 3.5, 4), phase("request_write", 7.5, 0.5)],
      },
      conn: {
        ...newConn,
        proxy: {
          scheme: "socks5h",
          version: "SOCKS5",
          proxy: "proxy.lan:1080",
          proxyAddr: "10.0.0.1:1080",
          target: "api.test:443",
          auth: "username/password",
          remoteDns: true,
          reply: 0,
          replyText: "succeeded",
          forward: false,
          lookupMs: 0,
          connectMs: 1,
          tlsMs: 0,
          handshakeMs: 2,
        },
      },
    };
    render(<ResponseTimeline timeline={{ ...timeline, hops: [proxied], events: [{ atMs: 3.5, name: "proxy_hop", detail: "SOCKS5 proxy.lan:1080" }] }} />);
    expect(screen.getByRole("rowheader", { name: "Proxy handshake" })).toBeTruthy();
    const conn = screen.getByLabelText("Connection");
    expect(conn.textContent).toContain("SOCKS5 proxy.lan:1080 (10.0.0.1:1080)");
    expect(conn.textContent).toContain("User name and password");
    expect(conn.textContent).toContain("Resolved by the proxy");
    expect(conn.textContent).toContain("0 succeeded");
    expect(conn.textContent).toContain("connect 1.000 ms, handshake 2.000 ms");
    expect(screen.getByText("Through the proxy")).toBeTruthy();
  });

  it("marks a response that went through a proxy, and names it on a reused connection (NET-06)", () => {
    const [first, final] = timeline.hops ?? [];
    const via = "socks4a://legacy.lan:1080";
    const proxied: TimelineDTO = {
      ...timeline,
      hops: [
        { ...first, conn: { ...newConn, via } },
        { ...final, conn: { ...newConn, reused: true, via } },
      ],
    };
    render(<ResponseView response={httpResponse({ timeline: proxied })} />);
    const mark = document.querySelector("[data-proxy]");
    expect(mark?.textContent).toBe("via SOCKS4a proxy");
    expect(mark?.getAttribute("title")).toBe("Sent through the proxy socks4a://legacy.lan:1080");
    fireEvent.click(screen.getByRole("tab", { name: "Timeline" }));
    const conns = screen.getAllByLabelText("Connection");
    expect(conns[1].textContent).toContain("Through proxy");
    expect(conns[1].textContent).toContain(via);
  });

  it("says when there is no timeline", () => {
    render(<ResponseTimeline timeline={{ hops: [], events: [], masked: false }} />);
    expect(screen.getByText("No timeline was recorded for this response.")).toBeTruthy();
  });

  it("formats offsets", () => {
    expect(formatOffsetMs(0.5)).toBe("0.500 ms");
    expect(formatOffsetMs(250)).toBe("250 ms");
    expect(formatOffsetMs(1234)).toBe("1.23 s");
  });
});
