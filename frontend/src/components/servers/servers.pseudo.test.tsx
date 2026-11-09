// The local servers UI in the pseudo-locale (UI-10): the
// Servers panel (list, empty state, new-server form) and the views of a
// mock HTTP, a WebSocket, a gRPC mock and a Socket.IO server. Every text of namespace
// "servers" (and of the message composer and log, "messagelog") must come
// from the catalogue: no English of their messages may be left. Text of
// areas not converted yet (the code editors, the rule and method labels of
// commands/ and lib/) is left out.
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const service = vi.hoisted(() => ({
  Types: vi.fn<() => Promise<unknown>>(),
  List: vi.fn<() => Promise<unknown>>(),
  Traffic: vi.fn<(id: string) => Promise<unknown>>(),
  UpdateConfig: vi.fn<(id: string, config: unknown) => Promise<unknown>>(),
  LocalCA: vi.fn<() => Promise<unknown>>(),
  MockRouteFromExample: vi.fn<(input: unknown) => unknown>(),
}));
const grpc = vi.hoisted(() => ({
  Describe: vi.fn<(input: unknown) => unknown>(),
  ValidateMessage: vi.fn<(input: unknown) => unknown>(),
  CompleteMessage: vi.fn<(input: unknown) => unknown>(),
  ExampleMessage: vi.fn<(input: unknown) => unknown>(),
}));
const format = vi.hoisted(() => ({
  ValidateJSON: vi.fn<(text: string) => unknown>(),
  BeautifyJSON: vi.fn<(text: string, indent: number) => unknown>(),
  MinifyJSON: vi.fn<(text: string) => unknown>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/servers", () => ({
  ServersService: service,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/grpc", () => ({
  GRPCService: grpc,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/format", () => ({
  FormatService: format,
}));

import { toLocalServer, toSioSocket, toWsClient } from "../../commands/servers";
import { DEFAULT_LOCALE, PSEUDO_LOCALE, setLocale, t } from "../../i18n";
import { pseudoString } from "../../i18n/pseudo";
import type { ServerDTO } from "../../lib/servers";
import { createStores, StoreProvider, type Stores } from "../../stores";
import { resetFakeWails, settle } from "../../test/fakeWails";
import { englishLeftovers, isPseudo, visibleTexts } from "../../test/pseudoLocale";
import { grpcMockServerDTO, runningDTO, serverDTO, sioServerDTO, sioSocketDTO, wsClientDTO, wsServerDTO } from "../../test/servers";
import { MockFromExampleDialog } from "./MockFromExampleDialog";
import { ServersPanel } from "./ServersPanel";
import { ServerDetails } from "./ServerView";

// jsdom has no layout: CodeMirror measures ranges for decorations.
for (const name of ["getClientRects", "getBoundingClientRect"] as const) {
  if (typeof Range.prototype[name] !== "function") {
    Object.defineProperty(Range.prototype, name, {
      configurable: true,
      value:
        name === "getClientRects" ? () => [] : () => ({ left: 0, right: 0, top: 0, bottom: 0, width: 0, height: 0 }),
    });
  }
}

const cancellable = <T,>(value: T) => Object.assign(Promise.resolve(value), { cancel: vi.fn() });

/**
 * Components of areas not converted yet inside the servers UI: the code
 * editors (editor), which still have English text of their own.
 */
function foreignElements(): Element[] {
  return [...document.querySelectorAll(".cm-editor")];
}

function expectNoEnglish(roots: readonly Element[]) {
  expect(englishLeftovers(roots, ["servers", "messagelog"], { exclude: foreignElements() })).toEqual([]);
  expect(visibleTexts(roots, foreignElements()).filter(isPseudo).length).toBeGreaterThan(3);
}

