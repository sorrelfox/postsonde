// The response panels and the message log in the pseudo-locale (UI-10):
// the HTTP response panel with its Body, Headers, Cookies,
// Tests and Timeline tabs, the SSE response panel with its Events, Merged
// and Headers tabs, and a message log with its detail pane. Every text they
// show must come from the catalogue, so no English text of the "response"
// and "messagelog" namespaces may be left (docs/frontend.md#localization).
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const http = vi.hoisted(() => ({
  Send: vi.fn<(input: { requestId: string }) => Promise<unknown>>(),
  Repeat: vi.fn<(input: { requestId: string; count: number; intervalMs: number }) => Promise<unknown>>(),
  CompareResponses: vi.fn<(input: unknown) => unknown>(),
}));
const history = vi.hoisted(() => ({
  Get: vi.fn<(id: string) => Promise<unknown>>(),
}));
const sse = vi.hoisted(() => ({
  Connect: vi.fn<(input: { requestId: string; allowUnresolved: boolean }) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http", () => ({
  HTTPService: http,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/history", () => ({ HistoryService: history }));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/sse", () => ({
  SSEService: sse,
}));
vi.mock(
  "../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/ops",
  async () => (await import("../../test/fakeWails")).fakeOpsBindings,
);

import { httpSends, resetHttpSends } from "../../commands/httpSends";
import { resetSseStreams, sseStreams } from "../../commands/sseStreams";
import { DEFAULT_LOCALE, PSEUDO_LOCALE, setLocale, type Namespace } from "../../i18n";
import { pseudoString } from "../../i18n/pseudo";
import type { TestResultDTO } from "../../lib/assertions";
import type { CookieDTO, ResponseDTO, TimelineDTO, TimelineHopDTO } from "../../lib/http";
import { MessageSource } from "../../lib/messageSource";
import type { StreamItemDTO } from "../../lib/sse";
import { createStores, StoreProvider } from "../../stores";
import { emit, resetFakeWails, settle } from "../../test/fakeWails";
import { httpResponse } from "../../test/httpResponse";
import { manualFrames } from "../../test/manualFrames";
import { englishLeftovers, isPseudo, visibleTexts } from "../../test/pseudoLocale";
import { MessageLog } from "../messagelog/MessageLog";
import { CompareResponsesDialog } from "./CompareResponsesDialog";
import { HttpResponsePanel } from "./HttpResponsePanel";
import { SseResponsePanel } from "./SseResponsePanel";
import { eventText, hopText } from "./timelineFormat";

/** The namespaces of the areas this test renders. */
const CONVERTED: readonly Namespace[] = ["response", "messagelog"];

/**
 * The kinds of the messages in a log ("event", "retry", "reconnect"): the
 * protocol's tokens, shown in the Kind column and filter as they are, like
 * the copied entry. Some are also words of the catalogue.
 */
const KINDS: readonly string[] = ["event", "retry", "reconnect", "open", "comment", "connected", "text", "binary"];

function expectNoEnglish(roots: readonly Element[], minPseudo = 1) {
  expect(englishLeftovers(roots, CONVERTED, { foreign: KINDS })).toEqual([]);
  expect(visibleTexts(roots).filter(isPseudo).length).toBeGreaterThanOrEqual(minPseudo);
}

const cookieBase: CookieDTO = {
  raw: "",
  name: "",
  value: "",
  domain: "",
  path: "",
  expires: "",
  maxAge: 0,
  hasMaxAge: false,
  secure: false,
  httpOnly: false,
  sameSite: "",
  error: "",
  notes: [],
  jar: "",
  jarReason: "",
  jarMessage: "",
};

const COOKIES: CookieDTO[] = [
  { ...cookieBase, raw: "sid=s1; Path=/; HttpOnly; SameSite=Lax", name: "sid", value: "s1", path: "/", httpOnly: true, sameSite: "Lax", jar: "stored" },
  { ...cookieBase, raw: "gone=; Max-Age=0", name: "gone", hasMaxAge: true, maxAge: 0, jar: "removed" },
  { ...cookieBase, raw: "keep=1; Max-Age=60; Expires=Wed, 09 Jun 2021 10:18:14 GMT", name: "keep", value: "1", hasMaxAge: true, maxAge: 60, expires: "2021-06-09T10:18:14Z", jar: "skipped" },
  { ...cookieBase, raw: "=x", value: "x" },
  { ...cookieBase, raw: "", error: "Not a valid cookie.", jar: "rejected" },
];

const test = (over: Partial<TestResultDTO>): TestResultDTO => ({
  index: 0,
  target: "status",
  expr: "",
  op: "equals",
  expected: "200",
  outcome: "passed",
  actual: "200",
  exists: true,
  message: "",
  code: "",
  schemaErrors: null,
  ...over,
});

const TESTS: TestResultDTO[] = [
  test({}),
  test({ index: 1, target: "header", expr: "X-Id", op: "exists", expected: "", outcome: "failed", actual: "", exists: false }),
  test({
    index: 2,
    target: "body",
    op: "matches_schema",
    expected: '{"type":"object"}',
    outcome: "failed",
    actual: "[]",
    schemaErrors: [{ path: "$", instancePath: "", schemaPath: "/type", keyword: "type", message: "" }],
  }),
  test({ index: 3, target: "jsonpath", expr: "$[", op: "exists", expected: "", outcome: "invalid", actual: "", exists: false }),
];

const request = (over: Partial<TimelineHopDTO["request"]> = {}): TimelineHopDTO["request"] => ({
  method: "GET",
  url: "https://api.test/items",
  proto: "HTTP/2.0",
  target: "/items",
  headers: [],
  head: "GET /items HTTP/2.0\r\nHost: api.test\r\n\r\n",
  body: "",
  bodyHex: true,
  bodyShown: 1024,
  bodyClipped: true,
  bodySize: 4096,
  bodyChunked: true,
  ...over,
});

const TIMELINE: TimelineDTO = {
  masked: true,
  hops: [
    {
      kind: "redirect",
      request: request({ proto: "HTTP/1.1", bodyHex: false, bodyClipped: false, bodyChunked: false }),
      startMs: 0,
      responseProto: "HTTP/1.1",
      statusCode: 302,
      status: "302 Found",
      responseHead: "HTTP/1.1 302 Found\r\nLocation: /items\r\n\r\n",
      timing: null,
      conn: null,
    },
    {
      kind: "auth",
      request: request({ proto: "HTTP/1.1", bodyHex: false, bodyClipped: false, bodyChunked: false }),
      startMs: 3,
      responseProto: "HTTP/1.1",
      statusCode: 401,
      status: "401 Unauthorized",
      responseHead: "HTTP/1.1 401 Unauthorized\r\n\r\n",
      timing: null,
      conn: null,
    },
    {
      kind: "final",
      request: request(),
      startMs: 5,
      responseProto: "HTTP/2.0",
      statusCode: 200,
      status: "200 OK",
      responseHead: "HTTP/2.0 200 OK\r\nContent-Type: application/json\r\n\r\n",
      timing: {
        totalMs: 1250,
        phases: [
          { phase: "dns", observed: false, startMs: 0, durationMs: 0, skipped: "no_lookup" },
          { phase: "connect", observed: false, startMs: 0, durationMs: 0, skipped: "reused" },
          { phase: "tls", observed: false, startMs: 0, durationMs: 0, skipped: "no_tls" },
          { phase: "request_write", observed: true, startMs: 0.5, durationMs: 0.25, skipped: "" },
          { phase: "wait", observed: true, startMs: 1, durationMs: 120, skipped: "" },
          { phase: "download", observed: false, startMs: 0, durationMs: 0, skipped: "not_final" },
        ],
      },
      conn: {
        reused: true,
        wasIdle: true,
        idleMs: 2500,
        localAddr: "192.0.2.10:50000",
        remoteAddr: "203.0.113.5:443",
        protocol: "h2",
        tls: {
          version: "TLS 1.3",
          cipherSuite: "TLS_AES_128_GCM_SHA256",
          serverName: "",
          alpn: "",
          resumed: true,
          certificates: [
            { subject: "CN=api.test", issuer: "CN=Test CA", notBefore: "2020-01-01T00:00:00Z", notAfter: "2021-01-01T00:00:00Z", dnsNames: [], ipAddresses: [], validity: "expired", selfSigned: false },
            { subject: "CN=Test CA", issuer: "CN=Test CA", notBefore: "2999-01-01T00:00:00Z", notAfter: "3000-01-01T00:00:00Z", dnsNames: [], ipAddresses: [], validity: "not_yet_valid", selfSigned: true },
          ],
        },
      },
    },
  ],
  events: [
    { atMs: 0.125, name: "get_conn", detail: "api.test:443" },
    { atMs: 1250, name: "body_done", detail: "" },
  ],
} as unknown as TimelineDTO;

function response(): ResponseDTO {
  return httpResponse({
    proto: "HTTP/2.0",
    protocol: "HTTP/2",
    redirects: 2,
    headers: [
      { name: "Content-Type", value: "application/json" },
      { name: "X-Request-Id", value: "abc" },
    ],
    cookies: COOKIES,
    contentEncoding: "gzip",
    size: 2048,
    bodySize: 4096,
    headerSize: 312,
    durationMs: 1234,
    timeoutMs: 5000,
    timeoutSource: "request",
    insecureTls: true,
    insecureTlsSource: "settings",
    charsetSource: "header",
    bodyBinary: true,
    bodyTruncated: true,
    bodyLimit: 4096,
    tests: TESTS,
    // Go's messages stay English: only the tab's own text is checked.
    extractions: [
      { index: 0, source: "jsonpath", expr: "$.t", variable: "token", scope: "runtime", secret: true, persist: true, outcome: "set", value: "••••••", valueClipped: true, message: "", code: "" },
      { index: 1, source: "status", expr: "", variable: "code", scope: "run", secret: false, persist: false, outcome: "skipped", value: "", valueClipped: false, message: "", code: "" },
    ],
    timeline: TIMELINE,
  });
}

const sseItem = (over: Partial<StreamItemDTO>): StreamItemDTO => ({
  type: "event",
  at: "2026-09-19T10:00:00.000Z",
  method: "",
  url: "",
  status: 0,
  statusText: "",
  proto: "",
  headers: null,
  index: 0,
  event: "",
  id: "",
  data: "",
  truncated: false,
  size: 0,
  retryMs: null,
  line: 0,
  text: "",
  attempt: 0,
  maxAttempts: 0,
  delayMs: 0,
  reason: "",
  events: 0,
  bytes: 0,
  reconnects: 0,
  discarded: false,
  elapsedMs: 0,
  tests: null,
  ...over,
});

beforeEach(() => {
  resetFakeWails();
  resetHttpSends();
  resetSseStreams();
  localStorage.clear();
  http.Send.mockReset().mockResolvedValue({ opId: "op_1" });
  http.Repeat.mockReset().mockResolvedValue({ opId: "op_1" });
  sse.Connect.mockReset().mockResolvedValue({ opId: "op_1" });
  setLocale(PSEUDO_LOCALE);
});

afterEach(() => {
  setLocale(DEFAULT_LOCALE);
});

describe("the pseudo-locale", () => {
  it("shows the HTTP response panel and every tab from the catalogue", async () => {
    render(
      <StoreProvider stores={createStores()}>
        <HttpResponsePanel id="req_1" />
      </StoreProvider>,
    );
    const panel = document.querySelector("[data-response-for]") as HTMLElement;
    expectNoEnglish([panel]);
    expect(screen.getByText(pseudoString("No response yet"))).toBeTruthy();

    act(() => httpSends().get("req_1").send());
    await act(settle);
    act(() => emit("http:op:progress", { opId: "op_1", seq: 1, done: 1024, total: 4096, message: "" }));
    expectNoEnglish([panel]);

    act(() => {
      emit("http:op:data", { opId: "op_1", seq: 2, items: [response()] });
      emit("http:op:done", { opId: "op_1", seq: 3, status: "ok", error: null, items: 1, batches: 1, elapsedMs: 5 });
    });
    expectNoEnglish([panel], 10);
    expect(screen.getByRole("tablist", { name: pseudoString("Response sections") })).toBeTruthy();

    for (const tab of ["Headers", "Cookies", "Tests", "Variables", "Timeline"]) {
      fireEvent.click(screen.getByRole("tab", { name: new RegExp(`^${escape(pseudoString(tab))}`) }));
      expectNoEnglish([panel], 5);
    }
    // The timeline's certificates list is open for a reused connection only when asked.
    const certs = panel.querySelector("details");
    if (certs !== null) certs.open = true;
    expectNoEnglish([panel], 5);
  });

  it("shows the SSE response panel from the catalogue", async () => {
    render(
      <StoreProvider>
        <SseResponsePanel id="req_sse" />
      </StoreProvider>,
    );
    const panel = document.querySelector("[data-response-for]") as HTMLElement;
    expectNoEnglish([panel]);

    act(() => sseStreams().get("req_sse").connect());
    await act(settle);
    act(() =>
      emit("sse:op:data", {
        opId: "op_1",
        seq: 1,
        items: [
          sseItem({ type: "open", method: "GET", url: "http://x/e", status: 200, statusText: "200 OK", proto: "HTTP/1.1", headers: [{ name: "Content-Type", value: "text/event-stream" }] }),
          sseItem({ type: "retry", retryMs: 3000 }),
          sseItem({ type: "event", index: 1, event: "price", id: "42", data: '{"px":1}', size: 8 }),
          sseItem({ type: "reconnect", id: "42", attempt: 1, maxAttempts: 5, delayMs: 3000, reason: "error", text: "The connection dropped." }),
        ],
      }),
    );
    act(() => sseStreams().get("req_sse").log.flush());
    expectNoEnglish([panel], 10);

    for (const tab of ["Merged", "Headers", "Events"]) {
      fireEvent.click(screen.getByRole("tab", { name: new RegExp(`^${escape(pseudoString(tab))}`) }));
      expectNoEnglish([panel], 3);
    }
  });

  it("shows a message log and its detail pane from the catalogue", () => {
    const frames = manualFrames();
    const source = new MessageSource({ maxMessages: 3, schedule: frames.schedule });
    const t0 = Date.UTC(2026, 8, 16, 10, 0, 0);
    source.append([
      { time: t0, direction: "system", kind: "connected", text: "" },
      { time: t0 + 1, direction: "sent", kind: "text", text: '{"hello":"world"}' },
      { time: t0 + 2, direction: "received", kind: "binary", data: new Uint8Array([0xde, 0xad, 0xbe, 0xef]), meta: { opcode: 2 } },
      { time: t0 + 3, direction: "received", kind: "text", badge: "update", text: "second" },
    ]);
    frames.frame();
    render(
      <div style={{ height: 400, display: "flex" }}>
        <MessageLog source={source} binaryToggle />
      </div>,
    );
    const log = screen.getByRole("region", { name: pseudoString("Messages") });
    expectNoEnglish([log], 10);

    fireEvent.change(screen.getByRole("textbox", { name: pseudoString("Search messages") }), { target: { value: "second" } });
    act(() => frames.frame());
    expectNoEnglish([log], 10);

    const grid = within(log).getByRole("grid");
    const binary = grid.querySelector<HTMLElement>('[data-seq="2"]');
    fireEvent.change(screen.getByRole("textbox", { name: pseudoString("Search messages") }), { target: { value: "" } });
    fireEvent.click(grid.querySelector<HTMLElement>('[data-seq="2"]') ?? (binary as HTMLElement));
    expectNoEnglish([log], 10);
    expect(screen.getByRole("region", { name: pseudoString("Message 3") })).toBeTruthy();
  });

  it("copies and saves the timeline in the language in use, Go's names kept", () => {
    const hop = TIMELINE.hops![2];
    const text = hopText(hop, 2, 3);
    expect(text).toContain(pseudoString("Timing"));
    expect(text).toContain(pseudoString("Content download"));
    expect(text).toContain("HTTP/2.0 200 OK");
    expect(eventText({ atMs: 0.125, name: "get_conn", detail: "api.test:443" })).toContain(`${pseudoString("Get connection")} (get_conn)`);
  });

  it("shows the Compare responses dialog from the catalogue (HTTP-40)", async () => {
    const same = (n: number, text: string) => ({ kind: "same", left: { n, text }, right: { n, text }, path: "$" });
    http.CompareResponses.mockImplementation(() =>
      Object.assign(
        Promise.resolve({
          status: { left: "200 OK", right: "404 Not Found", changed: true },
          headers: [{ name: "ETag", kind: "changed", left: ["a"], right: ["b"] }],
          headersAdded: 0,
          headersRemoved: 0,
          headersChanged: 1,
          body: {
            mode: "json",
            identical: false,
            rows: [
              same(1, "{"),
              { kind: "changed", left: { n: 2, text: '  "id": 1' }, right: { n: 2, text: '  "id": 2' }, path: "$.id" },
              ...Array.from({ length: 10 }, (_, i) => same(i + 3, `  "k${i}": ${i}`)),
              { kind: "added", left: null, right: { n: 13, text: '  "x": 1' }, path: "$.x" },
            ],
            changes: [{ path: "$.id", kind: "changed", left: "1", right: "2", leftType: "", rightType: "" }],
            added: 1,
            removed: 0,
            changed: 1,
            notices: [],
            leftSize: 10,
            rightSize: 10,
          },
        }),
        { cancel: vi.fn() },
      ),
    );
    history.Get.mockImplementation((id) =>
      Promise.resolve({
        id,
        request: { method: "GET", url: "https://a.test/", proto: "", headers: [], body: { text: "", base64: false, size: 0, clipped: false } },
        response: {
          proto: "", statusCode: 200, status: "200 OK", finalUrl: "", redirects: 0, headers: [], contentType: "",
          body: { text: "{}", base64: false, size: 2, clipped: false }, truncated: false,
        },
        durationMs: 1,
      }),
    );
    render(
      <StoreProvider stores={createStores()}>
        <CompareResponsesDialog
          id="req_1"
          open
          options={[
            { key: "h:h_1", label: "a" },
            { key: "h:h_2", label: "b" },
          ]}
          initialLeft="h:h_1"
          initialRight="h:h_2"
          onClose={() => undefined}
        />
      </StoreProvider>,
    );
    await act(settle);
    const dialog = screen.getByRole("dialog", { name: pseudoString("Compare responses") });
    fireEvent.click(within(dialog).getByRole("checkbox", { name: pseudoString("Show unchanged") }));
    expectNoEnglish([dialog], 5);
  });
});

/** A string as a regular expression that matches it literally. */
function escape(s: string): string {
  return s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");
}
