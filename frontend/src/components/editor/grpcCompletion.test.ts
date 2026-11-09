import { CompletionContext, completionStatus, currentCompletions, startCompletion, type Completion } from "@codemirror/autocomplete";
import { EditorState } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { afterEach, describe, expect, it, vi } from "vitest";
import type { CancellablePromise } from "../../lib/calls";
import type { MessageCompletionDTO, MessageCompletionItemDTO } from "../../lib/grpc";
import {
  completionTarget,
  fullType,
  grpcCompletion,
  grpcCompletionSource,
  installGrpcCompletion,
  sameTarget,
  setCompletionTarget,
  toCompletionResult,
  type CompleteMessageFn,
  type CompletionTarget,
  type GrpcCompletion,
} from "./grpcCompletion";

vi.mock("@wailsio/runtime", async () => (await import("../../test/fakeWails")).fakeRuntime);

const cancellable = <T,>(value: T) => Object.assign(Promise.resolve(value), { cancel: vi.fn() }) as unknown as CancellablePromise<T>;

const TARGET: CompletionTarget = { files: ["proto/shop.proto"], importPaths: ["proto"], service: "shop.v1.Orders", method: "Place" };

const item = (over: Partial<MessageCompletionItemDTO> = {}): MessageCompletionItemDTO => ({
  label: "lines",
  kind: "field",
  detail: "repeated Line",
  type: "shop.v1.Line",
  documentation: "The order lines.",
  deprecated: false,
  number: 4,
  oneof: "",
  insertText: '"lines"',
  suffix: ": []",
  suffixCursor: 3,
  retrigger: false,
  ...over,
});

const result = (over: Partial<MessageCompletionDTO> = {}): MessageCompletionDTO => ({
  type: "shop.v1.Order",
  context: "key",
  from: 1,
  to: 4,
  prefix: "li",
  owner: "shop.v1.Order",
  items: [
    item(),
    item({ label: "card", detail: "string", type: "", documentation: "", number: 8, oneof: "pay", deprecated: true, insertText: '"card"', suffix: ': ""' }),
  ],
  count: 2,
  ...over,
});

let views: EditorView[] = [];
afterEach(() => {
  for (const v of views) v.destroy();
  views = [];
});

function view(doc: string, complete: CompleteMessageFn, cursor = doc.length): EditorView {
  const v = new EditorView({
    parent: document.body,
    state: EditorState.create({ doc, selection: { anchor: cursor }, extensions: grpcCompletion(complete) }),
  });
  v.dispatch({ effects: setCompletionTarget.of(TARGET) });
  views.push(v);
  return v;
}

const info = (c: Completion) => (c.info as (c: Completion) => HTMLElement)(c);
const applyOption = (v: EditorView, c: Completion, from: number, to: number) =>
  (c.apply as (view: EditorView, c: Completion, from: number, to: number) => void)(v, c, from, to);

describe("toCompletionResult", () => {
  it("keeps Go's order and range, with the type as detail", () => {
    const r = toCompletionResult(result());
    expect(r?.from).toBe(1);
    expect(r?.to).toBe(4);
    expect(r?.filter).toBe(false);
    expect(r?.options.map((o) => [o.label, o.detail, o.type])).toEqual([
      ["lines", "repeated Line", "grpc-field"],
      ["card", "string", "grpc-field"],
    ]);
    expect(toCompletionResult(result({ items: [] }))).toBeNull();
  });

  it("renders the full type, comment, deprecation, number and oneof in the side panel", () => {
    const [lines, card] = toCompletionResult(result())!.options as GrpcCompletion[];
    expect(info(lines!).textContent).toBe("lines: repeated shop.v1.LineThe order lines.shop.v1.Order · Field · number 4");
    expect(info(card!).textContent).toBe("card: stringDeprecated.shop.v1.Order · Field · number 8 · oneof pay");
    const [value] = toCompletionResult(
      result({
        context: "value",
        owner: "shop.v1.Order.kind",
        items: [item({ label: "KIND_BOOK", kind: "enum-value", detail: "1", type: "shop.v1.Kind", documentation: "A book.", number: 1 })],
      }),
    )!.options;
    expect(info(value!).textContent).toBe("KIND_BOOK = 1A book.shop.v1.Order.kind · Enum value");
  });

  it("spells out the short type name", () => {
    expect(fullType("map<string, Line>", "shop.v1.Line")).toBe("map<string, shop.v1.Line>");
    expect(fullType("Kind", "shop.v1.Kind")).toBe("shop.v1.Kind");
    expect(fullType("int32", "")).toBe("int32");
  });
});

