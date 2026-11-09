import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";
import type { EntryDTO } from "../../lib/console";
import { ConsoleFeed } from "../../lib/console";
import { IS_MAC } from "../../lib/shortcuts";
import { lastCopied, logExportService, MASK, resetFakeLogExport, SECRET } from "../../test/fakeLogExport";
import { emit, fakeRuntime, resetFakeWails, settle } from "../../test/fakeWails";
import { ConsoleButton } from "./ConsoleButton";
import { ConsolePanel } from "./ConsolePanel";
import { fieldsText, filterEntries, formatClock, matchesText } from "./consoleView";

const consoleService = vi.hoisted(() => ({
  List: vi.fn(),
  Clear: vi.fn(),
}));

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/console", () => ({
  ConsoleService: consoleService,
}));
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/logexport", async () =>
  (await import("../../test/fakeLogExport")).fakeLogExportBindings,
);

function entry(seq: number, patch: Partial<EntryDTO> = {}): EntryDTO {
  return { seq, at: "2026-09-19T10:30:00.123Z", level: "info", message: `message ${seq}`, source: "", opId: "", opKind: "", fields: [], ...patch };
}

const records = () => screen.queryAllByText(/^message \d+$/).map((el) => el.textContent);

const copyKey = IS_MAC ? { key: "c", metaKey: true } : { key: "c", ctrlKey: true };

beforeEach(() => {
  resetFakeWails();
  resetFakeLogExport();
  consoleService.List.mockReset().mockResolvedValue({
    entries: [entry(1, { level: "debug" }), entry(2), entry(3, { level: "warn", source: "netx", opKind: "http.send", opId: "op_1" })],
    capacity: 100,
  });
  consoleService.Clear.mockReset().mockResolvedValue(undefined);
});

async function renderPanel(feed = new ConsoleFeed(), onClose = vi.fn()) {
  render(<ConsolePanel feed={feed} onClose={onClose} />);
  await act(settle);
  return { feed, onClose };
}

