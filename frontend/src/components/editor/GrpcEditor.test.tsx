import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const collection = vi.hoisted(() => ({
  Request: vi.fn<(id: string) => Promise<unknown>>(),
  UpdateRequest: vi.fn<(input: { request: unknown; revision: number }) => Promise<void>>(),
  Flush: vi.fn<(id: string) => Promise<void>>(),
  InheritedGRPCProto: vi.fn<(id: string) => unknown>(),
  UpdateFolderGRPCProto: vi.fn<(input: unknown) => unknown>(),
}));
const grpc = vi.hoisted(() => ({
  Describe: vi.fn<(input: { files: string[]; importPaths: string[]; reload: boolean }) => unknown>(),
  PickProtoFiles: vi.fn<() => unknown>(),
  PickImportPath: vi.fn<() => unknown>(),
  Invoke: vi.fn<(input: unknown) => unknown>(),
  SendMessage: vi.fn<(input: unknown) => unknown>(),
  CloseSend: vi.fn<(input: unknown) => unknown>(),
  ExampleMessage: vi.fn<(input: { files: string[]; importPaths: string[]; service: string; method: string; message: string }) => unknown>(),
  ValidateMessage: vi.fn<(input: { files: string[]; importPaths: string[]; service: string; method: string; message: string }) => unknown>(),
  CompleteMessage: vi.fn<(input: unknown) => unknown>(),
  Snippet: vi.fn<(input: { call: unknown; language: string; revealSecrets: boolean }) => unknown>(),
  SnippetLanguages: vi.fn<() => unknown>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection", () => ({
  CollectionService: collection,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/grpc", () => ({
  GRPCService: grpc,
}));
vi.mock(
  "../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/ops",
  async () => (await import("../../test/fakeWails")).fakeOpsBindings,
);

import { grpcCalls, resetGrpcCalls } from "../../commands/grpcCalls";
import type { RequestDTO } from "../../lib/collection";
import {
  canInvoke,
  diagnosticLocation,
  filterServices,
  findMethod,
  invokeInput,
  messageInput,
  methodKindLabel,
  sameJson,
  statusText,
  type CallEventDTO,
  type DescribeResultDTO,
  type ServiceDTO,
} from "../../lib/grpc";
import { createStores, StoreProvider, type Stores, type TreeNode } from "../../stores";
import { deferred, emit, opsCancel, resetFakeWails, settle } from "../../test/fakeWails";
import { TabBar } from "../tabs/TabBar";
import { TabPanel } from "../tabs/TabPanel";
import { GrpcResponsePanel } from "../response/GrpcResponsePanel";
import { EditorView } from "@codemirror/view";
import { completionStatus, currentCompletions, startCompletion } from "@codemirror/autocomplete";
import { DESCRIBE_DELAY_MS } from "./GrpcEditor";
import { MESSAGE_CHECK_DELAY_MS } from "./GrpcMessageEditor";

// jsdom has no layout: CodeMirror measures ranges when it draws the cursor.
for (const name of ["getClientRects", "getBoundingClientRect"] as const) {
  if (typeof Range.prototype[name] !== "function") {
    Object.defineProperty(Range.prototype, name, {
      configurable: true,
      value: name === "getClientRects" ? () => [] : () => ({ left: 0, right: 0, top: 0, bottom: 0, width: 0, height: 0 }),
    });
  }
}

const cancellable = <T,>(value: T) => Object.assign(Promise.resolve(value), { cancel: vi.fn() });

function request(over: Partial<RequestDTO> = {}): RequestDTO {
  return {
    id: "req_order",
    kind: "grpc",
    name: "Get order",
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
    address: "localhost:50051",
    sendMode: "",
    lineEnding: "",
    message: '{"name": "orders/1"}',
    subprotocols: [],
    messageFormat: "",
    service: "",
    proto: { files: ["proto/orders.proto"], importPaths: ["proto"], inherit: false, extra: '{"x-set":1}' },
    metadata: [{ name: "x-request-id", value: "1", description: "", enabled: true, extra: "" }],
    auth: { type: "", username: "", password: "", prefix: null, token: "", key: "", value: "", in: "", disabled: false, localSecrets: [], extra: "" },
    settings: { timeoutMs: null, followRedirects: null, maxRedirects: null, followOriginalMethod: null, forwardAuthorization: null, verifyTls: null, connectTimeoutMs: null, urlEncoding: "", httpVersion: "", sendCookies: null, storeCookies: null, tls: null, deadlineMs: null, extra: "" },
    extra: '{"x-note":"kept"}',
    ...over,
  };
}

const method = (name: string, kind: string, over: Partial<ServiceDTO["methods"] extends (infer M)[] | null ? M : never> = {}) => ({
  name,
  fullName: `shop.v1.OrderService.${name}`,
  inputType: "shop.v1.GetOrderRequest",
  outputType: "shop.v1.Order",
  clientStreaming: kind === "client_stream" || kind === "bidi",
  serverStreaming: kind === "server_stream" || kind === "bidi",
  kind,
  deprecated: false,
  comment: "",
  ...over,
});

const SERVICES: ServiceDTO[] = [
  {
    name: "Health",
    fullName: "grpc.health.v1.Health",
    file: "grpc/health/v1/health.proto",
    comment: "",
    deprecated: false,
    methods: [
      {
        name: "Check",
        fullName: "grpc.health.v1.Health.Check",
        inputType: "grpc.health.v1.HealthCheckRequest",
        outputType: "grpc.health.v1.HealthCheckResponse",
        clientStreaming: false,
        serverStreaming: false,
        kind: "unary",
        deprecated: false,
        comment: "",
      },
    ],
  },
  {
    name: "OrderService",
    fullName: "shop.v1.OrderService",
    file: "shop/v1/orders.proto",
    comment: "Orders of the shop.",
    deprecated: false,
    methods: [
      method("GetOrder", "unary", { comment: "GetOrder returns one order." }),
      method("WatchOrders", "server_stream"),
      method("ImportOrders", "client_stream"),
      method("Sync", "bidi"),
      method("OldOrder", "unary", { deprecated: true }),
    ],
  },
];

function result(over: Partial<DescribeResultDTO> = {}): DescribeResultDTO {
  return { services: SERVICES, diagnostics: [], files: 3, errors: 0, cached: false, ...over };
}

const node = (over: Partial<TreeNode> & { id: string }): TreeNode =>
  ({ kind: "request", parentId: null, name: over.id, path: `${over.id}.json`, fileKind: "grpc", readOnly: "", method: "", ...over }) as TreeNode;

