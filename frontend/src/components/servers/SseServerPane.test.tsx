import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { EditorView } from "@codemirror/view";

const service = vi.hoisted(() => ({
  Types: vi.fn<() => Promise<unknown>>(),
  List: vi.fn<() => Promise<unknown>>(),
  Traffic: vi.fn<(id: string) => Promise<unknown>>(),
  Subscribers: vi.fn<(id: string) => Promise<unknown>>(),
  Peers: vi.fn<(id: string) => Promise<unknown>>(),
  UpdateConfig: vi.fn<(id: string, config: unknown) => Promise<unknown>>(),
  SendEvent: vi.fn<(input: unknown) => Promise<unknown>>(),
  DisconnectSubscriber: vi.fn<(input: unknown) => Promise<unknown>>(),
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

import { followServers, toLocalServer, toSseSubscriber } from "../../commands/servers";
import { eventSourceSnippet, sseServerUrl } from "../../commands/sseServer";
import { SERVERS_SUBSCRIBERS_EVENT, type ServerDTO } from "../../lib/servers";
import { TrafficSources, trafficToMessage } from "../../lib/serverTraffic";
import { createStores, StoreProvider, type Stores } from "../../stores";
import { emit, resetFakeWails, settle } from "../../test/fakeWails";
import { runningDTO, sseServerDTO, sseSubscriberDTO, trafficDTO } from "../../test/servers";
import { ServerDetails } from "./ServerView";
import { SSE_SETTINGS_DELAY_MS } from "./SseServerPane";

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

function storesWith(dto: ServerDTO): Stores {
  const stores = createStores();
  stores.servers.dispatch({ type: "servers/loaded", servers: [toLocalServer(dto)], types: [] });
  return stores;
}

function renderPane(stores: Stores) {
  const server = stores.servers.getState().byId.srv_1;
  render(
    <StoreProvider stores={stores}>
      <ServerDetails server={server} />
    </StoreProvider>,
  );
}

/** Replaces the text of the composer's CodeMirror editor. */
function typeData(text: string) {
  const content = screen.getByRole("textbox", { name: "Event data" });
  const view = EditorView.findFromDOM(content.closest(".cm-editor") as HTMLElement);
  if (!view) throw new Error("no editor view");
  act(() => view.dispatch({ changes: { from: 0, to: view.state.doc.length, insert: text } }));
}

function appError(code: string, message: string, details: Record<string, string> = {}): Error {
  const err = new Error(message);
  (err as Error & { cause: unknown }).cause = { kind: "invalid_argument", code, message, hint: "", details, causes: [] };
  return err;
}

beforeEach(() => {
  resetFakeWails();
  for (const fn of Object.values(service)) fn.mockReset();
});

afterEach(() => {
  vi.useRealTimers();
});

describe("SSE server view", () => {
  it("builds the URL and the EventSource line", () => {
    const server = toLocalServer(sseServerDTO());
    expect(sseServerUrl(server)).toBe("http://127.0.0.1:52341/events");
    expect(sseServerUrl(toLocalServer(sseServerDTO({ tls: { certificate: "generated", certFile: "", keyFile: "", pfxFile: "", hasPassphrase: false } })))).toBe(
      "https://127.0.0.1:52341/events",
    );
    expect(sseServerUrl(toLocalServer(sseServerDTO({ state: "stopped", addresses: [] })))).toBeNull();
    expect(eventSourceSnippet("http://127.0.0.1:1/events", true)).toBe(
      'new EventSource("http://127.0.0.1:1/events", { withCredentials: true }).onmessage = (e) => console.log(e.data);',
    );
  });

  it("lists subscribers from servers:subscribers and loads them for running SSE servers", async () => {
    const stores = createStores();
    service.List.mockResolvedValue([sseServerDTO(), runningDTO({ id: "srv_2" })]);
    service.Types.mockResolvedValue([]);
    service.Traffic.mockResolvedValue([]);
    service.Subscribers.mockResolvedValue([sseSubscriberDTO()]);
    const stop = followServers(stores, new TrafficSources({ schedule: () => () => {} }));
    await settle();
    expect(service.Subscribers).toHaveBeenCalledTimes(1);
    expect(service.Subscribers).toHaveBeenCalledWith("srv_1");
    expect(service.Peers).not.toHaveBeenCalled();
    expect(stores.servers.getState().sseSubscribers.srv_1).toEqual([toSseSubscriber(sseSubscriberDTO())]);

    emit(SERVERS_SUBSCRIBERS_EVENT, { serverId: "srv_1", subscribers: [sseSubscriberDTO(), sseSubscriberDTO({ id: "2", lastEventId: "7" })] });
    expect(stores.servers.getState().sseSubscribers.srv_1.map((s) => s.lastEventId)).toEqual(["", "7"]);
    emit(SERVERS_SUBSCRIBERS_EVENT, { serverId: "srv_1", subscribers: null });
    expect(stores.servers.getState().sseSubscribers.srv_1).toEqual([]);
    stop();
  });

  it("shows subscribers with their Last-Event-ID, sends events to all or one, and disconnects one", async () => {
    const stores = storesWith(sseServerDTO());
    stores.servers.dispatch({
      type: "servers/sseSubscribers",
      id: "srv_1",
      subscribers: [sseSubscriberDTO(), sseSubscriberDTO({ id: "2", peer: "127.0.0.1:60002", lastEventId: "41", origin: "http://localhost:3000" })].map(
        toSseSubscriber,
      ),
    });
    renderPane(stores);

    expect(screen.getByRole("tablist", { name: "SSE server" })).toBeTruthy();
    expect(screen.getByRole("tab", { name: "Subscribers2" }).getAttribute("aria-selected")).toBe("true");
    expect(screen.getByText("http://127.0.0.1:52341/events")).toBeTruthy();
    const rows = within(screen.getByRole("list", { name: "Subscribers" })).getAllByRole("listitem");
    expect(rows).toHaveLength(2);
    expect(within(rows[1]).getByText(/Last-Event-ID 41/)).toBeTruthy();
    expect(within(rows[0]).getByText("↑3")).toBeTruthy();

    // An event with a type and retry, to everyone.
    fireEvent.change(screen.getByLabelText("Event"), { target: { value: "price" } });
    fireEvent.change(screen.getByLabelText("Retry"), { target: { value: "2000" } });
    typeData("line one\nline two {{$timestamp}}");
    service.SendEvent.mockResolvedValue({ sent: 2, failed: 0, id: "5" });
    fireEvent.click(screen.getByRole("button", { name: "Send to all" }));
    await act(settle);
    expect(service.SendEvent).toHaveBeenCalledWith({
      serverId: "srv_1",
      subscriberId: "",
      event: "price",
      data: "line one\nline two {{$timestamp}}",
      id: "",
      retryMs: 2000,
    });
    expect(screen.getByRole("status").textContent).toBe("Sent to 2 subscribers with ID 5.");

    // To the selected subscriber, with an ID.
    fireEvent.click(within(rows[1]).getByRole("button", { name: "Subscriber 2, 127.0.0.1:60002" }));
    expect(stores.servers.getState().sseSelected.srv_1).toBe("2");
    fireEvent.change(screen.getByLabelText("ID"), { target: { value: "custom" } });
    service.SendEvent.mockResolvedValue({ sent: 1, failed: 0, id: "custom" });
    fireEvent.click(screen.getByRole("button", { name: "Send to subscriber 2" }));
    await act(settle);
    expect(service.SendEvent).toHaveBeenLastCalledWith(expect.objectContaining({ subscriberId: "2", id: "custom" }));
    expect(screen.getByRole("status").textContent).toBe("Sent to 1 subscriber with ID custom.");

    // A bad retry is refused before Go; Go's refusal is shown too.
    fireEvent.change(screen.getByLabelText("Retry"), { target: { value: "soon" } });
    fireEvent.click(screen.getByRole("button", { name: "Send to all" }));
    await act(settle);
    expect(screen.getByText(/The retry must be a whole number/)).toBeTruthy();
    fireEvent.change(screen.getByLabelText("Retry"), { target: { value: "" } });
    service.SendEvent.mockRejectedValue(appError("sseserver.invalid_message", "The event type must be one line of at most 1024 bytes."));
    fireEvent.click(screen.getByRole("button", { name: "Send to all" }));
    await act(settle);
    expect(screen.getByText("The event type must be one line of at most 1024 bytes.")).toBeTruthy();

    service.DisconnectSubscriber.mockResolvedValue(undefined);
    fireEvent.click(within(rows[0]).getByRole("button", { name: "Disconnect" }));
    await act(settle);
    expect(service.DisconnectSubscriber).toHaveBeenCalledWith({ serverId: "srv_1", subscriberId: "1" });
  });

  it("disables sending without subscribers and says how to subscribe", () => {
    const stores = storesWith(sseServerDTO({ state: "stopped", addresses: [], boundPort: 0 }));
    renderPane(stores);
    expect(screen.getByText("Start the server to accept subscribers.")).toBeTruthy();
    expect(screen.getByText("No subscribers")).toBeTruthy();
    expect(screen.getByRole("button", { name: "Send to all" }).hasAttribute("disabled")).toBe(true);
  });

  it("sends settings and timers to Go once typing pauses, and shows a timer's problem under it", async () => {
    vi.useFakeTimers();
    const stores = storesWith(sseServerDTO());
    renderPane(stores);
    fireEvent.click(screen.getByRole("tab", { name: "Settings" }));
    service.UpdateConfig.mockImplementation((_id, config) => Promise.resolve(sseServerDTO({ config: config as ServerDTO["config"] })));

    fireEvent.change(screen.getByLabelText("CORS"), { target: { value: "any" } });
    fireEvent.change(screen.getByLabelText("Retry"), { target: { value: "3000" } });
    fireEvent.click(screen.getByRole("button", { name: "Add timer" }));
    fireEvent.change(screen.getByLabelText("Timer 1 interval"), { target: { value: "0.5" } });
    await act(async () => {
      vi.advanceTimersByTime(SSE_SETTINGS_DELAY_MS + 10);
    });
    expect(service.UpdateConfig).toHaveBeenCalledTimes(1);
    expect(service.UpdateConfig).toHaveBeenLastCalledWith("srv_1", {
      mockHttp: null,
      webSocket: null,
      webhook: null,
      sse: {
        path: "/events",
        allowOrigin: "*",
        keepAliveSec: 15,
        retryMs: 3000,
        autoId: true,
        timers: [{ enabled: true, event: "tick", data: '{"at": {{$timestamp}}}', id: "", intervalMs: 500 }],
      },
    });

    // A too short interval stays in the UI, under its timer.
    fireEvent.change(screen.getByLabelText("Timer 1 interval"), { target: { value: "0.01" } });
    await act(async () => {
      vi.advanceTimersByTime(SSE_SETTINGS_DELAY_MS + 10);
    });
    expect(service.UpdateConfig).toHaveBeenCalledTimes(1);
    const timers = screen.getByRole("list", { name: "Timers" });
    expect(within(timers).getByRole("alert").textContent).toMatch(/Timer 1: the interval/);

    // Go's refusal names the timer.
    service.UpdateConfig.mockImplementation(() =>
      Promise.reject(appError("sseserver.invalid_config", "Timer 1: The event type must be one line.", { field: "timers", timerIndex: "0", timerField: "event" })),
    );
    fireEvent.change(screen.getByLabelText("Timer 1 interval"), { target: { value: "2" } });
    await act(async () => {
      vi.advanceTimersByTime(SSE_SETTINGS_DELAY_MS + 10);
    });
    await act(async () => {
      await Promise.resolve();
    });
    expect(within(timers).getByRole("alert").textContent).toMatch(/event type must be one line/);
  });

  it("shows events, subscribers and Last-Event-ID in the traffic", () => {
    const sent = trafficDTO({
      kind: "message",
      direction: "out",
      peer: "",
      connId: 0,
      summary: "Event “tick” id 8 to 2 subscribers (timer 1)",
      text: "1790000000",
      size: 10,
      http: null,
      sse: { subscriberId: "", event: "tick", id: "8", retryMs: 0, recipients: 2, source: "timer 1", lastEventId: "", initiatedBy: "" },
    });
    expect(trafficToMessage(sent).meta).toEqual({
      summary: "Event “tick” id 8 to 2 subscribers (timer 1)",
      event: "tick",
      id: "8",
      recipients: 2,
      "sent by": "timer 1",
    });
    const resumed = trafficDTO({
      direction: "out",
      kind: "response",
      summary: "200 OK: subscriber 2 (Last-Event-ID “7”)",
      http: { method: "GET", target: "/events", proto: "HTTP/1.1", status: 200, headers: [], route: null, delayMs: 0 },
      sse: { subscriberId: "2", event: "", id: "", retryMs: 0, recipients: 0, source: "", lastEventId: "7", initiatedBy: "" },
    });
    const meta = trafficToMessage(resumed).meta ?? {};
    expect(meta.subscriber).toBe("2");
    expect(meta["last event ID"]).toBe("7");
  });
});
