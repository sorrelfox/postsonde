import { describe, expect, it } from "vitest";
import {
  autosaveReducer,
  initialAutosaveState,
  selectRequestSaveStatus,
  selectSaveSummary,
  type AutosaveAction,
  type AutosaveState,
} from "./autosave";
import { shallowEqual } from "./store";

const run = (actions: AutosaveAction[], state: AutosaveState = initialAutosaveState) =>
  actions.reduce(autosaveReducer, state);

const error = { message: "A project file could not be saved.", hint: "Check the disk.", code: "projectfile.save_failed" };

describe("autosave reducer", () => {
  it("goes idle → saving → saved", () => {
    let s = initialAutosaveState;
    expect(selectRequestSaveStatus(s, "a")).toBe("idle");
    s = run([{ type: "autosave/queued", id: "a", revision: 1 }], s);
    expect(selectRequestSaveStatus(s, "a")).toBe("saving");
    s = run([{ type: "autosave/saved", id: "a", revision: 1, at: "2026-09-16T10:00:00Z" }], s);
    expect(selectRequestSaveStatus(s, "a")).toBe("saved");
    expect(s.pending).toEqual({});
    expect(s.lastSavedAt).toBe("2026-09-16T10:00:00Z");
  });

  it("keeps saving while a newer edit is pending", () => {
    const s = run([
      { type: "autosave/queued", id: "a", revision: 1 },
      { type: "autosave/queued", id: "a", revision: 2 },
      { type: "autosave/saved", id: "a", revision: 1, at: "t1" },
    ]);
    expect(selectRequestSaveStatus(s, "a")).toBe("saving");
    expect(run([{ type: "autosave/saved", id: "a", revision: 2, at: "t2" }], s).pending).toEqual({});
  });

  it("records failures until a later save succeeds", () => {
    let s = run([
      { type: "autosave/queued", id: "a", revision: 3 },
      { type: "autosave/failed", id: "a", revision: 3, error, at: "t1" },
    ]);
    expect(selectRequestSaveStatus(s, "a")).toBe("error");
    expect(s.pending).toEqual({});
    expect(s.failed.a).toEqual({ revision: 3, at: "t1", ...error });

    // A newer edit is pending: the failure stays visible.
    s = run([{ type: "autosave/queued", id: "a", revision: 4 }], s);
    expect(selectRequestSaveStatus(s, "a")).toBe("error");
    // An older failure arriving late does not replace it.
    expect(run([{ type: "autosave/failed", id: "a", revision: 2, error, at: "t0" }], s)).toBe(s);

    s = run([{ type: "autosave/saved", id: "a", revision: 4, at: "t2" }], s);
    expect(selectRequestSaveStatus(s, "a")).toBe("saved");
    expect(s.failed).toEqual({});
  });

  it("dismisses failures", () => {
    const s = run([
      { type: "autosave/failed", id: "a", revision: 1, error, at: "t1" },
      { type: "autosave/failed", id: "b", revision: 1, error, at: "t2" },
    ]);
    expect(Object.keys(run([{ type: "autosave/dismiss", ids: ["a"] }], s).failed)).toEqual(["b"]);
    expect(run([{ type: "autosave/dismiss" }], s).failed).toEqual({});
    expect(run([{ type: "autosave/dismiss", ids: ["zzz"] }], s)).toBe(s);
    expect(run([{ type: "autosave/dismiss" }])).toBe(initialAutosaveState);
  });

  it("returns the same state for a repeated queue", () => {
    const s = run([{ type: "autosave/queued", id: "a", revision: 1 }]);
    expect(run([{ type: "autosave/queued", id: "a", revision: 1 }], s)).toBe(s);
  });
});

describe("selectSaveSummary", () => {
  it("summarises for the status bar", () => {
    expect(selectSaveSummary(initialAutosaveState)).toEqual({
      status: "idle",
      pendingCount: 0,
      failedCount: 0,
      lastSavedAt: null,
      failure: null,
    });
    const s = run([
      { type: "autosave/queued", id: "a", revision: 1 },
      { type: "autosave/queued", id: "b", revision: 1 },
      { type: "autosave/saved", id: "a", revision: 1, at: "t1" },
    ]);
    expect(selectSaveSummary(s)).toMatchObject({ status: "saving", pendingCount: 1, lastSavedAt: "t1" });

    const failed = run(
      [
        { type: "autosave/failed", id: "b", revision: 1, error, at: "t3" },
        { type: "autosave/failed", id: "c", revision: 1, error: { ...error, message: "older" }, at: "t2" },
      ],
      s,
    );
    const summary = selectSaveSummary(failed);
    expect(summary).toMatchObject({ status: "error", failedCount: 2, failure: failed.failed.b });
    // Stable for shallowEqual while nothing changes.
    expect(shallowEqual(summary, selectSaveSummary(failed))).toBe(true);
  });
});