async function setup(dto: RequestDTO = request()): Promise<Stores> {
  collection.Request.mockImplementation(() => Promise.resolve(structuredClone(dto)));
  const stores = createStores();
  const nodes = [node({ id: "req_order", name: "Get order" })];
  stores.collection.dispatch({ type: "collection/loading", workspaceId: "ws_1" });
  stores.collection.dispatch({ type: "collection/load", workspaceId: "ws_1", nodes, rootIds: nodes.map((n) => n.id), readOnly: "" });
  stores.tabs.dispatch({ type: "tabs/restore", order: ["req_order"], activeId: "req_order" });
  render(
    <StoreProvider stores={stores}>
      <TabBar />
      <TabPanel />
      <GrpcResponsePanel id="req_order" />
    </StoreProvider>,
  );
  await act(settle);
  await waitForDescribe();
  return stores;
}

const editor = () => document.querySelector("[data-request-id]") as HTMLElement;
const sent = () => {
  const calls = collection.UpdateRequest.mock.calls;
  return calls[calls.length - 1]?.[0] as { request: RequestDTO; revision: number };
};
const button = (name: string | RegExp) => within(editor()).getByRole("button", { name }) as HTMLButtonElement;
const tab = (name: RegExp) => within(within(editor()).getByRole("tablist", { name: "Request options" })).getByRole("tab", { name });
const response = () => document.querySelector("[data-response-for]") as HTMLElement;
const responseTab = (name: RegExp) => within(within(response()).getByRole("tablist", { name: "Response" })).getByRole("tab", { name });
const callStatus = () => response().querySelector("[data-call-status]")?.textContent ?? "";
/** The call's message log; a unary call's Messages tab switches from the response message to it. */
const callLog = () => {
  const log = within(response()).queryByRole("button", { name: "Log" });
  if (log !== null && log.getAttribute("aria-pressed") !== "true") fireEvent.click(log);
  act(() => grpcCalls().get("req_order").log.flush());
  return within(response()).getByRole("grid", { name: "gRPC call" });
};
const waitForDescribe = () => act(() => new Promise<void>((r) => setTimeout(r, DESCRIBE_DELAY_MS + 30)));
const lastDescribe = () => grpc.Describe.mock.calls[grpc.Describe.mock.calls.length - 1]?.[0];

beforeEach(() => {
  resetFakeWails();
  resetGrpcCalls();
  grpc.Invoke.mockReset();
  collection.Request.mockReset();
  collection.UpdateRequest.mockReset().mockResolvedValue(undefined);
  collection.Flush.mockReset().mockResolvedValue(undefined);
  collection.InheritedGRPCProto.mockReset().mockImplementation(() =>
    cancellable({ folderId: "", folderName: "", files: [], importPaths: [], parentId: "fld_shop", parentName: "Shop" }),
  );
  collection.UpdateFolderGRPCProto.mockReset().mockImplementation(() => cancellable(undefined));
  grpc.Describe.mockReset().mockImplementation(() => cancellable(result()));
  grpc.PickProtoFiles.mockReset();
  grpc.PickImportPath.mockReset();
  grpc.ExampleMessage.mockReset().mockImplementation((input) => cancellable({ type: "shop.v1.GetOrderRequest", json: exampleOf(input.method) }));
  grpc.ValidateMessage.mockReset().mockImplementation(() => cancellable({ type: "shop.v1.GetOrderRequest", valid: true, error: null }));
  grpc.CompleteMessage.mockReset().mockImplementation(() =>
    cancellable({ type: "shop.v1.GetOrderRequest", context: "none", from: 0, to: 0, prefix: "", owner: "", items: [], count: 0 }),
  );
});

/** The example the mocked ExampleMessage returns for a method. */
const exampleOf = (method: string) => `{\n  "${method.toLowerCase()}": ""\n}`;

