import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";

const service = vi.hoisted(() => ({
  UpdateConfig: vi.fn<(id: string, config: unknown) => Promise<unknown>>(),
}));
const grpc = vi.hoisted(() => ({
  Describe: vi.fn<(input: { files: string[]; importPaths: string[]; reload: boolean }) => unknown>(),
  PickProtoFiles: vi.fn<() => unknown>(),
  PickImportPath: vi.fn<() => unknown>(),
  ExampleMessage: vi.fn<(input: unknown) => unknown>(),
  ValidateMessage: vi.fn<(input: unknown) => unknown>(),
  CompleteMessage: vi.fn<(input: unknown) => unknown>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/servers", () => ({
  ServersService: service,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/grpc", () => ({
  GRPCService: grpc,
}));

import { grpcurlListCommand, toGrpcMockConfigDTO } from "../../commands/grpcMock";
import { toLocalServer } from "../../commands/servers";
import type { DescribeResultDTO } from "../../lib/grpc";
import type { ServerDTO } from "../../lib/servers";
import { trafficToMessage } from "../../lib/serverTraffic";
import { createStores, serverKindBadge, StoreProvider, type Stores } from "../../stores";
import { resetFakeWails, settle } from "../../test/fakeWails";
import { grpcMockServerDTO, trafficDTO } from "../../test/servers";
import { GRPC_MOCK_CONFIG_DELAY_MS } from "./GrpcMockPane";
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

function described(): DescribeResultDTO {
  const method = (name: string, kind: string, output = "shop.v1.Order") => ({
    name,
    fullName: `shop.v1.Orders.${name}`,
    inputType: "shop.v1.GetOrderRequest",
    outputType: output,
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
        methods: [method("GetOrder", "unary"), method("ListOrders", "server_stream"), method("Cancel", "unary", "shop.v1.Ack")],
      },
    ],
    diagnostics: [],
    files: 1,
    errors: 0,
    cached: false,
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
  await act(settle);
}

async function flushEdits() {
  await act(async () => {
    vi.advanceTimersByTime(GRPC_MOCK_CONFIG_DELAY_MS + 10);
  });
}

function lastConfig() {
  const call = service.UpdateConfig.mock.lastCall;
  if (!call) throw new Error("UpdateConfig was not called");
  return (call[1] as { grpcMock: ReturnType<typeof toGrpcMockConfigDTO> }).grpcMock;
}

function lastMethods() {
  return lastConfig().methods ?? [];
}

beforeEach(() => {
  resetFakeWails();
  for (const fn of [...Object.values(service), ...Object.values(grpc)]) fn.mockReset();
  grpc.Describe.mockImplementation(() => cancellable(described()));
  grpc.ValidateMessage.mockImplementation(() => cancellable({ type: "shop.v1.Order", valid: true, error: null }));
  service.UpdateConfig.mockImplementation((_id, config) => Promise.resolve(grpcMockServerDTO({ config: config as ServerDTO["config"] })));
});

afterEach(() => {
  vi.useRealTimers();
});

