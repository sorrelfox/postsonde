// A fake of LogExportService (DBG-07) for tests of copy and save: it masks
// SECRET the way Go masks a known secret, and records what it was given.
//
//   vi.mock("<path to bindings>/internal/app/services/logexport", async () =>
//     (await import("<path>/test/fakeLogExport")).fakeLogExportBindings);
import { vi } from "vitest";

/** The secret the fake knows; tests put it in the data they copy or save. */
export const SECRET = "s3cr3t-token-value";
export const MASK = "••••••";

function mask(text: string): { text: string; masked: boolean } {
  const out = text.split(SECRET).join(MASK);
  return { text: out, masked: out !== text };
}

export const logExportService = {
  Mask: vi.fn((input: { text: string; format: string }) => Promise.resolve(mask(input.text))),
  Save: vi.fn((input: { title: string; fileName: string; format: string; content: string }) => {
    const m = mask(input.content);
    return Promise.resolve({ saved: true, path: `/tmp/${input.fileName}.${input.format === "json" ? "json" : "txt"}`, size: m.text.length, masked: m.masked });
  }),
};

export const fakeLogExportBindings = { LogExportService: logExportService };

/** Clears the recorded calls; call in beforeEach. */
export function resetFakeLogExport(): void {
  logExportService.Mask.mockClear();
  logExportService.Save.mockClear();
}

/** The text last put on the clipboard through the fake Wails runtime's SetText mock. */
export function lastCopied(setText: { mock: { calls: unknown[][] } }): string | undefined {
  const calls = setText.mock.calls;
  return calls.length === 0 ? undefined : (calls[calls.length - 1][0] as string);
}
