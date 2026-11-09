import { act, fireEvent, render, screen, within } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

const graphqlService = vi.hoisted(() => ({
  DocsOverview: vi.fn<(input: { key: string }) => unknown>(),
  DocsType: vi.fn<(input: { key: string; name: string }) => unknown>(),
  DocsSearch: vi.fn<(input: { key: string; query: string; limit: number }) => unknown>(),
}));
vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);
vi.mock("../../../bindings/github.com/sorrelfox/postsonde/internal/app/services/graphql", () => ({
  GraphQLService: graphqlService,
}));

import type { DocsFieldDTO, DocsHitDTO, DocsInputValueDTO, DocsOverviewDTO, DocsTypeDTO } from "../../lib/graphqlSchema";
import { settle } from "../../test/fakeWails";
import { DOCS_PAGE_SIZE, DOCS_SEARCH_DELAY_MS, generateRequestFor, GraphqlDocs } from "./GraphqlDocs";

const KEY = "https://api.test/graphql";
const cancellable = <T,>(value: T) => Object.assign(Promise.resolve(value), { cancel: vi.fn() });

const overview = (over: Partial<DocsOverviewDTO> = {}): DocsOverviewDTO => ({
  schema: true,
  description: "A test schema.",
  roots: [
    { operation: "query", type: "Query" },
    { operation: "mutation", type: "Mutation" },
  ],
  types: [
    { name: "Boolean", kind: "SCALAR" },
    { name: "Mutation", kind: "OBJECT" },
    { name: "Query", kind: "OBJECT" },
    { name: "SearchType", kind: "ENUM" },
    { name: "User", kind: "OBJECT" },
  ],
  directives: [{ name: "include", description: "Includes a field.", locations: ["FIELD"], args: [], repeatable: false }],
  ...over,
});

const input = (over: Partial<DocsInputValueDTO> = {}): DocsInputValueDTO => ({
  name: "id",
  description: "",
  type: "ID!",
  typeName: "ID",
  typeKind: "SCALAR",
  defaultValue: "",
  hasDefault: false,
  deprecated: false,
  deprecationReason: "",
  ...over,
});

const field = (over: Partial<DocsFieldDTO> = {}): DocsFieldDTO => ({
  name: "user",
  description: "",
  args: [],
  type: "User",
  typeName: "User",
  typeKind: "OBJECT",
  deprecated: false,
  deprecationReason: "",
  ...over,
});

const type = (over: Partial<DocsTypeDTO> = {}): DocsTypeDTO => ({
  found: true,
  name: "Query",
  kind: "OBJECT",
  description: "",
  root: "",
  specifiedByUrl: "",
  isOneOf: false,
  interfaces: [],
  possibleTypes: [],
  implementations: [],
  fields: [],
  inputFields: [],
  enumValues: [],
  ...over,
});

const TYPES: Record<string, DocsTypeDTO> = {
  Query: type({
    name: "Query",
    root: "query",
    description: "The query root.",
    fields: [
      field({ name: "user", description: "Looks up a user.", args: [input({ name: "id" }), input({ name: "first", type: "Int", typeName: "Int", hasDefault: true, defaultValue: "10" })], type: "User" }),
      field({ name: "users", type: "[User!]!", deprecated: true, deprecationReason: "Use search." }),
    ],
  }),
  User: type({
    name: "User",
    interfaces: ["Node"],
    fields: [field({ name: "login", type: "String!", typeName: "String", typeKind: "SCALAR" }), field({ name: "name", type: "String", typeName: "String", typeKind: "SCALAR" })],
  }),
  SearchType: type({
    name: "SearchType",
    kind: "ENUM",
    enumValues: [
      { name: "USER", description: "", deprecated: false, deprecationReason: "" },
      { name: "DISCUSSION", description: "", deprecated: true, deprecationReason: "Gone." },
    ],
  }),
};

const hit = (over: Partial<DocsHitDTO> = {}): DocsHitDTO => ({
  kind: "field",
  name: "login",
  type: "User",
  member: "login",
  parent: "User",
  detail: "String!",
  description: "The user's login.",
  deprecated: false,
  ...over,
});

