import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { useState } from "react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const ws = vi.hoisted(() => ({ CheckAssertions: vi.fn<(input: { connId: string }) => Promise<unknown>>() }));
const grpc = vi.hoisted(() => ({ CheckAssertions: vi.fn<(input: { opId: string }) => Promise<unknown>>() }));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/websocket", () => ({ WebSocketService: ws }));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/grpc", () => ({ GRPCService: grpc }));

import { resetAssertionJumps } from "../../commands/assertionJump";
import { resetHttpSends } from "../../commands/httpSends";
import { resetStreamTests, streamEnded, streamRef, streamStarted, useStreamTests } from "../../commands/streamTests";
import { describeAssertion, describeResult, newAssertion, withScope, type AssertionDTO, type StreamTestResultDTO } from "../../lib/assertions";
import { createStores, StoreProvider, type RequestData } from "../../stores";
import { settle } from "../../test/fakeWails";
import { AssertionsEditor } from "../editor/AssertionsEditor";
import { StreamTests } from "./StreamTests";

const result = (over: Partial<StreamTestResultDTO>): StreamTestResultDTO => ({
  index: 0,
  target: "message_count",
  expr: "",
  op: "equals",
  expected: "3",
  messages: "",
  messageIndex: 0,
  outcome: "passed",
  actual: "3",
  exists: true,
  message: "Message count equals 3: it was 3.",
  code: "",
  schemaErrors: [],
  ...over,
});

beforeEach(() => {
  resetStreamTests();
  resetHttpSends();
  resetAssertionJumps();
  ws.CheckAssertions.mockReset();
  grpc.CheckAssertions.mockReset();
});

describe("stream assertions (TEST-08)", () => {
  it("describes scoped assertions and results", () => {
    expect(describeAssertion({ ...newAssertion({ target: "jsonpath", expr: "$.done", expected: "true" }), messages: "last" })).toBe(
      "Last message: JSONPath $.done equals true",
    );
    expect(describeAssertion({ ...newAssertion({ target: "body", op: "exists", expected: "" }), messages: "nth", index: 3 })).toBe("Message 3: Body exists");
    expect(describeResult(result({ messages: "nth", messageIndex: 2, index: 5, target: "body", op: "contains", expected: "x" }))).toBe(
      "Message 2: Body contains x",
    );
    expect(describeResult(result({}))).toBe("Message count equals 3");
  });

  it("changes a row's scope, keeping a target the scope offers", () => {
    const count = newAssertion({ target: "message_count", op: "gte", expected: "1" });
    // Into a scope: the message count is the stream's, so the target becomes the body.
    expect(withScope(count, "any")).toMatchObject({ messages: "any", index: 0, target: "body" });
    const nth = withScope(newAssertion({ target: "jsonpath", expr: "$.id" }), "nth");
    expect(nth).toMatchObject({ messages: "nth", index: 1, target: "jsonpath", expr: "$.id" });
    // Out of one: the status is a target of the whole stream again.
    expect(withScope({ ...nth, target: "jsonpath" }, "")).toMatchObject({ messages: "", index: 0, target: "jsonpath" });
  });

  it("shows the results of the end of the stream, and checks again on demand", async () => {
    const stores = createStores();
    render(
      <StoreProvider stores={stores}>
        <StreamTests id="feed" />
      </StoreProvider>,
    );
    const check = () => screen.getByRole("button", { name: "Check now" }) as HTMLButtonElement;
    expect(check().disabled).toBe(true);
    act(() => {
      streamStarted("feed", "websocket");
      streamRef("feed", "ws_1");
    });
    expect(check().disabled).toBe(false);
    expect(screen.getByText(/Checked when the stream ends/)).toBeTruthy();

    act(() => streamEnded("feed", [result({}), result({ index: 1, messages: "last", target: "body", op: "equals", expected: "bye", outcome: "failed", actual: '"hi"', message: 'Expected the last message\'s body to equal "bye", but it was "hi".' })]));
    expect(screen.getByText("Checked when the stream ended.")).toBeTruthy();
    const items = within(screen.getByRole("list", { name: "Test results" })).getAllByRole("listitem");
    expect(items.map((li) => li.getAttribute("data-outcome"))).toEqual(["passed", "failed"]);
    expect(within(items[1] as HTMLElement).getByRole("button", { name: 'Last message: Body equals bye' })).toBeTruthy();

    // Jumping to a result opens the Tests sub-tab.
    fireEvent.click(within(items[1] as HTMLElement).getByRole("button", { name: 'Last message: Body equals bye' }));
    expect(stores.drafts.getState().subTabs.feed).toBe("tests");

    ws.CheckAssertions.mockResolvedValue([result({ expected: "4", outcome: "failed", actual: "3", message: "Expected message count to equal 4, but it was 3." })]);
    fireEvent.click(check());
    await act(settle);
    expect(ws.CheckAssertions).toHaveBeenCalledWith({ connId: "ws_1" });
    expect(screen.getByText("Checked on demand, with the assertions as they are now.")).toBeTruthy();
    expect(screen.getByText("Expected message count to equal 4, but it was 3.")).toBeTruthy();

    // A failed check says why, and keeps the results.
    ws.CheckAssertions.mockRejectedValue({ kind: "not_found", code: "streamcheck.not_found", message: "That stream is no longer kept: run it again to check its messages." });
    fireEvent.click(check());
    await act(settle);
    expect(screen.getByRole("alert").textContent).toContain("That stream is no longer kept");
  });

  it("checks a gRPC call by its operation", async () => {
    act(() => {
      streamStarted("rpc", "grpc");
      streamRef("rpc", "op_9");
    });
    grpc.CheckAssertions.mockResolvedValue(null);
    render(
      <StoreProvider stores={createStores()}>
        <StreamTests id="rpc" />
      </StoreProvider>,
    );
    fireEvent.click(screen.getByRole("button", { name: "Check now" }));
    await act(settle);
    expect(grpc.CheckAssertions).toHaveBeenCalledWith({ opId: "op_9" });
    // No assertions: the empty list's text.
    expect(screen.getByText(/No assertions were checked/)).toBeTruthy();
  });
});

