// Schema-aware autocomplete for the GraphQL query editor (GQL-13): a
// CodeMirror completion source that asks the Go completion engine
// (GraphQLService.Complete, internal/core/graphql/complete) at the cursor,
// with the cached schema of the request's endpoint, so it works offline.
// Go filters and ranks; the popup keeps its order (filter: false) and asks
// again on the next keystroke. Each option shows a kind badge, the label
// and its type; the side panel shows the type, the description and the
// deprecation (styles in completionPopup.ts). See docs/graphql.md.
//
// The schema key lives in a StateField (setCompletionSchema), because a
// query editor's state outlives the component (editorHistory.tsx) and the
// source must read the key of now, not of when it was installed.

import {
  autocompletion,
  pickedCompletion,
  type Completion,
  type CompletionContext,
  type CompletionResult,
  type CompletionSource,
} from "@codemirror/autocomplete";
import { StateEffect, StateField, type Extension } from "@codemirror/state";
import type { EditorView } from "@codemirror/view";
import { t, type MessageKey } from "../../i18n";
import type { CancellablePromise } from "../../lib/calls";
import { isCancelled } from "../../lib/errors";
import { graphqlComplete, type CompletionDTO, type CompletionItemDTO } from "../../lib/graphqlSchema";
import { completionPopupTheme, kindBadge } from "./completionPopup";

/** Sets the schema key (SchemaInfoDTO.key; "" for syntax only) completions use. */
export const setCompletionSchema = StateEffect.define<string>();

const schemaField = StateField.define<string>({
  create: () => "",
  update(value, tr) {
    for (const effect of tr.effects) if (effect.is(setCompletionSchema)) value = effect.value;
    return value;
  },
});

/** The schema key completions use now. */
export function completionSchema(state: EditorView["state"]): string {
  return state.field(schemaField, false) ?? "";
}

export type CompleteFn = (key: string, text: string, offset: number) => CancellablePromise<CompletionDTO>;

/** A completion option with the Go item it came from. */
export interface GraphqlCompletion extends Completion {
  readonly item: CompletionItemDTO;
  /** Where the item belongs: "User", "Query.user", "@include", … */
  readonly owner: string;
}

/** The badge letter of each kind, as in the mockup ("F" for a field). */
const BADGES: Record<string, string> = {
  field: "F",
  argument: "A",
  "input-field": "I",
  type: "T",
  "enum-value": "E",
  directive: "@",
  variable: "$",
  fragment: "…",
  keyword: "K",
};

/** Words for the side panel's footer (message keys). */
const KIND_NAMES: Record<string, MessageKey> = {
  field: "editorProtocols.completion.kind.field",
  argument: "editorProtocols.completion.kind.argument",
  "input-field": "editorProtocols.completion.kind.inputField",
  type: "editorProtocols.completion.kind.type",
  "enum-value": "editorProtocols.completion.kind.enumValue",
  directive: "editorProtocols.completion.kind.directive",
  variable: "editorProtocols.completion.kind.variable",
  fragment: "editorProtocols.completion.kind.fragment",
  keyword: "editorProtocols.completion.kind.keyword",
};

