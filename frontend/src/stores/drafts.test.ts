import { describe, expect, it } from "vitest";
import { draftsReducer, initialDraftsState, type DraftsState } from "./drafts";

describe("draftsReducer sub-tabs", () => {
  it("remembers the sub-tab per request and drops it with the draft", () => {
    let s: DraftsState = draftsReducer(initialDraftsState, { type: "drafts/open", id: "a", request: { url: "/a" } });
    s = draftsReducer(s, { type: "drafts/setSubTab", id: "a", subTab: "headers" });
    s = draftsReducer(s, { type: "drafts/setSubTab", id: "b", subTab: "body" });
    expect(s.subTabs).toEqual({ a: "headers", b: "body" });
    expect(draftsReducer(s, { type: "drafts/setSubTab", id: "a", subTab: "headers" })).toBe(s);

    // Edits keep it.
    s = draftsReducer(s, { type: "drafts/edit", id: "a", patch: { url: "/b" } });
    expect(s.subTabs.a).toBe("headers");

    const discarded = draftsReducer(s, { type: "drafts/discard", ids: ["a", "b"] });
    expect(discarded).toEqual(initialDraftsState);
    expect(draftsReducer(discarded, { type: "drafts/discard", ids: ["a"] })).toBe(discarded);
  });
});

describe("draftsReducer body stash", () => {
  it("keeps the last body per mode and drops it with the draft", () => {
    const raw = { mode: "raw", type: "json", text: "{}" };
    let s: DraftsState = draftsReducer(initialDraftsState, { type: "drafts/open", id: "a", request: { url: "/a" } });
    s = draftsReducer(s, { type: "drafts/stashBody", id: "a", mode: "raw", body: raw });
    expect(draftsReducer(s, { type: "drafts/stashBody", id: "a", mode: "raw", body: raw })).toBe(s);
    const xml = { mode: "raw", type: "xml", text: "<a/>" };
    s = draftsReducer(s, { type: "drafts/stashBody", id: "a", mode: "raw", body: xml });
    s = draftsReducer(s, { type: "drafts/stashBody", id: "a", mode: "none", body: { mode: "none" } });
    expect(s.bodyStash).toEqual({ a: { raw: xml, none: { mode: "none" } } });

    expect(draftsReducer(s, { type: "drafts/discard", ids: ["a"] })).toEqual(initialDraftsState);
  });
});

describe("draftsReducer path parameter stash", () => {
  it("keeps the last row per name and drops it with the draft", () => {
    const id = { name: "id", value: "1" };
    let s: DraftsState = draftsReducer(initialDraftsState, { type: "drafts/open", id: "a", request: { url: "/a" } });
    s = draftsReducer(s, { type: "drafts/stashPathParams", id: "a", rows: [id] });
    expect(draftsReducer(s, { type: "drafts/stashPathParams", id: "a", rows: [id] })).toBe(s);
    expect(draftsReducer(s, { type: "drafts/stashPathParams", id: "a", rows: [] })).toBe(s);
    const id2 = { name: "id", value: "2" };
    const org = { name: "org", value: "x" };
    s = draftsReducer(s, { type: "drafts/stashPathParams", id: "a", rows: [id2, org] });
    expect(s.pathParamStash).toEqual({ a: { id: id2, org } });

    expect(draftsReducer(s, { type: "drafts/discard", ids: ["a"] })).toEqual(initialDraftsState);
  });
});

describe("draftsReducer conflicts (STO-05)", () => {
  it("marks, resolves and replaces a draft in conflict with its file", () => {
    let s: DraftsState = draftsReducer(initialDraftsState, { type: "drafts/open", id: "a", request: { url: "/a" } });
    s = draftsReducer(s, { type: "drafts/edit", id: "a", patch: { url: "/mine" } });
    s = draftsReducer(s, { type: "drafts/conflict", id: "a" });
    expect(s.conflicts).toEqual({ a: true });
    expect(draftsReducer(s, { type: "drafts/conflict", id: "a" })).toBe(s);
    // Only a draft can be in conflict.
    expect(draftsReducer(s, { type: "drafts/conflict", id: "none" })).toBe(s);

    const resolved = draftsReducer(s, { type: "drafts/resolve", id: "a" });
    expect(resolved.conflicts).toEqual({});
    expect(resolved.byId.a).toMatchObject({ dirty: true, request: { url: "/mine" } });
    expect(draftsReducer(resolved, { type: "drafts/resolve", id: "a" })).toBe(resolved);

    const replaced = draftsReducer(s, { type: "drafts/replace", id: "a", request: { url: "/theirs" } });
    expect(replaced.byId.a).toEqual({ request: { url: "/theirs" }, dirty: false, revision: 2 });
    expect(replaced.conflicts).toEqual({});

    expect(draftsReducer(s, { type: "drafts/discard", ids: ["a"] })).toEqual(initialDraftsState);
  });
});
