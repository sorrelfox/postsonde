import { describe, expect, it, vi } from "vitest";
import { ConsoleFeed, formatConsoleEntry, levelRank, type EntryDTO } from "./console";

vi.mock("../../bindings/github.com/sorrelfox/postsonde/internal/app/services/console", () => ({ ConsoleService: {} }));

function entry(seq: number, patch: Partial<EntryDTO> = {}): EntryDTO {
  return {
    seq,
    at: "2026-09-19T10:30:00.123Z",
    level: "info",
    message: `m${seq}`,
    source: "",
    opId: "",
    opKind: "",
    fields: [],
    ...patch,
  };
}

const seqs = (feed: ConsoleFeed) => feed.getSnapshot().entries.map((e) => e.seq);

describe("ConsoleFeed", () => {
  it("merges the list with records streamed before it arrived, in seq order", () => {
    const feed = new ConsoleFeed();
    feed.add([entry(4), entry(5)]);
    feed.load({ entries: [entry(1), entry(2), entry(3), entry(4)], capacity: 10 });
    expect(seqs(feed)).toEqual([1, 2, 3, 4, 5]);
    feed.add([entry(5), entry(6)]);
    expect(seqs(feed)).toEqual([1, 2, 3, 4, 5, 6]);
  });

  it("keeps at most the capacity", () => {
    const feed = new ConsoleFeed(3);
    feed.add([1, 2, 3, 4, 5].map((n) => entry(n)));
    expect(seqs(feed)).toEqual([3, 4, 5]);
    feed.load({ entries: null, capacity: 2 });
    expect(seqs(feed)).toEqual([4, 5]);
  });

  it("counts unseen warnings and errors until marked seen or cleared", () => {
    const feed = new ConsoleFeed();
    const listener = vi.fn();
    feed.subscribe(listener);
    feed.add([entry(1, { level: "warn" }), entry(2, { level: "error" }), entry(3), entry(4, { level: "debug" })]);
    expect(feed.getSnapshot().unseenProblems).toBe(2);
    feed.markSeen();
    expect(feed.getSnapshot().unseenProblems).toBe(0);
    feed.add([entry(5, { level: "error" })]);
    feed.clear();
    expect(feed.getSnapshot()).toMatchObject({ entries: [], unseenProblems: 0 });
    // Records logged after a clear still arrive; older ones do not come back.
    feed.add([entry(3), entry(6)]);
    expect(seqs(feed)).toEqual([6]);
    expect(listener).toHaveBeenCalledTimes(5);
  });

  it("keeps the same snapshot until something changes", () => {
    const feed = new ConsoleFeed();
    const a = feed.getSnapshot();
    feed.add([]);
    feed.markSeen();
    expect(feed.getSnapshot()).toBe(a);
    feed.add([entry(1)]);
    expect(feed.getSnapshot()).not.toBe(a);
  });
});

describe("formatConsoleEntry", () => {
  it("writes the time, level, source, operation, message and fields", () => {
    const text = formatConsoleEntry(
      entry(1, {
        level: "warn",
        source: "netx",
        opKind: "http.send",
        opId: "op_1",
        message: "dial refused",
        fields: [
          { key: "address", value: "example.com:443" },
          { key: "err", value: "no route to host" },
        ],
      }),
    );
    expect(text).toBe('2026-09-19T10:30:00.123Z WARN  [netx] (http.send op_1) dial refused address=example.com:443 err="no route to host"');
  });
});

describe("levelRank", () => {
  it("orders the levels and treats unknown ones as info", () => {
    expect(["debug", "info", "warn", "error", "trace"].map(levelRank)).toEqual([0, 1, 2, 3, 1]);
  });
});