/** Characters after which completion opens by itself (besides names). */
const TRIGGER = /(?:[A-Za-z0-9_$@]|\.\.\.|[({[:,]\s*)$/;

/** Inserts the item's text and hint, and puts the cursor in the hint. */
function apply(view: EditorView, completion: Completion, from: number, to: number): void {
  const { item } = completion as GraphqlCompletion;
  const text = item.insertText + item.suffix;
  const cursor = from + item.insertText.length + (item.suffix === "" ? 0 : item.suffixCursor);
  view.dispatch({
    changes: { from, to, insert: text },
    selection: { anchor: cursor },
    userEvent: "input.complete",
    annotations: pickedCompletion.of(completion),
    scrollIntoView: true,
  });
}

/** The side panel: name and type, description, deprecation, and where the item belongs. */
function info(completion: Completion): HTMLElement {
  const { item, owner } = completion as GraphqlCompletion;
  const dom = document.createElement("div");
  dom.className = "cm-gqlInfo";
  const head = dom.appendChild(document.createElement("div"));
  head.className = "cm-gqlInfoHead";
  head.appendChild(document.createTextNode(item.label));
  if (item.detail !== "") {
    const type = head.appendChild(document.createElement("span"));
    type.className = "cm-gqlInfoType";
    type.textContent = item.detail.startsWith("(") || item.detail.startsWith("on ") ? ` ${item.detail}` : `: ${item.detail}`;
  }
  if (item.documentation !== "") {
    const doc = dom.appendChild(document.createElement("div"));
    doc.className = "cm-gqlInfoDoc";
    doc.textContent = item.documentation;
  }
  if (item.deprecated) {
    const dep = dom.appendChild(document.createElement("div"));
    dep.className = "cm-gqlInfoDeprecated";
    dep.textContent =
      item.deprecationReason === ""
        ? t("editorProtocols.completion.deprecated")
        : t("editorProtocols.completion.deprecatedReason", { reason: item.deprecationReason });
  }
  const foot = dom.appendChild(document.createElement("div"));
  foot.className = "cm-gqlInfoFoot";
  const kindKey = KIND_NAMES[item.kind];
  const kind = kindKey === undefined ? item.kind : t(kindKey);
  foot.textContent = owner === "" ? kind : `${owner} · ${kind}`;
  return dom;
}

/** Go's result as CodeMirror options, in Go's order. */
export function toCompletionResult(result: CompletionDTO): CompletionResult | null {
  const items = result.items ?? [];
  if (items.length === 0) return null;
  const owner = result.owner || result.parentType || result.expectedType;
  const options: GraphqlCompletion[] = items.map((item) => ({
    label: item.label,
    detail: item.detail,
    type: `gql-${item.kind}`,
    info,
    apply,
    item,
    owner,
  }));
  return { from: result.from, to: result.to, options, filter: false };
}

/** A completion source over Go's completion engine; `complete` is replaceable for tests. */
export function graphqlCompletionSource(complete: CompleteFn = graphqlComplete): CompletionSource {
  return async (ctx: CompletionContext) => {
    if (ctx.state.readOnly) return null;
    if (!ctx.explicit && ctx.matchBefore(TRIGGER) === null) return null;
    const call = complete(completionSchema(ctx.state), ctx.state.doc.toString(), ctx.pos);
    ctx.addEventListener("abort", () => void call.cancel(), { onDocChange: true });
    try {
      const result = await call;
      return ctx.aborted ? null : toCompletionResult(result);
    } catch (err: unknown) {
      if (!isCancelled(err)) console.warn("graphql: completion failed", err);
      return null;
    }
  };
}

/** The kind badge before each option. */
const badge = kindBadge(BADGES, (c) => (c as GraphqlCompletion).item.kind);

/** Autocomplete for a GraphQL query editor, with its schema field and styles. */
export function graphqlCompletion(complete: CompleteFn = graphqlComplete): Extension {
  return [
    schemaField,
    autocompletion({
      override: [graphqlCompletionSource(complete)],
      icons: false,
      addToOptions: [badge],
      optionClass: (c) => ((c as Partial<GraphqlCompletion>).item?.deprecated ? "cm-gqlDeprecated" : ""),
      maxRenderedOptions: 100,
    }),
    completionPopupTheme,
  ];
}

/** Adds GraphQL completion to a live view (e.g. from CodeEditor's onReady), once. */
export function installGraphqlCompletion(view: EditorView, complete: CompleteFn = graphqlComplete): void {
  if (view.state.field(schemaField, false) !== undefined) return;
  view.dispatch({ effects: StateEffect.appendConfig.of(graphqlCompletion(complete)) });
}