beforeEach(() => {
  graphqlService.DocsOverview.mockReset().mockImplementation(() => cancellable(overview()));
  graphqlService.DocsType.mockReset().mockImplementation(({ name }) => cancellable(TYPES[name] ?? type({ name, found: false })));
  graphqlService.DocsSearch.mockReset().mockImplementation(() => cancellable({ schema: true, items: [hit()], count: 1 }));
});

async function show(props: Partial<Parameters<typeof GraphqlDocs>[0]> = {}) {
  const onClose = vi.fn();
  render(<GraphqlDocs schemaKey={KEY} version="v1" onClose={onClose} {...props} />);
  await act(settle);
  return { onClose, panel: screen.getByRole("complementary", { name: "GraphQL docs" }) };
}

const waitForSearch = () => act(() => new Promise<void>((r) => setTimeout(r, DOCS_SEARCH_DELAY_MS + 30)));

describe("GraphqlDocs (GQL-14)", () => {
  it("starts with the roots, all types by kind and the directives from the cache", async () => {
    const { panel } = await show();
    expect(graphqlService.DocsOverview).toHaveBeenCalledWith({ key: KEY });
    expect(panel.textContent).toContain("A test schema.");
    const roots = within(panel).getByRole("region", { name: "Root types" });
    expect(roots.textContent).toContain("query: Query");
    expect(roots.textContent).toContain("mutation: Mutation");
    const all = within(panel).getByRole("region", { name: "All types" });
    expect(all.textContent).toContain("All types · 5");
    const heads = within(all)
      .getAllByRole("button", { expanded: true })
      .map((b) => b.textContent);
    expect(heads).toEqual(["Objects3", "Enums1", "Scalars1"]);
    expect(within(panel).getByRole("region", { name: "Directives" }).textContent).toContain("@include");
    expect((within(panel).getByRole("button", { name: "Back" }) as HTMLButtonElement).disabled).toBe(true);
  });

  it("browses into types and back, with breadcrumbs", async () => {
    const { panel } = await show();
    fireEvent.click(within(within(panel).getByRole("region", { name: "Root types" })).getByRole("button", { name: "Query" }));
    await act(settle);
    expect(graphqlService.DocsType).toHaveBeenCalledWith({ key: KEY, name: "Query" });
    const fields = within(panel).getByRole("region", { name: "Fields" });
    expect(fields.textContent).toContain("Fields · 2");
    expect(fields.textContent).toContain("Looks up a user.");
    // Arguments with types and defaults; deprecation with its reason.
    expect(within(fields).getAllByRole("list", { name: "Arguments" })[0].textContent).toBe("id: ID!first: Int = 10");
    expect(fields.textContent).toContain("deprecated");
    expect(fields.textContent).toContain("Use search.");
    expect(panel.textContent).toContain("query root");

    // A field's type opens it; the breadcrumbs show the way back.
    fireEvent.click(within(fields).getAllByRole("button", { name: "User" })[0]);
    await act(settle);
    expect(within(panel).getByRole("heading", { name: "User" })).toBeTruthy();
    const crumbs = within(panel).getByRole("navigation", { name: "Breadcrumbs" });
    expect(crumbs.textContent).toBe("Schema›Query›User");
    expect(panel.textContent).toContain("Implements");

    fireEvent.click(within(panel).getByRole("button", { name: "Back" }));
    await act(settle);
    expect(within(panel).getByRole("heading", { name: "Query" })).toBeTruthy();
    fireEvent.click(within(within(panel).getByRole("navigation", { name: "Breadcrumbs" })).getByRole("button", { name: "Schema" }));
    await act(settle);
    expect(within(panel).getByRole("region", { name: "Root types" })).toBeTruthy();
  });

  it("shows enum values with their deprecation, and says when a type is unknown", async () => {
    const { panel } = await show();
    fireEvent.click(within(panel).getByRole("button", { name: "SearchType" }));
    await act(settle);
    const values = within(panel).getByRole("region", { name: "Values" });
    expect(values.textContent).toContain("USER");
    expect(values.textContent).toContain("Gone.");
    expect(values.querySelector("[data-deprecated]")?.textContent).toBe("DISCUSSION");
    fireEvent.click(within(panel).getByRole("button", { name: "Back" }));
    await act(settle);
    fireEvent.click(within(panel).getByRole("button", { name: "Boolean" }));
    await act(settle);
    expect(panel.textContent).toContain("The schema has no type Boolean.");
  });

  it("searches in Go and opens a hit at its member", async () => {
    const { panel } = await show();
    const box = within(panel).getByRole("searchbox", { name: "Search the schema" });
    fireEvent.change(box, { target: { value: "log" } });
    await waitForSearch();
    expect(graphqlService.DocsSearch).toHaveBeenLastCalledWith({ key: KEY, query: "log", limit: 100 });
    const results = within(panel).getByRole("listbox", { name: "Search results" });
    const option = within(results).getByRole("option");
    expect(option.textContent).toContain("User.login");
    expect(option.textContent).toContain("The user's login.");
    expect(option.querySelector("mark")?.textContent).toBe("log");
    expect(panel.textContent).toContain("1 match");

    // Enter opens the active hit: its type, the member marked.
    fireEvent.keyDown(box, { key: "Enter" });
    await act(settle);
    expect((box as HTMLInputElement).value).toBe("");
    expect(within(panel).getByRole("heading", { name: "User" })).toBeTruthy();
    expect(panel.querySelector("[data-target]")?.getAttribute("data-member")).toBe("login");
  });

  it("says when nothing matches and clears the search with Escape", async () => {
    graphqlService.DocsSearch.mockImplementation(() => cancellable({ schema: true, items: [], count: 0 }));
    const { panel } = await show();
    const box = within(panel).getByRole("searchbox", { name: "Search the schema" });
    fireEvent.change(box, { target: { value: "zzz" } });
    await waitForSearch();
    expect(panel.textContent).toContain("Nothing matches “zzz”.");
    fireEvent.keyDown(box, { key: "Escape" });
    expect((box as HTMLInputElement).value).toBe("");
    expect(within(panel).getByRole("region", { name: "Root types" })).toBeTruthy();
  });

  it("renders a long field list in pages", async () => {
    const many = Array.from({ length: DOCS_PAGE_SIZE + 50 }, (_, i) => field({ name: `f${i}`, type: "String", typeName: "String" }));
    graphqlService.DocsType.mockImplementation(({ name }) => cancellable(type({ name, fields: many })));
    const { panel } = await show();
    fireEvent.click(within(within(panel).getByRole("region", { name: "Root types" })).getByRole("button", { name: "Query" }));
    await act(settle);
    const fields = () => within(panel).getByRole("region", { name: "Fields" });
    expect(fields().querySelectorAll("[data-member]").length).toBe(DOCS_PAGE_SIZE);
    fireEvent.click(within(fields()).getByRole("button", { name: "Show 50 more of 50" }));
    expect(fields().querySelectorAll("[data-member]").length).toBe(DOCS_PAGE_SIZE + 50);
  });

  it("inserts a field at the cursor when the query editor is there", async () => {
    const onInsert = vi.fn();
    const { panel } = await show({ onInsert });
    fireEvent.click(within(within(panel).getByRole("region", { name: "Root types" })).getByRole("button", { name: "Query" }));
    await act(settle);
    fireEvent.click(within(panel).getByRole("button", { name: "Insert user at the cursor" }));
    expect(onInsert).toHaveBeenCalledWith("user");
  });

  it("writes operations for root fields and fragments for other types (GQL-16)", async () => {
    const onGenerate = vi.fn();
    const onDepth = vi.fn();
    const { panel } = await show({ onInsert: vi.fn(), onGenerate, depth: 2, onDepth });
    const depth = within(panel).getByRole("combobox", { name: "Depth" }) as HTMLSelectElement;
    expect(depth.value).toBe("2");
    fireEvent.change(depth, { target: { value: "4" } });
    expect(onDepth).toHaveBeenCalledWith(4);

    fireEvent.click(within(within(panel).getByRole("region", { name: "Root types" })).getByRole("button", { name: "Query" }));
    await act(settle);
    // A root type: an operation per field, no fragment on the root.
    expect(within(panel).queryByRole("button", { name: /^Fragment/ })).toBeNull();
    fireEvent.click(within(panel).getByRole("button", { name: "Write a query for user into the query" }));
    expect(onGenerate).toHaveBeenLastCalledWith({ kind: "operation", type: "Query", field: "user" });

    fireEvent.click(within(panel).getAllByRole("button", { name: "User" })[0] as HTMLElement);
    await act(settle);
    fireEvent.click(within(panel).getByRole("button", { name: "Fragment" }));
    expect(onGenerate).toHaveBeenLastCalledWith({ kind: "fragment", type: "User", field: "" });
    fireEvent.click(within(panel).getByRole("button", { name: "Write a fragment for login into the query" }));
    expect(onGenerate).toHaveBeenLastCalledWith({ kind: "fragment", type: "User", field: "login" });
  });

  it("offers no fragment for a field with required arguments", () => {
    const f = field({ name: "repo", args: [input({ name: "name", type: "String!" })] });
    expect(generateRequestFor("User", "", f)).toBeNull();
    expect(generateRequestFor("Query", "query", f)).toEqual({ kind: "operation", type: "Query", field: "repo" });
    const optional = field({ name: "repos", args: [input({ name: "first", type: "Int!", hasDefault: true, defaultValue: "10" })] });
    expect(generateRequestFor("User", "", optional)).toEqual({ kind: "fragment", type: "User", field: "repos" });
  });

  it("opens the page of a jump from the query, and a directive on the start page (GQL-15)", async () => {
    const onClose = vi.fn();
    const { rerender } = render(<GraphqlDocs schemaKey={KEY} version="v1" onClose={onClose} jump={null} />);
    await act(settle);
    const panel = screen.getByRole("complementary", { name: "GraphQL docs" });
    // A jump opens its page with the member marked; the next one opens on top.
    rerender(<GraphqlDocs schemaKey={KEY} version="v1" onClose={onClose} jump={{ page: { kind: "type", name: "User", member: "name" }, seq: 1 }} />);
    await act(settle);
    expect(graphqlService.DocsType).toHaveBeenCalledWith({ key: KEY, name: "User" });
    expect(panel.querySelector('[data-member="name"]')?.hasAttribute("data-target")).toBe(true);
    rerender(<GraphqlDocs schemaKey={KEY} version="v1" onClose={onClose} jump={{ page: { kind: "type", name: "Query", member: "user" }, seq: 2 }} />);
    await act(settle);
    expect(within(panel).getByRole("navigation", { name: "Breadcrumbs" }).textContent).toBe("Schema›User›Query");
  });

  it("marks a directive on the start page", async () => {
    const { panel } = await show({ jump: { page: { kind: "home", member: "@include" }, seq: 1 } });
    expect(panel.querySelector('[data-member="@include"]')?.hasAttribute("data-target")).toBe(true);
  });

  it("offers to fetch the schema when none is cached", async () => {
    const onFetch = vi.fn();
    const { panel, onClose } = await show({ schemaKey: "", onFetch });
    expect(graphqlService.DocsOverview).not.toHaveBeenCalled();
    expect(panel.textContent).toContain("No schema yet");
    expect((within(panel).getByRole("searchbox") as HTMLInputElement).disabled).toBe(true);
    fireEvent.click(within(panel).getByRole("button", { name: "Fetch schema" }));
    expect(onFetch).toHaveBeenCalled();
    fireEvent.click(within(panel).getByRole("button", { name: "Close the docs" }));
    expect(onClose).toHaveBeenCalled();
  });
});
