// Copying and saving message log entries (DBG-07): the WebSocket, SSE,
// Socket.IO, TCP, gRPC and GraphQL subscription logs and the local
// servers' traffic all use MessageLog.
import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/logexport", async () =>
  (await import("../../test/fakeLogExport")).fakeLogExportBindings,
);

import { toaster } from "../../commands/toasts";
import { MessageSource, type LogMessageInput } from "../../lib/messageSource";
import { IS_MAC } from "../../lib/shortcuts";
import { lastCopied, logExportService, MASK, resetFakeLogExport, SECRET } from "../../test/fakeLogExport";
import { fakeRuntime, resetFakeWails, settle } from "../../test/fakeWails";
import { manualFrames } from "../../test/manualFrames";
import { messageEntryJson, messageEntryText } from "./format";
import { MessageLog } from "./MessageLog";

const T0 = Date.UTC(2026, 8, 19, 10, 0, 0);

const messages: LogMessageInput[] = [
  { time: T0, direction: "system", kind: "connected", text: `Connected to wss://api.test/ws?token=${SECRET}`, meta: { status: 101, tls: "TLS 1.3" } },
  { time: T0 + 42, direction: "sent", kind: "text", text: `{"auth":"${SECRET}","op":"subscribe"}` },
  { time: T0 + 100, direction: "received", kind: "binary", data: new Uint8Array([0xde, 0xad, 0xbe, 0xef]), meta: { opcode: 2, fin: true } },
];

const copyKey = IS_MAC ? { key: "c", metaKey: true } : { key: "c", ctrlKey: true };

function setup() {
  const frames = manualFrames();
  const source = new MessageSource({ schedule: frames.schedule });
  source.append(messages);
  frames.frame();
  render(
    <div style={{ height: 400, display: "flex" }}>
      <MessageLog source={source} label="WebSocket messages" />
    </div>,
  );
  const grid = screen.getByRole("grid", { name: "WebSocket messages" });
  const row = (seq: number) => grid.querySelector<HTMLElement>(`[data-seq="${seq}"]`)!;
  return { source, row };
}

beforeEach(() => {
  resetFakeWails();
  resetFakeLogExport();
  toaster.clear();
});

describe("message entries (DBG-07)", () => {
  it("formats every detail: time, direction, kind, size, frame details and the whole payload", () => {
    const source = new MessageSource({ schedule: (f) => (f(), () => {}) });
    source.append(messages);
    source.flush();
    const [open, sent, binary] = [source.get(0)!, source.get(1)!, source.get(2)!];
    expect(messageEntryText(open, T0)).toMatch(/^\d{4}-\d\d-\d\d \d\d:\d\d:\d\d\.000 \(\+0\.000\)  System  connected  \d+ bytes\n  status: 101\n  tls: TLS 1\.3\nConnected to wss:/);
    expect(messageEntryText(sent, T0)).toMatch(/\(\+0\.042\)  Sent  text  46 bytes\n\{"auth":/);
    expect(messageEntryText(binary, T0)).toMatch(/Received  binary  4 bytes\n  opcode: 2\n  fin: true\nBinary payload, Base64:\n3q2\+7w==$/);
    expect(messageEntryJson(binary)).toEqual({
      seq: 2,
      time: "2026-09-19T10:00:00.100Z",
      timeMs: T0 + 100,
      direction: "received",
      kind: "binary",
      size: 4,
      meta: { opcode: 2, fin: true },
      base64: "3q2+7w==",
    });
  });

  it("copies the focused message with ⌘C / Ctrl+C, secrets masked", async () => {
    const { row } = setup();
    fireEvent.click(row(1));
    row(1).focus();
    fireEvent.keyDown(row(1), copyKey);
    await act(settle);
    expect(logExportService.Mask).toHaveBeenCalledWith({ text: expect.stringContaining(SECRET), format: "text" });
    const copied = lastCopied(fakeRuntime.Clipboard.SetText) ?? "";
    expect(copied).toContain(`{"auth":"${MASK}","op":"subscribe"}`);
    expect(copied).not.toContain(SECRET);
    expect(toaster.toasts()[0]?.message).toBe("Copied entry, secrets masked.");
  });

  it("opens the message menu with Shift+F10 or a right-click and copies as JSON", async () => {
    const { row } = setup();
    fireEvent.keyDown(row(0), { key: "F10", shiftKey: true });
    const menu = screen.getByRole("menu", { name: "Message" });
    fireEvent.click(within(menu).getByRole("menuitem", { name: /Copy entry as JSON/ }));
    await act(settle);
    const copied = JSON.parse(lastCopied(fakeRuntime.Clipboard.SetText) ?? "");
    expect(copied).toMatchObject({ seq: 0, direction: "system", kind: "connected", text: `Connected to wss://api.test/ws?token=${MASK}`, meta: { status: 101 } });

    fireEvent.contextMenu(row(2));
    fireEvent.click(screen.getAllByRole("menuitem")[0]);
    await act(settle);
    expect(lastCopied(fakeRuntime.Clipboard.SetText)).toMatch(/Binary payload, Base64:\n3q2\+7w==$/);
  });

  it("copies the selected message from the detail pane", async () => {
    const { row } = setup();
    fireEvent.click(row(1));
    fireEvent.click(screen.getByRole("button", { name: "Copy entry" }));
    await act(settle);
    expect(lastCopied(fakeRuntime.Clipboard.SetText)).toContain(`"auth":"${MASK}"`);
  });

  it("saves the shown messages as text or JSON, masked in Go", async () => {
    setup();
    // Only received messages are shown: the save takes what the filters show.
    fireEvent.click(screen.getByRole("button", { name: /Sent/ }));
    fireEvent.click(screen.getByRole("button", { name: /System/ }));
    fireEvent.click(screen.getByRole("button", { name: "Save…" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Save as JSON…" }));
    await act(settle);
    const input = logExportService.Save.mock.calls[0][0];
    expect(input).toMatchObject({ title: "WebSocket messages", fileName: "WebSocket messages", format: "json" });
    const doc = JSON.parse(input.content);
    expect(doc).toMatchObject({ format: "postsonde-log", version: 1, kind: "messages", count: 1 });
    expect(doc.entries[0]).toMatchObject({ seq: 2, base64: "3q2+7w==" });

    fireEvent.click(screen.getByRole("button", { name: /Sent/ }));
    fireEvent.click(screen.getByRole("button", { name: "Save…" }));
    fireEvent.click(screen.getByRole("menuitem", { name: "Save as text…" }));
    await act(settle);
    const text = logExportService.Save.mock.calls[1][0].content;
    expect(text).toMatch(/^PostSonde WebSocket messages\nSaved .*, 2 entries\./);
    expect(text).toContain(`{"auth":"${SECRET}","op":"subscribe"}`);
    const toasts = toaster.toasts();
    expect(toasts[toasts.length - 1]?.message).toBe("Saved WebSocket messages to /tmp/WebSocket messages.txt, secrets masked.");
  });
});
