import { describe, expect, it } from "vitest";
import type { LogMessage } from "../../lib/messageSource";
import {
  base64,
  base64Head,
  searchableText,
  formatRelative,
  formatSize,
  hexDump,
  highlightParts,
  messagePreview,
  messageSize,
  PREVIEW_BYTES,
  PREVIEW_CHARS,
  prettyJson,
  utf8Length,
} from "./format";

describe("message log formatting", () => {
  it("previews text on one line, cut, and binary as hex", () => {
    expect(messagePreview({ direction: "sent", kind: "text", text: "a\n  b\tc" })).toBe("a b c");
    const long = messagePreview({ direction: "sent", kind: "text", text: "x".repeat(PREVIEW_CHARS * 10) });
    expect(long).toHaveLength(PREVIEW_CHARS + 1);
    expect(long.endsWith("…")).toBe(true);
    const data = new Uint8Array(PREVIEW_BYTES + 5).fill(0xab);
    const hex = messagePreview({ direction: "received", kind: "binary", data });
    expect(hex.startsWith("ab ab ab")).toBe(true);
    expect(hex.endsWith(" …")).toBe(true);
  });

  it("previews and searches binary as Base64 too (WS-06)", () => {
    const data = Uint8Array.from({ length: PREVIEW_BYTES + 3 }, (_, i) => i);
    const preview = messagePreview({ direction: "received", kind: "binary", data }, "base64");
    // The head of the whole payload's Base64.
    expect(preview).toBe(`${base64(data).slice(0, (PREVIEW_BYTES / 3) * 4)} …`);
    const small = Uint8Array.of(0, 0xff, 0x62);
    expect(messagePreview({ direction: "received", kind: "binary", data: small }, "base64")).toBe("AP9i");
    expect(base64Head(data, 3)).toBe("AAEC");
    const message = { seq: 0, time: 0, direction: "received", kind: "binary", data: small } as LogMessage;
    expect(searchableText(message)).toBe("00 ff 62\nAP9i");
  });

  it("sizes payloads in UTF-8 bytes", () => {
    expect(utf8Length("aé€😀")).toBe(1 + 2 + 3 + 4);
    expect(utf8Length("aé€😀")).toBe(new TextEncoder().encode("aé€😀").length);
    expect(messageSize({ direction: "sent", kind: "text", text: "héllo" })).toBe(6);
    expect(messageSize({ direction: "sent", kind: "binary", data: new Uint8Array(3) })).toBe(3);
    expect(messageSize({ direction: "sent", kind: "close", size: 9 })).toBe(9);
    expect(messageSize({ direction: "system", kind: "connected" })).toBeNull();
    expect(formatSize(1536)).toBe("1.5 KB");
  });

  it("formats relative times", () => {
    expect(formatRelative(1042, 1000)).toBe("+0.042");
    expect(formatRelative(1000 + 187_250, 1000)).toBe("+3:07.250");
    expect(formatRelative(3_723_000, 0)).toBe("+1:02:03.000");
  });

  it("pretty-prints JSON objects and arrays only", () => {
    expect(prettyJson('{"a":[1,2]}')).toBe('{\n  "a": [\n    1,\n    2\n  ]\n}');
    expect(prettyJson("42")).toBeNull();
    expect(prettyJson("{not json")).toBeNull();
  });

  it("dumps bytes with offsets and ASCII", () => {
    const data = new TextEncoder().encode("Hello, binary world!");
    const lines = hexDump(data).split("\n");
    expect(lines).toHaveLength(2);
    expect(lines[0]).toBe("00000000  48 65 6c 6c 6f 2c 20 62  69 6e 61 72 79 20 77 6f  |Hello, binary wo|");
    expect(lines[1].startsWith("00000010  72 6c 64 21")).toBe(true);
    expect(lines[1].endsWith("|rld!|")).toBe(true);
    expect(lines[1].indexOf("|")).toBe(lines[0].indexOf("|"));
    expect(base64(data)).toBe(btoa("Hello, binary world!"));
  });

  it("splits text into highlighted parts", () => {
    expect(highlightParts("Abc abc", /abc/i)).toEqual([
      { text: "Abc", match: true },
      { text: " ", match: false },
      { text: "abc", match: true },
    ]);
    expect(highlightParts("aaa", /x*/i)).toEqual([{ text: "aaa", match: false }]);
    expect(highlightParts("abc", null)).toEqual([{ text: "abc", match: false }]);
  });
});
