import { describe, expect, it } from "vitest";
import { MessageSource, type LogMessageInput, type MessageDirection } from "../../lib/messageSource";
import { manualFrames } from "../../test/manualFrames";
import { DEFAULT_LOG_FILTER, MessageView, SeqList, compileQuery, type LogFilter } from "./messageView";

/** Small deterministic PRNG (mulberry32). */
function rng(seed: number) {
  let a = seed >>> 0;
  return () => {
    a = (a + 0x6d2b79f5) >>> 0;
    let t = a;
    t = Math.imul(t ^ (t >>> 15), t | 1);
    t ^= t + Math.imul(t ^ (t >>> 7), t | 61);
    return ((t ^ (t >>> 14)) >>> 0) / 4294967296;
  };
}

const DIRS: MessageDirection[] = ["sent", "received", "system"];
const KINDS = ["text", "binary", "ping", "error"];
const WORDS = ["alpha", "Beta", "gamma", "DELTA", "tick", "order-42", "a.b", "héllo"];

function randomMessage(r: () => number, i: number): LogMessageInput {
  const kind = KINDS[Math.floor(r() * KINDS.length)];
  const direction = DIRS[Math.floor(r() * DIRS.length)];
  if (kind === "binary") {
    const data = new Uint8Array(Math.floor(r() * 8));
    for (let k = 0; k < data.length; k++) data[k] = Math.floor(r() * 256);
    return { time: i, direction, kind, data };
  }
  const words = Array.from({ length: 1 + Math.floor(r() * 3) }, () => WORDS[Math.floor(r() * WORDS.length)]);
  return { time: i, direction, kind, text: words.join(" "), badge: r() < 0.1 ? "evt-" + WORDS[0] : undefined };
}

function randomFilter(r: () => number): LogFilter {
  const queries = ["", "alpha", "BETA", "a.b", "^tick", "[", "ff", "evt", "é"];
  return {
    directions: { sent: r() < 0.7, received: r() < 0.8, system: r() < 0.7 },
    kind: r() < 0.6 ? null : KINDS[Math.floor(r() * KINDS.length)],
    hiddenKinds: r() < 0.7 ? [] : [KINDS[Math.floor(r() * KINDS.length)]],
    query: queries[Math.floor(r() * queries.length)],
    regex: r() < 0.4,
    onlyMatching: r() < 0.4,
  };
}

