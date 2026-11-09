import { describe, expect, it } from "vitest";
import { describeEntry, formatPartTime, partGap, summarizeIncremental, usesIncrementalDelivery } from "./graphqlIncremental";
import type { IncrementalDTO, IncrementalEntryDTO, IncrementalPartDTO } from "./http";

const entry = (over: Partial<IncrementalEntryDTO>): IncrementalEntryDTO => ({
  kind: "defer",
  path: "",
  label: "",
  id: "",
  items: 0,
  errors: 0,
  problem: "",
  ...over,
});

const part = (atMs: number): IncrementalPartDTO => ({ index: 0, atMs, text: "", clipped: false, size: 0, entries: [], hasNext: null, problem: "" });

const inc = (over: Partial<IncrementalDTO>): IncrementalDTO => ({
  format: "multipart",
  receiving: false,
  parts: [part(3), part(1250)],
  partCount: 2,
  merged: "",
  mergedTooLarge: false,
  mergedSize: 0,
  hasNext: false,
  completed: true,
  pending: 0,
  errors: 0,
  ...over,
});

describe("graphqlIncremental (GQL-08)", () => {
  it("describes entries", () => {
    expect(describeEntry(entry({ kind: "initial" }))).toBe("Initial result");
    expect(describeEntry(entry({ kind: "initial", errors: 2 }))).toBe("Initial result, 2 errors");
    expect(describeEntry(entry({ path: "user", label: "D" }))).toBe("Deferred data at user (D)");
    expect(describeEntry(entry({}))).toBe("Deferred data at the root");
    expect(describeEntry(entry({ kind: "stream", path: "a.b[1]", items: 1 }))).toBe("1 streamed item at a.b[1]");
    expect(describeEntry(entry({ kind: "pending", path: "a", id: "0" }))).toBe("Pending: a");
    expect(describeEntry(entry({ kind: "completed", path: "a", id: "0", errors: 1 }))).toBe("Completed: a, 1 error");
    expect(describeEntry(entry({ kind: "completed", errors: 1 }))).toBe("Errors, 1 error");
  });

  it("formats times and gaps", () => {
    expect(formatPartTime(0.04)).toBe("0 ms");
    expect(formatPartTime(3.26)).toBe("3.3 ms");
    expect(formatPartTime(132.4)).toBe("132 ms");
    expect(formatPartTime(1250)).toBe("1.25 s");
    const parts = [part(3), part(10.5), part(9)];
    expect(partGap(parts, 0)).toBe("");
    expect(partGap(parts, 1)).toBe("+7.5 ms");
    expect(partGap(parts, 2)).toBe("+0 ms");
  });

  it("summarizes the response", () => {
    expect(summarizeIncremental(inc({}))).toEqual({ text: "2 parts in 1.25 s, complete", tone: "ok" });
    expect(summarizeIncremental(inc({ errors: 1 }))).toEqual({ text: "2 parts in 1.25 s, complete, 1 error", tone: "bad" });
    expect(summarizeIncremental(inc({ receiving: true, completed: false, pending: 2 }))).toEqual({ text: "Receiving… 2 parts, 2 pending", tone: "busy" });
    expect(summarizeIncremental(inc({ completed: false, partCount: 1, parts: null }))).toEqual({ text: "1 part, ended before hasNext: false", tone: "warn" });
  });

  it("finds @defer and @stream as Go does", () => {
    expect(usesIncrementalDelivery("{ me { id ... @defer { name } } }")).toBe(true);
    expect(usesIncrementalDelivery("{ me { friends @stream(initialCount: 1) { id } } }")).toBe(true);
    expect(usesIncrementalDelivery("{ a @ defer }")).toBe(true);
    expect(usesIncrementalDelivery("{ me { id } } # @defer in a comment")).toBe(false);
    expect(usesIncrementalDelivery('{ me(q: "@stream") { id @skip(if: false) } }')).toBe(false);
    expect(usesIncrementalDelivery('{ me(q: """ @defer """) { id } }')).toBe(false);
    expect(usesIncrementalDelivery("{ a @deferred }")).toBe(false);
  });
});