function mockDTO(): ServerDTO {
  return runningDTO({
    type: "mock_http",
    typeLabel: "Mock HTTP",
    name: "shop-api",
    config: {
      mockHttp: {
        strictTrailingSlash: false,
        caseInsensitive: false,
        routes: [
          {
            id: "rt_users",
            enabled: true,
            method: "POST",
            path: "/users",
            status: 201,
            headers: [{ enabled: true, name: "X-Mock", value: "1" }],
            body: { mode: "raw", type: "json", text: '{"id":1}' },
            delay: { mode: "random", ms: 0, minMs: 100, maxMs: 500 },
            responses: [
              {
                name: "",
                status: 409,
                headers: [],
                body: { mode: "raw", type: "text", text: "{{request.body.email}} exists" },
                rules: [
                  { source: "json", name: "$.email", op: "equals", value: "taken@example.com" },
                  { source: "header", name: "X-Mode", op: "matches", value: "^dup" },
                ],
                templated: false,
              },
            ],
            templated: false,
          },
        ],
      },
      webSocket: null,
      webhook: null,
    },
    diagnostics: [],
  });
}

function described() {
  const method = (name: string, kind: string) => ({
    name,
    fullName: `shop.v1.Orders.${name}`,
    inputType: "shop.v1.GetOrderRequest",
    outputType: "shop.v1.Order",
    clientStreaming: kind === "client_stream" || kind === "bidi",
    serverStreaming: kind === "server_stream" || kind === "bidi",
    kind,
    deprecated: false,
    comment: "",
  });
  return {
    services: [
      {
        name: "Orders",
        fullName: "shop.v1.Orders",
        file: "orders.proto",
        comment: "",
        deprecated: false,
        methods: [method("GetOrder", "unary"), method("Watch", "bidi")],
      },
    ],
    diagnostics: [],
    files: 1,
    errors: 0,
    cached: false,
  };
}

function storesWith(...dtos: ServerDTO[]): Stores {
  const stores = createStores();
  stores.servers.dispatch({ type: "servers/loaded", servers: dtos.map(toLocalServer), types: [] });
  return stores;
}

async function renderDetails(stores: Stores, id = "srv_1") {
  function View() {
    return <ServerDetails server={stores.servers.getState().byId[id]} />;
  }
  const r = render(
    <StoreProvider stores={stores}>
      <View />
    </StoreProvider>,
  );
  stores.servers.subscribe(() =>
    r.rerender(
      <StoreProvider stores={stores}>
        <View />
      </StoreProvider>,
    ),
  );
  await act(settle);
  return r.container;
}

beforeEach(() => {
  resetFakeWails();
  for (const fn of [...Object.values(service), ...Object.values(grpc), ...Object.values(format)]) fn.mockReset();
  service.UpdateConfig.mockImplementation(() => new Promise(() => undefined));
  service.LocalCA.mockImplementation(() => Promise.resolve({ path: "/data/ca.pem", subject: "CN=PostSonde local CA", notAfter: "", sha256: "ab", pem: "", trusted: false }));
  grpc.Describe.mockImplementation(() => cancellable(described()));
  grpc.ValidateMessage.mockImplementation(() => cancellable({ type: "shop.v1.Order", valid: true, error: null }));
  grpc.CompleteMessage.mockImplementation(() => cancellable({ items: [] }));
  format.ValidateJSON.mockImplementation(() => Promise.resolve({ valid: true, error: null }));
  setLocale(PSEUDO_LOCALE);
});

afterEach(() => {
  setLocale(DEFAULT_LOCALE);
});