describe("MessageView", () => {
  it("incremental updates match a full recompute (randomized)", () => {
    for (let seed = 1; seed <= 25; seed++) {
      const r = rng(seed);
      const frames = manualFrames();
      const source = new MessageSource({ maxMessages: 20 + Math.floor(r() * 60), schedule: frames.schedule });
      let filter = randomFilter(r);
      let view = new MessageView(source, filter);
      let i = 0;
      for (let step = 0; step < 60; step++) {
        const action = r();
        if (action < 0.75) {
          const n = Math.floor(r() * 40);
          for (let k = 0; k < n; k++) source.append(randomMessage(r, i++));
          frames.frame();
        } else if (action < 0.85) {
          filter = randomFilter(r);
          view = new MessageView(source, filter);
        } else if (action < 0.9) {
          source.clear();
        } else {
          // Several frames without a sync.
          for (let f = 0; f < 3; f++) {
            for (let k = 0; k < 30; k++) source.append(randomMessage(r, i++));
            frames.frame();
          }
        }
        view.sync();
        const fresh = new MessageView(source, filter);
        expect(view.rows.toArray(), `seed ${seed} step ${step}`).toEqual(fresh.rows.toArray());
        expect(view.hits.toArray(), `seed ${seed} step ${step}`).toEqual(fresh.hits.toArray());
      }
    }
  });

  it("filters by direction and kind, and hides non-matching messages only when asked", () => {
    const frames = manualFrames();
    const source = new MessageSource({ schedule: frames.schedule });
    source.append([
      { direction: "sent", kind: "text", text: "hello world" },
      { direction: "received", kind: "text", text: "HELLO back" },
      { direction: "received", kind: "binary", data: new Uint8Array([0xff, 0x00]) },
      { direction: "system", kind: "closed", text: "bye" },
    ]);
    frames.frame();
    const make = (f: Partial<LogFilter>) => new MessageView(source, { ...DEFAULT_LOG_FILTER, ...f });

    expect(make({}).rows.toArray()).toEqual([0, 1, 2, 3]);
    expect(make({ directions: { sent: false, received: true, system: false } }).rows.toArray()).toEqual([1, 2]);
    expect(make({ kind: "binary" }).rows.toArray()).toEqual([2]);
    expect(make({ hiddenKinds: ["text", "closed"] }).rows.toArray()).toEqual([2]);
    const search = make({ query: "hello" });
    expect(search.rows.toArray()).toEqual([0, 1, 2, 3]);
    expect(search.hits.toArray()).toEqual([0, 1]);
    expect(make({ query: "hello", onlyMatching: true }).rows.toArray()).toEqual([0, 1]);
    // Binary payloads are searched as hex.
    expect(make({ query: "ff 00" }).hits.toArray()).toEqual([2]);
    expect(make({ query: "^h.*o ", regex: true }).hits.toArray()).toEqual([0, 1]);
  });

  it("reports an invalid regular expression and then hides nothing", () => {
    expect(compileQuery("(", true).error).toBeTruthy();
    expect(compileQuery("(", false)).toEqual({ pattern: /\(/i, error: null });
    const frames = manualFrames();
    const source = new MessageSource({ schedule: frames.schedule });
    source.append([{ direction: "sent", kind: "text", text: "a(b" }]);
    frames.frame();
    const view = new MessageView(source, { ...DEFAULT_LOG_FILTER, query: "(", regex: true, onlyMatching: true });
    expect(view.query.error).toBeTruthy();
    expect(view.rows.toArray()).toEqual([0]);
    expect(view.hits.length).toBe(0);
  });

  it("walks hits forwards and backwards, wrapping", () => {
    const frames = manualFrames();
    const source = new MessageSource({ schedule: frames.schedule });
    for (let i = 0; i < 10; i++) source.append({ direction: "received", kind: "text", text: i % 3 === 0 ? "hit" : "miss" });
    frames.frame();
    const view = new MessageView(source, { ...DEFAULT_LOG_FILTER, query: "hit" });
    expect(view.hits.toArray()).toEqual([0, 3, 6, 9]);
    expect(view.nextHit(null, 1)).toBe(0);
    expect(view.nextHit(null, -1)).toBe(9);
    expect(view.nextHit(0, 1)).toBe(3);
    expect(view.nextHit(9, 1)).toBe(0);
    expect(view.nextHit(0, -1)).toBe(9);
    expect(view.nextHit(4, -1)).toBe(3);
  });

  it("counts removed and added rows per sync", () => {
    const frames = manualFrames();
    const source = new MessageSource({ maxMessages: 5, schedule: frames.schedule });
    const view = new MessageView(source, DEFAULT_LOG_FILTER);
    for (let i = 0; i < 4; i++) source.append({ direction: "sent", kind: "text", text: `${i}` });
    frames.frame();
    expect(view.sync()).toEqual({ removedRows: 0, addedRows: 4, reset: false });
    for (let i = 0; i < 3; i++) source.append({ direction: "sent", kind: "text", text: `${i}` });
    frames.frame();
    expect(view.sync()).toEqual({ removedRows: 2, addedRows: 3, reset: false });
    expect(view.appendedRows).toBe(7);
    source.clear();
    expect(view.sync()).toEqual({ removedRows: 5, addedRows: 0, reset: true });
  });
});

describe("SeqList", () => {
  it("drops from the front, compacts, and searches", () => {
    const list = new SeqList();
    for (let i = 0; i < 10_000; i += 2) list.push(i);
    expect(list.dropBelow(9000)).toBe(4500);
    expect(list.length).toBe(500);
    expect(list.at(0)).toBe(9000);
    expect(list.indexOf(9002)).toBe(1);
    expect(list.indexOf(9003)).toBe(-1);
    expect(list.lowerBound(9003)).toBe(2);
    expect(list.lowerBound(100_000)).toBe(500);
  });
});
