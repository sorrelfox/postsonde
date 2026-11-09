import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";

const service = vi.hoisted(() => ({
  UpdateConfig: vi.fn<(id: string, config: unknown) => Promise<unknown>>(),
  Create: vi.fn<(input: unknown) => Promise<unknown>>(),
  Start: vi.fn<(id: string) => Promise<unknown>>(),
  MockRouteFromExample: vi.fn<(input: unknown) => unknown>(),
}));
const history = vi.hoisted(() => ({
  Example: vi.fn<(input: { id: string; name: string }) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/servers", () => ({
  ServersService: service,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/history", () => ({ HistoryService: history }));

import type { MockSource } from "../../commands/mockFromExample";
import { toLocalServer } from "../../commands/servers";
import type { MockExampleInputDTO, MockExampleResultDTO } from "../../lib/mockFromExample";
import type { ResponseExampleDTO } from "../../lib/responseHistory";
import type { MockHTTPConfigDTO, MockRouteDTO, ServerDTO } from "../../lib/servers";
import { createStores, StoreProvider, type Stores } from "../../stores";
import { expectAccessible } from "../../test/a11y";
import { resetFakeWails } from "../../test/fakeWails";
import { runningDTO } from "../../test/servers";
import { MockFromExampleDialog } from "./MockFromExampleDialog";

const cancellable = <T,>(value: T) => Object.assign(Promise.resolve(value), { cancel: vi.fn() });

const example: ResponseExampleDTO = {
  name: "201 Created",
  savedAt: "2026-09-23T10:15:02Z",
  request: { method: "POST", url: "https://api.test/v1/users/42", headers: [], body: { mode: "", type: "", text: "" } as never, extra: "" },
  statusCode: 201,
  statusText: "Created",
  headers: [
    { name: "Content-Type", value: "application/json", description: "", enabled: true, extra: "" },
    { name: "Transfer-Encoding", value: "chunked", description: "", enabled: true, extra: "" },
  ],
  body: `{"id":42}`,
  bodyEncoding: "",
  bodyClipped: false,
  bodySize: 0,
  extra: "",
};

function route(id: string, method: string, path: string): MockRouteDTO {
  return {
    id,
    enabled: true,
    method,
    path,
    status: 200,
    headers: [],
    body: { mode: "raw", type: "json", text: "{}" },
    delay: { mode: "none", ms: 0, minMs: 0, maxMs: 0 },
    responses: [],
    templated: false,
  };
}

const helloConfig: MockHTTPConfigDTO = { strictTrailingSlash: false, caseInsensitive: false, routes: [route("rt_hello", "GET", "/hello")] };

function serverDTO(config: MockHTTPConfigDTO = helloConfig, over: Partial<ServerDTO> = {}): ServerDTO {
  return runningDTO({
    type: "mock_http",
    typeLabel: "Mock HTTP",
    name: "Users mock",
    config: { mockHttp: config, webSocket: null, webhook: null },
    diagnostics: [],
    ...over,
  });
}

/** Go's answer: the route is added, or (existing) a route answers the same path. */
function answer(input: MockExampleInputDTO, existing = false): MockExampleResultDTO {
  const path = input.path ?? "/v1/users/:id";
  const added = route("rt_post_v1_users_id", "POST", path);
  const status = !existing ? "added" : input.mode === "response" ? "response_added" : input.mode === "replace" ? "replaced" : "existing";
  return {
    method: "POST",
    path,
    derivedPath: "/v1/users/:id",
    response: {
      name: "201 Created",
      status: 201,
      headers: [{ enabled: true, name: "Content-Type", value: "application/json" }],
      body: { mode: "raw", type: "json", text: `{"id":42}` },
      rules: [],
      templated: false,
    },
    dropped: [{ name: "Transfer-Encoding", value: "chunked", reason: "hop_by_hop" }],
    notes: [{ severity: "info", code: "mockexample.path", message: "The path /v1 of the base URL is put in front." }],
    masked: false,
    existing: existing ? { index: 0, id: "rt_user", method: "POST", path: "/v1/users/:userId" } : null,
    status,
    routeId: status === "existing" ? "" : added.id,
    responseName: status === "response_added" ? input.name || "201 Created" : "",
    diagnostics: [],
    config: { ...input.config, routes: status === "added" ? [...(input.config.routes ?? []), added] : (input.config.routes ?? []) },
  };
}

const lastInput = () => {
  const calls = service.MockRouteFromExample.mock.calls;
  return calls[calls.length - 1][0] as MockExampleInputDTO;
};

function setup(source: MockSource, servers: ServerDTO[] = [serverDTO()]): { stores: Stores; onClose: ReturnType<typeof vi.fn> } {
  const stores = createStores();
  stores.servers.dispatch({ type: "servers/loaded", servers: servers.map(toLocalServer), types: [] });
  const onClose = vi.fn();
  render(
    <StoreProvider stores={stores}>
      <MockFromExampleDialog source={source} method="POST" url="{{baseUrl}}/users/:id" onClose={onClose} />
    </StoreProvider>,
  );
  return { stores, onClose };
}

beforeEach(() => {
  resetFakeWails();
  service.UpdateConfig.mockReset().mockImplementation((_id, config) =>
    Promise.resolve(serverDTO((config as { mockHttp: MockHTTPConfigDTO }).mockHttp)),
  );
  service.Create.mockReset().mockImplementation((input) =>
    Promise.resolve(
      serverDTO((input as { config: { mockHttp: MockHTTPConfigDTO } }).config.mockHttp, { id: "srv_new", name: "Mock HTTP", state: "stopped" }),
    ),
  );
  service.Start.mockReset().mockImplementation((id) => Promise.resolve(serverDTO(helloConfig, { id, name: "Mock HTTP" })));
  service.MockRouteFromExample.mockReset().mockImplementation((input) => cancellable(answer(input as MockExampleInputDTO)));
  history.Example.mockReset().mockResolvedValue(example);
});

afterEach(() => {
  vi.useRealTimers();
});

describe("Create mock route (MOCK-03)", () => {
  it("previews the route of a saved example and adds it to the server", async () => {
    const { onClose } = setup({ kind: "example", example });
    const dialog = await screen.findByRole("dialog", { name: "Create mock route" });
    await within(dialog).findByText(/The route POST \/v1\/users\/:id is added to Users mock\./);
    expect(lastInput()).toMatchObject({ method: "POST", url: "{{baseUrl}}/users/:id", path: null, mode: "route", name: "" });
    expect(lastInput().example).toEqual(example);
    expect(lastInput().config.routes?.map((r) => r.id)).toEqual(["rt_hello"]);
    expect(history.Example).not.toHaveBeenCalled();

    expect((within(dialog).getByLabelText("Mock server") as HTMLSelectElement).value).toBe(serverDTO().id);
    expect((within(dialog).getByLabelText("Path") as HTMLInputElement).value).toBe("/v1/users/:id");
    const headers = within(dialog).getByRole("table", { name: "Headers sent" });
    expect(within(headers).getByText("application/json")).toBeTruthy();
    expect(within(dialog).getByText("Sends 201")).toBeTruthy();
    expect(within(dialog).getByText(/JSON body, 9 B/)).toBeTruthy();
    const left = within(dialog).getByRole("region", { name: "Left out" });
    expect(within(left).getByText("Transfer-Encoding")).toBeTruthy();
    expect(within(left).getByText(/belongs to the connection/)).toBeTruthy();
    expect(within(dialog).getByRole("list", { name: "Notes" }).textContent).toContain("base URL");
    expectAccessible(dialog);

    fireEvent.click(within(dialog).getByRole("button", { name: "Add route" }));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(service.UpdateConfig).toHaveBeenCalledTimes(1);
    const sent = (service.UpdateConfig.mock.calls[0][1] as { mockHttp: MockHTTPConfigDTO }).mockHttp;
    expect(sent.routes?.map((r) => r.id)).toEqual(["rt_hello", "rt_post_v1_users_id"]);
  });

  it("makes the example of a history entry in Go first", async () => {
    setup({ kind: "history", id: "h_1" });
    const dialog = await screen.findByRole("dialog", { name: "Create mock route" });
    await within(dialog).findByText(/is added to Users mock/);
    expect(history.Example).toHaveBeenCalledWith({ id: "h_1", name: "" });
    expect(lastInput().example).toEqual(example);
  });

  it("adds the response to the route already there, or replaces its response", async () => {
    service.MockRouteFromExample.mockImplementation((input) => cancellable(answer(input as MockExampleInputDTO, true)));
    setup({ kind: "example", example });
    const dialog = await screen.findByRole("dialog", { name: "Create mock route" });
    // A route answers: the response is added to it by default.
    await within(dialog).findByText(/The response “201 Created” is added to the route/);
    expect(lastInput().mode).toBe("response");
    expect(within(dialog).getByText(/already has the route POST \/v1\/users\/:userId/)).toBeTruthy();
    expect((within(dialog).getByRole("radio", { name: /as a named response/ }) as HTMLInputElement).checked).toBe(true);
    expect(within(dialog).getByText(/X-PostSonde-Response: 201 Created/)).toBeTruthy();
    expect(within(dialog).getByRole("button", { name: "Add response" })).toBeTruthy();

    fireEvent.change(within(dialog).getByLabelText("Response name"), { target: { value: "created" } });
    await waitFor(() => expect(lastInput().name).toBe("created"));

    fireEvent.click(within(dialog).getByRole("radio", { name: /Replace the route's own response/ }));
    await within(dialog).findByText(/The response of the route .* is replaced\./);
    expect(lastInput().mode).toBe("replace");
    expect(within(dialog).getByRole("button", { name: "Replace response" })).toBeTruthy();

    fireEvent.click(within(dialog).getByRole("radio", { name: /Keep the route as it is/ }));
    await within(dialog).findByText(/is kept as it is; nothing is added/);
    const confirm = within(dialog).getByRole("button", { name: "Add route" }) as HTMLButtonElement;
    expect(confirm.disabled || confirm.getAttribute("aria-disabled") === "true").toBe(true);
  });

  it("asks again once the path is edited", async () => {
    setup({ kind: "example", example });
    const dialog = await screen.findByRole("dialog", { name: "Create mock route" });
    await within(dialog).findByText(/is added to/);
    fireEvent.change(within(dialog).getByLabelText("Path"), { target: { value: "/users/:id" } });
    await waitFor(() => expect(lastInput().path).toBe("/users/:id"));
    await within(dialog).findByText(/The route POST \/users\/:id is added/);
    fireEvent.click(within(dialog).getByRole("button", { name: /Use the request's \(\/v1\/users\/:id\)/ }));
    await waitFor(() => expect(lastInput().path).toBe("/v1/users/:id"));
  });

  it("creates and starts a mock server when there is none", async () => {
    const { stores, onClose } = setup({ kind: "example", example }, []);
    const dialog = await screen.findByRole("dialog", { name: "Create mock route" });
    await within(dialog).findByText(/is added to a new mock server/);
    expect((within(dialog).getByLabelText("Mock server") as HTMLSelectElement).value).toBe("");
    expect(lastInput().config.routes).toEqual([]);
    fireEvent.click(within(dialog).getByRole("button", { name: "Create server with route" }));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(service.Create).toHaveBeenCalledTimes(1);
    const created = service.Create.mock.calls[0][0] as { type: string; config: { mockHttp: MockHTTPConfigDTO } };
    expect(created.type).toBe("mock_http");
    expect(created.config.mockHttp.routes?.map((r) => r.path)).toEqual(["/v1/users/:id"]);
    expect(service.Start).toHaveBeenCalledWith("srv_new");
    await act(async () => {});
    expect(stores.servers.getState().ids).toContain("srv_new");
  });

  it("leaves out the servers of another project", async () => {
    const other = serverDTO(helloConfig, { id: "srv_other", name: "Other", folder: { workspaceId: "ws_other", folderId: "f", folderName: "F" } });
    setup({ kind: "example", example }, [other]);
    const dialog = await screen.findByRole("dialog", { name: "Create mock route" });
    const options = Array.from((within(dialog).getByLabelText("Mock server") as HTMLSelectElement).options).map((o) => o.textContent);
    expect(options).toEqual(["New mock server"]);
  });
});
