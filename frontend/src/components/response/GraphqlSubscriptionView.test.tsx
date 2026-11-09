import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const graphql = vi.hoisted(() => ({
  Subscribe: vi.fn<(input: unknown) => Promise<unknown>>(),
  StopSubscription: vi.fn<(input: unknown) => Promise<unknown>>(),
}));
const http = vi.hoisted(() => ({
  Send: vi.fn<(input: { requestId: string }) => Promise<unknown>>(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/graphql", () => ({
  GraphQLService: graphql,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http", () => ({
  HTTPService: http,
}));
vi.mock(
  "../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/ops",
  async () => (await import("../../test/fakeWails")).fakeOpsBindings,
);

import { graphqlSubscriptions, resetGraphqlSubscriptions } from "../../commands/graphqlSubscriptions";
import { httpSends, resetHttpSends } from "../../commands/httpSends";
import type { SubscriptionEventDTO } from "../../lib/graphqlSubscription";
import { createStores, StoreProvider } from "../../stores";
import { emit, resetFakeWails, settle } from "../../test/fakeWails";
import { HttpResponsePanel } from "./HttpResponsePanel";
import { graphqlPayloadOf, subscriptionStatusText } from "./GraphqlSubscriptionView";

const AT = "2026-09-19T10:00:00.000Z";

function ev(over: Partial<SubscriptionEventDTO>): SubscriptionEventDTO {
  return {
    at: AT,
    kind: "message",
    direction: "received",
    type: "",
    id: "",
    payload: "",
    size: 0,
    binary: false,
    errorCount: 0,
    note: "",
    url: "",
    subprotocol: "",
    close: null,
    error: null,
    ...over,
  };
}

function renderPanel() {
  return render(
    <StoreProvider stores={createStores()}>
      <div style={{ height: 600, display: "flex" }}>
        <HttpResponsePanel id="req_sub" graphql />
      </div>
    </StoreProvider>,
  );
}

const grid = () => screen.getByRole("grid", { name: "Subscription events" });
const status = () => document.querySelector("[data-subscription-status]")?.textContent;

beforeEach(() => {
  resetFakeWails();
  resetGraphqlSubscriptions();
  resetHttpSends();
  graphql.Subscribe.mockReset().mockResolvedValue({ opId: "op_sub" });
  graphql.StopSubscription.mockReset().mockResolvedValue(true);
  http.Send.mockReset().mockResolvedValue({ opId: "op_send" });
});

describe("graphqlPayloadOf", () => {
  it("reads received next and error payloads as GraphQL results", () => {
    const base = { seq: 0, time: 0, direction: "received" as const };
    expect(graphqlPayloadOf({ ...base, kind: "next", text: '{"data":{"a":1}}' })?.dataPresent).toBe(true);
    expect(graphqlPayloadOf({ ...base, kind: "error", text: '[{"message":"no"}]' })?.errors.map((e) => e.message)).toEqual(["no"]);
    expect(graphqlPayloadOf({ ...base, kind: "connection_ack", text: "{}" })).toBeNull();
    expect(graphqlPayloadOf({ ...base, direction: "sent", kind: "next", text: '{"data":1}' })).toBeNull();
  });
});

describe("GraphqlSubscriptionView", () => {
  it("shows the live event log, the status line and a next payload's data and errors", async () => {
    renderPanel();
    expect(screen.getByText("No response yet")).toBeTruthy();
    const sub = graphqlSubscriptions().get("req_sub");
    act(() => sub.start());
    await act(settle);
    expect(grid()).toBeTruthy();
    act(() => {
      emit("graphql.subscription:op:data", {
        opId: "op_sub",
        seq: 1,
        items: [
          ev({ kind: "connecting", direction: "", url: "wss://api.test/graphql" }),
          ev({ kind: "open", direction: "", subprotocol: "graphql-transport-ws" }),
          ev({ direction: "sent", type: "connection_init", payload: '{"authToken":"tok"}' }),
          ev({ type: "connection_ack" }),
          ev({ direction: "sent", type: "subscribe", id: "1", payload: '{"query":"subscription { n }"}' }),
          ev({ type: "next", id: "1", payload: '{"data":{"n":1}}', size: 16 }),
          ev({
            type: "next",
            id: "1",
            payload: '{"data":{"n":null},"errors":[{"message":"boom","locations":[{"line":1,"column":16}]}]}',
            size: 80,
            errorCount: 1,
          }),
        ],
      });
      sub.log.flush();
    });
    expect(status()).toBe("Subscribed · 2 events · 1 with errors");
    const rows = within(grid()).queryAllByRole("row").filter((r) => r.hasAttribute("data-seq"));
    expect(rows).toHaveLength(7);
    expect(rows[6]?.textContent).toContain("1 error");

    fireEvent.click(rows[6]!);
    const detail = screen.getByRole("region", { name: "Message 7" });
    expect(within(detail).getByText("1 error, partial data")).toBeTruthy();
    expect(within(detail).getByText("boom")).toBeTruthy();
    expect(within(detail).getByRole("button", { name: "Line 1, column 16" })).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: /Stop/ }));
    await act(settle);
    expect(graphql.StopSubscription).toHaveBeenCalledWith({ opId: "op_sub" });
    act(() => {
      emit("graphql.subscription:op:data", {
        opId: "op_sub",
        seq: 2,
        items: [ev({ kind: "closed", direction: "", close: { code: 1000, codeName: "normal closure", reason: "", wasClean: true, initiatedBy: "local" } })],
      });
      emit("graphql.subscription:op:done", { opId: "op_sub", seq: 3, status: "cancelled", error: null, items: 8, batches: 2, elapsedMs: 1500 });
    });
    expect(status()).toBe("Stopped · 2 events · 1 with errors · 1.50 s");
    expect(screen.getByText("1000 (normal closure)")).toBeTruthy();

    // A query sent afterwards takes the pane over.
    act(() => httpSends().get("req_sub").send());
    await act(settle);
    expect(screen.queryByRole("grid", { name: "Subscription events" })).toBeNull();
  });

  it("describes each state in one line", () => {
    const base = {
      connected: false,
      events: 0,
      errors: 0,
      receivedBytes: 0,
      close: null,
      error: null,
      startedAt: 1,
      elapsedMs: null,
      transport: "",
      operationType: "",
      result: null,
    };
    expect(subscriptionStatusText({ ...base, status: "starting" })).toBe("Connecting…");
    expect(subscriptionStatusText({ ...base, status: "ok", events: 3, elapsedMs: 20 })).toBe("Completed · 3 events · 20 ms");
    const error = { kind: "protocol" as const, code: "graphql.subscription_error", message: "The server rejected it.", hint: "", details: {}, causes: [] };
    expect(subscriptionStatusText({ ...base, status: "error", error })).toBe("Failed: The server rejected it. · 0 events");
    expect(subscriptionStatusText({ ...base, status: "running", connected: true, operationType: "query" })).toBe("Waiting for the result… · 0 events");
  });

  // GQL-35: a query run over WebSocket shows its result as a GraphQL
  // response, the messages on their own tab.
  it("shows the result of a query run over WebSocket", async () => {
    renderPanel();
    const sub = graphqlSubscriptions().get("req_sub");
    act(() => sub.start());
    await act(settle);
    act(() => {
      emit("graphql.subscription:op:data", {
        opId: "op_sub",
        seq: 1,
        items: [
          ev({ kind: "connecting", direction: "", url: "ws://api.test/graphql", transport: "graphql-ws", operationType: "query" }),
          ev({ kind: "open", direction: "", subprotocol: "graphql-ws" }),
          ev({ direction: "sent", type: "connection_init" }),
          ev({ type: "connection_ack" }),
          ev({ type: "ka" }),
          ev({ direction: "sent", type: "start", id: "1", payload: '{"query":"{ a }"}' }),
          ev({ type: "data", id: "1", payload: '{"data":{"a":1}}', size: 16 }),
        ],
      });
      sub.log.flush();
    });
    expect(status()).toBe("Waiting for the result… · 1 event");
    expect(screen.getByRole("tab", { name: "Response", selected: true })).toBeTruthy();
    expect(screen.getByText("legacy WebSocket")).toBeTruthy();
    act(() => {
      emit("graphql.subscription:op:data", {
        opId: "op_sub",
        seq: 2,
        items: [
          ev({ type: "complete", id: "1" }),
          ev({ kind: "result", direction: "", payload: '{\n  "data": {\n    "a": 1,\n    "b": [2]\n  }\n}', size: 30 }),
          ev({ kind: "closed", direction: "", close: { code: 1000, codeName: "normal closure", reason: "", wasClean: true, initiatedBy: "local" } }),
        ],
      });
      emit("graphql.subscription:op:done", { opId: "op_sub", seq: 3, status: "ok", error: null, items: 10, batches: 2, elapsedMs: 12 });
      sub.log.flush();
    });
    expect(status()).toBe("Completed · 1 event · 12 ms");
    const panel = screen.getByRole("tabpanel");
    expect(panel.textContent).toContain('"b"');
    expect(screen.getByRole("button", { name: /Send again/ })).toBeTruthy();

    fireEvent.click(screen.getByRole("tab", { name: "Messages" }));
    const rows = within(screen.getByRole("grid", { name: "Messages" }))
      .queryAllByRole("row")
      .filter((r) => r.hasAttribute("data-seq"));
    // The merged result is not a message of the log.
    expect(rows).toHaveLength(9);
    expect(rows[4]?.textContent).toContain("ka");
  });

  it("reads legacy error objects and SSE responses", () => {
    const base = { seq: 0, time: 0, direction: "received" as const };
    expect(graphqlPayloadOf({ ...base, kind: "error", text: '{"message":"no"}' })?.errors.map((e) => e.message)).toEqual(["no"]);
    expect(graphqlPayloadOf({ ...base, kind: "data", text: '{"data":{"a":1}}' })?.dataPresent).toBe(true);
    expect(graphqlPayloadOf({ ...base, kind: "response", text: '{"errors":[{"message":"bad"}]}' })?.errors).toHaveLength(1);
  });
});