describe("the servers UI in the pseudo-locale", () => {
  it("shows the Servers panel, its empty state and the new-server form from the catalogue", async () => {
    const empty = render(
      <StoreProvider stores={storesWith()}>
        <ServersPanel label={t("shell.view.servers")} />
      </StoreProvider>,
    );
    expect(screen.getByText(pseudoString("No servers"))).toBeTruthy();
    expect(screen.getByRole("button", { name: pseudoString("Start a mock server") })).toBeTruthy();
    expectNoEnglish([empty.container]);
    empty.unmount();

    const stores = storesWith(
      runningDTO({ id: "srv_1", name: "orders", type: "mock_http", typeLabel: "Mock HTTP" }),
      serverDTO({ id: "srv_2", name: "chat", type: "websocket", typeLabel: "WebSocket", port: 0 }),
      serverDTO({ id: "srv_3", name: "broken", state: "failed", error: { kind: "internal", code: "", message: "bind: in use", hint: "", details: {}, causes: [] } }),
    );
    const { container } = render(
      <StoreProvider stores={stores}>
        <ServersPanel label={t("shell.view.servers")} />
      </StoreProvider>,
    );
    await act(settle);
    expect(screen.getByText(pseudoString("Running"))).toBeTruthy();
    expect(screen.getAllByText(t("servers.address.anyPort", { host: "localhost" })).length).toBe(2);
    expectNoEnglish([container]);

    fireEvent.click(screen.getByRole("button", { name: pseudoString("New server") }));
    const form = screen.getByRole("form", { name: pseudoString("New server") });
    fireEvent.click(within(form).getByRole("checkbox", { name: pseudoString("Allow connections from other devices (LAN)") }));
    fireEvent.click(within(form).getByRole("checkbox", { name: t("servers.tls.serve", { protocol: "HTTPS" }) }));
    await act(settle);
    fireEvent.change(within(form).getAllByRole("textbox")[1], { target: { value: "x" } });
    fireEvent.submit(form);
    expect(within(form).getByRole("alert").textContent).toBe(pseudoString("The port must be a number from 0 to 65535 (0 or empty for any free port)."));
    expectNoEnglish([container]);
    fireEvent.click(within(form).getByRole("radio", { name: pseudoString("My certificate") }));
    expectNoEnglish([container]);
  });

  it("shows a mock HTTP server's routes and server details from the catalogue", async () => {
    const container = await renderDetails(storesWith(mockDTO()));
    expect(screen.getByRole("tablist", { name: pseudoString("Mock server") })).toBeTruthy();
    expect(screen.getByRole("button", { name: pseudoString("Add route") })).toBeTruthy();
    expectNoEnglish([container]);

    // The other response: its name, rules and the rule editor.
    fireEvent.click(screen.getByRole("tab", { name: /409/ }));
    expect(screen.getByRole("list", { name: pseudoString("Rules") })).toBeTruthy();
    expectNoEnglish([container]);

    fireEvent.click(screen.getByRole("tab", { name: pseudoString("Server") }));
    expect(screen.getByRole("heading", { name: "shop-api" })).toBeTruthy();
    expectNoEnglish([container]);
  });

  it("shows a WebSocket server's clients and settings from the catalogue", async () => {
    const stores = storesWith(
      wsServerDTO({
        name: "chat",
        config: {
          mockHttp: null,
          webSocket: { path: "/ws", subprotocols: ["chat"], echo: true, maxMessageSize: 0, rules: [{ enabled: true, match: "regex", pattern: "^get (\\d+)$", reply: "$1", delayMs: 20 }] },
          webhook: null,
        },
      }),
    );
    stores.servers.dispatch({ type: "servers/wsClients", id: "srv_1", clients: [wsClientDTO(), wsClientDTO({ id: "2", peer: "127.0.0.1:60002" })].map(toWsClient) });
    const container = await renderDetails(stores);
    expect(screen.getByRole("tablist", { name: pseudoString("WebSocket server") })).toBeTruthy();
    expect(screen.getByRole("button", { name: pseudoString("Send to all") })).toBeTruthy();
    fireEvent.click(screen.getAllByRole("button", { name: pseudoString("Disconnect…") })[0]);
    expectNoEnglish([container]);

    fireEvent.click(screen.getByRole("tab", { name: pseudoString("Settings") }));
    expect(screen.getByRole("list", { name: pseudoString("Auto-reply rules") })).toBeTruthy();
    expectNoEnglish([container]);
  });

  it("shows a Socket.IO server's sockets and settings from the catalogue", async () => {
    const stores = storesWith(sioServerDTO({ name: "live" }));
    stores.servers.dispatch({
      type: "servers/sioSockets",
      id: "srv_1",
      sockets: [sioSocketDTO({ rooms: ["lobby"] }), sioSocketDTO({ id: "1/admin", namespace: "/admin", auth: '{"token":"abc"}' })].map(toSioSocket),
    });
    const container = await renderDetails(stores);
    expect(screen.getByRole("tablist", { name: pseudoString("Socket.IO server") })).toBeTruthy();
    expect(screen.getByRole("button", { name: pseudoString("Emit to all") })).toBeTruthy();
    fireEvent.click(screen.getAllByRole("button", { name: pseudoString("Rooms…") })[0]);
    expectNoEnglish([container]);

    fireEvent.click(screen.getByRole("tab", { name: pseudoString("Settings") }));
    expect(screen.getByRole("list", { name: pseudoString("Auto-reply rules") })).toBeTruthy();
    expectNoEnglish([container]);
  });

  it("shows a gRPC mock server's methods and a method's canned response from the catalogue", async () => {
    const container = await renderDetails(storesWith(grpcMockServerDTO({ name: "orders" })));
    expect(screen.getByRole("tablist", { name: pseudoString("gRPC mock server") })).toBeTruthy();
    expect(screen.getByText(pseudoString("canned response"))).toBeTruthy();
    expectNoEnglish([container]);

    fireEvent.click(screen.getByRole("button", { name: t("servers.grpc.method", { name: "GetOrder" }) }));
    await act(settle);
    expect(screen.getByRole("region", { name: t("servers.grpc.responseOf", { name: "GetOrder" }) })).toBeTruthy();
    expectNoEnglish([container]);

    fireEvent.click(screen.getByRole("button", { name: t("servers.grpc.method", { name: "Watch" }) }));
    await act(settle);
    expect(screen.getByText(pseudoString("No canned response: calls answer UNIMPLEMENTED."))).toBeTruthy();
    expectNoEnglish([container]);
  });

  it("shows the Create mock route dialog from the catalogue", async () => {
    service.MockRouteFromExample.mockImplementation((input) =>
      cancellable({
        method: "GET",
        path: "/users/:id",
        derivedPath: "/users/:id",
        response: {
          name: "200 OK",
          status: 200,
          headers: [{ enabled: true, name: "Content-Type", value: "application/json" }],
          body: { mode: "raw", type: "json", text: "{}" },
          rules: [],
          templated: false,
        },
        dropped: [{ name: "Date", value: "Mon", reason: "date" }],
        notes: [],
        masked: false,
        existing: { index: 0, id: "rt_users", method: "GET", path: "/users/:id" },
        status: (input as { mode: string }).mode === "route" ? "existing" : "response_added",
        routeId: "rt_users",
        responseName: "200 OK",
        diagnostics: [],
        config: (input as { config: unknown }).config,
      }),
    );
    const example = {
      name: "200 OK", savedAt: "", request: null, statusCode: 200, statusText: "OK",
      headers: [], body: "{}", bodyEncoding: "", bodyClipped: false, bodySize: 0, extra: "",
    };
    render(
      <StoreProvider stores={storesWith(mockDTO())}>
        <MockFromExampleDialog source={{ kind: "example", example }} method="GET" url="{{base}}/users/:id" onClose={() => undefined} />
      </StoreProvider>,
    );
    await act(settle);
    const dialog = screen.getByRole("dialog", { name: pseudoString("Create mock route") });
    await act(settle);
    expect(within(dialog).getByRole("radio", { name: pseudoString("Add this response to it as a named response") })).toBeTruthy();
    expectNoEnglish([dialog]);
  });
});