describe("gRPC mock server view", () => {
  it("lists the methods of the files with whether each has a canned response", async () => {
    await renderPane(storesWith(grpcMockServerDTO()));
    expect(screen.getByRole("tablist", { name: "gRPC mock server" })).toBeTruthy();
    expect(grpc.Describe).toHaveBeenCalledWith({ files: ["proto/orders.proto"], importPaths: [], reload: false });
    expect(screen.getByText("grpcurl -plaintext localhost:52341 list")).toBeTruthy();
    const rows = within(screen.getByRole("list", { name: "Methods of shop.v1.Orders" })).getAllByRole("listitem");
    expect(rows.map((r) => r.textContent)).toEqual([
      "GetOrderunary→ shop.v1.Ordercanned response",
      "ListOrdersserver stream→ shop.v1.OrderUNIMPLEMENTED",
      "Cancelunary→ shop.v1.AckUNIMPLEMENTED",
    ]);
  });

  it("mocks a method with an example of its output type, then edits its status and stream", async () => {
    await renderPane(storesWith(grpcMockServerDTO()));
    vi.useFakeTimers();
    grpc.ExampleMessage.mockImplementation(() => cancellable({ type: "shop.v1.Order", json: '{\n  "id": ""\n}' }));
    fireEvent.click(screen.getByRole("button", { name: "Method ListOrders" }));
    fireEvent.click(screen.getByRole("button", { name: "Mock this method" }));
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    expect(grpc.ExampleMessage).toHaveBeenCalledWith({
      files: ["proto/orders.proto"],
      importPaths: [],
      service: "shop.v1.Orders",
      method: "ListOrders",
      message: "",
      output: true,
    });
    await flushEdits();
    expect(lastMethods().map((m) => [m.method, m.messages])).toEqual([
      ["GetOrder", ['{"id": "o-1"}']],
      ["ListOrders", ['{\n  "id": ""\n}']],
    ]);

    // A second message, an interval, a failing status with a trailer.
    fireEvent.click(screen.getByRole("button", { name: "Add message" }));
    fireEvent.change(screen.getByLabelText("Interval (ms)"), { target: { value: "250" } });
    fireEvent.change(screen.getByLabelText("Status"), { target: { value: "14" } });
    fireEvent.change(screen.getByLabelText("Status message"), { target: { value: "try later" } });
    fireEvent.click(screen.getByRole("button", { name: "Add trailer" }));
    fireEvent.change(screen.getByLabelText("Trailers 1 name"), { target: { value: "x-retry" } });
    fireEvent.change(screen.getByLabelText("Trailers 1 value"), { target: { value: "5" } });
    await flushEdits();
    const m = lastMethods()[1];
    expect(m).toMatchObject({
      service: "shop.v1.Orders",
      method: "ListOrders",
      status: 14,
      statusMessage: "try later",
      intervalMs: 250,
      trailers: [{ enabled: true, name: "x-retry", value: "5" }],
    });
    expect(m.messages).toHaveLength(2);
    expect(screen.getByRole("textbox", { name: "Message 2" })).toBeTruthy();

    // The response editor checks against the output type.
    await act(async () => {
      vi.advanceTimersByTime(400);
    });
    expect(grpc.ValidateMessage).toHaveBeenCalledWith(expect.objectContaining({ method: "ListOrders", output: true }));

    fireEvent.click(screen.getByRole("button", { name: "Remove canned response" }));
    await flushEdits();
    expect(lastMethods().map((x) => x.method)).toEqual(["GetOrder"]);
  });

  it("adds .proto files from the dialog and turns reflection off", async () => {
    await renderPane(storesWith(grpcMockServerDTO()));
    vi.useFakeTimers();
    grpc.PickProtoFiles.mockResolvedValue(["proto/users.proto", "proto/orders.proto"]);
    fireEvent.click(screen.getByRole("button", { name: "Add .proto files…" }));
    await act(async () => {
      await Promise.resolve();
      await Promise.resolve();
    });
    fireEvent.click(screen.getByLabelText(/Serve server reflection/));
    await flushEdits();
    expect(lastConfig()).toMatchObject({ files: ["proto/orders.proto", "proto/users.proto"], reflection: false });
    expect(grpc.Describe).toHaveBeenLastCalledWith({ files: ["proto/orders.proto", "proto/users.proto"], importPaths: [], reload: false });
  });

  it("shows calls in the traffic with their status", () => {
    const end = trafficDTO({
      direction: "out",
      kind: "response",
      summary: "NOT_FOUND: gone (call 2, 1 message received, 0 messages sent)",
      http: { method: "POST", target: "/shop.v1.Orders/GetOrder", proto: "HTTP/2.0", status: 200, headers: [], route: null, delayMs: 0 },
      grpc: {
        call: 2,
        method: "/shop.v1.Orders/GetOrder",
        kind: "unary",
        web: false,
        type: "",
        index: 0,
        code: 5,
        codeName: "NOT_FOUND",
        message: "gone",
        trailers: [{ name: "x-reason", value: "deleted" }],
      },
    });
    const msg = trafficToMessage(end);
    expect(msg.badge).toBe("NOT_FOUND");
    expect(msg.meta).toMatchObject({
      call: 2,
      method: "/shop.v1.Orders/GetOrder",
      "call type": "unary",
      status: "5 NOT_FOUND",
      "status message": "gone",
      "trailer x-reason": "deleted",
    });
    expect(trafficToMessage(trafficDTO({ kind: "request", grpc: { ...end.grpc!, codeName: "" } })).badge).toBe("CALL");
  });

  it("names the kind and suggests grpcurl", () => {
    expect(serverKindBadge("grpc_mock")).toBe("GRPC");
    const server = toLocalServer(grpcMockServerDTO());
    expect(grpcurlListCommand(server, "localhost:1")).toBe("grpcurl -plaintext localhost:1 list");
    const tls = toLocalServer(
      grpcMockServerDTO({ tls: { certificate: "generated", certFile: "", keyFile: "", pfxFile: "", hasPassphrase: false } }),
    );
    expect(grpcurlListCommand(tls, "localhost:1")).toBe("grpcurl -cacert ca.pem localhost:1 list");
  });
});
