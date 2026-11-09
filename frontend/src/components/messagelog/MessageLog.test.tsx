import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { Profiler } from "react";
import { describe, expect, it, vi } from "vitest";
import { MessageSource, type LogMessageInput } from "../../lib/messageSource";
import { manualFrames } from "../../test/manualFrames";
import { MESSAGE_FALLBACK_VIEWPORT, MESSAGE_OVERSCAN, MESSAGE_ROW_HEIGHT, MessageLog } from "./MessageLog";

const text = (i: number, extra: Partial<LogMessageInput> = {}): LogMessageInput => ({
  time: Date.UTC(2026, 8, 16, 10, 0, 0) + i,
  direction: i % 2 === 0 ? "received" : "sent",
  kind: "text",
  text: `message ${i}`,
  ...extra,
});

function setup(options: { max?: number; messages?: LogMessageInput[] } = {}) {
  const frames = manualFrames();
  const source = new MessageSource({ maxMessages: options.max, schedule: frames.schedule });
  if (options.messages) {
    source.append(options.messages);
    frames.frame();
  }
  let renders = 0;
  const view = render(
    <div style={{ height: 400, display: "flex" }}>
      <Profiler id="log" onRender={() => renders++}>
        <MessageLog source={source} label="Socket messages" />
      </Profiler>
    </div>,
  );
  const append = (messages: LogMessageInput[]) =>
    act(() => {
      source.append(messages);
      frames.frame();
    });
  return { source, frames, view, append, renders: () => renders };
}

const grid = () => screen.getByRole("grid", { name: "Socket messages" });
const dataRows = () => within(grid()).queryAllByRole("row").filter((r) => r.hasAttribute("data-seq"));
const row = (seq: number) => grid().querySelector<HTMLElement>(`[data-seq="${seq}"]`);
const scroller = () => within(grid()).getAllByRole("rowgroup")[1];
const bound = Math.ceil(MESSAGE_FALLBACK_VIEWPORT / MESSAGE_ROW_HEIGHT) + 2 * MESSAGE_OVERSCAN + 2;

function scrollTo(top: number) {
  const el = scroller();
  el.scrollTop = top;
  fireEvent.scroll(el);
}

