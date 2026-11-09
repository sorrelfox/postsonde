import { CompletionContext, currentCompletions, startCompletion, completionStatus, type Completion } from "@codemirror/autocomplete";
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { CancellablePromise } from "../../lib/calls";
import type { CompletionDTO, CompletionItemDTO } from "../../lib/graphqlSchema";
import {
  graphqlCompletion,
  graphqlCompletionSource,
  installGraphqlCompletion,
  setCompletionSchema,
  toCompletionResult,
  type CompleteFn,
  type GraphqlCompletion,
} from "./graphqlCompletion";

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);

const cancellable = <T,>(value: T) => Object.assign(Promise.resolve(value), { cancel: vi.fn() }) as unknown as CancellablePromise<T>;

const item = (over: Partial<CompletionItemDTO> = {}): CompletionItemDTO => ({
  label: "login",
  kind: "field",
  detail: "String!",
  documentation: "The username.",
  deprecated: false,
  deprecationReason: "",
  insertText: "login",
  suffix: "",
  suffixCursor: 0,
  ...over,
});

const result = (over: Partial<CompletionDTO> = {}): CompletionDTO => ({
  context: "selection",
  from: 11,
  to: 13,
  prefix: "lo",
  parentType: "User",
  owner: "",
  expectedType: "",
  schema: true,
  items: [item(), item({ label: "location", detail: "String", documentation: "", deprecated: true, deprecationReason: "Use address.", insertText: "location" })],
  count: 2,
  ...over,
});

let views: EditorView[] = [];
afterEach(() => {
  for (const v of views) v.destroy();
  views = [];
});

function view(doc: string, complete: CompleteFn, cursor = doc.length): EditorView {
  const v = new EditorView({
    parent: document.body,
    state: EditorState.create({ doc, selection: { anchor: cursor }, extensions: graphqlCompletion(complete) }),
  });
  views.push(v);
  return v;
}

describe("toCompletionResult", () => {
  it("keeps Go's order and range, with the type as detail", () => {
    const r = toCompletionResult(result());
    expect(r?.from).toBe(11);
    expect(r?.to).toBe(13);
    expect(r?.filter).toBe(false);
    expect(r?.options.map((o) => [o.label, o.detail, o.type])).toEqual([
      ["login", "String!", "gql-field"],
      ["location", "String", "gql-field"],
    ]);
    expect((r?.options[0] as GraphqlCompletion).owner).toBe("User");
    expect(toCompletionResult(result({ items: [] }))).toBeNull();
  });

  it("renders the description, the deprecation and the owner in the side panel", () => {
    const [login, location] = toCompletionResult(result())!.options as GraphqlCompletion[];
    const info = (c: Completion) => (c.info as (c: Completion) => HTMLElement)(c);
    expect(info(login!).textContent).toBe("login: String!The username.User · Field");
    expect(info(location!).textContent).toBe("location: StringDeprecated: Use address.User · Field");
  });
});

describe("graphqlCompletionSource", () => {
  it("asks Go at the cursor with the schema key of the state", async () => {
    const complete = vi.fn<CompleteFn>(() => cancellable(result()));
    const v = view("{ viewer { lo }", complete, 13);
    v.dispatch({ effects: setCompletionSchema.of("https://api.test/graphql") });
    const source = graphqlCompletionSource(complete);
    const r = await source(new CompletionContext(v.state, 13, false));
    expect(complete).toHaveBeenCalledWith("https://api.test/graphql", "{ viewer { lo }", 13);
    expect(r?.options).toHaveLength(2);
  });

  it("opens by itself only after a name or a trigger character", async () => {
    const complete = vi.fn<CompleteFn>(() => cancellable(result()));
    const source = graphqlCompletionSource(complete);
    const state = EditorState.create({ doc: "{ a } " });
    expect(await source(new CompletionContext(state, 5, false))).toBeNull();
    expect(complete).not.toHaveBeenCalled();
    await source(new CompletionContext(EditorState.create({ doc: "{ a(" }), 4, false));
    await source(new CompletionContext(EditorState.create({ doc: "{ ..." }), 5, false));
    await source(new CompletionContext(state, 5, true)); // Ctrl-Space
    expect(complete).toHaveBeenCalledTimes(3);
    // Nothing in a read-only editor.
    const ro = EditorState.create({ doc: "{ a", extensions: EditorState.readOnly.of(true) });
    expect(await source(new CompletionContext(ro, 3, true))).toBeNull();
  });
});

describe("the completion popup", () => {
  it("shows Go's options and inserts the pick with its hint", async () => {
    const complete = vi.fn<CompleteFn>(() =>
      cancellable(
        result({
          context: "argument",
          from: 13,
          to: 13,
          prefix: "",
          owner: "Query.repository",
          items: [item({ label: "owner", kind: "argument", detail: "String!", insertText: "owner", suffix: ": ", suffixCursor: 2 })],
        }),
      ),
    );
    const v = view("{ repository() }", complete, 13);
    startCompletion(v);
    await vi.waitFor(() => expect(completionStatus(v.state)).toBe("active"));
    expect(currentCompletions(v.state).map((c) => c.label)).toEqual(["owner"]);
    const list = document.querySelector(".cm-tooltip-autocomplete");
    expect(list?.textContent).toContain("owner");
    expect(list?.querySelector(".cm-gqlBadge")?.textContent).toBe("A");

    const option = currentCompletions(v.state)[0]!;
    (option.apply as (view: EditorView, c: Completion, from: number, to: number) => void)(v, option, 13, 13);
    expect(v.state.doc.toString()).toBe("{ repository(owner: ) }");
    expect(v.state.selection.main.head).toBe(13 + "owner: ".length);
  });

  it("is installed once on a live view", () => {
    const v = new EditorView({ parent: document.body, state: EditorState.create({ doc: "{" }) });
    views.push(v);
    const complete = vi.fn<CompleteFn>(() => cancellable(result()));
    installGraphqlCompletion(v, complete);
    const config = v.state;
    installGraphqlCompletion(v, complete);
    expect(v.state).toBe(config);
  });
});
