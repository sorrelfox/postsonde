import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("@wailsio/runtime", async () => (await import("../test/fakeWails")).fakeRuntime);
vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/logexport", async () =>
  (await import("../test/fakeLogExport")).fakeLogExportBindings,
);

import { jsonLogDocument, textLogDocument } from "../lib/logExport";
import { lastCopied, logExportService, MASK, resetFakeLogExport, SECRET } from "../test/fakeLogExport";
import { fakeRuntime, resetFakeWails } from "../test/fakeWails";
import { copyLogText, inSentence, saveLog } from "./logExport";
import { toaster } from "./toasts";

beforeEach(() => {
  resetFakeWails();
  resetFakeLogExport();
  toaster.clear();
});

describe("copyLogText (DBG-07)", () => {
  it("masks in Go before the text reaches the clipboard", async () => {
    const ok = await copyLogText(`Connecting to wss://h/ws?token=${SECRET}`);
    expect(ok).toBe(true);
    expect(logExportService.Mask).toHaveBeenCalledWith({ text: `Connecting to wss://h/ws?token=${SECRET}`, format: "text" });
    expect(lastCopied(fakeRuntime.Clipboard.SetText)).toBe(`Connecting to wss://h/ws?token=${MASK}`);
    expect(toaster.toasts()[0]?.message).toBe("Copied entry, secrets masked.");
  });

  it("copies JSON as JSON and says what was copied", async () => {
    await copyLogText('{"a":1}', "json", "3 records");
    expect(logExportService.Mask).toHaveBeenCalledWith({ text: '{"a":1}', format: "json" });
    expect(toaster.toasts()[0]?.message).toBe("Copied 3 records as JSON.");
  });

  it("copies nothing when masking fails", async () => {
    logExportService.Mask.mockRejectedValueOnce({ kind: "invalid_argument", message: "The log is not valid JSON." });
    expect(await copyLogText("{", "json")).toBe(false);
    expect(fakeRuntime.Clipboard.SetText).not.toHaveBeenCalled();
    expect(toaster.toasts()[0]?.message).toMatch(/^Could not copy the entry: .*not valid JSON/);
  });
});

describe("saveLog (DBG-07)", () => {
  it("builds the log in the chosen format and saves it through Go", async () => {
    const build = vi.fn((format: string) => (format === "json" ? `{"t":"${SECRET}"}` : `t=${SECRET}`));
    const path = await saveLog({ title: "Console", fileName: "console", format: "json", build });
    expect(build).toHaveBeenCalledWith("json");
    expect(logExportService.Save).toHaveBeenCalledWith({ title: "Console", fileName: "console", format: "json", content: `{"t":"${SECRET}"}` });
    expect(path).toBe("/tmp/console.json");
    expect(toaster.toasts()[0]?.message).toBe("Saved console to /tmp/console.json, secrets masked.");
  });

  it("says nothing when the dialog is cancelled and reports failures", async () => {
    logExportService.Save.mockResolvedValueOnce({ saved: false, path: "", size: 0, masked: false });
    expect(await saveLog({ title: "Timeline", fileName: "timeline", format: "text", build: () => "x" })).toBeNull();
    expect(toaster.toasts()).toHaveLength(0);
    logExportService.Save.mockRejectedValueOnce({ kind: "io", message: "The log could not be saved to /x." });
    expect(await saveLog({ title: "Timeline", fileName: "timeline", format: "text", build: () => "x" })).toBeNull();
    expect(toaster.toasts()[0]?.message).toMatch(/^Could not save the timeline: The log could not be saved/);
  });
});

describe("inSentence", () => {
  it("lowers the first letter unless the first word has another capital", () => {
    expect(["Console", "Connection log", "WebSocket messages", "TCP messages", "gRPC call", ""].map(inSentence)).toEqual([
      "console",
      "connection log",
      "WebSocket messages",
      "TCP messages",
      "gRPC call",
      "",
    ]);
  });
});

describe("log documents", () => {
  const savedAt = new Date("2026-09-19T10:00:00Z");
  it("lays out text with a header and the entries", () => {
    expect(textLogDocument({ kind: "console", title: "Console", savedAt }, ["a", "b"], true)).toBe(
      "PostSonde Console\nSaved 2026-09-19T10:00:00.000Z, 2 entries. Secrets are masked as ••••••.\n\na\nb\n",
    );
    expect(textLogDocument({ kind: "messages", title: "Messages", savedAt }, ["a\n1", "b"])).toMatch(/\n\na\n1\n\nb\n$/);
  });

  it("wraps JSON entries with what they are", () => {
    expect(JSON.parse(jsonLogDocument({ kind: "timeline", title: "Timeline", savedAt }, [{ x: 1 }]))).toEqual({
      format: "postsonde-log",
      version: 1,
      kind: "timeline",
      title: "Timeline",
      savedAt: "2026-09-19T10:00:00.000Z",
      count: 1,
      entries: [{ x: 1 }],
    });
  });
});