describe("GrpcEditor", () => {
  it("renders a grpc request with its address, a disabled Invoke and sub-tabs", async () => {
    await setup();
    expect(screen.getByRole("tab", { name: "GRPC Get order" })).toBeTruthy();
    expect((within(editor()).getByRole("textbox", { name: "Server address" }) as HTMLInputElement).value).toBe("localhost:50051");
    expect(button("Invoke").disabled).toBe(true);
    expect(button("TLS").getAttribute("aria-pressed")).toBe("false");
    const tabs = within(editor()).getByRole("tablist", { name: "Request options" });
    expect(within(tabs).getAllByRole("tab").map((t) => t.textContent)).toEqual(["Proto1", "Message", "Metadata1", "Auth", "Tests", "Settings", "Notes", "Code"]);
    // A request with files opens on the message.
    expect(tab(/Message/).getAttribute("aria-selected")).toBe("true");
    expect(lastDescribe()).toEqual({ files: ["proto/orders.proto"], importPaths: ["proto"], reload: false });
    expect(within(editor()).getByText("Choose a service and method")).toBeTruthy();
    expect(collection.UpdateRequest).not.toHaveBeenCalled();
  });

  it("writes the call Invoke would make as a grpcurl command in the Code tab", async () => {
    grpc.SnippetLanguages.mockReset().mockImplementation(() => cancellable([{ id: "grpcurl", name: "grpcurl", syntax: "shell" }]));
    grpc.Snippet.mockReset().mockImplementation(() =>
      cancellable({ language: "grpcurl", mode: "resolved", code: "grpcurl -plaintext \\\n  localhost:50051 list", masked: false, revealed: false, unresolved: [] }),
    );
    await setup();
    fireEvent.click(tab(/Code/));
    await act(settle);
    const dto = request();
    expect(grpc.Snippet).toHaveBeenLastCalledWith({
      call: invokeInput(dto, { files: dto.proto.files ?? [], importPaths: dto.proto.importPaths ?? [] }),
      language: "grpcurl",
      revealSecrets: false,
    });
    expect(within(editor()).getByLabelText("grpcurl code").textContent).toBe("grpcurl -plaintext \\\n  localhost:50051 list");
    expect(within(editor()).queryByRole("group", { name: "Values" })).toBeNull();
    expect(within(editor()).getByRole("status").textContent).toBe("The call as it is set up now.");
  });

  it("groups methods by service with streaming badges, filters them and saves the choice", async () => {
    await setup();
    fireEvent.click(button(/Choose a service and method/));
    const list = within(editor()).getByRole("listbox", { name: "Methods" });
    const groups = within(list).getAllByRole("group");
    expect(groups.map((g) => g.getAttribute("aria-label"))).toEqual(["grpc.health.v1.Health", "shop.v1.OrderService"]);
    const orders = groups[1] as HTMLElement;
    const options = within(orders).getAllByRole("option");
    expect(options.map((o) => o.querySelector("[data-kind]")?.textContent)).toEqual(["unary", "server stream", "client stream", "bidi", "unary"]);
    expect(within(orders).getByText("GetOrder returns one order.")).toBeTruthy();
    expect(within(orders).getByText("OldOrder").hasAttribute("data-deprecated")).toBe(true);

    const filter = within(editor()).getByRole("combobox", { name: "Filter methods" });
    fireEvent.change(filter, { target: { value: "watch" } });
    expect(within(list).getAllByRole("option").map((o) => o.querySelector("span")?.textContent)).toEqual(["WatchOrders"]);
    fireEvent.keyDown(filter, { key: "Enter" });
    await act(settle);
    expect(sent().request).toEqual(request({ service: "shop.v1.OrderService", method: "WatchOrders" }));
    expect(within(editor()).queryByRole("listbox")).toBeNull();
    const picker = button(/shop\.v1\.OrderService/);
    expect(picker.textContent).toContain("server stream");
    expect(within(editor()).getByText("Input shop.v1.GetOrderRequest, output shop.v1.Order")).toBeTruthy();

    // Keyboard and mouse choice.
    fireEvent.click(picker);
    const filter2 = within(editor()).getByRole("combobox", { name: "Filter methods" });
    fireEvent.keyDown(filter2, { key: "ArrowDown" });
    fireEvent.keyDown(filter2, { key: "Enter" });
    await act(settle);
    expect(sent().request).toMatchObject({ service: "shop.v1.OrderService", method: "ImportOrders" });
    fireEvent.click(button(/shop\.v1\.OrderService/));
    fireEvent.click(within(within(editor()).getByRole("listbox")).getByText("Check"));
    await act(settle);
    expect(sent().request).toMatchObject({ service: "grpc.health.v1.Health", method: "Check" });
  });

  it("warns when the saved method is not in the loaded files", async () => {
    await setup(request({ service: "shop.v1.Gone", method: "Nope" }));
    expect(within(editor()).getByText("shop.v1.Gone/Nope is not in the loaded .proto files.")).toBeTruthy();
  });

  it("edits the request's own setup with the native pickers and shows diagnostics", async () => {
    grpc.Describe.mockImplementation(() =>
      cancellable(
        result({
          errors: 1,
          diagnostics: [
            {
              code: "proto.import_not_found",
              severity: "error",
              file: "shop/v1/orders.proto",
              path: "proto/shop/v1/orders.proto",
              line: 5,
              column: 8,
              message: '"google/api/annotations.proto" not found',
            },
            { code: "grpc.import_path_missing", severity: "warning", file: "vendor", path: "", line: 0, column: 0, message: "the import path is not a folder that exists" },
          ],
        }),
      ),
    );
    await setup();
    expect(within(tab(/Proto/)).getByLabelText("1 error")).toBeTruthy();
    fireEvent.click(tab(/Proto/));
    const problems = within(editor()).getByRole("list", { name: "Problems" });
    expect(within(problems).getAllByRole("listitem").map((li) => li.textContent)).toEqual([
      'error:shop/v1/orders.proto:5:8"google/api/annotations.proto" not found',
      "warning:vendorthe import path is not a folder that exists",
    ]);
    expect(within(editor()).getByRole("status").textContent).toBe("3 files loaded · 2 services · 1 error · 1 warning");

    grpc.PickProtoFiles.mockReturnValue(cancellable(["proto/health.proto", "proto/orders.proto"]));
    fireEvent.click(button("Add .proto files"));
    await act(settle);
    expect(sent().request.proto).toEqual({ files: ["proto/orders.proto", "proto/health.proto"], importPaths: ["proto"], inherit: false, extra: '{"x-set":1}' });
    await waitForDescribe();
    expect(lastDescribe()).toEqual({ files: ["proto/orders.proto", "proto/health.proto"], importPaths: ["proto"], reload: false });

    grpc.PickImportPath.mockReturnValue(cancellable("/opt/include"));
    fireEvent.click(button("Add import path"));
    await act(settle);
    expect(sent().request.proto.importPaths).toEqual(["proto", "/opt/include"]);

    grpc.PickImportPath.mockReturnValue(cancellable(""));
    const before = collection.UpdateRequest.mock.calls.length;
    fireEvent.click(button("Add import path"));
    await act(settle);
    expect(collection.UpdateRequest.mock.calls.length).toBe(before);

    fireEvent.click(button("Remove proto/orders.proto from the request setup"));
    await act(settle);
    expect(sent().request.proto.files).toEqual(["proto/health.proto"]);

    await waitForDescribe();
    fireEvent.click(button("Reload"));
    await waitForDescribe();
    expect(lastDescribe()?.reload).toBe(true);
  });

  it("uses and edits the folder's setup when inheriting", async () => {
    collection.InheritedGRPCProto.mockImplementation(() =>
      cancellable({ folderId: "fld_shop", folderName: "Shop", files: ["shared/orders.proto"], importPaths: ["shared"], parentId: "fld_v1", parentName: "V1" }),
    );
    await setup(request({ proto: { files: ["own.proto"], importPaths: [], inherit: true, extra: "" } }));
    expect(collection.InheritedGRPCProto).toHaveBeenCalledWith("req_order");
    expect(lastDescribe()).toEqual({ files: ["shared/orders.proto"], importPaths: ["shared"], reload: false });

    fireEvent.click(tab(/Proto/));
    const inherit = within(editor()).getByRole("checkbox", { name: "Use the folder's proto setup" }) as HTMLInputElement;
    expect(inherit.checked).toBe(true);
    expect(editor().textContent).toContain("Using the setup of Shop");
    // Read-only until "Edit folder setup".
    expect(within(editor()).queryByRole("button", { name: "Add import path" })).toBeNull();
    fireEvent.click(button("Edit folder setup"));
    grpc.PickImportPath.mockReturnValue(cancellable("/opt/include"));
    fireEvent.click(button("Add import path"));
    await act(settle);
    expect(collection.UpdateFolderGRPCProto).toHaveBeenCalledWith({ folderId: "fld_shop", files: ["shared/orders.proto"], importPaths: ["shared", "/opt/include"] });
    expect(collection.InheritedGRPCProto).toHaveBeenCalledTimes(2);

    // A tree change reloads the inherited setup.
    act(() => emit("collection:changed", { workspaceId: "ws_1", op: "update_folder", nodeId: "fld_shop", trashId: "" }));
    await act(settle);
    expect(collection.InheritedGRPCProto).toHaveBeenCalledTimes(3);

    // Turning it off uses the request's own files again.
    fireEvent.click(inherit);
    await act(settle);
    expect(sent().request.proto).toEqual({ files: ["own.proto"], importPaths: [], inherit: false, extra: "" });
    await waitForDescribe();
    expect(lastDescribe()).toEqual({ files: ["own.proto"], importPaths: [], reload: false });
  });

  it("offers to set up the parent folder when no folder has a setup", async () => {
    await setup(request({ proto: { files: [], importPaths: [], inherit: true, extra: "" } }));
    fireEvent.click(tab(/Proto/));
    expect(editor().textContent).toContain("No folder above this request has a proto setup yet.");
    fireEvent.click(button("Set up Shop"));
    grpc.PickProtoFiles.mockReturnValue(cancellable(["shop.proto"]));
    fireEvent.click(button("Add .proto files"));
    await act(settle);
    expect(collection.UpdateFolderGRPCProto).toHaveBeenCalledWith({ folderId: "fld_shop", files: ["shop.proto"], importPaths: [] });
  });

  it("starts on the Proto tab without a setup and auto-saves address, TLS, metadata, message and settings", async () => {
    await setup(request({ proto: { files: [], importPaths: [], inherit: false, extra: "" } }));
    expect(tab(/Proto/).getAttribute("aria-selected")).toBe("true");
    expect(grpc.Describe).not.toHaveBeenCalled();
    expect(within(editor()).getByText("Add .proto files to choose a method.")).toBeTruthy();

    fireEvent.change(within(editor()).getByRole("textbox", { name: "Server address" }), { target: { value: "api.example:443" } });
    await act(settle);
    expect(sent().request.address).toBe("api.example:443");
    fireEvent.click(button("TLS"));
    await act(settle);
    expect(sent().request.settings.tls).toBe(true);
    expect(button("TLS").getAttribute("aria-pressed")).toBe("true");

    fireEvent.click(tab(/Settings/));
    fireEvent.click(within(editor()).getByRole("checkbox", { name: /Verify TLS certificates/ }));
    fireEvent.change(within(editor()).getByRole("spinbutton", { name: /Deadline/ }), { target: { value: "5000" } });
    await act(settle);
    expect(sent().request.settings).toEqual({ ...request().settings, tls: true, verifyTls: false, deadlineMs: 5000 });

    fireEvent.click(tab(/Metadata/));
    expect(within(editor()).getByRole("grid", { name: "Metadata" })).toBeTruthy();
    fireEvent.click(tab(/Message/));
    expect(within(editor()).getByRole("textbox", { name: "Message" }).textContent).toBe('{"name": "orders/1"}');
    expect(sent().request).toMatchObject({ extra: '{"x-note":"kept"}', metadata: request().metadata });
  });
});

