import { describe, expect, it, vi } from "vitest";
import { manualFrames } from "../test/manualFrames";
import { MessageSource, type LogMessageInput } from "./messageSource";

const msg = (i: number, extra: Partial<LogMessageInput> = {}): LogMessageInput => ({
  time: 1000 + i,
  direction: "received",
  kind: "text",
  text: `m${i}`,
  ...extra,
});

describe("MessageSource", () => {
  it("applies appends once per frame and notifies once", () => {
    const frames = manualFrames();
    const source = new MessageSource({ maxMessages: 100, schedule: frames.schedule });
    const listener = vi.fn();
    source.subscribe(listener);

    for (let i = 0; i < 50; i++) source.append(msg(i));
    expect(frames.scheduled).toBe(1);
    expect(source.getSnapshot().count).toBe(0);
    expect(listener).not.toHaveBeenCalled();

    frames.frame();
    expect(listener).toHaveBeenCalledTimes(1);
    const snap = source.getSnapshot();
    expect(snap).toMatchObject({ count: 50, firstSeq: 0, nextSeq: 50, dropped: 0, startTime: 1000, kinds: ["text"] });
    expect(source.get(0)?.text).toBe("m0");
    expect(source.get(49)?.seq).toBe(49);
    expect(source.get(50)).toBeUndefined();
  });

  it("keeps the last maxMessages in a ring and counts the dropped ones", () => {
    const frames = manualFrames();
    const source = new MessageSource({ maxMessages: 10, schedule: frames.schedule });
    for (let i = 0; i < 7; i++) source.append(msg(i));
    frames.frame();
    for (let i = 7; i < 25; i++) source.append(msg(i));
    frames.frame();
    const snap = source.getSnapshot();
    expect(snap).toMatchObject({ count: 10, firstSeq: 15, nextSeq: 25, dropped: 15 });
    expect(source.get(14)).toBeUndefined();
    for (let seq = 15; seq < 25; seq++) expect(source.get(seq)?.text).toBe(`m${seq}`);
  });

  it("bounds the queue when no frame comes, keeping seqs contiguous", () => {
    const frames = manualFrames();
    const source = new MessageSource({ maxMessages: 10, schedule: frames.schedule });
    source.append(msg(0));
    frames.frame();
    // A hidden window: 95 appends and no frame.
    for (let i = 1; i < 96; i++) source.append(msg(i));
    frames.frame();
    const snap = source.getSnapshot();
    expect(snap).toMatchObject({ count: 10, firstSeq: 86, nextSeq: 96, dropped: 86 });
    for (let seq = 86; seq < 96; seq++) expect(source.get(seq)?.text).toBe(`m${seq}`);
  });

  it("clears at once: drops messages, queued ones, the drop count and the kinds", () => {
    const frames = manualFrames();
    const source = new MessageSource({ maxMessages: 5, schedule: frames.schedule });
    for (let i = 0; i < 8; i++) source.append(msg(i, { kind: i % 2 ? "binary" : "text" }));
    frames.frame();
    source.append(msg(8));
    const listener = vi.fn();
    source.subscribe(listener);
    source.clear();
    expect(listener).toHaveBeenCalledTimes(1);
    expect(frames.pending).toBe(false);
    expect(source.getSnapshot()).toMatchObject({ count: 0, dropped: 0, clearedAt: 8, firstSeq: 8, startTime: null, kinds: [] });
    source.append(msg(9, { time: 5000 }));
    frames.frame();
    expect(source.getSnapshot()).toMatchObject({ count: 1, firstSeq: 8, startTime: 5000 });
    expect(source.get(8)?.text).toBe("m9");
  });

  it("stamps messages without a time when they are appended", () => {
    const frames = manualFrames();
    const source = new MessageSource({ schedule: frames.schedule, now: () => 42 });
    source.append({ direction: "system", kind: "connected" });
    frames.frame();
    expect(source.get(0)?.time).toBe(42);
  });
});