function StreamHarness({ initial, edits }: { initial: AssertionDTO[]; edits: AssertionDTO[][] }) {
  const [list, setList] = useState(initial);
  const tests = useStreamTests("feed").tests;
  return (
    <AssertionsEditor
      id="feed"
      assertions={list}
      readOnly={false}
      stream
      results={tests}
      edit={(patch: RequestData) => {
        const next = patch.assertions as AssertionDTO[];
        edits.push(next);
        setList(next);
      }}
    />
  );
}

describe("AssertionsEditor on a stream (TEST-08)", () => {
  it("picks the messages a row checks, the message number and the targets they offer", () => {
    const edits: AssertionDTO[][] = [];
    render(
      <StoreProvider stores={createStores()}>
        <StreamHarness initial={[]} edits={edits} />
      </StoreProvider>,
    );
    expect(screen.getByText(/check the messages the stream receives/)).toBeTruthy();
    fireEvent.click(screen.getByRole("button", { name: "Add assertion" }));
    expect(edits[edits.length - 1]).toEqual([newAssertion({ target: "message_count", op: "gte", expected: "1" })]);

    const scope = screen.getByRole("combobox", { name: "Messages checked, assertion 1" }) as HTMLSelectElement;
    expect(scope.value).toBe("");
    const target = () => screen.getByRole("combobox", { name: "Target, assertion 1" }) as HTMLSelectElement;
    expect([...target().options].map((o) => o.value)).toContain("message_count");
    expect([...target().options].map((o) => o.value)).not.toContain("cookie");

    fireEvent.change(scope, { target: { value: "nth" } });
    expect(edits[edits.length - 1]?.[0]).toMatchObject({ messages: "nth", index: 1, target: "body" });
    expect([...target().options].map((o) => o.value)).not.toContain("status");
    fireEvent.change(screen.getByRole("spinbutton", { name: "Message number, assertion 1" }), { target: { value: "4" } });
    expect(edits[edits.length - 1]?.[0]).toMatchObject({ messages: "nth", index: 4 });

    // The latest stream's result marks the row while it checks the same thing.
    act(() => streamEnded("feed", [result({ target: "body", op: "gte", messages: "nth", messageIndex: 4, outcome: "failed", message: "Expected message 4's body to equal 200, but there were only 3 messages." })]));
    expect(screen.getByRole("img", { name: /Failed in the last send: Expected message 4's body/ })).toBeTruthy();
  });
});