const messageView = () => EditorView.findFromDOM(editor().querySelector(".cm-editor") as HTMLElement) as EditorView;
const typeMessage = (text: string) =>
  act(() => {
    const v = messageView();
    v.dispatch({ changes: { from: 0, to: v.state.doc.length, insert: text } });
  });
const waitForCheck = () => act(() => new Promise<void>((r) => setTimeout(r, MESSAGE_CHECK_DELAY_MS + 30)));
const pick = async (name: string) => {
  fireEvent.click(button(/Choose a service and method|shop\.v1\.OrderService|grpc\.health/));
  fireEvent.click(within(within(editor()).getByRole("listbox")).getByText(name));
  await act(settle);
};

describe("GrpcEditor message", () => {
  it("fills in the example when a method is chosen while the message is untouched", async () => {
    await setup(request({ message: "" }));
    await pick("GetOrder");
    expect(grpc.ExampleMessage).toHaveBeenCalledWith({
      files: ["proto/orders.proto"],
      importPaths: ["proto"],
      service: "shop.v1.OrderService",
      method: "GetOrder",
      message: "",
      output: false,
    });
    expect(sent().request).toMatchObject({ method: "GetOrder", message: exampleOf("GetOrder") });
    expect(messageView().state.doc.toString()).toBe(exampleOf("GetOrder"));

    // Still the previous method's example (reformatted): replaced.
    typeMessage('{"getorder":""}');
    await act(settle);
    await pick("WatchOrders");
    expect(sent().request).toMatchObject({ method: "WatchOrders", message: exampleOf("WatchOrders") });

    // Edited: kept.
    typeMessage('{"watchorders": "mine"}');
    await act(settle);
    await pick("OldOrder");
    expect(sent().request).toMatchObject({ method: "OldOrder", message: '{"watchorders": "mine"}' });
  });

  it("keeps a message written before any method was chosen", async () => {
    await setup();
    await pick("GetOrder");
    expect(grpc.ExampleMessage).not.toHaveBeenCalled();
    expect(sent().request).toMatchObject({ method: "GetOrder", message: '{"name": "orders/1"}' });
  });

  it("generates an example on demand", async () => {
    await setup();
    expect(button("Generate example").disabled).toBe(true);
    await pick("GetOrder");
    expect(within(editor()).getByText("shop.v1.GetOrderRequest")).toBeTruthy();
    fireEvent.click(button("Generate example"));
    await act(settle);
    expect(sent().request.message).toBe(exampleOf("GetOrder"));

    grpc.ExampleMessage.mockImplementation(() => Promise.reject({ kind: "not_found", code: "grpc.method_not_found", message: "Gone.", hint: "", details: {} }));
    fireEvent.click(button("Generate example"));
    await act(settle);
    expect(within(editor()).getByRole("status").textContent).toContain("Gone.");
  });

  it("validates the message in Go and marks the first problem", async () => {
    grpc.ValidateMessage.mockImplementation((input) =>
      cancellable(
        input.message.includes("nope")
          ? {
              type: "shop.v1.GetOrderRequest",
              valid: false,
              error: { offset: 1, byteOffset: 1, line: 1, column: 2, message: 'unknown field "nope"' },
            }
          : { type: "shop.v1.GetOrderRequest", valid: true, error: null },
      ),
    );
    await setup(request({ service: "shop.v1.OrderService", method: "GetOrder", message: '{"nope": 1}' }));
    await waitForCheck();
    expect(grpc.ValidateMessage).toHaveBeenLastCalledWith({
      files: ["proto/orders.proto"],
      importPaths: ["proto"],
      service: "shop.v1.OrderService",
      method: "GetOrder",
      message: '{"nope": 1}',
      output: false,
    });
    const status = within(editor()).getByRole("status");
    expect(status.textContent).toBe('Line 1, column 2unknown field "nope"');
    expect(editor().querySelector(".cm-errorMarker")?.textContent).toBe('"');

    fireEvent.click(within(status).getByRole("button", { name: "Line 1, column 2" }));
    expect(messageView().state.selection.main.head).toBe(1);

    typeMessage('{"name": "x"}');
    await waitForCheck();
    expect(within(editor()).queryByRole("status")).toBeNull();
    expect(editor().querySelector(".cm-errorMarker")).toBeNull();
  });

  it("autocompletes fields of the chosen method from Go (GRPC-17)", async () => {
    grpc.CompleteMessage.mockImplementation(() =>
      cancellable({
        type: "shop.v1.GetOrderRequest",
        context: "key",
        from: 1,
        to: 1,
        prefix: "",
        owner: "shop.v1.GetOrderRequest",
        items: [
          {
            label: "name",
            kind: "field",
            detail: "string",
            type: "",
            documentation: "The order name.",
            deprecated: false,
            number: 1,
            oneof: "",
            insertText: '"name"',
            suffix: ': ""',
            suffixCursor: 3,
            retrigger: false,
          },
        ],
        count: 1,
      }),
    );
    await setup(request({ service: "shop.v1.OrderService", method: "GetOrder", message: "{}" }));
    const v = messageView();
    act(() => {
      v.dispatch({ selection: { anchor: 1 } });
      startCompletion(v);
    });
    await act(() => vi.waitFor(() => expect(completionStatus(v.state)).toBe("active")));
    expect(grpc.CompleteMessage).toHaveBeenLastCalledWith({
      files: ["proto/orders.proto"],
      importPaths: ["proto"],
      service: "shop.v1.OrderService",
      method: "GetOrder",
      message: "{}",
      offset: 1,
      output: false,
    });
    expect(currentCompletions(v.state).map((c) => c.label)).toEqual(["name"]);
    expect(document.querySelector(".cm-tooltip-autocomplete")?.textContent).toContain("string");
  });

  it("does not autocomplete without a chosen method", async () => {
    await setup();
    const v = messageView();
    act(() => startCompletion(v));
    await act(settle);
    expect(grpc.CompleteMessage).not.toHaveBeenCalled();
  });

  it("does not validate without a chosen method", async () => {
    await setup();
    await waitForCheck();
    expect(grpc.ValidateMessage).not.toHaveBeenCalled();
  });
});

