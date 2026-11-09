import { act, fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import { emit, opsCancel, resetFakeWails, settle } from "../test/fakeWails";
import { StreamDemo } from "./StreamDemo";

const stream = vi.hoisted(() => vi.fn((_rate: number, _ms: number) => Promise.resolve({ opId: "op_demo" })));

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);
vi.mock(
  "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/ops",
  async () => (await import("../test/fakeWails")).fakeOpsBindings,
);
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/demo", () => ({
  DemoService: { Stream: stream },
}));

function value(term: string): string {
  const dt = screen.getByText(term, { selector: "dt" });
  return dt.nextElementSibling?.textContent ?? "";
}

beforeEach(() => {
  resetFakeWails();
  stream.mockClear();
});

describe("StreamDemo", () => {
  it("starts the stream, counts batches and cancels", async () => {
    render(<StreamDemo />);
    expect(screen.getByRole("status").textContent).toBe("Not started");

    await act(async () => {
      fireEvent.click(screen.getByRole("button", { name: "Start stream" }));
      await settle();
    });
    expect(stream).toHaveBeenCalledWith(2000, 5000);
    expect(screen.getByRole("status").textContent).toBe("Streaming…");

    act(() => {
      emit("demo:op:data", { opId: "op_demo", seq: 1, items: [{ index: 0, text: "item 0" }, { index: 1, text: "item 1" }] });
      emit("demo:op:progress", { opId: "op_demo", seq: 2, done: 2, total: 10000, message: "" });
      emit("demo:op:data", { opId: "op_demo", seq: 3, items: [{ index: 2, text: "item 2" }] });
    });
    expect(value("Items received")).toBe("3");
    expect(value("Batches (UI updates)")).toBe("2");
    expect(value("Progress")).toBe("2 / 10000");
    expect(value("Last item")).toBe("item 2");

    // The items reach the message log on the next animation frame, after the "connected" message.
    const log = screen.getByRole("grid", { name: "Demo stream messages" });
    await waitFor(() => expect(within(log).getAllByRole("row")).toHaveLength(5));
    const kinds = within(log)
      .getAllByRole("row")
      .slice(1)
      .map((row) => within(row).getAllByRole("gridcell")[2].textContent);
    expect(kinds).toEqual(["connected", "text", "text", "text"]);

    fireEvent.click(screen.getByRole("button", { name: "Cancel stream" }));
    expect(opsCancel).toHaveBeenCalledWith("op_demo");
    act(() =>
      emit("demo:op:done", { opId: "op_demo", seq: 4, status: "cancelled", error: null, items: 3, batches: 2, elapsedMs: 80 }),
    );
    expect(screen.getByRole("status").textContent).toBe("Cancelled");
    expect((screen.getByRole("button", { name: "Start stream" }) as HTMLButtonElement).disabled).toBe(false);
  });
});
