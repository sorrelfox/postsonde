import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";

const service = vi.hoisted(() => ({
  UpdateConfig: vi.fn<(id: string, config: unknown) => Promise<unknown>>(),
  MockTemplateNames: vi.fn<() => unknown>(),
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
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/format", () => ({
  FormatService: format,
}));

import { editMockConfig, flushAllMockConfigs, MOCK_UPDATE_DELAY_MS } from "../../commands/mockRoutes";
import { toLocalServer } from "../../commands/servers";
import type { MockHTTPConfigDTO, MockRouteDTO, RouteDiagnosticDTO, ServerDTO } from "../../lib/servers";
import { createStores, StoreProvider, type Stores } from "../../stores";
import { resetFakeWails, settle } from "../../test/fakeWails";
import { runningDTO } from "../../test/servers";
import { t } from "../../i18n";
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

function mockConfig(): MockHTTPConfigDTO {
  return {
    strictTrailingSlash: false,
    caseInsensitive: false,
    routes: [
      {
        id: "rt_users",
        enabled: true,
        method: "GET",
        path: "/users/:id",
        status: 200,
        headers: [{ enabled: true, name: "X-Mock", value: "1" }],
        body: { mode: "raw", type: "json", text: '{"id":1}' },
        delay: { mode: "none", ms: 0, minMs: 0, maxMs: 0 },
        responses: [],
        templated: false,
      },
      {
        id: "rt_create",
        enabled: true,
        method: "POST",
        path: "/users",
        status: 201,
        headers: [],
        body: { mode: "none", type: "", text: "" },
        delay: { mode: "fixed", ms: 500, minMs: 0, maxMs: 0 },
        responses: [
          {
            name: "conflict",
            status: 409,
            headers: [],
            body: { mode: "raw", type: "json", text: '{"error":"exists"}' },
            rules: [{ source: "json", name: "$.email", op: "equals", value: "taken@example.com" }],
            templated: false,
          },
        ],
        templated: false,
      },
    ],
  };
}

function mockDTO(config: MockHTTPConfigDTO = mockConfig(), diagnostics: RouteDiagnosticDTO[] = []): ServerDTO {
  return runningDTO({
    type: "mock_http",
    typeLabel: "Mock HTTP",
    name: "Mock HTTP",
    config: { mockHttp: config, webSocket: null, webhook: null },
    diagnostics,
  });
}

function setup(dto: ServerDTO = mockDTO()): Stores {
  const stores = createStores();
  stores.servers.dispatch({ type: "servers/loaded", servers: [toLocalServer(dto)], types: [] });
  stores.servers.dispatch({ type: "servers/select", id: dto.id });
  function View() {
    return <ServerDetails server={stores.servers.getState().byId[dto.id]} />;
  }
  const r = render(
    <StoreProvider stores={stores}>
      <View />
    </StoreProvider>,
  );
  // Re-render with the server from the store after each change.
  stores.servers.subscribe(() =>
    r.rerender(
      <StoreProvider stores={stores}>
        <View />
      </StoreProvider>,
    ),
  );
  return stores;
}

/** The config UpdateConfig was last called with. */
function lastSent(): MockHTTPConfigDTO & { routes: MockRouteDTO[] } {
  const calls = service.UpdateConfig.mock.calls;
  const config = (calls[calls.length - 1][1] as { mockHttp: MockHTTPConfigDTO }).mockHttp;
  return { ...config, routes: config.routes ?? [] };
}

/** The routes of the fixture. */
const fixtureRoutes = (): MockRouteDTO[] => mockConfig().routes ?? [];

beforeEach(() => {
  resetFakeWails();
  service.UpdateConfig.mockReset();
  format.ValidateJSON.mockReset();
  format.ValidateJSON.mockImplementation(() => cancellable({ valid: true, error: null }));
  service.UpdateConfig.mockImplementation((_id, config) =>
    Promise.resolve(mockDTO((config as { mockHttp: MockHTTPConfigDTO }).mockHttp)),
  );
});

afterEach(() => {
  vi.useRealTimers();
});

