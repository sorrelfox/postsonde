import { beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";

const service = vi.hoisted(() => ({
  Types: vi.fn<() => Promise<unknown>>(),
  List: vi.fn<() => Promise<unknown>>(),
  Traffic: vi.fn<(id: string) => Promise<unknown>>(),
  Clients: vi.fn<(id: string) => Promise<unknown>>(),
  Connections: vi.fn<(id: string) => Promise<unknown>>(),
  Captures: vi.fn<(id: string) => Promise<unknown>>(),
  Capture: vi.fn<(input: unknown) => Promise<unknown>>(),
  Replay: vi.fn<(input: unknown) => Promise<unknown>>(),
  UpdateConfig: vi.fn<(id: string, config: unknown) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/servers", () => ({
  ServersService: service,
}));
vi.mock(
  "../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/ops",
  async () => (await import("../../test/fakeWails")).fakeOpsBindings,
);

import { followServers, toLocalServer, toWebhookCapture } from "../../commands/servers";
import { defaultReplayUrl, webhookUrl } from "../../commands/webhookServer";
import { SERVERS_CAPTURES_EVENT, type CaptureDTO, type ServerDTO } from "../../lib/servers";
import { TrafficSources } from "../../lib/serverTraffic";
import { createStores, StoreProvider, type Stores } from "../../stores";
import { httpResponse } from "../../test/httpResponse";
import { emit, opsCancel, resetFakeWails, settle } from "../../test/fakeWails";
import { captureSummaryDTO, runningDTO, webhookServerDTO } from "../../test/servers";
import { ServerDetails } from "./ServerView";
import { WEBHOOK_CONFIG_DELAY_MS } from "./WebhookServerPane";

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

function captureDTO(over: Partial<CaptureDTO> = {}): CaptureDTO {
  return {
    serverId: "srv_1",
    summary: captureSummaryDTO(),
    proto: "HTTP/1.1",
    host: "127.0.0.1:52341",
    path: "/hooks/pay",
    query: [{ name: "id", value: "7" }],
    headers: [
      { name: "Host", value: "127.0.0.1:52341" },
      { name: "Content-Type", value: "application/json" },
      { name: "Stripe-Signature", value: "t=1,v1=abc" },
    ],
    body: httpResponse({ body: '{"amount":42}', bodyPretty: '{\n  "amount": 42\n}', prettyState: "formatted", size: 13 }),
    ...over,
  };
}

function storesWith(dto: ServerDTO): Stores {
  const stores = createStores();
  stores.servers.dispatch({ type: "servers/loaded", servers: [toLocalServer(dto)], types: [] });
  return stores;
}

function renderPane(stores: Stores) {
  const server = stores.servers.getState().byId.srv_1;
  return render(
    <StoreProvider stores={stores}>
      <ServerDetails server={server} />
    </StoreProvider>,
  );
}

function appError(code: string, message: string): Error {
  const err = new Error(message);
  (err as Error & { cause: unknown }).cause = { kind: "invalid_argument", code, message, hint: "", details: {}, causes: [] };
  return err;
}

beforeEach(() => {
  resetFakeWails();
  for (const fn of Object.values(service)) fn.mockReset();
});

describe("webhook receiver view (MOCK-08)", () => {
  it("gives the URL to send to and a replay URL with the request's path", () => {
    expect(webhookUrl(toLocalServer(webhookServerDTO()))).toBe(["http", "127.0.0.1:52341/"].join("://"));
    expect(webhookUrl(toLocalServer(webhookServerDTO({ state: "stopped", addresses: [] })))).toBeNull();
    const tls = { certificate: "generated", certFile: "", keyFile: "", pfxFile: "", hasPassphrase: false };
    expect(webhookUrl(toLocalServer(webhookServerDTO({ tls })))).toBe(["https", "127.0.0.1:52341/"].join("://"));
    expect(defaultReplayUrl("/hooks/pay?id=7")).toBe(["http", "localhost:3000/hooks/pay?id=7"].join("://"));
    expect(toLocalServer(webhookServerDTO()).webhook).toEqual({ status: 202, headers: [], body: "" });
  });

  it("loads the kept requests of webhook receivers and follows servers:captures", async () => {
    const stores = createStores();
    service.List.mockResolvedValue([webhookServerDTO(), runningDTO({ id: "srv_2" })]);
    service.Types.mockResolvedValue([]);
    service.Traffic.mockResolvedValue([]);
    service.Captures.mockResolvedValue([captureSummaryDTO()]);
    const stop = followServers(stores, new TrafficSources({ schedule: () => () => {} }));
    await settle();
    expect(service.Captures).toHaveBeenCalledTimes(1);
    expect(service.Captures).toHaveBeenCalledWith("srv_1");
    expect(stores.servers.getState().webhookCaptures.srv_1).toEqual([toWebhookCapture(captureSummaryDTO())]);

    emit(SERVERS_CAPTURES_EVENT, { serverId: "srv_1", captures: [captureSummaryDTO(), captureSummaryDTO({ seq: 6, method: "PUT" })] });
    expect(stores.servers.getState().webhookCaptures.srv_1.map((c) => c.seq)).toEqual([3, 6]);
    emit(SERVERS_CAPTURES_EVENT, { serverId: "srv_1", captures: null });
    expect(stores.servers.getState().webhookCaptures.srv_1).toEqual([]);

    // Kept requests outlive a stop.
    emit(SERVERS_CAPTURES_EVENT, { serverId: "srv_1", captures: [captureSummaryDTO()] });
    stores.servers.dispatch({ type: "servers/set", servers: [toLocalServer(webhookServerDTO({ state: "stopped", addresses: [] }))] });
    expect(stores.servers.getState().webhookCaptures.srv_1).toHaveLength(1);
    stop();
  });

  it("lists the requests newest first and shows the selected one in full", async () => {
    const stores = storesWith(webhookServerDTO());
    stores.servers.dispatch({
      type: "servers/webhookCaptures",
      id: "srv_1",
      captures: [captureSummaryDTO(), captureSummaryDTO({ seq: 6, method: "PUT", target: "/other", size: 0 })].map(toWebhookCapture),
    });
    service.Capture.mockImplementation((input) => {
      const seq = (input as { seq: number }).seq;
      return Promise.resolve(
        seq === 3
          ? captureDTO()
          : captureDTO({ summary: captureSummaryDTO({ seq: 6, method: "PUT", target: "/other", size: 0 }), query: [], body: httpResponse({ body: "", size: 0 }) }),
      );
    });
    renderPane(stores);

    expect(screen.getByRole("tablist", { name: "Webhook receiver" })).toBeTruthy();
    expect(screen.getByRole("tab", { name: "Requests2" }).getAttribute("aria-selected")).toBe("true");
    expect(screen.getByText(["http", "127.0.0.1:52341/"].join("://"))).toBeTruthy();
    const rows = within(screen.getByRole("list", { name: "Received requests" })).getAllByRole("listitem");
    expect(rows.map((r) => r.textContent)).toEqual([expect.stringContaining("PUT/other"), expect.stringContaining("POST/hooks/pay?id=7")]);

    // The newest request is shown until another one is selected.
    await act(settle);
    expect(service.Capture).toHaveBeenLastCalledWith({ serverId: "srv_1", seq: 6 });
    expect(screen.getByText("The request has no body.")).toBeTruthy();

    fireEvent.click(within(rows[1]).getByRole("button", { name: "POST /hooks/pay?id=7, request 3" }));
    expect(stores.servers.getState().webhookSelected.srv_1).toBe(3);
    await act(settle);
    expect(service.Capture).toHaveBeenLastCalledWith({ serverId: "srv_1", seq: 3 });
    const inspector = screen.getByRole("region", { name: "Request 3" });
    expect(within(inspector).getByRole("textbox", { name: "Request body" }).textContent).toContain('"amount": 42');
    fireEvent.click(within(inspector).getByRole("button", { name: "Raw" }));
    expect(within(inspector).getByRole("textbox", { name: "Request body" }).textContent).toContain('{"amount":42}');

    fireEvent.click(within(inspector).getByRole("tab", { name: "Headers3" }));
    const headers = within(inspector).getByRole("table", { name: "Request headers" });
    expect(within(headers).getByText("Stripe-Signature")).toBeTruthy();
    expect(within(headers).getByText("t=1,v1=abc")).toBeTruthy();

    fireEvent.click(within(inspector).getByRole("tab", { name: "Query1" }));
    const query = within(inspector).getByRole("table", { name: "Query parameters" });
    expect(within(query).getByText("id")).toBeTruthy();
  });

  it("says when a request is no longer kept", async () => {
    const stores = storesWith(webhookServerDTO());
    stores.servers.dispatch({ type: "servers/webhookCaptures", id: "srv_1", captures: [toWebhookCapture(captureSummaryDTO())] });
    service.Capture.mockRejectedValue(appError("servers.capture_not_found", "This request is no longer kept."));
    renderPane(stores);
    await act(settle);
    expect(screen.getByRole("alert").textContent).toBe("This request is no longer kept.");
  });

  it("replays a request to the chosen URL and shows the response", async () => {
    const stores = storesWith(webhookServerDTO());
    stores.servers.dispatch({ type: "servers/webhookCaptures", id: "srv_1", captures: [toWebhookCapture(captureSummaryDTO())] });
    service.Capture.mockResolvedValue(captureDTO());
    renderPane(stores);
    await act(settle);

    fireEvent.click(screen.getByRole("tab", { name: "Replay" }));
    const input = screen.getByLabelText("Replay to") as HTMLInputElement;
    expect(input.value).toBe(defaultReplayUrl("/hooks/pay?id=7"));
    const target = ["http", "localhost:8080/webhooks"].join("://");
    fireEvent.change(input, { target: { value: target } });
    expect(stores.servers.getState().webhookReplayUrl.srv_1).toBe(target);

    service.Replay.mockResolvedValue({ opId: "op_9" });
    fireEvent.click(screen.getByRole("button", { name: "Replay" }));
    await act(settle);
    expect(service.Replay).toHaveBeenCalledWith({ serverId: "srv_1", seq: 3, url: target });
    act(() => emit("http:op:progress", { opId: "op_9", seq: 1, done: 0, total: 0, message: "Waiting for the response" }));
    expect(screen.getByRole("status").textContent).toBe("Waiting for the response");
    expect(screen.getByRole("button", { name: "Cancel" })).toBeTruthy();
    act(() => {
      emit("http:op:data", { opId: "op_9", seq: 2, items: [httpResponse({ statusCode: 204, status: "204 No Content", body: "", size: 0 })] });
      emit("http:op:done", { opId: "op_9", seq: 3, status: "ok", error: null, items: 1, batches: 1, elapsedMs: 5 });
    });
    const response = screen.getByLabelText("Replay response");
    expect(response.querySelector("[data-response-status]")?.textContent).toContain("204 No Content");

    // A bad URL is refused at once.
    service.Replay.mockRejectedValue(appError("servers.invalid_replay_url", "Enter the URL to send the request to."));
    fireEvent.change(input, { target: { value: "localhost" } });
    fireEvent.click(screen.getByRole("button", { name: "Replay" }));
    await act(settle);
    expect(screen.getByRole("alert").textContent).toBe("Enter the URL to send the request to.");
    expect(opsCancel).not.toHaveBeenCalled();
  });

  it("does not offer to replay a request whose body was cut", async () => {
    const stores = storesWith(webhookServerDTO());
    stores.servers.dispatch({
      type: "servers/webhookCaptures",
      id: "srv_1",
      captures: [toWebhookCapture(captureSummaryDTO({ truncated: true, size: 20_000_000 }))],
    });
    service.Capture.mockResolvedValue(captureDTO());
    renderPane(stores);
    await act(settle);
    fireEvent.click(screen.getByRole("tab", { name: "Replay" }));
    expect(screen.getByRole("button", { name: "Replay" }).hasAttribute("disabled")).toBe(true);
    expect(screen.getByText(/cannot be replayed unchanged/)).toBeTruthy();
  });

  it("edits the response and sends it after a pause", async () => {
    vi.useFakeTimers();
    try {
      const stores = storesWith(webhookServerDTO());
      service.UpdateConfig.mockImplementation((_id, config) =>
        Promise.resolve(webhookServerDTO({ config: config as ServerDTO["config"] })),
      );
      renderPane(stores);
      fireEvent.click(screen.getByRole("tab", { name: "Response" }));
      fireEvent.change(screen.getByLabelText("Status"), { target: { value: "99" } });
      expect(screen.getByRole("alert").textContent).toMatch(/200 to 599/);
      fireEvent.change(screen.getByLabelText("Status"), { target: { value: "201" } });
      fireEvent.click(screen.getByRole("button", { name: "Add header" }));
      fireEvent.change(screen.getByLabelText("Header 1 name"), { target: { value: "Content-Type" } });
      fireEvent.change(screen.getByLabelText("Header 1 value"), { target: { value: "application/json" } });
      fireEvent.change(screen.getByLabelText("Body"), { target: { value: '{"ok":true}' } });
      await act(async () => {
        vi.advanceTimersByTime(WEBHOOK_CONFIG_DELAY_MS + 10);
      });
      expect(service.UpdateConfig).toHaveBeenCalledTimes(1);
      expect(service.UpdateConfig).toHaveBeenCalledWith("srv_1", {
        mockHttp: null,
        webSocket: null,
        webhook: { status: 201, headers: [{ enabled: true, name: "Content-Type", value: "application/json" }], body: '{"ok":true}' },
      });
      await act(async () => {
        await Promise.resolve();
      });
      expect(stores.servers.getState().byId.srv_1.webhook?.status).toBe(201);
    } finally {
      vi.useRealTimers();
    }
  });
});
