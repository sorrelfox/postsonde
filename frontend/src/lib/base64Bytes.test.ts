import { describe, expect, it } from "vitest";
import { parseBase64 } from "./base64Bytes";

// The same cases as internal/app/services/websocket/binary_test.go.
describe("parseBase64", () => {
  it.each([
    ["", []],
    ["  \n", []],
    ["3q2+7w==", [0xde, 0xad, 0xbe, 0xef]],
    ["3q2+7w", [0xde, 0xad, 0xbe, 0xef]],
    ["3q2-7w==", [0xde, 0xad, 0xbe, 0xef]],
    ["3q2-\n7w=\n= ", [0xde, 0xad, 0xbe, 0xef]],
    ["AAEC", [0, 1, 2]],
    ["AAE", [0, 1]],
    ["_w==", [0xff]],
    ["/w", [0xff]],
  ])("parses %j", (input, bytes) => {
    const result = parseBase64(input);
    expect(result).toEqual({ ok: true, bytes: Uint8Array.from(bytes) });
  });

  it.each([
    ["3q2*7w", 3],
    ["ab é", 3],
    ["3q2+7w==x", 6],
    ["3q2+7w===", 8],
    ["3q2+7", 4],
    ["3q2+7w=", 6],
    ["=", 0],
    ["AA", 2],
    ["😀A", 0],
  ])("refuses %j at %d", (input, offset) => {
    const result = parseBase64(input);
    expect(result.ok).toBe(false);
    if (!result.ok) {
      expect(result.code).toBe("ws.invalid_base64");
      expect(result.offset).toBe(offset);
      expect(result.message).toContain(`position ${offset + 1}`);
    }
  });

  it("decodes what btoa encodes", () => {
    const bytes = Uint8Array.from({ length: 256 }, (_, i) => i);
    const text = btoa(String.fromCharCode(...bytes));
    expect(parseBase64(text)).toEqual({ ok: true, bytes });
  });
});
