import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";

const service = vi.hoisted(() => ({
  UpdateConfig: vi.fn<(id: string, config: unknown) => Promise<unknown>>(),
  DescribeGraphQLMock: vi.fn<(config: unknown) => unknown>(),
  PickGraphQLSchemaFile: vi.fn<() => unknown>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/servers", () => ({
  ServersService: service,
}));

import { graphQLMockUrl, toGraphQLMockConfigDTO } from "../../commands/graphqlMock";
import { toLocalServer } from "../../commands/servers";
import type { GraphQLMockDescriptionDTO, ServerDTO } from "../../lib/servers";
import { trafficToMessage } from "../../lib/serverTraffic";
import { createStores, serverKindBadge, StoreProvider, type Stores } from "../../stores";
import { resetFakeWails } from "../../test/fakeWails";
import { graphqlMockServerDTO, trafficDTO } from "../../test/servers";
import { GRAPHQL_MOCK_CONFIG_DELAY_MS } from "./GraphQLMockPane";
import { ServerDetails } from "./ServerView";

// The pane debounces with timers, which the tests fake: only promises are
// let run here.
async function microtasks() {
  for (let i = 0; i < 10; i++) await Promise.resolve();
}

const cancellable = <T,>(value: T) => Object.assign(Promise.resolve(value), { cancel: vi.fn() });

function description(over: Partial<GraphQLMockDescriptionDTO> = {}): GraphQLMockDescriptionDTO {
  return {
    source: "text",
    format: "sdl",
    types: 2,
    queryFields: 1,
    mutationFields: 0,
    subscriptionFields: 0,
    targets: [
      { target: "Query.user", type: "User", args: ["id"], root: true },
      { target: "User.id", type: "ID!", args: [], root: false },
      { target: "User.name", type: "String!", args: [], root: false },
    ],
    problems: [],
    sdl: "",
    ...over,
  };
}

function storesWith(dto: ServerDTO): Stores {
  const stores = createStores();
  stores.servers.dispatch({ type: "servers/loaded", servers: [toLocalServer(dto)], types: [] });
  return stores;
}

async function renderPane(stores: Stores) {
  const server = stores.servers.getState().byId.srv_1;
  render(
    <StoreProvider stores={stores}>
      <ServerDetails server={server} />
    </StoreProvider>,
  );
  await act(microtasks);
}

async function pause() {
  await act(async () => {
    vi.advanceTimersByTime(GRAPHQL_MOCK_CONFIG_DELAY_MS + 10);
  });
  await act(microtasks);
}

function lastConfig() {
  const call = service.UpdateConfig.mock.lastCall;
  if (!call) throw new Error("UpdateConfig was not called");
  return (call[1] as { graphqlMock: ReturnType<typeof toGraphQLMockConfigDTO> }).graphqlMock;
}

beforeEach(() => {
  resetFakeWails();
  for (const fn of Object.values(service)) fn.mockReset();
  service.DescribeGraphQLMock.mockImplementation(() => cancellable(description()));
  service.UpdateConfig.mockImplementation((_id, config) => Promise.resolve(graphqlMockServerDTO({ config: config as ServerDTO["config"] })));
});

afterEach(() => {
  vi.useRealTimers();
});

