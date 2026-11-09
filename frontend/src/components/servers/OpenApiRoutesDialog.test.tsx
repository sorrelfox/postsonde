import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";

const service = vi.hoisted(() => ({
  UpdateConfig: vi.fn<(id: string, config: unknown) => Promise<unknown>>(),
  PickOpenAPIFile: vi.fn<() => unknown>(),
  GenerateMockRoutes: vi.fn<(input: unknown) => unknown>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/servers", () => ({
  ServersService: service,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/format", () => ({
  FormatService: { ValidateJSON: () => Object.assign(Promise.resolve({ valid: true, error: null }), { cancel: vi.fn() }) },
}));

import { flushAllMockConfigs } from "../../commands/mockRoutes";
import { toLocalServer } from "../../commands/servers";
import type {
  MockHTTPConfigDTO,
  MockOpenAPIInputDTO,
  MockOpenAPIResultDTO,
  MockRouteDTO,
  ServerDTO,
} from "../../lib/servers";
import { createStores, StoreProvider, type Stores } from "../../stores";
import { resetFakeWails } from "../../test/fakeWails";
import { runningDTO } from "../../test/servers";
import { PREFIX_DELAY_MS } from "./OpenApiRoutesDialog";
import { ServerDetails } from "./ServerView";

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
const rejected = (err: unknown) => Object.assign(Promise.reject(err), { cancel: vi.fn() });

function route(id: string, method: string, path: string, status: number): MockRouteDTO {
  return {
    id,
    enabled: true,
    method,
    path,
    status,
    headers: [],
    body: { mode: "raw", type: "json", text: "{}" },
    delay: { mode: "none", ms: 0, minMs: 0, maxMs: 0 },
    responses: [],
    templated: false,
  };
}

const existing: MockHTTPConfigDTO = {
  strictTrailingSlash: false,
  caseInsensitive: false,
  routes: [route("rt_hello", "GET", "/hello", 200)],
};

function result(input: MockOpenAPIInputDTO): MockOpenAPIResultDTO {
  const prefix = input.prefix ?? "/v1";
  const generated = [
    route("rt_listpets", "GET", `${prefix}/pets`, 200),
    route("rt_createpet", "POST", `${prefix}/pets`, 201),
    route("rt_hello_gen", "GET", "/hello", 200),
  ];
  const replace = input.mode === "replace";
  const added = replace ? generated : generated.slice(0, 2);
  return {
    title: "Petstore",
    version: "1.0.0",
    openapi: "3.0.3",
    swagger: "",
    defaultPrefix: "/v1",
    prefix,
    routes: generated.map((r, i) => ({
      route: r,
      operation: `${r.method} ${r.path.replace(prefix, "")}`,
      operationId: r.id.slice(3),
      summary: i === 0 ? "List all pets" : "",
      response: String(r.status),
      mediaType: "application/json",
      status: !replace && i === 2 ? "existing" : "added",
      id: !replace && i === 2 ? "" : r.id,
    })),
    diagnostics: [
      {
        severity: "warning",
        code: "mockopenapi.status",
        message: "The operation has no 2xx or default response.",
        operation: "DELETE /pets/{petId}",
        routeIndex: 1,
        line: 12,
        column: 3,
        pointer: "",
      },
      {
        severity: "info",
        code: "mockopenapi.media_type",
        message: "The body is application/json.",
        operation: "GET /pets",
        routeIndex: 0,
        line: 0,
        column: 0,
        pointer: "",
      },
    ],
    config: { ...existing, routes: replace ? added : [...(existing.routes ?? []), ...added] },
    added: added.length,
    kept: replace ? 0 : 1,
    removed: replace ? 1 : 0,
  };
}

function dto(config: MockHTTPConfigDTO = existing): ServerDTO {
  return runningDTO({
    type: "mock_http",
    typeLabel: "Mock HTTP",
    name: "Mock HTTP",
    config: { mockHttp: config, webSocket: null, webhook: null },
    diagnostics: [],
  });
}

