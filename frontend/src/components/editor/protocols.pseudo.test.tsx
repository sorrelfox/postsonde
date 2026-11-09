// The protocol editors in the pseudo-locale (UI-10): the gRPC,
// GraphQL, WebSocket, Socket.IO and TCP editors with their tabs, the method
// picker, the schema strip, the docs explorer and the saved messages. Every
// text of namespace "editorProtocols" must come from the catalogue: no
// English of en/editorProtocols.ts may be left. The code editors' content
// and the lib/ labels, not converted yet, are left out.
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

const cancellable = <T,>(value: T) => Object.assign(Promise.resolve(value), { cancel: vi.fn() });

const collection = vi.hoisted(() => ({
  Request: vi.fn<(id: string) => Promise<unknown>>(),
  UpdateRequest: vi.fn<(input: unknown) => Promise<void>>(),
  Flush: vi.fn<(id: string) => Promise<void>>(),
  InheritedGRPCProto: vi.fn<(id: string) => unknown>(),
  UpdateFolderGRPCProto: vi.fn<(input: unknown) => unknown>(),
  InheritedAuth: vi.fn<(id: string) => unknown>(),
}));
const grpc = vi.hoisted(() => ({
  Describe: vi.fn<(input: unknown) => unknown>(),
  PickProtoFiles: vi.fn<() => unknown>(),
  PickImportPath: vi.fn<() => unknown>(),
  Invoke: vi.fn<(input: unknown) => unknown>(),
  ExampleMessage: vi.fn<(input: unknown) => unknown>(),
  ValidateMessage: vi.fn<(input: unknown) => unknown>(),
  CompleteMessage: vi.fn<(input: unknown) => unknown>(),
}));
const graphql = vi.hoisted(() => ({
  SchemaInfo: vi.fn<(input: unknown) => unknown>(),
  FetchSchema: vi.fn<(input: unknown) => unknown>(),
  Diagnostics: vi.fn<(input: unknown) => unknown>(),
  Complete: vi.fn<(input: unknown) => unknown>(),
  DocsOverview: vi.fn<(input: unknown) => unknown>(),
  DocsType: vi.fn<(input: { name: string }) => unknown>(),
  DocsSearch: vi.fn<(input: unknown) => unknown>(),
  DocsTarget: vi.fn<(input: unknown) => unknown>(),
}));
const format = vi.hoisted(() => ({
  ValidateJSON: vi.fn<(text: string) => unknown>(),
  CheckGraphQLVariables: vi.fn<(query: string, variables: string, operationName: string) => unknown>(),
}));
const http = vi.hoisted(() => ({
  Send: vi.fn<(input: unknown) => Promise<unknown>>(),
  Variables: vi.fn<(id: string) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({
  CollectionService: collection,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/grpc", () => ({
  GRPCService: grpc,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/graphql", () => ({
  GraphQLService: graphql,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/format", () => ({
  FormatService: format,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http", () => ({
  HTTPService: http,
}));
vi.mock(
  "../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/ops",
  async () => (await import("../../test/fakeWails")).fakeOpsBindings,
);

import { DEFAULT_LOCALE, PSEUDO_LOCALE, setLocale, t } from "../../i18n";
import { pseudoString } from "../../i18n/pseudo";
import type { RequestDTO } from "../../lib/collection";
import { KIND_LABELS } from "../../lib/graphqlDocs";
import { createStores, StoreProvider, type TreeNode } from "../../stores";
import { resetFakeWails, settle } from "../../test/fakeWails";
import { englishLeftovers, isPseudo, visibleTexts } from "../../test/pseudoLocale";
import { GraphqlEditor, resetGraphqlDocsPanel } from "./GraphqlEditor";
import { DESCRIBE_DELAY_MS, GrpcEditor } from "./GrpcEditor";
import { SocketIoEditor } from "./SocketIoEditor";
import { TcpEditor } from "./TcpEditor";
import { WebSocketEditor } from "./WebSocketEditor";

// jsdom has no layout: CodeMirror measures ranges when it draws the cursor.
for (const name of ["getClientRects", "getBoundingClientRect"] as const) {
  if (typeof Range.prototype[name] !== "function") {
    Object.defineProperty(Range.prototype, name, {
      configurable: true,
      value: name === "getClientRects" ? () => [] : () => ({ left: 0, right: 0, top: 0, bottom: 0, width: 0, height: 0 }),
    });
  }
}

function request(over: Partial<RequestDTO>): RequestDTO {
  return {
    id: "req",
    kind: "http",
    name: "Request",
    description: "",
    method: "",
    url: "",
    params: [],
    pathParams: [],
    headers: [],
    body: { mode: "", type: "", text: "", fields: [], parts: [], path: "", contentType: "", extra: "" },
    bodyExamples: [],
    query: "",
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
    auth: { type: "", username: "", password: "", prefix: null, token: "", key: "", value: "", in: "", disabled: false, localSecrets: [], extra: "" },
    settings: {
      timeoutMs: null,
      followRedirects: null,
      maxRedirects: null,
      followOriginalMethod: null,
      forwardAuthorization: null,
      verifyTls: null,
      connectTimeoutMs: null,
      urlEncoding: "",
      httpVersion: "",
      sendCookies: null,
      storeCookies: null,
      tls: null,
      deadlineMs: null,
      extra: "",
    },
    extra: "",
    ...over,
  };
}

const method = (name: string, kind: string, over: Record<string, unknown> = {}) => ({
  name,
  fullName: `shop.v1.Orders.${name}`,
  inputType: "shop.v1.GetOrderRequest",
  outputType: "shop.v1.Order",
  clientStreaming: kind === "client_stream" || kind === "bidi",
  serverStreaming: kind === "server_stream" || kind === "bidi",
  kind,
  deprecated: false,
  comment: "",
  ...over,
});

function described() {
  return {
    services: [
      {
        name: "Orders",
        fullName: "shop.v1.Orders",
        file: "shop/v1/orders.proto",
        comment: "Orders of the shop.",
        deprecated: false,
        methods: [method("GetOrder", "unary"), method("Sync", "bidi"), method("OldOrder", "unary", { deprecated: true })],
      },
    ],
    diagnostics: [
      { file: "shop/v1/orders.proto", line: 3, column: 1, severity: "warning", message: "import unused" },
      { file: "shop/v1/orders.proto", line: 9, column: 5, severity: "error", message: "undefined: Missing" },
    ],
    files: 2,
    errors: 1,
    cached: false,
  };
}

/**
 * Text that is not the catalogue's: the GraphQL type kinds of the docs'
 * badges (lib/graphqlDocs), GraphQL keywords shown as the SDL writes them.
 */
const FOREIGN: readonly string[] = Object.values(KIND_LABELS);

/**
 * Left out of the check: the code editors' content (text the user typed),
 * and a directive's locations in the docs, which read as in the SDL
 * ("on FIELD").
 */
function foreignElements(): Element[] {
  const out: Element[] = [...document.querySelectorAll(".cm-editor")];
  out.push(...[...document.querySelectorAll("p")].filter((p) => p.textContent?.startsWith("on ")));
  return out;
}

/** `names`: format names another area shows as they are (the message log's "Hex"). */
function expectNoEnglish(root: Element, names: readonly string[] = []) {
  const exclude = foreignElements();
  expect(englishLeftovers([root], ["editorProtocols"], { exclude, foreign: [...FOREIGN, ...names] })).toEqual([]);
  expect(visibleTexts([root], exclude).filter(isPseudo).length).toBeGreaterThan(3);
}

async function renderEditor(dto: RequestDTO, fileKind: string, Editor: (props: { id: string }) => JSX.Element) {
  collection.Request.mockImplementation(() => Promise.resolve(structuredClone(dto)));
  const stores = createStores();
  const node = { kind: "request", parentId: null, name: dto.name, path: `${dto.id}.json`, fileKind, readOnly: "", method: "" } as unknown as TreeNode;
  const nodes = [{ ...node, id: dto.id } as TreeNode];
  stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
  stores.collection.dispatch({ type: "collection/load", workspaceId: "ws_1", nodes, rootIds: [dto.id], readOnly: "" });
  const { container } = render(
    <StoreProvider stores={stores}>
      <Editor id={dto.id} />
    </StoreProvider>,
  );
  await act(settle);
  return container;
}

const editor = () => document.querySelector("[data-request-id]") as HTMLElement;
const tabOf = (list: string, name: string) =>
  within(within(editor()).getByRole("tablist", { name: pseudoString(list) })).getByRole("tab", { name: new RegExp(escape(pseudoString(name))) });
const escape = (s: string) => s.replace(/[.*+?^${}()|[\]\\]/g, "\\$&");

beforeEach(() => {
  resetFakeWails();
  resetGraphqlDocsPanel();
  for (const fn of [...Object.values(collection), ...Object.values(grpc), ...Object.values(graphql), ...Object.values(format), ...Object.values(http)]) {
    fn.mockReset();
  }
  collection.UpdateRequest.mockResolvedValue(undefined);
  collection.Flush.mockResolvedValue(undefined);
  collection.InheritedGRPCProto.mockImplementation(() =>
    cancellable({ folderId: "", folderName: "", files: [], importPaths: [], parentId: "fld_shop", parentName: "Shop" }),
  );
  collection.InheritedAuth.mockImplementation(() => cancellable({ type: "none", disabled: false, folderId: "", folderName: "" }));
  grpc.Describe.mockImplementation(() => cancellable(described()));
  grpc.ExampleMessage.mockImplementation(() => cancellable({ type: "shop.v1.GetOrderRequest", json: "{}" }));
  grpc.ValidateMessage.mockImplementation(() => cancellable({ type: "shop.v1.GetOrderRequest", valid: true, error: null }));
  grpc.CompleteMessage.mockImplementation(() => cancellable({ items: [] }));
  format.ValidateJSON.mockImplementation(() => cancellable({ valid: true, error: null }));
  format.CheckGraphQLVariables.mockImplementation(() =>
    cancellable({
      error: null,
      notObject: false,
      resolved: true,
      operation: "User",
      declared: [],
      missing: [{ name: "id", type: "ID!", required: true }],
      unknown: [{ name: "extra", offset: 1, line: 1, column: 2 }],
    }),
  );
  graphql.SchemaInfo.mockImplementation(() =>
    cancellable({
      source: "introspection",
      file: "",
      modifiedAt: "",
      error: null,
      problemList: [],
      endpoint: "https://api.test/graphql",
      cached: true,
      key: "https://api.test/graphql",
      fetchedAt: new Date(Date.now() - 120_000).toISOString(),
      typeCount: 14,
      query: "conservative",
      problems: 2,
    }),
  );
  graphql.Diagnostics.mockImplementation(() =>
    cancellable({
      schema: true,
      operations: [{ name: "User", type: "query" }],
      diagnostics: [{ offset: 0, endOffset: 5, line: 1, column: 1, severity: "error", message: "Cannot query field" }],
      count: 1,
      syntaxCount: 0,
    }),
  );
  graphql.DocsOverview.mockImplementation(() =>
    cancellable({
      schema: true,
      description: "",
      // Not "Query": a type name is data, and "Query" is also the English of the Query tab.
      roots: [{ operation: "query", type: "ShopQuery" }],
      types: [{ name: "ShopQuery", kind: "OBJECT" }],
      directives: [{ name: "cached", description: "", args: [], locations: ["FIELD"], repeatable: true }],
    }),
  );
  graphql.DocsType.mockImplementation(({ name }: { name: string }) =>
    cancellable({
      found: true,
      name,
      kind: "OBJECT",
      description: "",
      root: "query",
      specifiedByUrl: "",
      isOneOf: false,
      interfaces: ["Node"],
      possibleTypes: [],
      implementations: [],
      fields: [
        {
          name: "user",
          description: "",
          args: [{ name: "id", description: "", type: "ID!", typeName: "ID", hasDefault: false, defaultValue: "", deprecated: false, deprecationReason: "" }],
          type: "User",
          typeName: "User",
          typeKind: "OBJECT",
          deprecated: true,
          deprecationReason: "",
        },
      ],
      inputFields: [],
      enumValues: [],
    }),
  );
  graphql.DocsSearch.mockImplementation(() =>
    cancellable({
      query: "user",
      count: 3,
      items: [{ kind: "field", parent: "ShopQuery", name: "user", type: "ShopQuery", member: "user", detail: "User", description: "", deprecated: true }],
    }),
  );
  setLocale(PSEUDO_LOCALE);
});

afterEach(() => {
  setLocale(DEFAULT_LOCALE);
});

describe("the protocol editors in the pseudo-locale", () => {
  it("show the gRPC editor, its method picker and every tab from the catalogue", async () => {
    const container = await renderEditor(
      request({
        id: "req_grpc",
        kind: "grpc",
        address: "localhost:50051",
        service: "shop.v1.Orders",
        method: "Sync",
        proto: { files: ["shop/v1/orders.proto"], importPaths: ["proto"], inherit: false, extra: "" },
        metadata: [{ name: "x-id", value: "1", description: "", enabled: true, extra: "" }],
      }),
      "grpc",
      GrpcEditor,
    );
    await act(() => new Promise<void>((r) => setTimeout(r, DESCRIBE_DELAY_MS + 30)));
    expect(within(editor()).getByRole("button", { name: pseudoString("Start stream") })).toBeTruthy();
    expectNoEnglish(container);

    fireEvent.click(within(editor()).getByRole("button", { name: /shop\.v1\.Orders/ }));
    expect(within(editor()).getByRole("listbox", { name: pseudoString("Methods") })).toBeTruthy();
    expectNoEnglish(container);
    fireEvent.change(within(editor()).getByRole("combobox", { name: pseudoString("Filter methods") }), { target: { value: "nothing" } });
    expect(within(editor()).getByText(pseudoString("No method matches."))).toBeTruthy();
    expectNoEnglish(container);
    fireEvent.keyDown(within(editor()).getByRole("combobox", { name: pseudoString("Filter methods") }), { key: "Escape" });

    for (const name of ["Proto", "Metadata", "Settings", "Message"]) {
      fireEvent.click(tabOf("Request options", name));
      await act(settle);
      expectNoEnglish(container);
    }
    fireEvent.click(tabOf("Request options", "Proto"));
    expect(within(editor()).getByRole("list", { name: pseudoString("Problems") })).toBeTruthy();
    expect(within(editor()).getByText(t("editorProtocols.grpc.errorCount", { count: 1 }), { exact: false })).toBeTruthy();
    fireEvent.click(within(editor()).getByRole("checkbox", { name: pseudoString("Use the folder's proto setup") }));
    await act(settle);
    expectNoEnglish(container);
  });

  it("show the GraphQL editor, its schema strip, tabs and docs from the catalogue", async () => {
    const container = await renderEditor(
      request({
        id: "req_gql",
        kind: "graphql",
        url: "https://api.test/graphql",
        query: "query User($id: ID!) { user(id: $id) { name } }",
        variables: '{"extra": 1}',
        connectionParams: "[1]",
        extensions: "[]",
      } as Partial<RequestDTO>),
      "graphql",
      GraphqlEditor,
    );
    await act(() => new Promise<void>((r) => setTimeout(r, 400)));
    const strip = within(editor()).getByRole("group", { name: pseudoString("GraphQL schema") });
    expect(strip.textContent).toContain(t("editorProtocols.schema.types", { count: 14 }));
    expectNoEnglish(container);

    for (const name of ["Variables", "Extensions", "Files", "Connection", "Headers", "Query"]) {
      fireEvent.click(tabOf("Request sections", name));
      await act(() => new Promise<void>((r) => setTimeout(r, 400)));
      expectNoEnglish(container);
    }

    fireEvent.click(within(strip).getByRole("button", { name: pseudoString("Docs") }));
    await act(settle);
    const docs = screen.getByRole("complementary", { name: pseudoString("GraphQL docs") });
    expect(within(docs).getByRole("region", { name: pseudoString("Root types") })).toBeTruthy();
    expectNoEnglish(container);
    fireEvent.click(within(docs).getAllByRole("button", { name: "ShopQuery" })[0]!);
    await act(settle);
    expect(within(docs).getByRole("region", { name: pseudoString("Fields") })).toBeTruthy();
    expectNoEnglish(container);
    fireEvent.change(within(docs).getByRole("searchbox", { name: pseudoString("Search the schema") }), { target: { value: "user" } });
    await act(() => new Promise<void>((r) => setTimeout(r, 200)));
    expect(within(docs).getByRole("listbox", { name: pseudoString("Search results") })).toBeTruthy();
    expectNoEnglish(container);
  });

  it("show the WebSocket editor, its tabs and saved messages from the catalogue", async () => {
    const container = await renderEditor(
      request({
        id: "req_ws",
        kind: "websocket",
        url: "http://localhost:8080/feed",
        message: "hello",
        subprotocols: ["chat.v1", "bad value", "chat.v1"],
        savedMessages: [
          { name: "Hello", text: "hi", format: "", event: "", ack: false, extra: "" },
          { name: "", text: "00ff", format: "hex", event: "", ack: false, extra: "" },
        ],
      } as Partial<RequestDTO>),
      "websocket",
      WebSocketEditor,
    );
    expect(within(editor()).getByRole("button", { name: pseudoString("Connect") })).toBeTruthy();
    expect(within(editor()).getByRole("region", { name: pseudoString("Saved messages") })).toBeTruthy();
    expectNoEnglish(container, ["Hex"]);
    for (const name of ["Headers", "Subprotocols", "Settings", "Params"]) {
      fireEvent.click(tabOf("Handshake options", name));
      await act(settle);
      expectNoEnglish(container, ["Hex"]);
    }
  });

  it("show the Socket.IO and TCP editors from the catalogue", async () => {
    const sio = await renderEditor(
      request({
        id: "req_sio",
        kind: "socketio",
        url: "ftp://localhost:3000",
        authPayload: "[1]",
        event: "",
        savedMessages: [{ name: "Join", text: "[]", format: "", event: "join", ack: true, extra: "" }],
      } as Partial<RequestDTO>),
      "socketio",
      SocketIoEditor,
    );
    expect(within(editor()).getByRole("region", { name: pseudoString("Saved emits") })).toBeTruthy();
    expectNoEnglish(sio);
    for (const name of ["Auth", "Options", "Settings", "Params"]) {
      fireEvent.click(tabOf("Connection options", name));
      await act(() => new Promise<void>((r) => setTimeout(r, 400)));
      expectNoEnglish(sio);
    }
    sio.remove();

    const tcp = await renderEditor(
      request({
        id: "req_tcp",
        kind: "tcp",
        address: "localhost:7000",
        sendMode: "hex",
        message: "zz",
        framing: { mode: "length", delimiter: "", prefixBytes: 2, byteOrder: "", maxMessageBytes: null, extra: "" },
        settings: { ...request({}).settings, tls: true },
      } as Partial<RequestDTO>),
      "tcp",
      TcpEditor,
    );
    expect(within(editor()).getByRole("group", { name: pseudoString("Connection options") })).toBeTruthy();
    expect(within(editor()).getByRole("textbox", { name: pseudoString("Message") })).toBeTruthy();
    expectNoEnglish(tcp);
  });
});