describe("ConsolePanel", () => {
  it("shows what Go kept, then new records live; debug is hidden until asked for", async () => {
    await renderPanel();
    expect(records()).toEqual(["message 2", "message 3"]);
    expect(screen.getByText("http.send op_1")).toBeTruthy();

    act(() => emit("console:entries", { entries: [entry(4, { level: "error" })] }));
    expect(records()).toEqual(["message 2", "message 3", "message 4"]);

    fireEvent.click(screen.getByRole("button", { name: /Debug/ }));
    expect(records()).toEqual(["message 1", "message 2", "message 3", "message 4"]);
    fireEvent.click(screen.getByRole("button", { name: /Info/ }));
    expect(records()).toEqual(["message 1", "message 3", "message 4"]);
  });

  it("filters by text", async () => {
    await renderPanel();
    fireEvent.change(screen.getByRole("textbox", { name: "Filter the console" }), { target: { value: "NETX" } });
    expect(records()).toEqual(["message 3"]);
    expect(screen.getByText("2 hidden")).toBeTruthy();
    fireEvent.change(screen.getByRole("textbox", { name: "Filter the console" }), { target: { value: "nothing like it" } });
    expect(screen.getByText("No records match the filter.")).toBeTruthy();
  });

  it("clears the console here and in Go", async () => {
    await renderPanel();
    fireEvent.click(screen.getByRole("button", { name: "Clear the console" }));
    await act(settle);
    expect(consoleService.Clear).toHaveBeenCalledTimes(1);
    expect(screen.getByText("Nothing logged yet.")).toBeTruthy();
    act(() => emit("console:entries", { entries: [entry(5)] }));
    expect(records()).toEqual(["message 5"]);
  });

  it("copies the shown records, masked in Go", async () => {
    await renderPanel();
    fireEvent.click(screen.getByRole("button", { name: "Copy the shown records" }));
    await act(settle);
    const text = "2026-09-19T10:30:00.123Z INFO  message 2\n2026-09-19T10:30:00.123Z WARN  [netx] (http.send op_1) message 3";
    expect(logExportService.Mask).toHaveBeenCalledWith({ text, format: "text" });
    expect(lastCopied(fakeRuntime.Clipboard.SetText)).toBe(text);
  });

  it("copies a focused record with its fields, secrets masked (DBG-07)", async () => {
    consoleService.List.mockResolvedValue({
      entries: [entry(1), entry(2, { level: "error", source: "http", fields: [{ key: "url", value: `https://h/?k=${SECRET}` }] })],
      capacity: 100,
    });
    await renderPanel();
    const first = screen.getByText("message 1").closest("[data-seq]") as HTMLElement;
    const row = screen.getByText("message 2").closest("[data-seq]") as HTMLElement;
    expect(first.getAttribute("tabindex")).toBe("0");
    first.focus();
    fireEvent.keyDown(first, { key: "ArrowDown" });
    expect(document.activeElement).toBe(row);
    fireEvent.keyDown(row, copyKey);
    await act(settle);
    expect(lastCopied(fakeRuntime.Clipboard.SetText)).toBe(`2026-09-19T10:30:00.123Z ERROR [http] message 2 url="https://h/?k=${MASK}"`);

    // Shift+F10 opens the record's menu; Copy entry as JSON copies the record.
    fireEvent.keyDown(row, { key: "F10", shiftKey: true });
    fireEvent.click(screen.getByRole("menuitem", { name: /Copy entry as JSON/ }));
    await act(settle);
    expect(logExportService.Mask).toHaveBeenLastCalledWith(expect.objectContaining({ format: "json" }));
    expect(JSON.parse(lastCopied(fakeRuntime.Clipboard.SetText) ?? "")).toMatchObject({
      seq: 2,
      level: "error",
      fields: [{ key: "url", value: `https://h/?k=${MASK}` }],
    });
  });

  it("saves the shown records as text or JSON (DBG-07)", async () => {
    await renderPanel();
    fireEvent.click(screen.getByRole("button", { name: "Save the shown records…" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Save as JSON…" }));
    await act(settle);
    const input = logExportService.Save.mock.calls[0][0];
    expect(input).toMatchObject({ title: "Console", fileName: "postsonde-console", format: "json" });
    const doc = JSON.parse(input.content);
    expect(doc).toMatchObject({ format: "postsonde-log", kind: "console", count: 2 });
    expect(doc.entries.map((e: EntryDTO) => e.message)).toEqual(["message 2", "message 3"]);

    fireEvent.click(screen.getByRole("button", { name: "Save the shown records…" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Save as text…" }));
    await act(settle);
    expect(logExportService.Save.mock.calls[1][0].content).toMatch(/^PostSonde Console\n.*\n\n.*message 2\n.*message 3\n$/);
  });

  it("pauses and resumes following", async () => {
    await renderPanel();
    const pause = screen.getByRole("button", { name: "Pause" });
    fireEvent.click(pause);
    expect(screen.getByRole("button", { name: "Resume" }).getAttribute("aria-pressed")).toBe("true");
    fireEvent.click(screen.getByRole("button", { name: "Resume" }));
    expect(screen.getByRole("button", { name: "Pause" })).toBeTruthy();
  });

  it("closes on Escape and from the close button", async () => {
    const { onClose } = await renderPanel();
    fireEvent.keyDown(screen.getByRole("log"), { key: "Escape" });
    fireEvent.click(screen.getByRole("button", { name: "Close the console" }));
    expect(onClose).toHaveBeenCalledTimes(2);
  });
});

describe("ConsoleButton", () => {
  it("counts new warnings and errors while the console is hidden", async () => {
    const { rerender } = render(<ConsoleButton open={false} onToggle={() => {}} />);
    await act(settle);
    const button = screen.getByRole("button", { name: /Console/ });
    act(() => emit("console:entries", { entries: [entry(10, { level: "error" }), entry(11)] }));
    // Only records logged since the button follows the log count, not the warning List returned.
    expect(within(button).getByText("1")).toBeTruthy();
    rerender(<ConsoleButton open onToggle={() => {}} />);
    expect(within(button).queryByText("1")).toBeNull();
    expect(button.getAttribute("aria-pressed")).toBe("true");
  });
});

describe("consoleView", () => {
  it("matches text in the message, source, operation and fields", () => {
    const e = entry(1, { source: "netx", opKind: "tcp.connect", fields: [{ key: "address", value: "localhost:9" }] });
    expect(["", "MESSAGE", "netx", "tcp.conn", "address=local"].every((q) => matchesText(e, q))).toBe(true);
    expect(matchesText(e, "absent")).toBe(false);
    expect(filterEntries([e, entry(2, { level: "debug" })], { debug: false, info: true, warn: true, error: true }, "")).toEqual([e]);
  });

  it("leaves out the fields the row shows already", () => {
    const e = entry(1, {
      source: "operation",
      opId: "op_1",
      opKind: "http.send",
      fields: [
        { key: "component", value: "operation" },
        { key: "opId", value: "op_1" },
        { key: "domain", value: "http" },
        { key: "kind", value: "send" },
        { key: "status", value: "ok" },
      ],
    });
    expect(fieldsText(e)).toBe("status=ok");
    expect(fieldsText(entry(2, { fields: [{ key: "kind", value: "x" }] }))).toBe("kind=x");
  });

  it("formats the clock, or keeps text it cannot parse", () => {
    expect(formatClock("not a time")).toBe("not a time");
    expect(formatClock("2026-09-19T10:30:00.123Z")).toMatch(/^\d\d:\d\d:\d\d\.123$/);
  });
});