const AT = "2026-09-18T10:00:00.000000001Z";

function callEvent(over: Partial<CallEventDTO>): CallEventDTO {
  return {
    type: "request",
    at: AT,
    method: "",
    metadata: [],
    json: "",
    size: 0,
    index: 0,
    decodeError: "",
    raw: "",
    deadlineMs: 0,
    transport: "grpc",
    compression: "identity",
    clientStreaming: false,
    status: null,
    messages: 0,
    sent: 0,
    elapsedMs: 0,
    tests: null,
    ...over,
  };
}

let seq = 0;
function data(opId: string, items: CallEventDTO[]) {
  act(() => emit("grpc:op:data", { opId, seq: ++seq, items }));
}
function done(opId: string, status: "ok" | "error" | "cancelled", error: unknown = null) {
  act(() => emit("grpc:op:done", { opId, seq: ++seq, status, error, items: 0, batches: 0, elapsedMs: 15 }));
}

describe("GrpcEditor calls", () => {
  beforeEach(() => {
    seq = 0;
  });

  it("invokes a unary call and shows the messages, headers, trailers and status", async () => {
    const started = deferred<{ opId: string }>();
    grpc.Invoke.mockReturnValue(started.promise);
    await setup(
      request({
        service: "shop.v1.OrderService",
        method: "GetOrder",
        address: " localhost:50051 ",
        metadata: [
          { name: "x-request-id", value: "1", description: "", enabled: true, extra: "" },
          { name: "x-off", value: "2", description: "", enabled: false, extra: "" },
        ],
        settings: { ...request().settings, tls: true, verifyTls: false, deadlineMs: 5000 },
      }),
    );
    expect(within(response()).getByText("No response yet")).toBeTruthy();
    const invoke = button("Invoke");
    expect(invoke.disabled).toBe(false);
    fireEvent.click(invoke);
    expect(grpc.Invoke).toHaveBeenCalledWith({
      requestId: "req_order",
      files: ["proto/orders.proto"],
      importPaths: ["proto"],
      address: "localhost:50051",
      transport: "grpc",
      compression: "identity",
      tls: true,
      verifyTls: false,
      connectTimeoutMs: null,
      deadlineMs: 5000,
      service: "shop.v1.OrderService",
      method: "GetOrder",
      message: '{"name": "orders/1"}',
      metadata: [
        { name: "x-request-id", value: "1", enabled: true },
        { name: "x-off", value: "2", enabled: false },
      ],
    });
    expect(callStatus()).toBe("Calling shop.v1.OrderService/GetOrder…");
    expect(button("Cancel")).toBeTruthy();

    await act(async () => {
      started.resolve({ opId: "op_1" });
      await settle();
    });
    data("op_1", [
      callEvent({ type: "request", method: "/shop.v1.OrderService/GetOrder", json: '{\n  "name": "orders/1"\n}', size: 10, deadlineMs: 5000 }),
      callEvent({ type: "headers", metadata: [{ key: "content-type", value: "application/grpc", binary: false }] }),
      callEvent({ type: "message", json: '{\n  "id": "1"\n}', size: 3 }),
      callEvent({
        type: "end",
        status: { code: 0, name: "OK", message: "", details: "", rich: null },
        metadata: [{ key: "t-bin", value: "AAEC", binary: true }],
        messages: 1,
        elapsedMs: 12,
      }),
    ]);
    done("op_1", "ok");
    await act(settle);

    expect(callStatus()).toBe("OK · 1 message · 12 ms");
    expect(button("Invoke")).toBeTruthy();
    // The status line: status and code, time, messages, their size, call type and method.
    const line = response().querySelector("[data-response-status]") as HTMLElement;
    expect(line.querySelector("[data-call-tone]")?.textContent).toBe("OK0");
    expect(line.querySelector("[data-call-tone]")?.getAttribute("data-tone")).toBe("ok");
    expect(line.textContent).toContain("12 ms1 message3 Bunary");
    expect(line.textContent).toContain("GetOrder");
    // A unary call's response is pretty JSON in a read-only editor, with copy and search.
    const message = within(response()).getByRole("textbox", { name: "Response message" });
    expect(message.textContent).toBe('{  "id": "1"}');
    expect(within(response()).getByRole("button", { name: "Copy the response message" })).toBeTruthy();
    fireEvent.click(within(response()).getByRole("button", { name: "Search in the message" }));
    await act(settle);
    expect(response().querySelector(".cm-search")).toBeTruthy();
    const log = callLog();
    expect(log.textContent).toContain('"orders/1"');
    expect(log.textContent).toContain("#0");
    expect(log.textContent).toContain("OK");
    expect(responseTab(/Messages/).textContent).toBe("Messages1");

    fireEvent.click(responseTab(/Headers/));
    expect(within(response()).getByRole("table", { name: "Response headers" }).querySelector("tbody")?.textContent).toBe("content-typeapplication/grpc");
    fireEvent.click(responseTab(/Trailers/));
    expect(within(response()).getByRole("table", { name: "Trailers" }).querySelector("tbody")?.textContent).toBe("t-binAAECBase64");
    expect(responseTab(/Trailers/).textContent).toBe("Trailers1");
  });

  it("follows a server stream and cancels it", async () => {
    grpc.Invoke.mockResolvedValue({ opId: "op_2" });
    await setup(request({ service: "shop.v1.OrderService", method: "WatchOrders" }));
    fireEvent.change(within(editor()).getByRole("textbox", { name: "Server address" }), { target: { value: "localhost:1" } });
    fireEvent.keyDown(within(editor()).getByRole("textbox", { name: "Server address" }), { key: "Enter" });
    await act(settle);
    expect(grpc.Invoke).toHaveBeenCalledTimes(1);
    expect((grpc.Invoke.mock.calls[0]?.[0] as { address: string }).address).toBe("localhost:1");
    data("op_2", [callEvent({ type: "request" }), callEvent({ type: "message", json: "{}", index: 0 }), callEvent({ type: "message", json: "{}", index: 1 })]);
    await act(settle);
    expect(callStatus()).toBe("Calling shop.v1.OrderService/WatchOrders… 2 messages received");
    // A stream shows the log at once, with no switch; the status line runs with a Cancel of its own.
    expect(within(response()).queryByRole("button", { name: "Log" })).toBeNull();
    expect(response().querySelector("[data-call-tone]")?.textContent).toBe("Calling…");
    expect(response().querySelector("[data-response-status]")?.textContent).toContain("server stream");
    expect(within(response()).getByRole("button", { name: "Cancel" })).toBeTruthy();
    // Thousands of messages: the log renders only the rows in view.
    data(
      "op_2",
      Array.from({ length: 5000 }, (_, i) => callEvent({ type: "message", json: `{"n": ${i}}`, index: i + 2, size: 8 })),
    );
    await act(settle);
    const grid = callLog();
    expect(grid.querySelectorAll("[role='row']").length).toBeLessThan(100);
    expect(responseTab(/Messages/).textContent).toBe("Messages5002");
    expect(response().querySelector("[data-response-status]")?.textContent).toContain("5002 messages");

    fireEvent.click(button("Cancel"));
    await act(settle);
    expect(opsCancel).toHaveBeenCalledWith("op_2");
    data("op_2", [callEvent({ type: "end", status: { code: 1, name: "CANCELLED", message: "context canceled", details: "", rich: null }, messages: 2, elapsedMs: 40 })]);
    done("op_2", "cancelled");
    await act(settle);
    expect(callStatus()).toBe("Cancelled · 5002 messages · 40 ms");
    expect(response().querySelector("[data-call-tone]")?.textContent).toBe("Cancelled");
  });

  it("shows why a call failed or could not start", async () => {
    grpc.Invoke.mockRejectedValueOnce(
      Object.assign(new Error("x"), {
        cause: { kind: "invalid_argument", code: "grpc.invalid_message", message: "The message is not a valid shop.v1.GetOrderRequest.", hint: "", details: {}, causes: [] },
      }),
    );
    await setup(request({ service: "shop.v1.OrderService", method: "GetOrder" }));
    fireEvent.click(button("Invoke"));
    await act(settle);
    // Nothing reached the server: the error box instead of the view.
    const alert = within(response()).getByRole("alert");
    expect(alert.textContent).toContain("The message is not a valid shop.v1.GetOrderRequest.");
    expect(within(response()).queryByRole("tablist", { name: "Response" })).toBeNull();

    grpc.Invoke.mockResolvedValueOnce({ opId: "op_3" });
    fireEvent.click(button("Invoke"));
    await act(settle);
    data("op_3", [callEvent({ type: "request" }), callEvent({ type: "end", status: { code: 5, name: "NOT_FOUND", message: "no such order", details: "", rich: null }, elapsedMs: 3 })]);
    done("op_3", "error", { kind: "protocol", code: "grpctransport.status", message: "The server returned NOT_FOUND: no such order", hint: "", details: {}, causes: [] });
    await act(settle);
    expect(callStatus()).toBe("NOT_FOUND: no such order · 0 messages · 3 ms");
    const line = response().querySelector("[data-response-status]") as HTMLElement;
    expect(line.querySelector("[data-call-tone]")?.getAttribute("data-tone")).toBe("server");
    expect(line.textContent).toContain("no such order");
    expect(within(response()).getByText("The call returned no message: see the Status tab.")).toBeTruthy();
    // The log starts over with each call.
    expect(callLog().textContent).not.toContain("The message is not a valid");
  });

  it("shows the status with its decoded error details", async () => {
    grpc.Invoke.mockResolvedValue({ opId: "op_5" });
    await setup(request({ service: "shop.v1.OrderService", method: "GetOrder" }));
    fireEvent.click(button("Invoke"));
    await act(settle);
    fireEvent.click(responseTab(/Status/));
    expect(within(response()).getByText("No status yet.")).toBeTruthy();
    const detail = (over: Partial<{ typeUrl: string; type: string; known: boolean; json: string; error: string; raw: string }>) => ({
      typeUrl: "",
      type: "",
      known: true,
      json: "",
      error: "",
      raw: "",
      ...over,
    });
    data("op_5", [
      callEvent({ type: "request" }),
      callEvent({
        type: "end",
        status: {
          code: 3,
          name: "INVALID_ARGUMENT",
          message: "the order is invalid",
          details: "CAM=",
          rich: {
            code: 3,
            name: "INVALID_ARGUMENT",
            message: "the order is invalid",
            error: "",
            details: [
              detail({ typeUrl: "type.googleapis.com/google.rpc.BadRequest", type: "google.rpc.BadRequest", json: '{\n  "fieldViolations": []\n}' }),
              detail({ typeUrl: "type.googleapis.com/other.v1.Secret", type: "other.v1.Secret", known: false, raw: "CAE=" }),
              detail({ typeUrl: "type.googleapis.com/google.rpc.ErrorInfo", type: "google.rpc.ErrorInfo", error: "The detail is not a valid google.rpc.ErrorInfo.", raw: "Cgl4" }),
            ],
          },
        },
        elapsedMs: 5,
      }),
    ]);
    done("op_5", "error", { kind: "protocol", code: "grpctransport.status", message: "The server returned INVALID_ARGUMENT", hint: "", details: {}, causes: [] });
    await act(settle);

    expect(callStatus()).toBe("INVALID_ARGUMENT: the order is invalid · 3 error details · 0 messages · 5 ms");
    expect(responseTab(/Status/).textContent).toBe("Status3");
    const panel = within(response()).getByRole("region", { name: "Status" });
    expect(panel.querySelector("p")?.textContent).toBe("INVALID_ARGUMENT3the order is invalid");
    const items = within(within(panel).getByRole("list", { name: "Error details" })).getAllByRole("listitem");
    expect(items.map((li) => li.getAttribute("data-detail-type"))).toEqual(["google.rpc.BadRequest", "other.v1.Secret", "google.rpc.ErrorInfo"]);
    expect(items[0]?.textContent).toContain("BadRequest");
    expect(items[0]?.querySelector("pre")?.textContent).toBe('{\n  "fieldViolations": []\n}');
    expect(items[1]?.textContent).toContain("is not in the loaded .proto files");
    expect(items[1]?.querySelector("pre")?.textContent).toBe("CAE=Base64");
    expect(items[2]?.textContent).toContain("The detail is not a valid google.rpc.ErrorInfo.");

    // Details that are not a google.rpc.Status: the reason and the bytes.
    grpc.Invoke.mockResolvedValue({ opId: "op_6" });
    fireEvent.click(button("Invoke"));
    await act(settle);
    data("op_6", [
      callEvent({ type: "request" }),
      callEvent({
        type: "end",
        status: { code: 13, name: "INTERNAL", message: "", details: "AQID", rich: { code: 0, name: "OK", message: "", details: [], error: "The details could not be decoded." } },
      }),
    ]);
    done("op_6", "error");
    await act(settle);
    const panel2 = within(response()).getByRole("region", { name: "Status" });
    expect(panel2.textContent).toContain("The details could not be decoded.");
    expect(panel2.querySelector("pre")?.textContent).toBe("AQIDBase64");
    expect(responseTab(/Status/).textContent).toBe("Status");
  });

  it("shows a unary response that did not decode as Base64", async () => {
    grpc.Invoke.mockResolvedValue({ opId: "op_7" });
    await setup(request({ service: "shop.v1.OrderService", method: "GetOrder" }));
    fireEvent.click(button("Invoke"));
    await act(settle);
    fireEvent.click(responseTab(/Messages/));
    expect(within(response()).getByText("Waiting for the response…")).toBeTruthy();
    data("op_7", [
      callEvent({ type: "request" }),
      callEvent({ type: "message", json: "", decodeError: "The message is not a valid shop.v1.Order.", raw: "CgVh", size: 3 }),
      callEvent({ type: "message", json: "{}", index: 1 }),
    ]);
    await act(settle);
    expect(response().textContent).toContain("The message is not a valid shop.v1.Order. It is shown as Base64.");
    expect(response().textContent).toContain("The server sent 2 messages; the first is shown.");
    expect(response().querySelector("pre")?.textContent).toBe("CgVhBase64");
  });

  it("disables Invoke until a callable method and an address are set", async () => {
    await setup(request({ service: "shop.v1.OrderService", method: "" }));
    expect(button("Invoke").disabled).toBe(true);
    expect(button("Invoke").title).toBe("Choose a method");
    fireEvent.change(within(editor()).getByRole("textbox", { name: "Server address" }), { target: { value: "" } });
    await act(settle);
    expect(button("Invoke").title).toBe("Enter the server address");
  });

  it("saves the transport and compression and sends them with the call", async () => {
    grpc.Invoke.mockResolvedValue({ opId: "op_web" });
    await setup(request({ service: "shop.v1.OrderService", method: "GetOrder" }));
    fireEvent.click(tab(/Settings/));
    const transport = within(editor()).getByRole("combobox", { name: /Transport/ }) as HTMLSelectElement;
    const compression = within(editor()).getByRole("combobox", { name: /Compression/ }) as HTMLSelectElement;
    expect(transport.value).toBe("grpc");
    expect(compression.value).toBe("identity");
    fireEvent.change(transport, { target: { value: "grpc-web-text" } });
    fireEvent.change(compression, { target: { value: "gzip" } });
    await act(settle);
    expect(sent().request.settings).toMatchObject({ transport: "grpc-web-text", compression: "gzip" });
    expect(within(editor()).getByText(/application\/grpc-web-text\+proto/)).toBeTruthy();
    expect(within(editor()).getByText("gRPC-Web")).toBeTruthy();

    fireEvent.click(button("Invoke"));
    await act(settle);
    expect(grpc.Invoke).toHaveBeenCalledWith(expect.objectContaining({ transport: "grpc-web-text", compression: "gzip" }));

    // Back to the defaults, which are saved as missing values.
    fireEvent.change(transport, { target: { value: "grpc" } });
    fireEvent.change(compression, { target: { value: "identity" } });
    await act(settle);
    expect(sent().request.settings).toMatchObject({ transport: "", compression: "" });
  });

  it("does not start streams from the client over gRPC-Web", async () => {
    await setup(
      request({ service: "shop.v1.OrderService", method: "ImportOrders", settings: { ...request().settings, transport: "grpc-web" } }),
    );
    const start = button("Start stream");
    expect(start.disabled).toBe(true);
    expect(start.title).toMatch(/gRPC-Web cannot stream from the client/);
  });

  it("starts a bidi stream, sends messages, ends it and shows the timeline", async () => {
    grpc.Invoke.mockResolvedValue({ opId: "op_8" });
    grpc.SendMessage.mockResolvedValue({ index: 0, size: 10 });
    grpc.CloseSend.mockResolvedValue(undefined);
    await setup(request({ service: "shop.v1.OrderService", method: "Sync" }));
    expect(within(editor()).getByText(/A bidi call: Start stream opens it/)).toBeTruthy();
    expect(within(editor()).queryByRole("button", { name: "Invoke" })).toBeNull();
    fireEvent.click(button("Start stream"));
    await act(settle);
    expect(grpc.Invoke).toHaveBeenCalledTimes(1);
    data("op_8", [callEvent({ type: "request", method: "/shop.v1.OrderService/Sync", clientStreaming: true })]);
    await act(settle);
    expect(callStatus()).toBe("Calling shop.v1.OrderService/Sync… 0 sent");

    // Send message sends the editor's message; the editor and the response each have the controls.
    fireEvent.click(button("Send message"));
    await act(settle);
    expect(grpc.SendMessage).toHaveBeenCalledWith({ opId: "op_8", message: '{"name": "orders/1"}' });
    fireEvent.click(within(response()).getByRole("button", { name: "Send message" }));
    await act(settle);
    expect(grpc.SendMessage).toHaveBeenCalledTimes(2);
    data("op_8", [
      callEvent({ type: "sent", json: '{\n  "name": "orders/1"\n}', size: 10, index: 0 }),
      callEvent({ type: "headers", metadata: [{ key: "content-type", value: "application/grpc", binary: false }] }),
      callEvent({ type: "message", json: '{\n  "id": "1"\n}', size: 3, index: 0 }),
      callEvent({ type: "sent", json: '{\n  "name": "orders/1"\n}', size: 10, index: 1 }),
    ]);
    await act(settle);
    expect(callStatus()).toBe("Calling shop.v1.OrderService/Sync… 2 sent, 1 message received");
    // A stream opens on the timeline: sent and received messages in order.
    const rows = [...callLog().querySelectorAll("[role='row'][data-seq]")].map((r) => r.textContent ?? "");
    const at = (text: string) => rows.findIndex((r) => r.includes(text));
    expect(rows.length).toBe(5);
    expect(at("Stream opened")).toBe(0);
    expect(at("#0")).toBe(1);
    expect(at('"id"')).toBe(3);
    expect(at("#1")).toBe(4);

    // A refused send shows why, next to the controls.
    grpc.SendMessage.mockRejectedValueOnce(
      Object.assign(new Error("x"), {
        cause: { kind: "invalid_argument", code: "grpc.invalid_message", message: "The message is not a valid shop.v1.GetOrderRequest.", hint: "", details: {}, causes: [] },
      }),
    );
    fireEvent.click(button("Send message"));
    await act(settle);
    expect(within(editor()).getByRole("alert").textContent).toContain("The message is not a valid shop.v1.GetOrderRequest.");

    fireEvent.click(button("End stream"));
    await act(settle);
    expect(grpc.CloseSend).toHaveBeenCalledWith({ opId: "op_8" });
    expect(within(editor()).queryByRole("alert")).toBeNull();
    data("op_8", [callEvent({ type: "half-close", index: 2 })]);
    await act(settle);
    // After the half-close only Cancel is left.
    expect(within(editor()).queryByRole("button", { name: "Send message" })).toBeNull();
    expect(within(response()).queryByRole("button", { name: "End stream" })).toBeNull();
    expect(button("Cancel")).toBeTruthy();
    expect(callStatus()).toBe("Calling shop.v1.OrderService/Sync… 2 sent, 1 message received, request stream ended");

    data("op_8", [callEvent({ type: "end", status: { code: 0, name: "OK", message: "", details: "", rich: null }, messages: 1, sent: 2, elapsedMs: 30 })]);
    done("op_8", "ok");
    await act(settle);
    expect(callStatus()).toBe("OK · 1 message · 2 sent · 30 ms");
    expect(response().querySelector("[data-response-status]")?.textContent).toContain("2 sent");
    expect(callLog().textContent).toContain("Request stream ended after 2 messages");
    expect(button("Start stream")).toBeTruthy();
  });

  it("opens a client stream on the timeline, with a switch to its one response", async () => {
    grpc.Invoke.mockResolvedValue({ opId: "op_9" });
    await setup(request({ service: "shop.v1.OrderService", method: "ImportOrders" }));
    expect(within(editor()).getByText(/A client stream: Start stream opens it/)).toBeTruthy();
    fireEvent.click(button("Start stream"));
    await act(settle);
    data("op_9", [callEvent({ type: "request", clientStreaming: true })]);
    await act(settle);
    expect(callLog()).toBeTruthy();
    expect(within(response()).getByRole("button", { name: "Response" })).toBeTruthy();
    fireEvent.click(button("End stream"));
    await act(settle);
    expect(grpc.CloseSend).toHaveBeenCalledWith({ opId: "op_9" });
  });

  it("keeps the call when the tab is switched away and back", async () => {
    grpc.Invoke.mockResolvedValue({ opId: "op_4" });
    const stores = await setup(request({ service: "shop.v1.OrderService", method: "GetOrder" }));
    fireEvent.click(button("Invoke"));
    await act(settle);
    act(() => stores.tabs.dispatch({ type: "tabs/restore", order: [], activeId: null }));
    data("op_4", [callEvent({ type: "request" }), callEvent({ type: "message", json: "{}" })]);
    act(() => stores.tabs.dispatch({ type: "tabs/restore", order: ["req_order"], activeId: "req_order" }));
    await act(settle);
    expect(callStatus()).toBe("Calling shop.v1.OrderService/GetOrder… 1 message received");
  });
});

