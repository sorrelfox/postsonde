import { describe, expect, it } from "vitest";
import { DEFAULT_MERGE_OPTIONS, extractPiece, mergeEvents, parsePath, type MergeEvent } from "./sseMerge";

const msg = (data: string, event = "message"): MergeEvent => ({ event, data });

describe("parsePath", () => {
  it("reads dotted paths, bracket indexes and a leading $", () => {
    expect(parsePath("choices.0.delta.content")).toEqual(["choices", "0", "delta", "content"]);
    expect(parsePath("$.choices[0].text")).toEqual(["choices", "0", "text"]);
    expect(parsePath("a['b c'].d")).toEqual(["a", "b c", "d"]);
    expect(parsePath("  ")).toEqual([]);
  });
});

describe("extractPiece", () => {
  it("takes the data as it is", () => {
    expect(extractPiece(" [DONE]", "data")).toBe(" [DONE]");
    expect(extractPiece('{"a":1}', "data")).toBe('{"a":1}');
  });

  it("takes a JSON field, numbers and booleans as text", () => {
    expect(extractPiece('{"a":{"b":["x","y"]}}', "field", ["a", "b", "1"])).toBe("y");
    expect(extractPiece('{"n":12}', "field", ["n"])).toBe("12");
    expect(extractPiece('{"a":{}}', "field", ["a"])).toBeNull();
    expect(extractPiece("not json", "field", ["a"])).toBeNull();
    expect(extractPiece("[DONE]", "field", ["a"])).toBeNull();
  });

  it("finds the text of common token streams automatically", () => {
    expect(extractPiece('{"choices":[{"delta":{"content":"Hel"}}]}', "auto")).toBe("Hel");
    expect(extractPiece('{"choices":[{"text":"lo"}]}', "auto")).toBe("lo");
    expect(extractPiece('{"type":"content_block_delta","delta":{"type":"text_delta","text":" wor"}}', "auto")).toBe(" wor");
    expect(extractPiece('{"message":{"role":"assistant","content":"ld"}}', "auto")).toBe("ld");
    expect(extractPiece('{"response":"!","done":false}', "auto")).toBe("!");
    expect(extractPiece('"quoted"', "auto")).toBe("quoted");
    // JSON without text, and the end sentinel, give nothing; other data is taken as it is.
    expect(extractPiece('{"type":"message_start","message":{"content":[]}}', "auto")).toBeNull();
    expect(extractPiece("[DONE]", "auto")).toBeNull();
    expect(extractPiece("plain words", "auto")).toBe("plain words");
    expect(extractPiece("42", "auto")).toBe("42");
    expect(extractPiece("{broken", "auto")).toBe("{broken");
  });
});

describe("mergeEvents (SSE-04)", () => {
  it("concatenates the data of every event by default", () => {
    const r = mergeEvents([msg("Hel"), msg("lo"), msg(", "), msg("world")], DEFAULT_MERGE_OPTIONS);
    expect(r).toEqual({ text: "Hello, world", pieces: 4, skipped: 0, matched: 4 });
  });

  it("assembles an OpenAI-style token stream, leaving out the sentinel", () => {
    const events = [
      msg('{"choices":[{"delta":{"role":"assistant"}}]}'),
      msg('{"choices":[{"delta":{"content":"The"}}]}'),
      msg('{"choices":[{"delta":{"content":" answer"}}]}'),
      msg('{"choices":[{"delta":{"content":" is 42."}}]}'),
      msg("[DONE]"),
    ];
    expect(mergeEvents(events)).toEqual({ text: "The answer is 42.", pieces: 3, skipped: 2, matched: 5 });
  });

  it("filters by event type, takes a chosen field and joins with new lines", () => {
    const events = [
      msg('{"type":"message_start"}', "message_start"),
      msg('{"delta":{"text":"a"}}', "content_block_delta"),
      msg('{"type":"ping"}', "ping"),
      msg('{"delta":{"text":"b"}}', "content_block_delta"),
    ];
    const r = mergeEvents(events, { eventType: "content_block_delta", extract: "field", path: "delta.text", join: "newline" });
    expect(r).toEqual({ text: "a\nb", pieces: 2, skipped: 0, matched: 2 });
    expect(mergeEvents(events, { ...DEFAULT_MERGE_OPTIONS, extract: "field", path: "" }).pieces).toBe(0);
    expect(mergeEvents(events, { ...DEFAULT_MERGE_OPTIONS, extract: "data", eventType: "ping" }).text).toBe('{"type":"ping"}');
  });
});
