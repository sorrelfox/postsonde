import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { completionStatus, currentCompletions, startCompletion } from "@codemirror/autocomplete";
import { EditorView } from "@codemirror/view";
import { beforeEach, describe, expect, it, vi } from "vitest";

const service = vi.hoisted(() => ({
  Request: vi.fn<(id: string) => Promise<unknown>>(),
  UpdateRequest: vi.fn<(input: { request: unknown; revision: number }) => Promise<void>>(),
  Flush: vi.fn<(id: string) => Promise<void>>(),
  InheritedAuth: () => Object.assign(Promise.resolve({ type: "none", disabled: false, folderId: "", folderName: "" }), { cancel: () => undefined }),
}));
const cancellable = <T,>(value: T) => Object.assign(Promise.resolve(value), { cancel: () => undefined });

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({
  CollectionService: service,
}));
const http = vi.hoisted(() => ({
  Send: vi.fn<(input: { requestId: string }) => Promise<unknown>>(),
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http", () => ({
  HTTPService: http,
}));
vi.mock(
  "../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/ops",
  async () => (await import("../../test/fakeWails")).fakeOpsBindings,
);
const graphqlService = vi.hoisted(() => ({
  SchemaInfo: vi.fn<(input: { requestId: string }) => unknown>(),
  FetchSchema: vi.fn<(input: { requestId: string }) => unknown>(),
  ReloadSchemaFile: vi.fn<(input: { requestId: string }) => unknown>(),
  PickSchemaFile: vi.fn<() => unknown>(),
  SaveSchemaSDL: vi.fn<(input: { key: string; name: string }) => unknown>(),
  Diagnostics: vi.fn<(input: { key: string; text: string }) => unknown>(),
  Complete: vi.fn<(input: { key: string; text: string; offset: number }) => unknown>(),
  DocsOverview: vi.fn<(input: { key: string }) => unknown>(),
  DocsType: vi.fn<(input: { key: string; name: string }) => unknown>(),
  DocsSearch: vi.fn<(input: { key: string; query: string; limit: number }) => unknown>(),
  DocsTarget: vi.fn<(input: { key: string; text: string; offset: number }) => unknown>(),
  GenerateOperation: vi.fn<(input: { key: string; kind: string; type: string; field: string; depth: number; text: string }) => unknown>(),
  FillFields: vi.fn<(input: { key: string; text: string; offset: number; depth: number }) => unknown>(),
  Subscribe: vi.fn<(input: { requestId: string; allowUnresolved: boolean }) => unknown>(),
  StopSubscription: vi.fn<(input: { opId: string }) => unknown>(),
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/graphql", () => ({
  GraphQLService: graphqlService,
}));
const format = vi.hoisted(() => ({
  ValidateJSON: vi.fn<(text: string) => unknown>(),
  CheckGraphQLVariables: vi.fn<(query: string, variables: string, operationName: string) => unknown>(),
  PrettifyGraphQL: vi.fn<(text: string) => unknown>(),
  MinifyGraphQL: vi.fn<(text: string) => unknown>(),
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/format", () => ({
  FormatService: format,
}));
/** WorkspaceService's body file calls (the Files tab, GQL-07). */
const workspace = vi.hoisted(() => ({
  PickBodyFile: vi.fn<() => unknown>(),
  BodyFile: vi.fn<(path: string) => unknown>(),
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/workspace", () => ({
  WorkspaceService: workspace,
}));

import type { KeyValueDTO, PartDTO, RequestDTO } from "../../lib/collection";
import { forgetBodyFiles } from "../../commands/useBodyFile";
import { resetGraphqlSubscriptions } from "../../commands/graphqlSubscriptions";
import { resetHttpSends } from "../../commands/httpSends";
import { GRAPHQL_CHECK_DELAY_MS } from "../../commands/useGraphqlDiagnostics";
import { IS_MAC } from "../../lib/shortcuts";
import { createStores, StoreProvider, type Stores, type TreeNode } from "../../stores";
import { emit, opsCancel, resetFakeWails, settle } from "../../test/fakeWails";
import { httpResponse } from "../../test/httpResponse";
import { TabPanel } from "../tabs/TabPanel";
import { currentDiagnostics } from "./diagnostics";
import { currentErrorMarker } from "./errorMarker";
import { operationChoice, resetGraphqlDocsPanel } from "./GraphqlEditor";
import { installShortcuts } from "../../commands/shortcuts";

const row = (name: string, value: string, over: Partial<KeyValueDTO> = {}): KeyValueDTO => ({
  name,
  value,
  description: "",
  enabled: true,
  extra: "",
  ...over,
});

const QUERY = "query User($id: ID!) {\n  user(id: $id) {\n    name\n  }\n}";

function request(over: Partial<RequestDTO> = {}): RequestDTO {
  return {
    id: "gql",
    kind: "graphql",
    name: "User by ID",
    description: "",
    method: "",
    url: "{{baseUrl}}/graphql",
    params: [],
    pathParams: [],
    headers: [row("X-Trace", "1")],
    body: { mode: "", type: "", text: "", fields: [], parts: [], path: "", contentType: "", extra: "" },
    query: QUERY,
    variables: '{"id": "{{userId}}"}',
    operationName: "",
    connectionParams: "",
    schemaFile: "",
    address: "",
    sendMode: "",
    lineEnding: "",
    message: "",
    subprotocols: [],
    messageFormat: "",
    service: "",
    proto: { files: [], importPaths: [], inherit: false, extra: "" },
    metadata: [],
    bodyExamples: [],
    auth: { type: "inherit", username: "", password: "", prefix: null, token: "", key: "", value: "", in: "", disabled: false, localSecrets: [], extra: "" },
    settings: { timeoutMs: null, followRedirects: null, maxRedirects: null, followOriginalMethod: null, forwardAuthorization: null, verifyTls: null, connectTimeoutMs: null, urlEncoding: "", httpVersion: "", sendCookies: null, storeCookies: null, tls: null, deadlineMs: null, extra: "" },
    extra: '{"x-custom":1}',
    ...over,
  };
}

const variablesCheck = (over: Record<string, unknown> = {}) => ({
  error: null,
  notObject: false,
  resolved: true,
  operation: "User",
  declared: [],
  missing: [],
  unknown: [],
  ...over,
});

const schemaInfo = (over: Record<string, unknown> = {}) => ({
  source: "introspection",
  file: "",
  modifiedAt: "",
  error: null,
  problemList: [],
  endpoint: "https://api.test/graphql",
  cached: false,
  key: "",
  fetchedAt: "",
  typeCount: 0,
  query: "",
  problems: 0,
  ...over,
});

const diagnostics = (over: Record<string, unknown> = {}) => ({
  schema: false,
  operations: [{ name: "User", type: "query" }],
  diagnostics: [],
  count: 0,
  syntaxCount: 0,
  ...over,
});

const node = (over: Partial<TreeNode> & { id: string }): TreeNode =>
  ({
    kind: "request",
    parentId: null,
    name: over.id,
    path: `${over.id}.json`,
    fileKind: "graphql",
    readOnly: "",
    method: "POST",
    ...over,
  }) as TreeNode;

async function setup(dto: RequestDTO = request(), opts: { readOnly?: boolean } = {}): Promise<Stores> {
  service.Request.mockImplementation(() => Promise.resolve(structuredClone(dto)));
  const stores = createStores();
  const nodes = [node({ id: "gql", name: "User by ID", readOnly: opts.readOnly === true ? "newer_version" : "" })];
  stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
  stores.collection.dispatch({ type: "collection/load", workspaceId: "ws_1", nodes, rootIds: ["gql"], readOnly: "" });
  stores.tabs.dispatch({ type: "tabs/restore", order: ["gql"], activeId: "gql" });
  render(
    <StoreProvider stores={stores}>
      <TabPanel />
    </StoreProvider>,
  );
  await act(settle);
  return stores;
}