describe("MessageLog", () => {
  it("shows time, direction, kind, size and a one-line preview", () => {
    setup({
      messages: [
        text(0, { text: '{"a": 1,\n "b": "x"}' }),
        { time: 5, direction: "received", kind: "binary", data: new Uint8Array([0xde, 0xad, 0xbe, 0xef]) },
        { time: 6, direction: "system", kind: "closed", badge: "1000" },
      ],
    });
    const [json, binary, closed] = dataRows();
    const cells = within(json).getAllByRole("gridcell").map((c) => c.textContent);
    expect(cells[1]).toBe("↓Received");
    expect(cells.slice(2)).toEqual(["text", "19 B", '{"a": 1, "b": "x"}']);
    expect(cells[0]).toMatch(/^\d\d:\d\d:\d\d\.000$/);
    expect(within(binary).getAllByRole("gridcell")[4].textContent).toBe("de ad be ef");
    expect(within(closed).getAllByRole("gridcell").map((c) => c.textContent).slice(1, 4)).toEqual(["•System", "closed1000", ""]);
    expect(grid().getAttribute("aria-rowcount")).toBe("4");
  });

  it("keeps the DOM bounded with 50,000 messages", () => {
    const messages = Array.from({ length: 50_000 }, (_, i) => text(i));
    const started = performance.now();
    setup({ messages });
    const elapsed = performance.now() - started;
    const sizer = scroller().firstElementChild as HTMLElement;
    expect(sizer.style.height).toBe(`${50_000 * MESSAGE_ROW_HEIGHT}px`);
    expect(dataRows().length).toBeLessThanOrEqual(bound);
    // Following: the last message is rendered.
    expect(row(49_999)).not.toBeNull();
    expect(row(0)).toBeNull();
    expect(elapsed).toBeLessThan(2000);
  });

  it("renders once per frame however many messages arrive", () => {
    const { source, frames, renders } = setup();
    const before = renders();
    // 10,000 messages in a second arrive as many small appends between frames.
    act(() => {
      for (let i = 0; i < 10_000; i++) source.append(text(i));
    });
    expect(renders()).toBe(before);
    expect(frames.scheduled).toBe(1);
    act(() => frames.frame());
    expect(renders() - before).toBeLessThanOrEqual(2);
    expect(dataRows().length).toBeLessThanOrEqual(bound);
    expect(screen.getByText("10,000 messages")).toBeTruthy();
  });

  it("drops the oldest messages and shows how many", () => {
    const { append } = setup({ max: 100 });
    append(Array.from({ length: 130 }, (_, i) => text(i)));
    scrollTo(0);
    const marker = within(grid()).getAllByRole("row")[1];
    expect(marker.textContent).toBe("30 older messages dropped (the log keeps the last 100)");
    expect(grid().getAttribute("aria-rowcount")).toBe("102");
    expect(row(30)).not.toBeNull();
    expect(row(29)).toBeNull();
  });

  it("follows new messages until scrolled up, then offers to jump to the latest", () => {
    const { append } = setup();
    append(Array.from({ length: 1000 }, (_, i) => text(i)));
    expect(screen.getByText("Following")).toBeTruthy();
    expect(row(999)).not.toBeNull();

    // Scroll up: following pauses and new rows do not move the view.
    fireEvent.wheel(scroller(), { deltaY: -100 });
    scrollTo(100 * MESSAGE_ROW_HEIGHT);
    expect(screen.getByText("Paused")).toBeTruthy();
    append(Array.from({ length: 50 }, (_, i) => text(1000 + i)));
    expect(scroller().scrollTop).toBe(100 * MESSAGE_ROW_HEIGHT);
    expect(row(100)).not.toBeNull();
    expect(row(1049)).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Jump to latest (50 new)" }));
    expect(screen.getByText("Following")).toBeTruthy();
    expect(row(1049)).not.toBeNull();
    append([text(1050)]);
    expect(row(1050)).not.toBeNull();
    expect(scroller().scrollTop).toBe(1051 * MESSAGE_ROW_HEIGHT - MESSAGE_FALLBACK_VIEWPORT);
    expect(screen.queryByRole("button", { name: /Jump to latest/ })).toBeNull();
  });

  it("keeps a paused view in place while old messages are dropped", () => {
    const { append } = setup({ max: 1000 });
    append(Array.from({ length: 1000 }, (_, i) => text(i)));
    fireEvent.wheel(scroller(), { deltaY: -100 });
    scrollTo(500 * MESSAGE_ROW_HEIGHT);
    append(Array.from({ length: 100 }, (_, i) => text(1000 + i)));
    // 100 rows left the top and the marker row appeared: the same message stays at the top.
    expect(scroller().scrollTop).toBe((500 - 100 + 1) * MESSAGE_ROW_HEIGHT);
    expect(row(500)?.style.top).toBe(`${(500 - 100 + 1) * MESSAGE_ROW_HEIGHT}px`);
  });

  it("searches case-insensitively, highlights matches and walks them", () => {
    setup({
      messages: Array.from({ length: 500 }, (_, i) => text(i, { text: i % 100 === 7 ? `Order ${i} filled` : `tick ${i}` })),
    });
    const search = screen.getByRole("textbox", { name: "Search messages" });
    fireEvent.change(search, { target: { value: "order" } });
    expect(screen.getByText("5 matches")).toBeTruthy();

    fireEvent.keyDown(search, { key: "Enter" });
    expect(screen.getByText("1 of 5")).toBeTruthy();
    expect(row(7)?.className).toMatch(/current/);
    expect(row(7)?.querySelector("mark")?.textContent).toBe("Order");
    expect(screen.getByText("Paused")).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: "Next match" }));
    expect(screen.getByText("2 of 5")).toBeTruthy();
    expect(row(107)?.className).toMatch(/current/);
    fireEvent.keyDown(search, { key: "Enter", shiftKey: true });
    fireEvent.keyDown(search, { key: "Enter", shiftKey: true });
    expect(screen.getByText("5 of 5")).toBeTruthy();
    expect(row(407)).not.toBeNull();

    fireEvent.click(screen.getByRole("button", { name: "Only matching" }));
    expect(dataRows().map((r) => r.dataset.seq)).toEqual(["7", "107", "207", "307", "407"]);
    expect(screen.getByText("5 of 500 messages")).toBeTruthy();

    fireEvent.keyDown(search, { key: "Escape" });
    expect((search as HTMLInputElement).value).toBe("");
    expect(screen.getByText("500 messages")).toBeTruthy();
  });

  it("flags an invalid regular expression", () => {
    setup({ messages: [text(0)] });
    fireEvent.click(screen.getByRole("button", { name: "Regular expression" }));
    const search = screen.getByRole("textbox", { name: "Search messages" });
    fireEvent.change(search, { target: { value: "mess(age" } });
    expect(search.getAttribute("aria-invalid")).toBe("true");
    const error = document.getElementById(search.getAttribute("aria-describedby") ?? "");
    expect(error?.textContent).toMatch(/^Invalid regular expression: /);
    expect(dataRows()).toHaveLength(1);
    fireEvent.change(search, { target: { value: "mess(age)? 0$" } });
    expect(search.getAttribute("aria-invalid")).toBeNull();
    expect(screen.getByText("1 match")).toBeTruthy();
  });

  it("filters by direction and kind, including messages that arrive later", () => {
    const { append } = setup({
      messages: [text(0), text(1), { time: 2, direction: "system", kind: "connected" }, { time: 3, direction: "received", kind: "ping" }],
    });
    fireEvent.click(within(screen.getByRole("group", { name: "Direction" })).getByRole("button", { name: /Sent/ }));
    expect(dataRows().map((r) => r.dataset.seq)).toEqual(["0", "2", "3"]);
    const kind = screen.getByRole("combobox", { name: "Kind" });
    expect(within(kind).getAllByRole("option").map((o) => o.textContent)).toEqual(["All kinds", "text", "connected", "ping"]);
    fireEvent.change(kind, { target: { value: "text" } });
    expect(dataRows().map((r) => r.dataset.seq)).toEqual(["0"]);
    append([text(4), text(5), { time: 6, direction: "received", kind: "ping" }]);
    expect(dataRows().map((r) => r.dataset.seq)).toEqual(["0", "4"]);
    expect(screen.getByText("2 of 7 messages")).toBeTruthy();
  });

  it("moves through rows with the keyboard and opens the detail pane", () => {
    setup({
      messages: [
        text(0, { text: '{"op":"subscribe","ids":[1,2]}', meta: { opcode: "text", fin: true, masked: true } }),
        { time: 1, direction: "received", kind: "binary", data: new TextEncoder().encode("Hi!\u0000") },
        text(2),
      ],
    });
    // One row is in the Tab order.
    expect(dataRows().filter((r) => r.tabIndex === 0)).toHaveLength(1);
    act(() => row(0)!.focus());
    fireEvent.keyDown(row(0)!, { key: "ArrowDown" });
    expect(document.activeElement).toBe(row(1));
    fireEvent.keyDown(row(1)!, { key: "ArrowUp" });
    expect(document.activeElement).toBe(row(0));

    fireEvent.keyDown(row(0)!, { key: "Enter" });
    expect(row(0)?.getAttribute("aria-selected")).toBe("true");
    const detail = screen.getByRole("region", { name: "Message 1" });
    expect(within(detail).getByLabelText("Payload").textContent).toBe('{\n  "op": "subscribe",\n  "ids": [\n    1,\n    2\n  ]\n}');
    expect(within(detail).getByText("masked").nextElementSibling?.textContent).toBe("true");

    fireEvent.keyDown(row(0)!, { key: "End" });
    expect(document.activeElement).toBe(row(2));
    fireEvent.keyDown(row(2)!, { key: "Escape" });
    expect(screen.queryByRole("region", { name: "Message 1" })).toBeNull();

    fireEvent.click(row(1)!);
    const binary = screen.getByRole("region", { name: "Message 2" });
    expect(within(binary).getByLabelText("Payload").textContent).toBe(
      "00000000  48 69 21 00                                       |Hi!.|",
    );
  });

  it("copies the payload", async () => {
    const writeText = vi.fn(() => Promise.resolve());
    Object.defineProperty(navigator, "clipboard", { value: { writeText }, configurable: true });
    try {
      setup({ messages: [{ time: 1, direction: "received", kind: "binary", data: new Uint8Array([1, 2, 255]) }] });
      fireEvent.click(row(0)!);
      await act(async () => {
        fireEvent.click(screen.getByRole("button", { name: "Copy hex" }));
      });
      expect(writeText).toHaveBeenCalledWith("01 02 ff");
      expect(screen.getByText("Copied hex")).toBeTruthy();
      await act(async () => {
        fireEvent.click(screen.getByRole("button", { name: "Copy Base64" }));
      });
      expect(writeText).toHaveBeenLastCalledWith("AQL/");
    } finally {
      Reflect.deleteProperty(navigator, "clipboard");
    }
  });

  it("clears the log", () => {
    setup({ messages: [text(0), text(1)] });
    fireEvent.click(screen.getByRole("button", { name: "Clear" }));
    expect(dataRows()).toHaveLength(0);
    expect(screen.getByText("No messages yet.")).toBeTruthy();
  });
});
