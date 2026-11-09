// Field-name and enum autocomplete for the gRPC message editor (GRPC-17):
// a CodeMirror completion source that asks Go (GRPCService.CompleteMessage,
// internal/core/grpc/protojson/complete.go) at the cursor, with the
// method's proto setup, whose linked descriptors Go keeps cached. Go reads
// the JSON path at the cursor, filters and ranks; the popup keeps its
// order (filter: false) and asks again on the next keystroke. Each option
// shows a kind badge, the name and its type; the side panel shows the full
// type, the .proto comment, the field number and oneof. It looks like the
// GraphQL popup (completionPopup.ts). See docs/grpc.md.
//
// The method lives in a StateField (setCompletionTarget), so the source
// reads the target of now, not of when it was installed.

import {
  autocompletion,
  pickedCompletion,
  startCompletion,
  type Completion,
  type CompletionContext,
  type CompletionResult,
  type CompletionSource,
} from "@codemirror/autocomplete";
import { StateEffect, StateField, type EditorState, type Extension } from "@codemirror/state";
import type { EditorView } from "@codemirror/view";
import { t, type MessageKey } from "../../i18n";
import type { CancellablePromise } from "../../lib/calls";
import { isCancelled } from "../../lib/errors";
import {
  completeMessage,
  type MessageCompleteInputDTO,
  type MessageCompletionDTO,
  type MessageCompletionItemDTO,
} from "../../lib/grpc";
import { completionPopupTheme, kindBadge } from "./completionPopup";

/** The method whose request message is completed: its proto setup, service and name. */
export interface CompletionTarget {
  readonly files: readonly string[];
  readonly importPaths: readonly string[];
  readonly service: string;
  readonly method: string;
  /** Completes the response type instead of the request type (gRPC mock responses). */
  readonly output?: boolean;
}

/** Sets the method completions are for; null turns them off. */
export const setCompletionTarget = StateEffect.define<CompletionTarget | null>();

const targetField = StateField.define<CompletionTarget | null>({
  create: () => null,
  update(value, tr) {
    for (const effect of tr.effects) if (effect.is(setCompletionTarget)) value = effect.value;
    return value;
  },
});

/** The method completions are for now. */
export function completionTarget(state: EditorState): CompletionTarget | null {
  return state.field(targetField, false) ?? null;
}

/** Whether two targets name the same method in the same setup. */
export function sameTarget(a: CompletionTarget | null, b: CompletionTarget | null): boolean {
  if (a === null || b === null) return a === b;
  return (
    JSON.stringify([a.files, a.importPaths, a.service, a.method, a.output === true]) ===
    JSON.stringify([b.files, b.importPaths, b.service, b.method, b.output === true])
  );
}

export type CompleteMessageFn = (input: MessageCompleteInputDTO) => CancellablePromise<MessageCompletionDTO>;

/** A completion option with the Go item it came from. */
export interface GrpcCompletion extends Completion {
  readonly item: MessageCompletionItemDTO;
  /** Where the item belongs: "shop.v1.Order", "shop.v1.Order.kind", … */
  readonly owner: string;
}

/** The badge letter of each kind ("F" for a field). */
const BADGES: Record<string, string> = {
  field: "F",
  extension: "X",
  "enum-value": "E",
  keyword: "K",
  type: "T",
  "map-key": "M",
};

/** Words for the side panel's footer (message keys). */
const KIND_NAMES: Record<string, MessageKey> = {
  field: "editorProtocols.completion.kind.field",
  extension: "editorProtocols.completion.kind.extension",
  "enum-value": "editorProtocols.completion.kind.enumValue",
  keyword: "editorProtocols.completion.kind.literal",
  type: "editorProtocols.completion.kind.typeUrl",
  "map-key": "editorProtocols.completion.kind.mapKey",
};