const editor = () => document.querySelector("[data-request-id]") as HTMLElement;
const viewOf = (name: string) => {
  const view = EditorView.findFromDOM(within(editor()).getByRole("textbox", { name }));
  if (view === null) throw new Error(`${name} is not a CodeMirror editor`);
  return view;
};
const typeInto = (name: string, text: string) =>
  act(() => {
    const view = viewOf(name);
    view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text }, userEvent: "input.type" });
  });
const tab = (name: string) => within(within(editor()).getByRole("tablist", { name: "Request sections" })).getByRole("tab", { name });
const button = (name: string) => within(editor()).getByRole("button", { name });
const modeField = () => within(editor()).getByRole("combobox", { name: "GraphQL mode" }) as HTMLSelectElement;
const sent = () => {
  const calls = service.UpdateRequest.mock.calls;
  return calls[calls.length - 1]?.[0] as { request: RequestDTO; revision: number };
};
const waitForCheck = () => act(() => new Promise<void>((r) => setTimeout(r, GRAPHQL_CHECK_DELAY_MS + 30)));

beforeEach(() => {
  resetFakeWails();
  resetHttpSends();
  resetGraphqlSubscriptions();
  graphqlService.Subscribe.mockReset().mockImplementation(() => cancellable({ opId: "op_sub" }));
  graphqlService.StopSubscription.mockReset().mockImplementation(() => cancellable(true));
  http.Send.mockReset().mockResolvedValue({ opId: "op_send" });
  service.Request.mockReset();
  service.UpdateRequest.mockReset().mockResolvedValue(undefined);
  service.Flush.mockReset().mockResolvedValue(undefined);
  format.ValidateJSON.mockReset().mockImplementation(() => cancellable({ valid: true, error: null }));
  graphqlService.Diagnostics.mockReset().mockImplementation(() => cancellable(diagnostics()));
  graphqlService.Complete.mockReset();
  format.CheckGraphQLVariables.mockReset().mockImplementation(() => cancellable(variablesCheck()));
  format.PrettifyGraphQL.mockReset();
  graphqlService.SchemaInfo.mockReset().mockImplementation(() => cancellable(schemaInfo()));
  graphqlService.FetchSchema.mockReset().mockImplementation(() => cancellable({ opId: "op_schema" }));
  graphqlService.ReloadSchemaFile.mockReset();
  graphqlService.PickSchemaFile.mockReset();
  graphqlService.SaveSchemaSDL.mockReset();
  format.MinifyGraphQL.mockReset();
  resetGraphqlDocsPanel();
  graphqlService.DocsOverview.mockReset().mockImplementation(() =>
    cancellable({ schema: true, description: "", roots: [{ operation: "query", type: "Query" }], types: [{ name: "Query", kind: "OBJECT" }], directives: [] }),
  );
  graphqlService.DocsType.mockReset().mockImplementation(({ name }) =>
    cancellable({
      found: true,
      name,
      kind: "OBJECT",
      description: "",
      root: "query",
      specifiedByUrl: "",
      isOneOf: false,
      interfaces: [],
      possibleTypes: [],
      implementations: [],
      fields: [
        { name: "viewer", description: "", args: [], type: "User!", typeName: "User", typeKind: "OBJECT", deprecated: false, deprecationReason: "" },
      ],
      inputFields: [],
      enumValues: [],
    }),
  );
  graphqlService.DocsSearch.mockReset().mockImplementation(() => cancellable({ schema: true, items: [], count: 0 }));
  forgetBodyFiles();
  workspace.PickBodyFile.mockReset();
  workspace.BodyFile.mockReset().mockImplementation((path: string) =>
    cancellable({ path, absolutePath: `/p/${path}`, name: path.split("/").pop() ?? path, size: 2048, exists: true }),
  );
  graphqlService.DocsTarget.mockReset();
  graphqlService.GenerateOperation.mockReset();
  graphqlService.FillFields.mockReset();
});

describe("operationChoice", () => {
  it("offers a choice only for several operations", () => {
    expect(operationChoice([], "")).toEqual({ pick: false });
    expect(operationChoice([{ name: "A", type: "query" }], "B")).toEqual({ pick: false });
    const ops = [
      { name: "A", type: "query" },
      { name: "", type: "mutation" },
      { name: "B", type: "mutation" },
    ];
    expect(operationChoice(ops, "B")).toEqual({ pick: true, names: ["A", "B"], missing: false });
    expect(operationChoice(ops, "")).toEqual({ pick: true, names: ["A", "B"], missing: true });
  });
});