describe("GraphQL mock server view", () => {
  it("shows the endpoint and what Go loaded from the pasted schema", async () => {
    vi.useFakeTimers();
    await renderPane(storesWith(graphqlMockServerDTO()));
    expect(screen.getByRole("tablist", { name: "GraphQL mock server" })).toBeTruthy();
    expect(screen.getByText("http://localhost:52341/graphql")).toBeTruthy();
    await pause();
    expect(service.DescribeGraphQLMock).toHaveBeenCalledTimes(1);
    expect(screen.getByText("SDL with 2 types, 1 query fields and 0 mutation fields.")).toBeTruthy();
    const text = screen.getByRole("textbox", { name: "Schema text" }) as HTMLTextAreaElement;
    expect(text.value).toContain("type Query");

    fireEvent.change(text, { target: { value: "type Query { a: Int }" } });
    fireEvent.click(screen.getByRole("checkbox", { name: /Answer introspection/ }));
    await pause();
    expect(lastConfig()).toMatchObject({ schemaText: "type Query { a: Int }", introspection: false });
    expect(service.DescribeGraphQLMock).toHaveBeenCalledTimes(2);
  });

  it("chooses a schema file and shows a schema that does not load", async () => {
    vi.useFakeTimers();
    service.PickGraphQLSchemaFile.mockImplementation(() => cancellable("schema/catalog.graphql"));
    await renderPane(storesWith(graphqlMockServerDTO()));
    service.DescribeGraphQLMock.mockImplementation(() =>
      Object.assign(Promise.reject({ kind: "invalid_argument", hint: "", code: "graphqlmock.schema_failed", message: "The schema file \"catalog.graphql\": syntax error." }), { cancel: vi.fn() }),
    );
    fireEvent.click(screen.getByRole("radio", { name: "A file (SDL or introspection JSON)" }));
    await act(microtasks);
    await pause();
    expect(screen.getByText("schema/catalog.graphql")).toBeTruthy();
    expect(screen.getByRole("alert").textContent).toContain("syntax error");
    expect(lastConfig().schemaFile).toBe("schema/catalog.graphql");
  });

  it("edits the overrides and shows the ones that do not fit the schema", async () => {
    vi.useFakeTimers();
    service.DescribeGraphQLMock.mockImplementation(() =>
      cancellable(description({ problems: [{ overrideIndex: 1, field: "target", message: "Override 2 (User.nope): the type User has no field \"nope\"." }] })),
    );
    await renderPane(storesWith(graphqlMockServerDTO()));
    fireEvent.click(screen.getByRole("tab", { name: /Responses/ }));
    await pause();
    const list = screen.getByRole("list", { name: "Overrides" });
    expect(within(list).getAllByRole("listitem")).toHaveLength(1);
    expect((screen.getByRole("combobox", { name: "Override 1 target" }) as HTMLInputElement).value).toBe("Query.user");

    fireEvent.click(screen.getByRole("button", { name: "Add override" }));
    fireEvent.change(screen.getByRole("combobox", { name: "Override 2 target" }), { target: { value: "User.nope" } });
    fireEvent.change(screen.getByRole("combobox", { name: "Override 2 answer" }), { target: { value: "error" } });
    fireEvent.change(screen.getByRole("textbox", { name: "Override 2 error message" }), { target: { value: "Not found." } });
    fireEvent.change(screen.getByRole("textbox", { name: "Override 2 error code" }), { target: { value: "NOT_FOUND" } });
    fireEvent.change(screen.getByRole("textbox", { name: "List length" }), { target: { value: "5" } });
    await pause();
    expect(lastConfig()).toMatchObject({
      listLength: 5,
      overrides: [
        { target: "Query.user", mode: "value", value: '{"name": "Ada"}' },
        { enabled: true, target: "User.nope", mode: "error", error: "Not found.", code: "NOT_FOUND" },
      ],
    });
    expect(screen.getByText('Override 2 (User.nope): the type User has no field "nope".')).toBeTruthy();

    fireEvent.change(screen.getByRole("textbox", { name: "Override 1 value" }), { target: { value: "{" } });
    expect(screen.getByText("Not valid JSON: the server skips this override.")).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Move override 2 up" }));
    fireEvent.click(screen.getByRole("button", { name: "Remove override 2" }));
    await pause();
    expect(lastConfig().overrides).toEqual([{ enabled: true, target: "User.nope", args: "", mode: "error", value: "", error: "Not found.", code: "NOT_FOUND" }]);
  });

  it("badges GraphQL traffic with the operation", () => {
    const msg = trafficToMessage(
      trafficDTO({
        kind: "request",
        direction: "in",
        summary: "query Q (POST /graphql)",
        http: { method: "POST", target: "/graphql", proto: "HTTP/1.1", status: 0, headers: [], route: null, delayMs: 0 },
        graphql: { request: 3, operation: "Q", operationType: "query", variables: '{"id":1}', introspection: false, errors: 0, hasData: false },
      }),
    );
    expect(msg.badge).toBe("QUERY");
    expect(msg.meta).toMatchObject({ operation: "Q", "operation type": "query", variables: '{"id":1}', "graphql request": 3 });
    expect(serverKindBadge("graphql_mock")).toBe("GQL");
    expect(graphQLMockUrl(toLocalServer(graphqlMockServerDTO({ state: "stopped" })))).toBeNull();
  });
});