describe("MockRoutesEditor", () => {
  it("lists the routes and shows the selected one", () => {
    setup();
    expect(screen.getByRole("tab", { name: /Routes/ }).getAttribute("aria-selected")).toBe("true");
    expect(screen.getByText(t("servers.mock.specificity"))).toBeTruthy();
    const list = screen.getByRole("list", { name: "Routes" });
    const rows = within(list).getAllByRole("listitem");
    expect(rows).toHaveLength(2);
    expect(within(rows[0]).getByText("/users/:id")).toBeTruthy();
    expect(within(rows[1]).getByText("201")).toBeTruthy();
    expect(within(rows[1]).getByText("delay")).toBeTruthy();

    const detail = screen.getByRole("region", { name: "Route 1" });
    expect((within(detail).getByLabelText("Method") as HTMLSelectElement).value).toBe("GET");
    expect((within(detail).getByLabelText("Status") as HTMLInputElement).value).toBe("200");
    expect(within(detail).getByText("OK")).toBeTruthy();
    expect(within(detail).getByText(/Content-Type: application\/json/)).toBeTruthy();

    fireEvent.click(within(rows[1]).getByText("/users"));
    const second = screen.getByRole("region", { name: "Route 2" });
    expect((within(second).getByLabelText("Delay in ms") as HTMLInputElement).value).toBe("500");

    fireEvent.click(screen.getByRole("tab", { name: "Server" }));
    expect(screen.getByTestId("server-details")).toBeTruthy();
  });

  it("offers the Base64 body type of binary bodies (MOCK-03)", () => {
    setup();
    const detail = screen.getByRole("region", { name: "Route 1" });
    const language = within(detail).getByLabelText("Body language") as HTMLSelectElement;
    expect(Array.from(language.options).map((o) => o.textContent)).toContain("Base64 (binary)");
    fireEvent.change(language, { target: { value: "base64" } });
    expect(within(detail).getByText(/written in Base64; the server sends the decoded bytes/)).toBeTruthy();
    expect(within(detail).getByText(/Content-Type: application\/octet-stream/)).toBeTruthy();
  });

  it("sends edits to Go once they pause, and keeps the draft meanwhile", async () => {
    vi.useFakeTimers();
    const stores = setup();
    const detail = screen.getByRole("region", { name: "Route 1" });
    fireEvent.change(within(detail).getByLabelText("Status"), { target: { value: "404" } });
    fireEvent.change(within(detail).getByLabelText("Method"), { target: { value: "ANY" } });
    fireEvent.click(within(detail).getByRole("radio", { name: "Random" }));
    fireEvent.change(within(detail).getByLabelText("Longest delay in ms"), { target: { value: "250" } });
    expect(stores.servers.getState().mockDrafts.srv_1).toBeDefined();
    expect(screen.getByText("Not Found")).toBeTruthy();

    await act(() => vi.advanceTimersByTimeAsync(MOCK_UPDATE_DELAY_MS - 1));
    expect(service.UpdateConfig).not.toHaveBeenCalled();
    await act(() => vi.advanceTimersByTimeAsync(1));
    expect(service.UpdateConfig).toHaveBeenCalledTimes(1);
    expect(service.UpdateConfig.mock.calls[0][0]).toBe("srv_1");
    const sent = lastSent();
    expect(sent.routes[0]).toEqual({
      ...fixtureRoutes()[0],
      method: "ANY",
      status: 404,
      delay: { mode: "random", ms: 0, minMs: 0, maxMs: 250 },
    });
    expect(sent.routes[1]).toEqual(fixtureRoutes()[1]);
    await act(() => vi.advanceTimersByTimeAsync(0));
    expect(stores.servers.getState().mockDrafts.srv_1).toBeUndefined();
    expect(stores.servers.getState().byId.srv_1.mock?.routes[0].status).toBe(404);
  });

  it("adds, disables and deletes routes, and edits headers and options", async () => {
    const stores = setup();
    fireEvent.click(screen.getByRole("button", { name: /Add route/ }));
    expect(screen.getByRole("region", { name: "Route 3" })).toBeTruthy();
    fireEvent.click(screen.getByRole("checkbox", { name: "Enable POST /users" }));
    fireEvent.click(screen.getByRole("checkbox", { name: "Ignore case" }));
    await act(() => flushAllMockConfigs(stores));
    let sent = lastSent();
    expect(sent.caseInsensitive).toBe(true);
    expect(sent.routes.map((r) => [r.method, r.path, r.enabled])).toEqual([
      ["GET", "/users/:id", true],
      ["POST", "/users", false],
      ["GET", "/new", true],
    ]);
    expect(sent.routes[2].id).toMatch(/^rt_[0-9a-f]{12}$/);

    fireEvent.click(within(screen.getByRole("region", { name: "Route 3" })).getByRole("button", { name: "Delete" }));
    expect(screen.getByRole("region", { name: "Route 2" })).toBeTruthy();
    fireEvent.click(screen.getAllByText("/users/:id")[0]);
    const detail = screen.getByRole("region", { name: "Route 1" });
    const table = within(detail).getByRole("grid", { name: "Response headers" });
    fireEvent.change(within(table).getByDisplayValue("1"), { target: { value: "2" } });
    await act(() => flushAllMockConfigs(stores));
    sent = lastSent();
    expect(sent.routes).toHaveLength(2);
    expect(sent.routes[0].headers).toEqual([{ enabled: true, name: "X-Mock", value: "2" }]);
  });

  it("shows the problems Go reports next to the fields", () => {
    const diagnostics: RouteDiagnosticDTO[] = [
      {
        code: "mockroute.invalid_segment",
        routeIndex: 0,
        routeId: "rt_users",
        field: "pattern",
        start: 7,
        end: 10,
        responseIndex: -1,
        headerIndex: -1,
        ruleIndex: -1,
        message: "A parameter in braces must fill the whole segment.",
      },
      {
        code: "mockhttp.invalid_header_name",
        routeIndex: 0,
        routeId: "rt_users",
        field: "header",
        start: 0,
        end: 0,
        responseIndex: -1,
        headerIndex: 0,
        ruleIndex: -1,
        message: '"X Mock" is not a valid header name.',
      },
    ];
    const config = mockConfig();
    const [users, create] = config.routes ?? [];
    setup(mockDTO({ ...config, routes: [{ ...users, path: "/users/{id" }, create] }, diagnostics));
    const row = within(screen.getByRole("list", { name: "Routes" })).getAllByRole("listitem")[0];
    expect(within(row).getByRole("img", { name: "A parameter in braces must fill the whole segment." })).toBeTruthy();
    const detail = screen.getByRole("region", { name: "Route 1" });
    expect(within(detail).getByText("A parameter in braces must fill the whole segment.")).toBeTruthy();
    expect(within(detail).getByText(/Row 1: "X Mock" is not a valid header name./)).toBeTruthy();
    const marked = detail.querySelector(".cm-rangeMarker");
    expect(marked?.textContent).toBe("{id");
  });

  it("adds, edits, orders and deletes the other responses of a route and their rules", async () => {
    const stores = setup();
    fireEvent.click(screen.getAllByText("/users")[0]);
    const detail = screen.getByRole("region", { name: "Route 2" });
    const tabs = within(detail).getByRole("tablist", { name: "Responses" });
    expect(within(tabs).getAllByRole("tab").map((t) => t.textContent)).toEqual(["Default201", "conflict409"]);
    expect(within(tabs).getByRole("tab", { name: /Default/ }).getAttribute("aria-selected")).toBe("true");

    // The existing response: its name, rule and body.
    fireEvent.click(within(tabs).getByRole("tab", { name: /conflict/ }));
    expect((within(detail).getByLabelText("Name") as HTMLInputElement).value).toBe("conflict");
    expect((within(detail).getByLabelText("Rule 1 source") as HTMLSelectElement).value).toBe("json");
    expect((within(detail).getByLabelText("Rule 1 name") as HTMLInputElement).value).toBe("$.email");
    expect((within(detail).getByLabelText("Status") as HTMLInputElement).value).toBe("409");

    // A new response, with a header rule and a body-only rule.
    fireEvent.click(within(detail).getByRole("button", { name: /Add response/ }));
    expect(within(tabs).getByRole("tab", { name: /response-1/ }).getAttribute("aria-selected")).toBe("true");
    fireEvent.change(within(detail).getByLabelText("Name"), { target: { value: "admin" } });
    expect(within(detail).getByText(/No rules: this response answers only when/)).toBeTruthy();
    fireEvent.click(within(detail).getByRole("button", { name: /Add rule/ }));
    fireEvent.change(within(detail).getByLabelText("Rule 1 source"), { target: { value: "header" } });
    fireEvent.change(within(detail).getByLabelText("Rule 1 name"), { target: { value: "X-Role" } });
    fireEvent.change(within(detail).getByLabelText("Rule 1 value"), { target: { value: "admin" } });
    fireEvent.click(within(detail).getByRole("button", { name: /Add rule/ }));
    fireEvent.change(within(detail).getByLabelText("Rule 2 source"), { target: { value: "body" } });
    fireEvent.change(within(detail).getByLabelText("Rule 2 operator"), { target: { value: "exists" } });
    expect(within(detail).queryByLabelText("Rule 2 name")).toBeNull();
    expect(within(detail).queryByLabelText("Rule 2 value")).toBeNull();
    fireEvent.change(within(detail).getByLabelText("Status"), { target: { value: "403" } });
    fireEvent.click(within(detail).getByRole("button", { name: "Move response earlier" }));
    await act(() => flushAllMockConfigs(stores));
    let sent = lastSent().routes[1];
    expect(sent.responses?.map((r) => [r.name, r.status])).toEqual([
      ["admin", 403],
      ["conflict", 409],
    ]);
    expect(sent.responses?.[0].rules).toEqual([
      { source: "header", name: "X-Role", op: "equals", value: "admin" },
      { source: "body", name: "", op: "exists", value: "" },
    ]);
    expect(sent.responses?.[0].body).toEqual({ mode: "raw", type: "json", text: "{}" });
    expect(sent.status).toBe(201);

    // Remove a rule, then the response.
    fireEvent.click(within(detail).getByRole("button", { name: "Remove rule 1" }));
    fireEvent.click(within(detail).getByRole("button", { name: "Delete response" }));
    expect(within(tabs).getByRole("tab", { name: /Default/ }).getAttribute("aria-selected")).toBe("true");
    await act(() => flushAllMockConfigs(stores));
    sent = lastSent().routes[1];
    expect(sent.responses?.map((r) => r.name)).toEqual(["conflict"]);
    const row = within(screen.getByRole("list", { name: "Routes" })).getAllByRole("listitem")[1];
    expect(within(row).getByText("+1")).toBeTruthy();
  });

  it("turns templates on per response and marks the references", async () => {
    service.MockTemplateNames.mockReturnValue(
      cancellable([
        { name: "request.method", description: "The request method", source: "request" },
        { name: "request.query.", description: "A query parameter", source: "request" },
        { name: "$uuid", description: "A random UUID", source: "dynamic" },
      ]),
    );
    const config = mockConfig();
    const [users, create] = config.routes ?? [];
    const body = { mode: "raw", type: "text", text: "{{request.params.id}} {{nope}}" };
    const stores = setup(mockDTO({ ...config, routes: [{ ...users, body }, create] }));
    const detail = screen.getByRole("region", { name: "Route 1" });
    expect(within(detail).getByText(/Sent as written. Turn on Templated/)).toBeTruthy();

    fireEvent.click(within(detail).getByRole("checkbox", { name: "Templated" }));
    expect(within(detail).getByText(/Filled in for each request/)).toBeTruthy();
    await act(() => settle());
    const chips = [...detail.querySelectorAll(".cm-templateVariable")].map((c) => [
      c.textContent,
      c.getAttribute("data-unresolved") === "true",
    ]);
    expect(chips).toEqual([
      ["{{request.params.id}}", false],
      ["{{nope}}", true],
    ]);
    await act(() => flushAllMockConfigs(stores));
    const sent = lastSent().routes;
    expect(sent[0].templated).toBe(true);
    expect(sent[1].templated).toBe(false);
    expect(sent[1].responses?.[0].templated).toBe(false);
  });

  it("shows the problems of a response and its rules", () => {
    const diagnostics: RouteDiagnosticDTO[] = [
      {
        code: "mockhttp.invalid_rule",
        routeIndex: 1,
        routeId: "rt_create",
        field: "rule",
        start: 0,
        end: 0,
        responseIndex: 0,
        headerIndex: -1,
        ruleIndex: 0,
        message: "Invalid JSONPath: unexpected end.",
      },
    ];
    setup(mockDTO(mockConfig(), diagnostics));
    fireEvent.click(screen.getAllByText("/users")[0]);
    const detail = screen.getByRole("region", { name: "Route 2" });
    const tabs = within(detail).getByRole("tablist", { name: "Responses" });
    expect(within(within(tabs).getByRole("tab", { name: /conflict/ })).getByRole("img", { name: "Has problems" })).toBeTruthy();
    expect(within(detail).queryByText("Invalid JSONPath: unexpected end.")).toBeNull();
    fireEvent.click(within(tabs).getByRole("tab", { name: /conflict/ }));
    expect(within(detail).getByText("Invalid JSONPath: unexpected end.")).toBeTruthy();
  });

  it("keeps the draft and shows why Go rejected it", async () => {
    const stores = setup();
    const err = new Error("rejected");
    (err as Error & { cause: unknown }).cause = {
      kind: "invalid_argument",
      code: "mockhttp.too_many_routes",
      message: "A mock server can have at most 1000 routes.",
      hint: "",
      details: {},
      causes: [],
    };
    service.UpdateConfig.mockRejectedValue(err);
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const current = stores.servers.getState().byId.srv_1.mock;
    act(() => editMockConfig(stores, "srv_1", { ...current!, strictTrailingSlash: true }));
    await act(() => flushAllMockConfigs(stores));
    await act(settle);
    expect(screen.getByRole("alert").textContent).toBe("A mock server can have at most 1000 routes.");
    expect(stores.servers.getState().mockDrafts.srv_1?.strictTrailingSlash).toBe(true);
    warn.mockRestore();
  });
});