function setup(): Stores {
  const stores = createStores();
  const server = dto();
  stores.servers.dispatch({ type: "servers/loaded", servers: [toLocalServer(server)], types: [] });
  stores.servers.dispatch({ type: "servers/select", id: server.id });
  function View() {
    return <ServerDetails server={stores.servers.getState().byId[server.id]} />;
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
  return stores;
}

const lastInput = () => {
  const calls = service.GenerateMockRoutes.mock.calls;
  return calls[calls.length - 1][0] as MockOpenAPIInputDTO;
};

beforeEach(() => {
  resetFakeWails();
  service.UpdateConfig.mockReset();
  service.PickOpenAPIFile.mockReset();
  service.GenerateMockRoutes.mockReset();
  service.UpdateConfig.mockImplementation((_id, config) =>
    Promise.resolve(dto((config as { mockHttp: MockHTTPConfigDTO }).mockHttp)),
  );
  service.PickOpenAPIFile.mockImplementation(() => cancellable("/specs/petstore.yaml"));
  service.GenerateMockRoutes.mockImplementation((input) => cancellable(result(input as MockOpenAPIInputDTO)));
});

afterEach(() => {
  vi.useRealTimers();
});

const openPreview = async () => {
  fireEvent.click(screen.getByRole("button", { name: /Generate routes from OpenAPI/ }));
  return await screen.findByRole("dialog", { name: "Generate routes from OpenAPI" });
};

describe("Generate routes from OpenAPI", () => {
  it("previews the generated routes and adds them to the existing ones", async () => {
    const stores = setup();
    const dialog = await openPreview();
    expect(service.PickOpenAPIFile).toHaveBeenCalledTimes(1);
    expect(lastInput()).toMatchObject({ path: "/specs/petstore.yaml", prefix: null, mode: "merge" });
    expect(lastInput().config.routes?.map((r) => r.id)).toEqual(["rt_hello"]);

    await within(dialog).findByText("petstore.yaml");
    expect(within(dialog).getByText(/Petstore 1\.0\.0/)).toBeTruthy();
    expect((within(dialog).getByLabelText("Path prefix") as HTMLInputElement).value).toBe("/v1");
    const list = within(dialog).getByRole("list", { name: "Generated routes" });
    const rows = within(list).getAllByRole("listitem");
    expect(rows).toHaveLength(3);
    expect(within(rows[0]).getByText("/v1/pets")).toBeTruthy();
    expect(within(rows[0]).getByText("List all pets")).toBeTruthy();
    expect(within(rows[2]).getByText("already there")).toBeTruthy();
    expect(within(dialog).getByText(/2 routes are added to the 1 route there now\. 1 operation already has its route\./)).toBeTruthy();
    const problems = within(dialog).getByRole("list", { name: "Problems" });
    expect(within(problems).getByText(/no 2xx or default response/)).toBeTruthy();
    expect(within(problems).getByText(/line 12/)).toBeTruthy();
    expect(within(dialog).getByText("1 note")).toBeTruthy();

    fireEvent.click(within(dialog).getByRole("button", { name: "Add 2 routes" }));
    await waitFor(() => expect(screen.queryByRole("dialog")).toBeNull());
    const list2 = screen.getByRole("list", { name: "Routes" });
    expect(within(list2).getAllByRole("listitem")).toHaveLength(3);
    // The first added route is selected.
    expect((screen.getByRole("region", { name: "Route 2" }) as HTMLElement).textContent).toBeTruthy();

    await act(() => flushAllMockConfigs(stores));
  });

  it("sends the applied routes to Go", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    const stores = setup();
    const dialog = await openPreview();
    await within(dialog).findByText(/routes are added/);
    fireEvent.click(within(dialog).getByRole("button", { name: "Add 2 routes" }));
    await act(() => flushAllMockConfigs(stores));
    expect(service.UpdateConfig).toHaveBeenCalledTimes(1);
    const sent = (service.UpdateConfig.mock.calls[0][1] as { mockHttp: MockHTTPConfigDTO }).mockHttp;
    expect(sent.routes?.map((r) => r.id)).toEqual(["rt_hello", "rt_listpets", "rt_createpet"]);
  });

  it("asks again when the mode or prefix changes", async () => {
    vi.useFakeTimers({ shouldAdvanceTime: true });
    setup();
    const dialog = await openPreview();
    await within(dialog).findByText(/routes are added/);

    fireEvent.click(within(dialog).getByRole("radio", { name: /Replace all routes/ }));
    await within(dialog).findByText(/3 routes from 3 operations replace 1 route\./);
    expect(lastInput().mode).toBe("replace");
    expect(within(dialog).getByRole("button", { name: "Replace routes" })).toBeTruthy();

    const calls = service.GenerateMockRoutes.mock.calls.length;
    // Strict fake timers for the debounce: with shouldAdvanceTime a slow
    // machine lets real time run the timer before the "not yet" check.
    vi.useRealTimers();
    vi.useFakeTimers();
    fireEvent.change(within(dialog).getByLabelText("Path prefix"), { target: { value: "/api" } });
    // The button waits for the new preview.
    expect((within(dialog).getByRole("button", { name: "Replace routes" }) as HTMLButtonElement).disabled).toBe(true);
    await act(() => vi.advanceTimersByTimeAsync(PREFIX_DELAY_MS - 1));
    expect(service.GenerateMockRoutes.mock.calls.length).toBe(calls);
    await act(() => vi.advanceTimersByTimeAsync(1));
    expect(lastInput()).toMatchObject({ prefix: "/api", mode: "replace" });
    vi.useRealTimers();
    expect(await within(dialog).findAllByText("/api/pets")).toHaveLength(2);

    vi.useFakeTimers();
    fireEvent.click(within(dialog).getByRole("button", { name: /Use the document's \(\/v1\)/ }));
    await act(() => vi.advanceTimersByTimeAsync(PREFIX_DELAY_MS));
    expect(lastInput().prefix).toBe("/v1");
  });

  it("does nothing when the file dialog is cancelled", async () => {
    service.PickOpenAPIFile.mockImplementation(() => cancellable(""));
    setup();
    fireEvent.click(screen.getByRole("button", { name: /Generate routes from OpenAPI/ }));
    await waitFor(() => expect(service.PickOpenAPIFile).toHaveBeenCalled());
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(service.GenerateMockRoutes).not.toHaveBeenCalled();
  });

  it("shows why a file cannot be read, and Cancel changes nothing", async () => {
    service.GenerateMockRoutes.mockImplementation(() =>
      rejected({ kind: "parse", code: "openapi.not_openapi", message: "The file is not an OpenAPI document.", hint: "" }),
    );
    setup();
    const dialog = await openPreview();
    expect((await within(dialog).findByRole("alert")).textContent).toContain("not an OpenAPI document");
    expect((within(dialog).getByRole("button", { name: /Add 0 routes/ }) as HTMLButtonElement).disabled).toBe(true);
    fireEvent.click(within(dialog).getByRole("button", { name: "Cancel" }));
    expect(screen.queryByRole("dialog")).toBeNull();
    expect(within(screen.getByRole("list", { name: "Routes" })).getAllByRole("listitem")).toHaveLength(1);
    expect(service.UpdateConfig).not.toHaveBeenCalled();
  });

  it("shows a file dialog failure in the editor", async () => {
    service.PickOpenAPIFile.mockImplementation(() =>
      rejected({ kind: "unsupported", code: "servers.pick_unavailable", message: "Choosing a file is not available here.", hint: "" }),
    );
    setup();
    fireEvent.click(screen.getByRole("button", { name: /Generate routes from OpenAPI/ }));
    expect((await screen.findByRole("alert")).textContent).toContain("Choosing a file is not available here.");
  });
});