describe("grpcCompletionSource", () => {
  it("asks Go at the cursor with the target of the state", async () => {
    const complete = vi.fn<CompleteMessageFn>(() => cancellable(result()));
    const v = view('{"li', complete);
    const r = await grpcCompletionSource(complete)(new CompletionContext(v.state, 4, false));
    expect(complete).toHaveBeenCalledWith({ ...TARGET, message: '{"li', offset: 4, output: false });
    expect(r?.options).toHaveLength(2);
  });

  it("opens by itself only after a name, a quote or a structural character, and needs a target", async () => {
    const complete = vi.fn<CompleteMessageFn>(() => cancellable(result()));
    const source = grpcCompletionSource(complete);
    const withTarget = (doc: string, extensions = grpcCompletion(complete)) => {
      const state = EditorState.create({ doc, extensions });
      return state.update({ effects: setCompletionTarget.of(TARGET) }).state;
    };
    expect(await source(new CompletionContext(withTarget('{"a": 1 '), 8, false))).toBeNull();
    expect(complete).not.toHaveBeenCalled();
    await source(new CompletionContext(withTarget("{"), 1, false));
    await source(new CompletionContext(withTarget('{"a": "'), 7, false));
    await source(new CompletionContext(withTarget('{"a": 1, '), 9, false));
    await source(new CompletionContext(withTarget('{"a": 1 '), 8, true)); // Ctrl-Space
    expect(complete).toHaveBeenCalledTimes(4);
    // No method: nothing.
    const none = EditorState.create({ doc: "{", extensions: grpcCompletion(complete) });
    expect(await source(new CompletionContext(none, 1, true))).toBeNull();
    // Nothing in a read-only editor.
    const ro = withTarget("{", [grpcCompletion(complete), EditorState.readOnly.of(true)]);
    expect(await source(new CompletionContext(ro, 1, true))).toBeNull();
    expect(complete).toHaveBeenCalledTimes(4);
  });

  it("gives nothing when Go fails", async () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {});
    const complete = vi.fn<CompleteMessageFn>(
      () => Object.assign(Promise.reject(new Error("gone")), { cancel: vi.fn() }) as unknown as CancellablePromise<MessageCompletionDTO>,
    );
    const v = view("{", complete);
    expect(await grpcCompletionSource(complete)(new CompletionContext(v.state, 1, true))).toBeNull();
    expect(warn).toHaveBeenCalled();
    warn.mockRestore();
  });
});

describe("the completion popup", () => {
  it("shows Go's options and inserts the pick with its hint", async () => {
    const complete = vi.fn<CompleteMessageFn>(() => cancellable(result()));
    const v = view('{"li}', complete, 4);
    startCompletion(v);
    await vi.waitFor(() => expect(completionStatus(v.state)).toBe("active"));
    expect(currentCompletions(v.state).map((c) => c.label)).toEqual(["lines", "card"]);
    const list = document.querySelector(".cm-tooltip-autocomplete");
    expect(list?.textContent).toContain("repeated Line");
    expect(list?.querySelector(".cm-gqlBadge")?.textContent).toBe("F");
    expect(list?.querySelector(".cm-gqlDeprecated")?.textContent).toContain("card");

    applyOption(v, currentCompletions(v.state)[0]!, 1, 4);
    expect(v.state.doc.toString()).toBe('{"lines": []}');
    expect(v.state.selection.main.head).toBe('{"lines": ['.length);
  });

  it("opens the values after a key whose value has suggestions", async () => {
    const complete = vi.fn<CompleteMessageFn>((input) =>
      cancellable(
        input.message === '{"k'
          ? result({ items: [item({ label: "kind", detail: "Kind", type: "shop.v1.Kind", insertText: '"kind"', suffix: ': ""', retrigger: true })] })
          : result({
              context: "value",
              from: 10,
              to: 10,
              prefix: "",
              owner: "shop.v1.Order.kind",
              items: [item({ label: "KIND_BOOK", kind: "enum-value", detail: "1", insertText: '"KIND_BOOK"', suffix: "" })],
            }),
      ),
    );
    const v = view('{"k', complete);
    startCompletion(v);
    await vi.waitFor(() => expect(completionStatus(v.state)).toBe("active"));
    applyOption(v, currentCompletions(v.state)[0]!, 1, 3);
    expect(v.state.doc.toString()).toBe('{"kind": ""');
    expect(v.state.selection.main.head).toBe(10);
    await vi.waitFor(() => expect(currentCompletions(v.state).map((c) => c.label)).toEqual(["KIND_BOOK"]));
    expect(document.querySelector(".cm-gqlBadge")?.textContent).toBe("E");
    expect(complete).toHaveBeenLastCalledWith({ ...TARGET, message: '{"kind": ""', offset: 10, output: false });
  });

  it("is installed once on a live view, and keeps its target", () => {
    const v = new EditorView({ parent: document.body, state: EditorState.create({ doc: "{" }) });
    views.push(v);
    const complete = vi.fn<CompleteMessageFn>(() => cancellable(result()));
    expect(completionTarget(v.state)).toBeNull();
    installGrpcCompletion(v, complete);
    const config = v.state;
    installGrpcCompletion(v, complete);
    expect(v.state).toBe(config);
    v.dispatch({ effects: setCompletionTarget.of(TARGET) });
    expect(completionTarget(v.state)).toEqual(TARGET);
    expect(sameTarget(completionTarget(v.state), { ...TARGET, files: [...TARGET.files] })).toBe(true);
    expect(sameTarget(TARGET, { ...TARGET, method: "Upload" })).toBe(false);
    expect(sameTarget(null, null)).toBe(true);
    expect(sameTarget(TARGET, null)).toBe(false);
  });
});
