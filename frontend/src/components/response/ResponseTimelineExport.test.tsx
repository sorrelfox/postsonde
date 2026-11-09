// Copying and saving the response timeline (DBG-07).
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/logexport", async () =>
  (await import("../../test/fakeLogExport")).fakeLogExportBindings,
);

import { toaster } from "../../commands/toasts";
import type { TimelineDTO, TimelineHopDTO } from "../../lib/http";
import { IS_MAC } from "../../lib/shortcuts";
import { lastCopied, logExportService, MASK, resetFakeLogExport, SECRET } from "../../test/fakeLogExport";
import { fakeRuntime, resetFakeWails, settle } from "../../test/fakeWails";
import { ResponseTimeline } from "./ResponseTimeline";
import { eventText, hopText, timelineDocument } from "./timelineFormat";

const hop: TimelineHopDTO = {
  kind: "final",
  request: {
    method: "GET",
    url: `https://api.test/items?key=${SECRET}`,
    proto: "HTTP/1.1",
    target: `/items?key=${SECRET}`,
    headers: [],
    head: `GET /items?key=${SECRET} HTTP/1.1\r\nHost: api.test\r\nAuthorization: Bearer ••••••\r\n\r\n`,
    body: "",
    bodyHex: false,
    bodyShown: 0,
    bodyClipped: false,
    bodySize: 0,
    bodyChunked: false,
  },
  startMs: 0,
  responseProto: "HTTP/1.1",
  statusCode: 200,
  status: "200 OK",
  responseHead: "HTTP/1.1 200 OK\r\nContent-Type: application/json\r\n\r\n",
  timing: {
    totalMs: 12.5,
    phases: [
      { phase: "dns", observed: true, startMs: 0, durationMs: 1, skipped: "" },
      { phase: "tls", observed: false, startMs: 0, durationMs: 0, skipped: "reused" },
    ],
  },
  conn: {
    reused: false,
    wasIdle: false,
    idleMs: 0,
    localAddr: "192.0.2.10:50000",
    remoteAddr: "203.0.113.5:443",
    protocol: "HTTP/1.1",
    via: "",
    proxy: null,
    tls: {
      version: "TLS 1.3",
      cipherSuite: "TLS_AES_128_GCM_SHA256",
      serverName: "api.test",
      alpn: "http/1.1",
      resumed: false,
      certificates: [
        { subject: "CN=api.test", issuer: "CN=Test CA", notBefore: "2026-01-01T00:00:00Z", notAfter: "2027-01-01T00:00:00Z", dnsNames: ["api.test"], ipAddresses: [], validity: "valid", selfSigned: false },
      ],
    },
  },
};

const timeline: TimelineDTO = {
  masked: true,
  hops: [hop],
  events: [
    { atMs: 0.125, name: "get_conn", detail: "api.test:443" },
    { atMs: 12.5, name: "body_done", detail: "12 bytes" },
  ],
};

beforeEach(() => {
  resetFakeWails();
  resetFakeLogExport();
  toaster.clear();
});

describe("timeline entries (DBG-07)", () => {
  it("formats a hop with its timing, connection, TLS, request and response", () => {
    const text = hopText(hop, 0, 1);
    for (const part of [
      "Exchange (at 0.000 ms, took 12.5 ms)",
      "  DNS lookup         1.000 ms (from 0.000 ms)",
      "  TLS handshake      not timed (reused)",
      "  Remote address: 203.0.113.5:443",
      "  TLS: TLS 1.3, TLS_AES_128_GCM_SHA256",
      "  Server name (SNI): api.test",
      "    Subject: CN=api.test",
      "    Names (SAN): api.test",
      "Request\nGET /items?key=",
      "Authorization: Bearer ••••••",
      "Response\nHTTP/1.1 200 OK\nContent-Type: application/json",
    ]) {
      expect(text).toContain(part);
    }
    expect(eventText({ atMs: 0.125, name: "get_conn", detail: "api.test:443" })).toBe("+0.125 ms  Get connection (get_conn)  api.test:443");
  });

  it("copies a hop from its button, masked in Go", async () => {
    render(<ResponseTimeline timeline={timeline} />);
    fireEvent.click(screen.getByRole("button", { name: "Copy exchange" }));
    fireEvent.click(screen.getAllByRole("menuitem")[0]);
    await act(settle);
    expect(logExportService.Mask).toHaveBeenCalledWith({ text: expect.stringContaining(`key=${SECRET}`), format: "text" });
    const copied = lastCopied(fakeRuntime.Clipboard.SetText) ?? "";
    expect(copied).toContain(`GET /items?key=${MASK} HTTP/1.1`);
    expect(copied).not.toContain(SECRET);

    fireEvent.click(screen.getByRole("button", { name: "Copy exchange" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Copy entry as JSON" }));
    await act(settle);
    expect(JSON.parse(lastCopied(fakeRuntime.Clipboard.SetText) ?? "")).toMatchObject({
      entry: "hop",
      title: "Exchange",
      status: "200 OK",
      request: { url: `https://api.test/items?key=${MASK}` },
      conn: { tls: { version: "TLS 1.3" } },
    });
  });

  it("copies a focused event with ⌘C / Ctrl+C and moves with the arrow keys", async () => {
    render(<ResponseTimeline timeline={timeline} />);
    const rows = within(screen.getByRole("table", { name: "Timeline events" })).getAllByRole("row").slice(1);
    expect(rows.map((r) => r.getAttribute("tabindex"))).toEqual(["0", "-1"]);
    rows[0].focus();
    fireEvent.keyDown(rows[0], { key: "ArrowDown" });
    expect(document.activeElement).toBe(rows[1]);
    fireEvent.keyDown(rows[1], IS_MAC ? { key: "c", metaKey: true } : { key: "c", ctrlKey: true });
    await act(settle);
    expect(lastCopied(fakeRuntime.Clipboard.SetText)).toBe("+12.5 ms  Body received (body_done)  12 bytes");

    fireEvent.keyDown(rows[1], { key: "ContextMenu" });
    expect(screen.getByRole("menu", { name: "Event" })).toBeTruthy();
  });

  it("saves the whole timeline as text or JSON", async () => {
    render(<ResponseTimeline timeline={timeline} />);
    fireEvent.click(screen.getByRole("button", { name: "Save timeline…" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Save as JSON…" }));
    await act(settle);
    const input = logExportService.Save.mock.calls[0][0];
    expect(input).toMatchObject({ title: "Timeline", fileName: "timeline", format: "json" });
    const doc = JSON.parse(input.content);
    expect(doc).toMatchObject({ format: "postsonde-log", kind: "timeline", count: 3 });
    expect(doc.entries.map((e: { entry: string }) => e.entry)).toEqual(["hop", "event", "event"]);
    expect(toaster.toasts()[0]?.message).toBe("Saved timeline to /tmp/timeline.json, secrets masked.");

    const text = timelineDocument(timeline, "text", new Date("2026-09-19T10:00:00Z"));
    expect(text).toMatch(/^PostSonde Timeline\nSaved 2026-09-19T10:00:00\.000Z, 2 entries\./);
    expect(text).toContain("Events\n+0.125 ms  Get connection (get_conn)  api.test:443\n+12.5 ms  Body received (body_done)  12 bytes");
  });
});
