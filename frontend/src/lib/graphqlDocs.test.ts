import { describe, expect, it } from "vitest";
import { docsPageFor, groupTypes, hitPositions, HOME, pageLabel, pushPage, splitTypeRef, type DocsPage } from "./graphqlDocs";

describe("groupTypes", () => {
  it("groups the types by kind in the explorer's order, keeping their order", () => {
    const groups = groupTypes([
      { name: "Boolean", kind: "SCALAR" },
      { name: "Issue", kind: "OBJECT" },
      { name: "Node", kind: "INTERFACE" },
      { name: "User", kind: "OBJECT" },
      { name: "Odd", kind: "NEW_KIND" },
    ]);
    expect(groups.map((g) => [g.label, g.types.map((t) => t.name)])).toEqual([
      ["Objects", ["Issue", "User"]],
      ["Interfaces", ["Node"]],
      ["Scalars", ["Boolean"]],
      ["NEW_KIND", ["Odd"]],
    ]);
    expect(groupTypes([])).toEqual([]);
  });
});

describe("splitTypeRef", () => {
  it("splits a reference around its named type", () => {
    expect(splitTypeRef("[User!]!", "User")).toEqual(["[", "User", "!]!"]);
    expect(splitTypeRef("String", "String")).toEqual(["", "String", ""]);
    expect(splitTypeRef("[[ID]]", "ID")).toEqual(["[[", "ID", "]]"]);
    expect(splitTypeRef("?", "")).toEqual(["?", "", ""]);
    expect(splitTypeRef("Foo", "Bar")).toEqual(["Foo", "", ""]);
  });
});

describe("hitPositions", () => {
  it("marks the matched characters, the member part of a dotted query only on members", () => {
    expect(hitPositions("log", "login", true)).toEqual([0, 1, 2]);
    expect(hitPositions("User.lo", "login", true)).toEqual([0, 1]);
    expect(hitPositions("User.lo", "User", false)).toEqual([]);
    expect(hitPositions("User.", "login", true)).toEqual([]);
    expect(hitPositions("zz", "login", true)).toEqual([]);
  });
});

describe("pushPage", () => {
  const type = (name: string, member?: string): DocsPage => ({ kind: "type", name, member });

  it("stacks pages on the start page and replaces the member of the page on top", () => {
    let stack: DocsPage[] = [HOME];
    stack = pushPage(stack, type("Query"));
    stack = pushPage(stack, type("User"));
    expect(stack.map(pageLabel)).toEqual(["Schema", "Query", "User"]);
    stack = pushPage(stack, type("User", "login"));
    expect(stack).toEqual([HOME, type("Query"), type("User", "login")]);
    expect(pushPage(stack, HOME)).toEqual([HOME]);
  });
});

describe("docsPageFor (GQL-15)", () => {
  const target = (over: Record<string, unknown>) => ({ found: true, kind: "field", type: "", member: "", directive: "", name: "x", from: 0, to: 1, ...over });

  it("opens a type page at the member, or a directive on the start page", () => {
    expect(docsPageFor(target({ type: "User", member: "login" }))).toEqual({ kind: "type", name: "User", member: "login" });
    expect(docsPageFor(target({ kind: "type", type: "User" }))).toEqual({ kind: "type", name: "User", member: undefined });
    expect(docsPageFor(target({ kind: "directive", directive: "include" }))).toEqual({ kind: "home", member: "@include" });
    expect(docsPageFor(target({ found: false, type: "User" }))).toBeNull();
    expect(docsPageFor(target({}))).toBeNull();
  });

  it("starts over at a start page with a member", () => {
    const home = { kind: "home", member: "@skip" } as const;
    expect(pushPage([HOME, { kind: "type", name: "User" }], home)).toEqual([home]);
  });
});