describe("GraphqlEditor", () => {
  it("opens a GraphQL request with its endpoint, query and sections", async () => {
    await setup();
    expect(service.Request).toHaveBeenCalledWith("gql");
    expect(modeField().value).toBe("POST");
    expect(viewOf("URL").state.doc.toString()).toBe("{{baseUrl}}/graphql");
    expect(viewOf("GraphQL query").state.doc.toString()).toBe(QUERY);
    const tabs = within(within(editor()).getByRole("tablist", { name: "Request sections" })).getAllByRole("tab");
    expect(tabs.map((t) => t.textContent)).toEqual([
      "Query",
      "Variables",
      "Extensions",
      "Files",
      "Connection",
      "Headers1",
      "Auth",
      "Tests",
      "Extract",
      "Examples",
      "Settings",
      "Notes",
      "Code",
    ]);
    expect(tabs[0]?.getAttribute("aria-selected")).toBe("true");
    // One operation: nothing to pick.
    await waitForCheck();
    expect(graphqlService.Diagnostics).toHaveBeenCalledWith({ key: "", text: QUERY });
    expect(within(editor()).queryByRole("combobox", { name: "Operation to run" })).toBeNull();
    expect(service.UpdateRequest).not.toHaveBeenCalled();
  });

  it("edits the request's assertions in the Tests sub-tab (TEST-01)", async () => {
    await setup();
    fireEvent.click(within(editor()).getByRole("tab", { name: "Tests" }));
    fireEvent.click(within(editor()).getByRole("button", { name: "Add assertion" }));
    const table = within(editor()).getByRole("table", { name: "Assertions" });
    fireEvent.change(within(table).getByRole("combobox", { name: "Target, assertion 1" }), { target: { value: "jsonpath" } });
    fireEvent.change(within(table).getByRole("textbox", { name: "Expression, assertion 1" }), { target: { value: "$.errors" } });
    fireEvent.change(within(table).getByRole("combobox", { name: "Operator, assertion 1" }), { target: { value: "not_exists" } });
    await act(settle);
    expect(sent().request.assertions).toEqual([
      { target: "jsonpath", expr: "$.errors", op: "not_exists", expected: "", enabled: true, extra: "" },
    ]);
    expect(within(editor()).getByRole("tab", { name: /^Tests/ }).textContent).toBe("Tests1");
  });

  it("auto-saves the mode, URL, query and variables with the whole request", async () => {
    const stores = await setup();
    fireEvent.change(modeField(), { target: { value: "GET" } });
    expect(sent()).toEqual({ request: request({ method: "GET" }), revision: 1, baseVersion: 0 });
    expect(stores.collection.getState().nodes.gql).toMatchObject({ method: "GET" });
    await act(settle);

    typeInto("URL", "https://api.test/graphql");
    await act(settle);
    typeInto("GraphQL query", "{ me { id } }");
    await act(settle);
    fireEvent.click(tab("Variables"));
    expect(viewOf("GraphQL variables").state.doc.toString()).toBe('{"id": "{{userId}}"}');
    typeInto("GraphQL variables", '{"id": 1}');
    await act(settle);
    expect(sent().request).toEqual(
      request({ method: "GET", url: "https://api.test/graphql", query: "{ me { id } }", variables: '{"id": 1}' }),
    );

    // Back to POST: the default is written as "".
    fireEvent.change(modeField(), { target: { value: "POST" } });
    await act(settle);
    expect(sent().request.method).toBe("");
  });

  it("lets the user choose the operation when the document has several", async () => {
    graphqlService.Diagnostics.mockImplementation(() =>
      cancellable(
        diagnostics({
          operations: [
            { name: "User", type: "query" },
            { name: "Rename", type: "mutation" },
          ],
        }),
      ),
    );
    await setup(request({ query: "query User { a }\nmutation Rename { b }" }));
    await waitForCheck();
    const picker = within(editor()).getByRole("combobox", { name: "Operation to run" }) as HTMLSelectElement;
    expect(picker.value).toBe("");
    expect([...picker.options].map((o) => o.textContent)).toEqual(["Choose the operation to run", "User", "Rename"]);
    fireEvent.change(picker, { target: { value: "Rename" } });
    expect(sent().request.operationName).toBe("Rename");
    expect(picker.value).toBe("Rename");
  });

  describe("diagnostics (GQL-12)", () => {
    const problemsBar = () => within(editor()).getByRole("status");
    const syntax = { source: "syntax", code: "graphql.syntax.unexpected_token", severity: "error", offset: 7, endOffset: 8, line: 1, column: 8, message: 'expected value, found ")"' };

    it("marks the problems Go reports and lists them in the problems bar", async () => {
      const schemaError = { source: "schema", code: "graphql.validate.unknown_field", severity: "error", offset: 2, endOffset: 3, line: 1, column: 3, message: 'Cannot query field "a" on type "Query".' };
      graphqlService.Diagnostics.mockImplementation(() =>
        cancellable(diagnostics({ schema: true, diagnostics: [schemaError, syntax], count: 2, syntaxCount: 1 })),
      );
      await setup(request({ query: "{ a(x: ) }" }));
      await waitForCheck();
      expect(currentDiagnostics(viewOf("GraphQL query").state)).toEqual([
        { from: 2, to: 3, severity: "error", message: 'Cannot query field "a" on type "Query".' },
        { from: 7, to: 8, severity: "error", message: 'expected value, found ")"' },
      ]);
      expect(problemsBar().textContent).toContain("2 problems");
      const list = within(problemsBar()).getByRole("list", { name: "Problems" });
      expect(within(list).getAllByRole("listitem").map((li) => li.textContent)).toEqual([
        'Ln 1, Col 3Cannot query field "a" on type "Query".',
        'Ln 1, Col 8expected value, found ")"',
      ]);
      fireEvent.click(within(list).getByRole("button", { name: "Ln 1, Col 8" }));
      expect(viewOf("GraphQL query").state.selection.main.head).toBe(7);
    });

    it("validates against the cached schema and clears the marks once fixed", async () => {
      graphqlService.SchemaInfo.mockImplementation(() =>
        cancellable(schemaInfo({ cached: true, key: "https://api.test/graphql", fetchedAt: new Date().toISOString(), typeCount: 3 })),
      );
      graphqlService.Diagnostics.mockImplementation(({ text }: { key: string; text: string }) =>
        cancellable(text === "{ a(x: ) }" ? diagnostics({ schema: true, diagnostics: [syntax], count: 1, syntaxCount: 1 }) : diagnostics({ schema: true })),
      );
      await setup(request({ query: "{ a(x: ) }" }));
      await waitForCheck();
      await waitForCheck();
      expect(graphqlService.Diagnostics).toHaveBeenLastCalledWith({ key: "https://api.test/graphql", text: "{ a(x: ) }" });
      expect(currentDiagnostics(viewOf("GraphQL query").state)).toHaveLength(1);
      expect(problemsBar().textContent).toContain("1 problem");

      typeInto("GraphQL query", "{ a(x: 1) }");
      await waitForCheck();
      expect(currentDiagnostics(viewOf("GraphQL query").state)).toEqual([]);
      expect(problemsBar().textContent).toBe("Document is valid against the schema.");
    });

    it("says what a valid document was checked against without a schema", async () => {
      await setup();
      await waitForCheck();
      expect(problemsBar().textContent).toContain("No syntax errors. Fetch the schema");
    });
  });

  it("marks invalid JSON in the variables", async () => {
    format.CheckGraphQLVariables.mockImplementation(() =>
      cancellable(variablesCheck({ error: { offset: 7, byteOffset: 7, line: 1, column: 8, message: "Invalid character" } })),
    );
    await setup(request({ variables: '{"id": }' }));
    fireEvent.click(tab("Variables"));
    await waitForCheck();
    expect(format.CheckGraphQLVariables).toHaveBeenCalledWith(QUERY, '{"id": }', "");
    expect(currentErrorMarker(viewOf("GraphQL variables").state)).toEqual({ offset: 7, message: "Invalid character" });
    expect(within(editor()).getByRole("status").textContent).toContain("Line 1, column 8");
  });

  it("warns about missing and unknown variables and jumps to an unknown one (GQL-03)", async () => {
    format.CheckGraphQLVariables.mockImplementation(() =>
      cancellable(
        variablesCheck({
          operation: "User",
          missing: [
            { name: "id", type: "ID!", required: true },
            { name: "n", type: "Int", required: false },
          ],
          unknown: [{ name: "extra", offset: 1, endOffset: 8, line: 1, column: 2 }],
        }),
      ),
    );
    await setup(request({ variables: '{"extra": 1}', operationName: "User" }));
    fireEvent.click(tab("Variables"));
    await waitForCheck();
    expect(format.CheckGraphQLVariables).toHaveBeenLastCalledWith(QUERY, '{"extra": 1}', "User");
    const list = within(editor()).getByRole("list", { name: "Variable warnings" });
    expect(within(list).getAllByRole("listitem").map((li) => li.textContent)).toEqual([
      "$id: ID! is required by operation User but not set.",
      "$n: Int is declared by operation User but not set.",
      'Line 1, column 2"extra" is not a variable of operation User.',
    ]);
    fireEvent.click(within(list).getByRole("button", { name: "Line 1, column 2" }));
    expect(viewOf("GraphQL variables").state.selection.main.head).toBe(1);
  });

  it("completes from the cached schema of the endpoint (GQL-13)", async () => {
    graphqlService.SchemaInfo.mockImplementation(() =>
      cancellable(schemaInfo({ cached: true, key: "https://api.test/graphql", fetchedAt: new Date().toISOString(), typeCount: 3 })),
    );
    graphqlService.Complete.mockImplementation(() =>
      cancellable({
        context: "selection", from: 30, to: 34, prefix: "name", parentType: "User", owner: "", expectedType: "", schema: true, count: 1,
        items: [{ label: "name", kind: "field", detail: "String", documentation: "", deprecated: false, deprecationReason: "", insertText: "name", suffix: "", suffixCursor: 0 }],
      }),
    );
    await setup();
    await waitForCheck();
    await waitForCheck();
    const view = viewOf("GraphQL query");
    act(() => {
      view.dispatch({ selection: { anchor: 34 } });
      startCompletion(view);
    });
    await vi.waitFor(() => expect(completionStatus(view.state)).toBe("active"));
    expect(graphqlService.Complete).toHaveBeenLastCalledWith({ key: "https://api.test/graphql", text: QUERY, offset: 34 });
    expect(currentCompletions(view.state).map((c) => c.label)).toEqual(["name"]);
  });

  describe("schema strip (GQL-10, GQL-21)", () => {
    const strip = () => within(editor()).getByRole("group", { name: "GraphQL schema" });

    it("shows the cached schema and never fetches on its own", async () => {
      const fetchedAt = new Date(Date.now() - 2 * 60_000).toISOString();
      graphqlService.SchemaInfo.mockImplementation(() =>
        cancellable(schemaInfo({ cached: true, key: "https://api.test/graphql", fetchedAt, typeCount: 14, query: "full" })),
      );
      await setup();
      await waitForCheck();
      expect(graphqlService.SchemaInfo).toHaveBeenCalledWith({ requestId: "gql" });
      expect(strip().textContent).toContain("Schema · 14 types · introspected 2 min ago");
      expect(within(strip()).getByRole("button", { name: "Refresh" })).toBeTruthy();
      expect(graphqlService.FetchSchema).not.toHaveBeenCalled();
    });

    it("fetches the schema on request and shows the result", async () => {
      await setup();
      await waitForCheck();
      expect(strip().textContent).toContain("No schema yet");
      fireEvent.click(within(strip()).getByRole("button", { name: "Fetch schema" }));
      await act(settle);
      expect(graphqlService.FetchSchema).toHaveBeenCalledWith({ requestId: "gql" });
      expect(within(strip()).getByRole("button", { name: "Fetching…" }).hasAttribute("disabled")).toBe(true);
      const info = schemaInfo({ cached: true, key: "https://api.test/graphql", fetchedAt: new Date().toISOString(), typeCount: 3, query: "full" });
      act(() => {
        emit("graphql:op:data", { opId: "op_schema", seq: 1, items: [info] });
        emit("graphql:op:done", { opId: "op_schema", seq: 2, status: "ok", error: null, items: 1, batches: 1, elapsedMs: 5 });
      });
      expect(strip().textContent).toContain("Schema · 3 types · introspected just now");
      expect(within(strip()).getByRole("button", { name: "Refresh" })).toBeTruthy();
    });

    it("shows why a fetch failed", async () => {
      await setup();
      await waitForCheck();
      fireEvent.click(within(strip()).getByRole("button", { name: "Fetch schema" }));
      await act(settle);
      const error = { kind: "protocol", code: "graphql.introspection_http_status", message: "The endpoint answered 404 Not Found instead of a GraphQL response.", hint: "", details: {}, causes: [] };
      act(() => emit("graphql:op:done", { opId: "op_schema", seq: 1, status: "error", error, items: 0, batches: 0, elapsedMs: 5 }));
      expect(within(strip()).getByRole("alert").textContent).toContain("404 Not Found");
      expect(within(strip()).getByRole("button", { name: "Fetch schema" }).hasAttribute("disabled")).toBe(false);
    });
  });

  describe("schema refresh and SDL export (GQL-19, GQL-20)", () => {
    const strip = () => within(editor()).getByRole("group", { name: "GraphQL schema" });
    const KEY = "https://api.test/graphql";
    const cachedInfo = (over: Record<string, unknown> = {}) =>
      schemaInfo({ cached: true, key: KEY, fetchedAt: new Date().toISOString(), typeCount: 14, query: "full", change: "", ...over });
    const fetchDone = (change: string) =>
      act(() => {
        emit("graphql:op:data", { opId: "op_schema", seq: 1, items: [cachedInfo({ change })] });
        emit("graphql:op:done", { opId: "op_schema", seq: 2, status: "ok", error: null, items: 1, batches: 1, elapsedMs: 5 });
      });
    const withRefresh = (onSend: boolean, intervalMinutes: number) =>
      request({ settings: { ...request().settings, graphqlSchemaRefresh: { onSend, intervalMinutes, extra: "" } } });

    it("saves the cached schema as SDL under the request's name", async () => {
      graphqlService.SchemaInfo.mockImplementation(() => cancellable(cachedInfo()));
      graphqlService.SaveSchemaSDL.mockImplementation(() => cancellable({ path: "/home/me/User by ID.graphql", typeCount: 14 }));
      await setup();
      await waitForCheck();
      fireEvent.click(within(strip()).getByRole("button", { name: "Save SDL…" }));
      await act(settle);
      expect(graphqlService.SaveSchemaSDL).toHaveBeenCalledWith({ key: KEY, name: "User by ID" });
      expect(within(strip()).getByRole("status").textContent).toBe("Saved to User by ID.graphql");
      expect(graphqlService.FetchSchema).not.toHaveBeenCalled();
    });

    it("says nothing when the dialog is cancelled, and why a save failed", async () => {
      graphqlService.SchemaInfo.mockImplementation(() => cancellable(cachedInfo()));
      graphqlService.SaveSchemaSDL.mockImplementation(() => cancellable({ path: "", typeCount: 0 }));
      await setup();
      await waitForCheck();
      fireEvent.click(within(strip()).getByRole("button", { name: "Save SDL…" }));
      await act(settle);
      expect(within(strip()).queryByRole("status")).toBeNull();
      const error = { kind: "io", code: "graphql.export_failed", message: "The schema could not be saved to /ro/s.graphql.", hint: "Check the folder.", details: {}, causes: [] };
      graphqlService.SaveSchemaSDL.mockImplementation(() => Object.assign(Promise.reject(error), { cancel: () => undefined }));
      fireEvent.click(within(strip()).getByRole("button", { name: "Save SDL…" }));
      await act(settle);
      expect(within(strip()).getByRole("alert").textContent).toContain("could not be saved");
    });

    it("has no Save SDL… without a schema", async () => {
      await setup();
      await waitForCheck();
      expect(within(strip()).queryByRole("button", { name: "Save SDL…" })).toBeNull();
    });

    it("says whether a refresh changed the schema", async () => {
      graphqlService.SchemaInfo.mockImplementation(() => cancellable(cachedInfo()));
      await setup();
      await waitForCheck();
      fireEvent.click(within(strip()).getByRole("button", { name: "Refresh" }));
      await act(settle);
      fetchDone("changed");
      expect(strip().textContent).toContain("Schema changed");
      fireEvent.click(within(strip()).getByRole("button", { name: "Refresh" }));
      await act(settle);
      fetchDone("unchanged");
      expect(strip().textContent).toContain("No changes");
    });

    it("turns the auto-refresh on and off in the Settings tab, off by default", async () => {
      await setup();
      fireEvent.click(tab("Settings"));
      const onSend = within(editor()).getByRole("checkbox", { name: /Refresh when the server rejects a query/ }) as HTMLInputElement;
      const every = within(editor()).getByRole("combobox", { name: "Refresh every" }) as HTMLSelectElement;
      expect(onSend.checked).toBe(false);
      expect(every.value).toBe("0");
      fireEvent.click(onSend);
      await act(settle);
      expect(sent().request.settings.graphqlSchemaRefresh).toEqual({ onSend: true, intervalMinutes: 0, extra: "" });
      fireEvent.change(every, { target: { value: "15" } });
      await act(settle);
      expect(sent().request.settings.graphqlSchemaRefresh).toEqual({ onSend: true, intervalMinutes: 15, extra: "" });
      fireEvent.click(onSend);
      fireEvent.change(every, { target: { value: "0" } });
      await act(settle);
      expect(sent().request.settings.graphqlSchemaRefresh).toBeNull();
    });

    it("cannot turn it on for a schema from a file", async () => {
      await setup(request({ schemaFile: "schemas/pets.graphql" }));
      fireEvent.click(tab("Settings"));
      expect((within(editor()).getByRole("combobox", { name: "Refresh every" }) as HTMLSelectElement).disabled).toBe(true);
      expect(within(editor()).getByText("The schema comes from a file, which is read again when it changes.")).toBeTruthy();
    });

    it("refreshes after a send the server rejects although the cached schema accepts the query", async () => {
      graphqlService.SchemaInfo.mockImplementation(() => cancellable(cachedInfo()));
      graphqlService.Diagnostics.mockImplementation(() => cancellable(diagnostics({ schema: true })));
      await setup(withRefresh(true, 0));
      await waitForCheck();
      fireEvent.click(button("Send"));
      await act(settle);
      const body = '{"errors":[{"message":"Cannot query field \\"name\\" on type \\"User\\"."}]}';
      act(() => {
        emit("http:op:data", { opId: "op_send", seq: 1, items: [httpResponse({ body, bodyPretty: body })] });
        emit("http:op:done", { opId: "op_send", seq: 2, status: "ok", error: null, items: 1, batches: 1, elapsedMs: 5 });
      });
      await act(settle);
      expect(graphqlService.FetchSchema).toHaveBeenCalledWith({ requestId: "gql" });
      fetchDone("changed");
      expect(strip().textContent).toContain("Refreshed automatically: schema changed");
    });

    it("does not refresh after a send while the option is off", async () => {
      graphqlService.SchemaInfo.mockImplementation(() => cancellable(cachedInfo()));
      graphqlService.Diagnostics.mockImplementation(() => cancellable(diagnostics({ schema: true })));
      await setup();
      await waitForCheck();
      fireEvent.click(button("Send"));
      await act(settle);
      const body = '{"errors":[{"message":"Cannot query field \\"name\\" on type \\"User\\"."}]}';
      act(() => {
        emit("http:op:data", { opId: "op_send", seq: 1, items: [httpResponse({ body, bodyPretty: body })] });
        emit("http:op:done", { opId: "op_send", seq: 2, status: "ok", error: null, items: 1, batches: 1, elapsedMs: 5 });
      });
      await act(settle);
      expect(graphqlService.FetchSchema).not.toHaveBeenCalled();
    });
  });

  describe("schema from a file (GQL-11)", () => {
    const strip = () => within(editor()).getByRole("group", { name: "GraphQL schema" });
    const FILE_KEY = "file\u0000/project/schemas/pets.graphql";
    const fileInfo = (over: Record<string, unknown> = {}) =>
      schemaInfo({
        source: "file",
        file: "schemas/pets.graphql",
        modifiedAt: "2026-09-19T10:00:00Z",
        cached: true,
        key: FILE_KEY,
        fetchedAt: "2026-09-19T10:00:00Z",
        typeCount: 4,
        ...over,
      });

    it("loads a schema file chosen in the dialog and stores its path", async () => {
      await setup();
      await waitForCheck();
      graphqlService.PickSchemaFile.mockImplementation(() => cancellable("schemas/pets.graphql"));
      graphqlService.SchemaInfo.mockImplementation(() => cancellable(fileInfo()));
      fireEvent.click(within(strip()).getByRole("button", { name: "Load from file…" }));
      await act(settle);
      expect(graphqlService.PickSchemaFile).toHaveBeenCalled();
      expect(sent().request.schemaFile).toBe("schemas/pets.graphql");
      await waitForCheck();
      expect(strip().textContent).toContain("Schema · 4 types · from file pets.graphql");
      await waitForCheck();
      expect(graphqlService.Diagnostics).toHaveBeenLastCalledWith({ key: FILE_KEY, text: QUERY });
    });

    it("keeps the path when the dialog is cancelled", async () => {
      await setup();
      await waitForCheck();
      graphqlService.PickSchemaFile.mockImplementation(() => cancellable(""));
      fireEvent.click(within(strip()).getByRole("button", { name: "Load from file…" }));
      await act(settle);
      expect(service.UpdateRequest).not.toHaveBeenCalled();
    });

    it("reloads the file, lists its problems and switches back to introspection", async () => {
      graphqlService.SchemaInfo.mockImplementation(() => cancellable(fileInfo()));
      await setup(request({ schemaFile: "schemas/pets.graphql" }));
      await waitForCheck();
      expect(strip().textContent).toContain("from file pets.graphql");
      expect(within(strip()).queryByRole("button", { name: "Fetch schema" })).toBeNull();

      graphqlService.ReloadSchemaFile.mockImplementation(() =>
        cancellable(
          fileInfo({
            problems: 2,
            problemList: [
              { message: 'Unknown type "Missing".', line: 3, column: 9 },
              { message: "Type A must define one or more fields.", line: 7, column: 6 },
            ],
          }),
        ),
      );
      fireEvent.click(within(strip()).getByRole("button", { name: "Reload" }));
      await act(settle);
      expect(graphqlService.ReloadSchemaFile).toHaveBeenCalledWith({ requestId: "gql" });
      expect(graphqlService.FetchSchema).not.toHaveBeenCalled();
      expect(strip().textContent).toContain('2 problems in the file: Ln 3, Col 9: Unknown type "Missing".');

      fireEvent.click(within(strip()).getByRole("button", { name: "Use introspection" }));
      await act(settle);
      expect(sent().request.schemaFile).toBe("");
    });

    it("says why a schema file cannot be used", async () => {
      graphqlService.SchemaInfo.mockImplementation(() =>
        cancellable(
          fileInfo({
            cached: false,
            key: "",
            typeCount: 0,
            error: { code: "graphql.schema_file_missing", message: "The schema file pets.graphql does not exist.", hint: "Choose it again." },
          }),
        ),
      );
      await setup(request({ schemaFile: "schemas/pets.graphql" }));
      await waitForCheck();
      expect(strip().textContent).toContain("Schema file pets.graphql");
      expect(within(strip()).getByRole("alert").textContent).toContain("does not exist");
      await waitForCheck();
      expect(graphqlService.Diagnostics).toHaveBeenLastCalledWith({ key: "", text: QUERY });
    });

    it("hides the source buttons for read-only requests", async () => {
      graphqlService.SchemaInfo.mockImplementation(() => cancellable(fileInfo()));
      await setup(request({ schemaFile: "schemas/pets.graphql" }), { readOnly: true });
      await waitForCheck();
      expect(within(strip()).queryByRole("button", { name: "Use introspection" })).toBeNull();
      expect(within(strip()).queryByRole("button", { name: "Change file…" })).toBeNull();
      expect(within(strip()).getByRole("button", { name: "Reload" })).toBeTruthy();
    });
  });

  describe("docs explorer (GQL-14)", () => {
    const KEY = "https://api.test/graphql";
    const docs = () => within(editor()).queryByRole("complementary", { name: "GraphQL docs" });
    const docsButton = () => within(within(editor()).getByRole("group", { name: "GraphQL schema" })).getByRole("button", { name: "Docs" });
    const cached = () =>
      graphqlService.SchemaInfo.mockImplementation(() =>
        cancellable(schemaInfo({ cached: true, key: KEY, fetchedAt: new Date().toISOString(), typeCount: 1, query: "full" })),
      );

    it("opens from the schema strip, browses the cached schema and inserts a field at the cursor", async () => {
      cached();
      await setup(request({ query: "{  }" }));
      await waitForCheck();
      expect(docs()).toBeNull();
      expect(docsButton().getAttribute("aria-pressed")).toBe("false");
      fireEvent.click(docsButton());
      await act(settle);
      expect(docsButton().getAttribute("aria-pressed")).toBe("true");
      const panel = docs() as HTMLElement;
      expect(graphqlService.DocsOverview).toHaveBeenCalledWith({ key: KEY });
      fireEvent.click(within(within(panel).getByRole("region", { name: "Root types" })).getByRole("button", { name: "Query" }));
      await act(settle);
      expect(graphqlService.DocsType).toHaveBeenCalledWith({ key: KEY, name: "Query" });

      const view = viewOf("GraphQL query");
      act(() => view.dispatch({ selection: { anchor: 2 } }));
      fireEvent.click(within(panel).getByRole("button", { name: "Insert viewer at the cursor" }));
      await act(settle);
      expect(view.state.doc.toString()).toBe("{ viewer }");
      expect(sent().request.query).toBe("{ viewer }");

      // Another sub-tab: nothing to insert into.
      fireEvent.click(tab("Variables"));
      await act(settle);
      expect(within(docs() as HTMLElement).queryByRole("button", { name: "Insert viewer at the cursor" })).toBeNull();

      fireEvent.click(within(docs() as HTMLElement).getByRole("button", { name: "Close the docs" }));
      expect(docs()).toBeNull();
    });

    it("toggles with the shortcut, and offers to fetch without a schema", async () => {
      const stores = await setup();
      const stop = installShortcuts(stores);
      try {
        const key = () =>
          new KeyboardEvent("keydown", { key: "D", code: "KeyD", shiftKey: true, metaKey: IS_MAC, ctrlKey: !IS_MAC, bubbles: true, cancelable: true });
        act(() => void window.dispatchEvent(key()));
        await act(settle);
        const panel = docs() as HTMLElement;
        expect(panel.textContent).toContain("No schema yet");
        fireEvent.click(within(panel).getByRole("button", { name: "Fetch schema" }));
        await act(settle);
        expect(graphqlService.FetchSchema).toHaveBeenCalledWith({ requestId: "gql" });
        act(() => void window.dispatchEvent(key()));
        expect(docs()).toBeNull();
      } finally {
        stop();
      }
    });
  });

  describe("schema actions (GQL-15, GQL-16, GQL-17)", () => {
    const KEY = "https://api.test/graphql";
    const docs = () => within(editor()).queryByRole("complementary", { name: "GraphQL docs" });
    const cached = () =>
      graphqlService.SchemaInfo.mockImplementation(() =>
        cancellable(schemaInfo({ cached: true, key: KEY, fetchedAt: new Date().toISOString(), typeCount: 1, query: "full" })),
      );
    const chord = (key: string, code: string) =>
      new KeyboardEvent("keydown", { key, code, altKey: true, [IS_MAC ? "metaKey" : "ctrlKey"]: true, bubbles: true, cancelable: true });
    const generated = (over: Record<string, unknown> = {}) => ({
      schema: true,
      text: "query Viewer {\n  viewer {\n    id\n  }\n}",
      name: "Viewer",
      insert: "\n\nquery Viewer {\n  viewer {\n    id\n  }\n}\n",
      from: 6,
      to: 6,
      cursor: 8,
      truncated: false,
      reason: "",
      message: "",
      ...over,
    });
    const filled = (over: Record<string, unknown> = {}) => ({
      schema: true,
      from: 11,
      to: 13,
      text: "  id\n  name",
      cursor: 21,
      type: "User",
      count: 2,
      truncated: false,
      reason: "",
      message: "",
      ...over,
    });

    it("writes an operation for a root field from the docs at the end of the query, at the chosen depth", async () => {
      cached();
      graphqlService.GenerateOperation.mockImplementation(() => cancellable(generated()));
      await setup(request({ query: "{ me }" }));
      await waitForCheck();
      fireEvent.click(within(within(editor()).getByRole("group", { name: "GraphQL schema" })).getByRole("button", { name: "Docs" }));
      await act(settle);
      const panel = docs() as HTMLElement;
      fireEvent.click(within(within(panel).getByRole("region", { name: "Root types" })).getByRole("button", { name: "Query" }));
      await act(settle);
      fireEvent.click(within(panel).getByRole("button", { name: "Write a query for viewer into the query" }));
      await act(settle);
      expect(graphqlService.GenerateOperation).toHaveBeenCalledWith({ key: KEY, kind: "operation", type: "Query", field: "viewer", depth: 2, text: "{ me }" });
      const view = viewOf("GraphQL query");
      expect(view.state.doc.toString()).toBe("{ me }\n\nquery Viewer {\n  viewer {\n    id\n  }\n}\n");
      expect(view.state.selection.main.head).toBe(8);
      expect(sent().request.query).toBe(view.state.doc.toString());

      // The depth is one choice for the docs and Fill fields.
      fireEvent.change(within(panel).getByRole("combobox", { name: "Depth" }), { target: { value: "3" } });
      const depths = within(editor()).getAllByRole("combobox", { name: "Depth" }) as HTMLSelectElement[];
      expect(depths.map((d) => d.value)).toEqual(["3", "3"]);
      graphqlService.GenerateOperation.mockImplementation(() =>
        cancellable(generated({ insert: "", text: "", reason: "required-arguments", message: "Query.viewer takes required arguments." })),
      );
      fireEvent.click(within(panel).getByRole("button", { name: "Write a query for viewer into the query" }));
      await act(settle);
      expect(graphqlService.GenerateOperation).toHaveBeenLastCalledWith(expect.objectContaining({ depth: 3 }));
      expect(within(editor()).getByText("Query.viewer takes required arguments.")).toBeTruthy();
    });

    it("fills in the fields at the cursor with the button and the shortcut", async () => {
      cached();
      graphqlService.FillFields.mockImplementation(() => cancellable(filled()));
      const query = "{ viewer {\n  \n} }";
      await setup(request({ query }));
      await waitForCheck();
      const view = viewOf("GraphQL query");
      act(() => view.dispatch({ selection: { anchor: 13 } }));
      expect(button("Fill fields").getAttribute("aria-keyshortcuts")).toBe(IS_MAC ? "Meta+Alt+F" : "Control+Alt+F");
      fireEvent.click(button("Fill fields"));
      await act(settle);
      expect(graphqlService.FillFields).toHaveBeenCalledWith({ key: KEY, text: query, offset: 13, depth: 2 });
      expect(view.state.doc.toString()).toBe("{ viewer {\n  id\n  name\n} }");
      expect(view.state.selection.main.head).toBe(21);
      expect(sent().request.query).toBe("{ viewer {\n  id\n  name\n} }");

      // Nothing left: the reason shows under the editor.
      graphqlService.FillFields.mockImplementation(() =>
        cancellable(filled({ text: "", reason: "nothing-to-add", message: "Every field of User that can be filled in is selected already." })),
      );
      const event = chord("ƒ", "KeyF");
      act(() => void view.contentDOM.dispatchEvent(event));
      await act(settle);
      expect(event.defaultPrevented).toBe(true);
      expect(graphqlService.FillFields).toHaveBeenCalledTimes(2);
      expect(within(editor()).getByText(/Every field of User/)).toBeTruthy();
      // Typing hides it.
      typeInto("GraphQL query", "{ viewer { id } }");
      await act(settle);
      expect(within(editor()).queryByText(/Every field of User/)).toBeNull();
    });

    it("asks for a schema before filling in fields", async () => {
      await setup(request({ query: "{ a { } }" }));
      fireEvent.click(button("Fill fields"));
      await act(settle);
      expect(graphqlService.FillFields).not.toHaveBeenCalled();
      expect(within(editor()).getByText("Fetch the schema to fill in fields.")).toBeTruthy();
    });

    it("opens the docs of the name at the cursor", async () => {
      cached();
      graphqlService.DocsTarget.mockImplementation(() =>
        cancellable({ found: true, kind: "field", type: "Query", member: "viewer", directive: "", name: "viewer", from: 2, to: 8 }),
      );
      await setup(request({ query: "{ viewer }" }));
      await waitForCheck();
      expect(docs()).toBeNull();
      const view = viewOf("GraphQL query");
      act(() => view.dispatch({ selection: { anchor: 4 } }));
      act(() => void view.contentDOM.dispatchEvent(chord("∂", "KeyD")));
      await act(settle);
      expect(graphqlService.DocsTarget).toHaveBeenCalledWith({ key: KEY, text: "{ viewer }", offset: 4 });
      const panel = docs() as HTMLElement;
      expect(graphqlService.DocsType).toHaveBeenCalledWith({ key: KEY, name: "Query" });
      expect(panel.querySelector('[data-member="viewer"]')?.hasAttribute("data-target")).toBe(true);

      // A name the schema does not know: a message, the panel stays.
      graphqlService.DocsTarget.mockImplementation(() =>
        cancellable({ found: false, kind: "", type: "", member: "", directive: "", name: "", from: 0, to: 0 }),
      );
      act(() => void view.contentDOM.dispatchEvent(chord("∂", "KeyD")));
      await act(settle);
      expect(within(editor()).getByText("The schema has no docs for the name at the cursor.")).toBeTruthy();
    });
  });

  it("shows the automatic headers, Content-Type only for POST", async () => {
    await setup(request({ headers: [row("accept", "application/json")] }));
    fireEvent.click(tab("Headers1"));
    const auto = () => within(editor()).getByRole("list", { name: "Automatic headers" });
    expect(within(auto()).getAllByRole("listitem").map((li) => li.textContent)).toEqual([
      expect.stringContaining("Content-Typeapplication/json"),
      expect.stringContaining("overriddenAccept"),
    ]);
    fireEvent.change(modeField(), { target: { value: "GET" } });
    expect(within(auto()).getAllByRole("listitem")).toHaveLength(1);
  });

  it("shows the incremental delivery Accept for a query with @defer (GQL-08)", async () => {
    await setup(request({ query: "{ me { id ... @defer { name } } }" }));
    fireEvent.click(tab("Headers1"));
    const auto = within(editor()).getByRole("list", { name: "Automatic headers" });
    expect(auto.textContent).toContain("multipart/mixed;deferSpec=20220824");
  });

  it("edits the extensions as JSON text with a check (GQL-06)", async () => {
    await setup();
    fireEvent.click(tab("Extensions"));
    expect(within(editor()).getAllByText("extensions").length).toBeGreaterThan(0);
    typeInto("GraphQL extensions", '{"persistedQuery": {"version": 1, "sha256Hash": "{{hash}}"}}');
    await act(settle);
    expect(sent().request.extensions).toBe('{"persistedQuery": {"version": 1, "sha256Hash": "{{hash}}"}}');
    typeInto("GraphQL extensions", "[1]");
    await act(settle);
    expect(within(editor()).getByText("The extensions must be a JSON object.")).toBeTruthy();
  });

  describe("file uploads (GQL-07)", () => {
    const upload = (name: string, file: string, over: Partial<PartDTO> = {}): PartDTO => ({
      name,
      value: "",
      file,
      contentType: "",
      description: "",
      enabled: true,
      extra: "",
      ...over,
    });
    const grid = () => within(editor()).getByRole("grid", { name: "Files" });
    const cell = (label: string) => within(grid()).getByRole("textbox", { name: label }) as HTMLInputElement;
    const autoHeaders = () =>
      within(within(editor()).getByRole("list", { name: "Automatic headers" }))
        .getAllByRole("listitem")
        .map((li) => li.textContent);

    it("binds files to variable paths with the native dialog and counts them", async () => {
      await setup();
      fireEvent.click(tab("Files"));
      fireEvent.change(cell("Key, row 1"), { target: { value: "variables.input.files.0" } });
      await act(settle);
      expect(sent().request.files).toEqual([upload("variables.input.files.0", "")]);
      expect(grid().textContent).toContain("No file chosen");
      // A row without a file still counts: Go refuses to send it.
      expect(tab("Files1")).toBeTruthy();

      workspace.PickBodyFile.mockImplementation(() =>
        cancellable({ path: "fixtures/a.png", absolutePath: "/p/fixtures/a.png", name: "a.png", size: 10, exists: true }),
      );
      fireEvent.click(within(grid()).getByRole("button", { name: "Choose file, row 1" }));
      await act(settle);
      expect(sent().request.files).toEqual([upload("variables.input.files.0", "fixtures/a.png")]);
      expect(grid().textContent).toContain("a.png");
      expect(cell("Content-Type, row 1").placeholder).toBe("image/png");

      fireEvent.change(cell("Content-Type, row 1"), { target: { value: "image/apng" } });
      await act(settle);
      expect(sent().request.files).toEqual([upload("variables.input.files.0", "fixtures/a.png", { contentType: "image/apng" })]);

      // A cancelled dialog changes nothing.
      const calls = service.UpdateRequest.mock.calls.length;
      workspace.PickBodyFile.mockImplementation(() =>
        cancellable({ path: "", absolutePath: "", name: "", size: 0, exists: false }),
      );
      fireEvent.click(within(grid()).getByRole("button", { name: "Choose file, row 1" }));
      await act(settle);
      expect(service.UpdateRequest.mock.calls.length).toBe(calls);
    });

    it("shows the multipart headers and turns the preflight header off", async () => {
      await setup(request({ files: [upload("variables.file", "a.txt")] }));
      fireEvent.click(tab("Headers1"));
      expect(autoHeaders()).toEqual([
        expect.stringContaining("multipart/form-data"),
        expect.stringContaining("Accept"),
        expect.stringContaining("Apollo-Require-Preflight"),
      ]);

      fireEvent.click(tab("Files1"));
      const box = within(editor()).getByRole("checkbox", { name: /Apollo-Require-Preflight/ }) as HTMLInputElement;
      expect(box.checked).toBe(true);
      fireEvent.click(box);
      await act(settle);
      expect(sent().request.settings.omitHeaders).toEqual(["Apollo-Require-Preflight"]);
      fireEvent.click(tab("Headers1"));
      expect(autoHeaders()).toHaveLength(2);

      fireEvent.click(tab("Files1"));
      fireEvent.click(within(editor()).getByRole("checkbox", { name: /Apollo-Require-Preflight/ }));
      await act(settle);
      expect(sent().request.settings.omitHeaders).toBeNull();
    });

    it("warns that files need POST", async () => {
      await setup(request({ method: "GET", files: [upload("variables.file", "a.txt")] }));
      fireEvent.click(tab("Files1"));
      expect(within(editor()).getByText(/Files are only sent with POST/)).toBeTruthy();
    });
  });

  it("sends through the HTTP service after the queued edits reached Go, and cancels", async () => {
    await setup();
    typeInto("URL", "https://api.test/graphql");
    fireEvent.click(button("Send"));
    await act(settle);
    expect(http.Send).toHaveBeenCalledWith({ requestId: "gql" });
    const order = service.UpdateRequest.mock.invocationCallOrder;
    expect(http.Send.mock.invocationCallOrder[0]).toBeGreaterThan(order[order.length - 1] ?? 0);

    fireEvent.click(button("Cancel"));
    await act(settle);
    expect(opsCancel).toHaveBeenCalledWith("op_send");
    act(() => emit("http:op:done", { opId: "op_send", seq: 1, status: "cancelled", error: null, items: 0, batches: 0, elapsedMs: 5 }));
    expect(button("Send")).toBeTruthy();
  });

  describe("subscriptions (GQL-33, GQL-34)", () => {
    const SUB = "subscription Events { events { n } }";
    const subDiagnostics = () => cancellable(diagnostics({ operations: [{ name: "Events", type: "subscription" }] }));

    it("edits the connection params as JSON text with a check", async () => {
      await setup(request({ query: SUB }));
      fireEvent.click(tab("Connection"));
      expect(within(editor()).getByText("connection_init")).toBeTruthy();
      typeInto("Connection params", '{"authToken": "{{token}}"}');
      await act(settle);
      expect(sent().request.connectionParams).toBe('{"authToken": "{{token}}"}');
      typeInto("Connection params", '"tok"');
      await act(settle);
      expect(within(editor()).getByText(/must be a JSON object/)).toBeTruthy();
    });

    it("turns Send into Subscribe and Stop, and subscribes again", async () => {
      graphqlService.Diagnostics.mockImplementation(subDiagnostics);
      await setup(request({ query: SUB }));
      await waitForCheck();
      expect(within(editor()).queryByRole("button", { name: "Send" })).toBeNull();
      typeInto("URL", "https://api.test/graphql");
      fireEvent.click(button("Subscribe"));
      await act(settle);
      expect(graphqlService.Subscribe).toHaveBeenCalledWith({ requestId: "gql", allowUnresolved: false });
      const order = service.UpdateRequest.mock.invocationCallOrder;
      expect(graphqlService.Subscribe.mock.invocationCallOrder[0]).toBeGreaterThan(order[order.length - 1] ?? 0);
      expect(http.Send).not.toHaveBeenCalled();

      fireEvent.click(button("Stop"));
      await act(settle);
      expect(graphqlService.StopSubscription).toHaveBeenCalledWith({ opId: "op_sub" });
      act(() => emit("graphql.subscription:op:done", { opId: "op_sub", seq: 1, status: "cancelled", error: null, items: 0, batches: 0, elapsedMs: 5 }));
      fireEvent.click(button("Subscribe"));
      await act(settle);
      expect(graphqlService.Subscribe).toHaveBeenCalledTimes(2);
    });

    it("keeps Send for a query", async () => {
      await setup();
      await waitForCheck();
      expect(button("Send")).toBeTruthy();
      expect(within(editor()).queryByRole("button", { name: "Subscribe" })).toBeNull();
    });
  });

  describe("transport per operation (GQL-35)", () => {
    const picker = (name: string) => within(editor()).getByRole("combobox", { name }) as HTMLSelectElement;

    it("picks the transport of each operation type in the Connection tab and saves it in the settings", async () => {
      await setup();
      fireEvent.click(tab("Connection"));
      expect(picker("Queries").value).toBe("http");
      expect(picker("Mutations").value).toBe("http");
      expect(picker("Subscriptions").value).toBe("ws");
      // Subscriptions cannot run over plain HTTP.
      expect([...picker("Subscriptions").options].map((o) => o.value)).toEqual(["ws", "ws-legacy", "sse"]);

      fireEvent.change(picker("Subscriptions"), { target: { value: "ws-legacy" } });
      await act(settle);
      expect(sent().request.settings.graphqlTransport).toEqual({ query: "", mutation: "", subscription: "ws-legacy", extra: "" });
      fireEvent.change(picker("Queries"), { target: { value: "sse" } });
      await act(settle);
      expect(sent().request.settings.graphqlTransport).toEqual({ query: "sse", mutation: "", subscription: "ws-legacy", extra: "" });
      expect(picker("Queries").value).toBe("sse");

      // Back to the defaults: nothing is stored.
      fireEvent.change(picker("Queries"), { target: { value: "http" } });
      fireEvent.change(picker("Subscriptions"), { target: { value: "ws" } });
      await act(settle);
      expect(sent().request.settings.graphqlTransport).toBeNull();
    });

    it("shows a stored transport it does not know", async () => {
      await setup(request({ settings: { ...request().settings, graphqlTransport: { query: "quic", mutation: "", subscription: "", extra: "" } } }));
      fireEvent.click(tab("Connection"));
      expect(picker("Queries").value).toBe("quic");
      expect(picker("Queries").selectedOptions[0]?.textContent).toBe("quic (unknown)");
    });

    it("runs a query set to WebSocket through the subscription slot", async () => {
      await setup(request({ settings: { ...request().settings, graphqlTransport: { query: "ws", mutation: "", subscription: "", extra: "" } } }));
      await waitForCheck();
      expect(within(editor()).queryByRole("button", { name: "Copy as curl" })).toBeNull();
      fireEvent.click(button("Send"));
      await act(settle);
      expect(graphqlService.Subscribe).toHaveBeenCalledWith({ requestId: "gql", allowUnresolved: false });
      expect(http.Send).not.toHaveBeenCalled();
      fireEvent.click(button("Cancel"));
      await act(settle);
      expect(graphqlService.StopSubscription).toHaveBeenCalledWith({ opId: "op_sub" });
    });

    it("sends a query set to SSE over HTTP, asking for an event stream", async () => {
      await setup(request({ settings: { ...request().settings, graphqlTransport: { query: "sse", mutation: "", subscription: "", extra: "" } } }));
      await waitForCheck();
      fireEvent.click(tab("Headers1"));
      expect(within(editor()).getByRole("list", { name: "Automatic headers" }).textContent).toContain("text/event-stream");
      fireEvent.click(button("Send"));
      await act(settle);
      expect(http.Send).toHaveBeenCalled();
      expect(graphqlService.Subscribe).not.toHaveBeenCalled();
    });
  });

  describe("prettify and minify (GQL-18)", () => {
    const PRETTY = "query User($id: ID!) {\n  user(id: $id) {\n    name\n  }\n}";
    const prettifyKey = () =>
      new KeyboardEvent("keydown", { key: "F", code: "KeyF", shiftKey: true, [IS_MAC ? "metaKey" : "ctrlKey"]: true, bubbles: true, cancelable: true });

    it("prettifies and minifies the query in Go and saves the result", async () => {
      format.PrettifyGraphQL.mockImplementation(() => cancellable({ valid: true, text: PRETTY, error: null, errorCount: 0 }));
      format.MinifyGraphQL.mockImplementation(() => cancellable({ valid: true, text: "query User($id:ID!){user(id:$id){name}}", error: null, errorCount: 0 }));
      await setup(request({ query: "query User($id: ID!) { user(id: $id) { name } }" }));
      expect(button("Prettify").getAttribute("aria-keyshortcuts")).toBe(IS_MAC ? "Meta+Shift+F" : "Control+Shift+F");

      fireEvent.click(button("Prettify"));
      await act(settle);
      expect(format.PrettifyGraphQL).toHaveBeenCalledWith("query User($id: ID!) { user(id: $id) { name } }");
      expect(sent().request.query).toBe(PRETTY);
      expect(viewOf("GraphQL query").state.doc.toString()).toBe(PRETTY);

      fireEvent.click(button("Minify"));
      await act(settle);
      expect(format.MinifyGraphQL).toHaveBeenCalledWith(PRETTY);
      expect(sent().request.query).toBe("query User($id:ID!){user(id:$id){name}}");
    });

    it("prettifies with Mod+Shift+F inside the query editor, which is marked with the graphql scope", async () => {
      format.PrettifyGraphQL.mockImplementation(() => cancellable({ valid: true, text: PRETTY, error: null, errorCount: 0 }));
      await setup(request({ query: "{ a }" }));
      const content = viewOf("GraphQL query").contentDOM;
      expect(content.closest('[data-shortcut-scope="graphql"]')).not.toBeNull();
      const event = prettifyKey();
      act(() => {
        content.dispatchEvent(event);
      });
      await act(settle);
      expect(event.defaultPrevented).toBe(true);
      expect(format.PrettifyGraphQL).toHaveBeenCalledWith("{ a }");
      expect(sent().request.query).toBe(PRETTY);
    });

    it("leaves an invalid query alone and marks its first error", async () => {
      const error = { offset: 5, endOffset: 6, byteOffset: 5, line: 1, column: 6, message: 'expected value, found ")"' };
      format.PrettifyGraphQL.mockImplementation(() => cancellable({ valid: false, text: "{ a(x: ) }", error, errorCount: 2 }));
      await setup(request({ query: "{ a(x: ) }" }));
      fireEvent.click(button("Prettify"));
      await act(settle);
      expect(service.UpdateRequest).not.toHaveBeenCalled();
      const view = viewOf("GraphQL query");
      expect(currentDiagnostics(view.state)).toEqual([{ from: 5, to: 6, severity: "error", message: 'expected value, found ")"' }]);
      expect(view.state.selection.main.head).toBe(5);
    });

    it("shows a failed call under the editor", async () => {
      format.PrettifyGraphQL.mockImplementation(() => Object.assign(Promise.reject(new Error("Go went away")), { cancel: () => undefined }));
      await setup();
      fireEvent.click(button("Prettify"));
      await act(settle);
      expect(within(editor()).getByRole("alert").textContent).toContain("Go went away");
    });

    it("is disabled for a blank query", async () => {
      await setup(request({ query: "  " }));
      expect((button("Prettify") as HTMLButtonElement).disabled).toBe(true);
      expect((button("Minify") as HTMLButtonElement).disabled).toBe(true);
    });
  });

  it("shares the HTTP settings: redirects and TLS, with the URL bar badge (HTTP-22, HTTP-23)", async () => {
    await setup();
    const badge = () => within(editor()).queryByRole("button", { name: /TLS unverified/ });
    expect(badge()).toBeNull();
    fireEvent.click(tab("Settings"));
    fireEvent.click(within(editor()).getByRole("checkbox", { name: /Keep the original method/ }));
    await act(settle);
    expect(sent().request.settings.followOriginalMethod).toBe(true);
    fireEvent.change(within(editor()).getByRole("combobox", { name: "Certificates" }), { target: { value: "off" } });
    await act(settle);
    expect(sent().request.settings.verifyTls).toBe(false);
    expect(badge()?.getAttribute("data-tls-off")).toBe("request");
  });

  it("is read-only, with a notice, for a read-only request", async () => {
    service.Request.mockImplementation(() => Promise.resolve(request()));
    const stores = createStores();
    const nodes = [node({ id: "gql", readOnly: "newer_version" })];
    stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
    stores.collection.dispatch({ type: "collection/load", workspaceId: "ws_1", nodes, rootIds: ["gql"], readOnly: "" });
    stores.tabs.dispatch({ type: "tabs/restore", order: ["gql"], activeId: "gql" });
    render(
      <StoreProvider stores={stores}>
        <TabPanel />
      </StoreProvider>,
    );
    await act(settle);
    expect(within(editor()).getByRole("note").textContent).toContain("newer version");
    expect(modeField().disabled).toBe(true);
    expect(viewOf("GraphQL query").state.readOnly).toBe(true);
    expect((within(editor()).getByRole("button", { name: "Prettify" }) as HTMLButtonElement).disabled).toBe(true);
    expect(screen.queryByText("editor for this item is not available yet")).toBeNull();
  });
});