describe("lib/grpc", () => {
  it("builds the Invoke input and status texts", () => {
    const input = invokeInput(request({ service: "s.S", method: "M" }), { files: ["a.proto"], importPaths: [] });
    expect(input).toMatchObject({ files: ["a.proto"], importPaths: [], address: "localhost:50051", tls: false, verifyTls: null, deadlineMs: null });
    expect(input.metadata).toEqual([{ name: "x-request-id", value: "1", enabled: true }]);
    expect(["unary", "server_stream", "client_stream", "bidi", "other"].map(canInvoke)).toEqual([true, true, true, true, false]);
    expect(statusText({ code: 5, name: "NOT_FOUND", message: "gone", details: "", rich: null })).toBe("NOT_FOUND: gone");
    expect(statusText({ code: 0, name: "OK", message: "", details: "", rich: null })).toBe("OK");
    expect(messageInput({ files: ["a.proto"], importPaths: ["p"] }, "s.S", "M")).toEqual({
      files: ["a.proto"],
      importPaths: ["p"],
      service: "s.S",
      method: "M",
      message: "",
      output: false,
    });
    expect(sameJson('{\n  "a": [1]\n}', '{"a":[1]}')).toBe(true);
    expect(sameJson('{"a": 2}', '{"a": 1}')).toBe(false);
    expect(sameJson("{", "{}")).toBe(false);
  });

  it("labels method kinds and filters services", () => {
    expect(["unary", "client_stream", "server_stream", "bidi", "other"].map(methodKindLabel)).toEqual([
      "unary",
      "client stream",
      "server stream",
      "bidi",
      "other",
    ]);
    expect(filterServices(SERVICES, "").length).toBe(2);
    expect(filterServices(SERVICES, "HEALTH").map((s) => [s.fullName, s.methods?.length])).toEqual([["grpc.health.v1.Health", 1]]);
    expect(filterServices(SERVICES, "order").map((s) => s.methods?.map((m) => m.name))).toEqual([
      ["GetOrder", "WatchOrders", "ImportOrders", "Sync", "OldOrder"],
    ]);
    expect(filterServices(SERVICES, "sync").map((s) => s.methods?.map((m) => m.name))).toEqual([["Sync"]]);
    expect(findMethod(SERVICES, "shop.v1.OrderService", "Sync")?.method.kind).toBe("bidi");
    expect(findMethod(SERVICES, "shop.v1.OrderService", "Nope")).toBeNull();
    expect(diagnosticLocation({ code: "", severity: "error", file: "a.proto", path: "p/a.proto", line: 3, column: 4, message: "" })).toBe("a.proto:3:4");
    expect(diagnosticLocation({ code: "", severity: "error", file: "", path: "p/a.proto", line: 0, column: 0, message: "" })).toBe("p/a.proto");
  });
});