/** Characters after which completion opens by itself: a name, a quote, or a structural character. */
const TRIGGER = /(?:[A-Za-z0-9_@.[]|["{[:,]\s*)$/;

/**
 * Inserts the item's text and hint and puts the cursor in the hint; when
 * the new position has suggestions of its own (the value of an enum
 * field), opens them.
 */
function apply(view: EditorView, completion: Completion, from: number, to: number): void {
  const { item } = completion as GrpcCompletion;
  const text = item.insertText + item.suffix;
  const cursor = from + item.insertText.length + (item.suffix === "" ? 0 : item.suffixCursor);
  view.dispatch({
    changes: { from, to, insert: text },
    selection: { anchor: cursor },
    userEvent: "input.complete",
    annotations: pickedCompletion.of(completion),
    scrollIntoView: true,
  });
  if (item.retrigger) startCompletion(view);
}

/** The detail with its short type name spelled out: "repeated shop.v1.Line". */
export function fullType(detail: string, type: string): string {
  const short = type.slice(type.lastIndexOf(".") + 1);
  const at = short === "" ? -1 : detail.lastIndexOf(short);
  return at < 0 ? detail : detail.slice(0, at) + type + detail.slice(at + short.length);
}

/** The side panel: name and type, comment, deprecation, number and where the item belongs. */
function info(completion: Completion): HTMLElement {
  const { item, owner } = completion as GrpcCompletion;
  const dom = document.createElement("div");
  dom.className = "cm-gqlInfo";
  const head = dom.appendChild(document.createElement("div"));
  head.className = "cm-gqlInfoHead";
  head.appendChild(document.createTextNode(item.label));
  const type = item.kind === "enum-value" ? `= ${item.detail}` : fullType(item.detail, item.type);
  if (type !== "" && item.kind !== "type") {
    const el = head.appendChild(document.createElement("span"));
    el.className = "cm-gqlInfoType";
    el.textContent = item.kind === "enum-value" ? ` ${type}` : `: ${type}`;
  }
  if (item.documentation !== "") {
    const doc = dom.appendChild(document.createElement("div"));
    doc.className = "cm-gqlInfoDoc";
    doc.textContent = item.documentation;
  }
  if (item.deprecated) {
    const dep = dom.appendChild(document.createElement("div"));
    dep.className = "cm-gqlInfoDeprecated";
    dep.textContent = t("editorProtocols.completion.deprecated");
  }
  const foot = dom.appendChild(document.createElement("div"));
  foot.className = "cm-gqlInfoFoot";
  const kindKey = KIND_NAMES[item.kind];
  const parts = [owner, kindKey === undefined ? item.kind : t(kindKey)];
  if (item.kind === "field" || item.kind === "extension") {
    parts.push(t("editorProtocols.completion.fieldNumber", { number: item.number }));
  }
  if (item.oneof !== "") parts.push(`oneof ${item.oneof}`); // i18n-ignore: the protobuf keyword and a oneof name
  foot.textContent = parts.filter((p) => p !== "").join(" · ");
  return dom;
}

/** Go's result as CodeMirror options, in Go's order. */
export function toCompletionResult(result: MessageCompletionDTO): CompletionResult | null {
  const items = result.items ?? [];
  if (items.length === 0) return null;
  const options: GrpcCompletion[] = items.map((item) => ({
    label: item.label,
    detail: item.detail,
    type: `grpc-${item.kind}`,
    info,
    apply,
    item,
    owner: result.owner,
  }));
  return { from: result.from, to: result.to, options, filter: false };
}

/** A completion source over Go's message completion; `complete` is replaceable for tests. */
export function grpcCompletionSource(complete: CompleteMessageFn = completeMessage): CompletionSource {
  return async (ctx: CompletionContext) => {
    if (ctx.state.readOnly) return null;
    const target = completionTarget(ctx.state);
    if (target === null) return null;
    if (!ctx.explicit && ctx.matchBefore(TRIGGER) === null) return null;
    const call = complete({
      files: [...target.files],
      importPaths: [...target.importPaths],
      service: target.service,
      method: target.method,
      message: ctx.state.doc.toString(),
      offset: ctx.pos,
      output: target.output === true,
    });
    ctx.addEventListener("abort", () => void call.cancel(), { onDocChange: true });
    try {
      const result = await call;
      return ctx.aborted ? null : toCompletionResult(result);
    } catch (err: unknown) {
      // A method no longer in the setup has nothing to suggest.
      if (!isCancelled(err)) console.warn("grpc: completion failed", err);
      return null;
    }
  };
}

const badge = kindBadge(BADGES, (c) => (c as GrpcCompletion).item.kind);

/** Autocomplete for the gRPC message editor, with its target field and styles. */
export function grpcCompletion(complete: CompleteMessageFn = completeMessage): Extension {
  return [
    targetField,
    autocompletion({
      override: [grpcCompletionSource(complete)],
      icons: false,
      addToOptions: [badge],
      optionClass: (c) => ((c as Partial<GrpcCompletion>).item?.deprecated ? "cm-gqlDeprecated" : ""),
      maxRenderedOptions: 100,
    }),
    completionPopupTheme,
  ];
}

/** Adds gRPC message completion to a live view (e.g. from CodeEditor's onReady), once. */
export function installGrpcCompletion(view: EditorView, complete: CompleteMessageFn = completeMessage): void {
  if (view.state.field(targetField, false) !== undefined) return;
  view.dispatch({ effects: StateEffect.appendConfig.of(grpcCompletion(complete)) });
}
