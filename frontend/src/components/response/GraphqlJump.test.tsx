// Jumping from a GraphQL error location in the response pane to the Query
// editor (GQL-05, commands/graphqlJump.ts), with the real editor and panel.
import { act, fireEvent, render, screen, within } from "@testing-library/react";
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
const format = vi.hoisted(() => ({
  ValidateJSON: vi.fn<(text: string) => unknown>(),
  CheckGraphQLVariables: vi.fn<(query: string, variables: string, operationName: string) => unknown>(),
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/format", () => ({
  FormatService: format,
}));
const graphqlService = vi.hoisted(() => ({
  SchemaInfo: vi.fn<(input: { requestId: string }) => unknown>(),
  Diagnostics: vi.fn<(input: { key: string; text: string }) => unknown>(),
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/graphql", () => ({
  GraphQLService: graphqlService,
}));

import { jumpToQueryLocation, resetQueryJumps } from "../../commands/graphqlJump";
import { httpSends, resetHttpSends } from "../../commands/httpSends";
import type { RequestDTO } from "../../lib/collection";
import { createStores, StoreProvider, type Stores, type TreeNode } from "../../stores";
import { emit, resetFakeWails, settle } from "../../test/fakeWails";
import { httpResponse } from "../../test/httpResponse";
import { TabPanel } from "../tabs/TabPanel";
import { HttpResponsePanel } from "./HttpResponsePanel";

// Line 2 has a character outside the BMP before `nme`: one code point, two UTF-16 units.
const QUERY = '{\n  a: user(note: "\u{1F600}") { nme }\n}';
const NME = QUERY.indexOf("nme");
const BODY = JSON.stringify({
  errors: [{ message: 'Cannot query field "nme" on type "User".', locations: [{ line: 2, column: 24 }] }],
  data: null,
});

function request(): RequestDTO {
  return {
    id: "gql",
    kind: "graphql",
    name: "User",
    description: "",
    method: "",
    url: "https://api.test/graphql",
    params: [],
    pathParams: [],
    headers: [],
    body: { mode: "", type: "", text: "", fields: [], parts: [], path: "", contentType: "", extra: "" },
    query: QUERY,
    variables: "",
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
    extra: "",
  } as RequestDTO;
}

async function setup(): Promise<Stores> {
  service.Request.mockImplementation(() => Promise.resolve(request()));
  const stores = createStores();
  const node = { kind: "request", id: "gql", parentId: null, name: "User", path: "gql.json", fileKind: "graphql", readOnly: "", method: "POST" } as TreeNode;
  stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
  stores.collection.dispatch({ type: "collection/load", workspaceId: "ws_1", nodes: [node], rootIds: ["gql"], readOnly: "" });
  stores.tabs.dispatch({ type: "tabs/restore", order: ["gql"], activeId: "gql" });
  render(
    <StoreProvider stores={stores}>
      <TabPanel />
      <HttpResponsePanel id="gql" graphql />
    </StoreProvider>,
  );
  await act(settle);
  return stores;
}

const editor = () => document.querySelector("[data-request-id]") as HTMLElement;
const tab = (name: string) => within(within(editor()).getByRole("tablist", { name: "Request sections" })).getByRole("tab", { name });
const queryView = () => EditorView.findFromDOM(within(editor()).getByRole("textbox", { name: "GraphQL query" })) as EditorView;

async function receive(body: string): Promise<void> {
  act(() => httpSends().get("gql").send());
  await act(settle);
  act(() => {
    emit("http:op:data", { opId: "op_send", seq: 1, items: [httpResponse({ body, bodyPretty: JSON.stringify(JSON.parse(body), null, 2) })] });
    emit("http:op:done", { opId: "op_send", seq: 2, status: "ok", error: null, items: 1, batches: 1, elapsedMs: 5 });
  });
}

beforeEach(() => {
  resetFakeWails();
  resetHttpSends();
  resetQueryJumps();
  http.Send.mockReset().mockResolvedValue({ opId: "op_send" });
  service.Request.mockReset();
  service.UpdateRequest.mockReset().mockResolvedValue(undefined);
  service.Flush.mockReset().mockResolvedValue(undefined);
  format.ValidateJSON.mockReset().mockImplementation(() => cancellable({ valid: true, error: null }));
  format.CheckGraphQLVariables.mockReset().mockImplementation(() =>
    cancellable({ error: null, notObject: false, resolved: true, operation: "", declared: [], missing: [], unknown: [] }),
  );
  graphqlService.SchemaInfo.mockReset().mockImplementation(() =>
    cancellable({ endpoint: "", cached: false, key: "", fetchedAt: "", typeCount: 0, query: "", problems: 0 }),
  );
  graphqlService.Diagnostics.mockReset().mockImplementation(() =>
    cancellable({ schema: false, operations: [{ name: "", type: "query" }], diagnostics: [], count: 0, syntaxCount: 0 }),
  );
});

describe("jumping to an error location (GQL-05)", () => {
  it("opens the Query tab from another sub-tab and puts the cursor on the location", async () => {
    await setup();
    fireEvent.click(tab("Variables"));
    expect(within(editor()).queryByRole("textbox", { name: "GraphQL query" })).toBeNull();

    await receive(BODY);
    fireEvent.click(screen.getByRole("button", { name: "Line 2, column 24" }));

    expect(tab("Query").getAttribute("aria-selected")).toBe("true");
    const view = queryView();
    expect(view.state.selection.main.head).toBe(NME);
    expect(view.state.doc.sliceString(NME, NME + 3)).toBe("nme");
    expect(view.hasFocus).toBe(true);
  });

  it("moves the cursor of an open Query editor", async () => {
    const stores = await setup();
    const view = queryView();
    act(() => jumpToQueryLocation(stores, "gql", { line: 1, column: 1 }));
    expect(view.state.selection.main.head).toBe(0);
    act(() => jumpToQueryLocation(stores, "gql", { line: 2, column: 24 }));
    expect(view.state.selection.main.head).toBe(NME);
    // Past the last line: the end of the query.
    act(() => jumpToQueryLocation(stores, "gql", { line: 40, column: 1 }));
    expect(view.state.selection.main.head).toBe(QUERY.length);
  });
});
