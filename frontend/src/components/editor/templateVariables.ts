// {{variable}} chips in the code editors (ENV-10, ENV-11): every reference
// (docs/templates.md syntax, lib/templateSyntax.ts) is drawn as a chip in
// the variable colour; once the request's resolved variables are known
// (setTemplateVariables), the ones that do not resolve turn red, and
// hovering a chip shows its value (masked when secret) and the scope it
// comes from, or why it does not resolve. Dotted references read from a
// JSON value (ENV-13) are checked like at send time (lookupPath); escaped
// braces (ENV-14) get no chip. Filter pipes (`{{token | base64}}`,
// DYN-04) resolve from their input (lookupPipe).
//
// Typing inside `{{` opens a completion list (lib/templateCompletion.ts):
// the request's variables and every dynamic value with its description
// (DYN-01..05), and after a `|` the filters. Editors with a completion of
// their own (GraphQL, gRPC messages) keep theirs: it overrides this one.
//
// The set lives in a state field, so an editor state kept across tab
// switches keeps it; CodeEditor sets it from the TemplateVariablesContext
// around it. Without a set (null) references are only highlighted.

import {
  autocompletion,
  type Completion,
  type CompletionContext,
  type CompletionResult,
} from "@codemirror/autocomplete";
import { EditorState, StateEffect, StateField, type Extension } from "@codemirror/state";
import {
  Decoration,
  EditorView,
  ViewPlugin,
  hoverTooltip,
  type DecorationSet,
  type ViewUpdate,
} from "@codemirror/view";
import { t } from "../../i18n";
import { hasResolutionHost, requestShowResolution } from "../../lib/resolution";
import { scanTemplate, type TemplateSpan } from "../../lib/templateSyntax";
import { templateCompletionsAt } from "../../lib/templateCompletion";
import { completionPopupTheme } from "./completionPopup";
import { loadResponseValue, responseValueLines } from "../../lib/templateResponse";
import {
  isResolved,
  lookupVariable,
  overridesText,
  responseVariable,
  scopeText,
  SECRET_MASK,
  shownValue,
  unresolvedReason,
  type TemplateVariableSet,
} from "../../lib/templateVariables";

/** Replaces the resolved variables the chips are checked against; null for none known. */
export const setTemplateVariables = StateEffect.define<TemplateVariableSet | null>();

const variablesField = StateField.define<TemplateVariableSet | null>({
  create: () => null,
  update(value, tr) {
    for (const e of tr.effects) if (e.is(setTemplateVariables)) value = e.value;
    return value;
  },
});

/** The set the editor's chips are checked against, or undefined when the editor has no chips. */
export function templateVariablesOf(state: EditorState): TemplateVariableSet | null | undefined {
  return state.field(variablesField, false);
}

const resolvedMark = Decoration.mark({ class: "cm-templateVariable" });
const unresolvedMark = Decoration.mark({
  class: "cm-templateVariable cm-templateVariable-unresolved", // i18n-ignore: class names
  attributes: { "data-unresolved": "true" },
});

/** Large documents are scanned around the visible part only. */
const FULL_SCAN_LIMIT = 100_000;

function scanRanges(view: EditorView): { from: number; to: number }[] {
  const doc = view.state.doc;
  if (doc.length <= FULL_SCAN_LIMIT) return [{ from: 0, to: doc.length }];
  const out: { from: number; to: number }[] = [];
  for (const r of view.visibleRanges) {
    const from = doc.lineAt(r.from).from;
    const to = doc.lineAt(r.to).to;
    const last = out[out.length - 1];
    if (last !== undefined && from <= last.to) last.to = Math.max(last.to, to);
    else out.push({ from, to });
  }
  return out;
}

function decorations(view: EditorView): DecorationSet {
  const set = view.state.field(variablesField, false) ?? null;
  const marks = [];
  for (const { from, to } of scanRanges(view)) {
    const text = view.state.doc.sliceString(from, to);
    for (const s of scanTemplate(text)) {
      if (s.kind !== "var") continue;
      const mark = set !== null && !isResolved(set, s.name) ? unresolvedMark : resolvedMark;
      marks.push(mark.range(from + s.start, from + s.end));
    }
  }
  return Decoration.set(marks);
}

const chips = ViewPlugin.fromClass(
  class {
    decorations: DecorationSet;
    constructor(view: EditorView) {
      this.decorations = decorations(view);
    }
    update(update: ViewUpdate) {
      const setChanged = update.startState.field(variablesField, false) !== update.state.field(variablesField, false);
      const large = update.state.doc.length > FULL_SCAN_LIMIT;
      if (update.docChanged || setChanged || (large && update.viewportChanged)) {
        this.decorations = decorations(update.view);
      }
    }
  },
  { decorations: (v) => v.decorations },
);

/** The reference at `pos` on its line, if any. */
function spanAt(state: EditorState, pos: number): { from: number; span: TemplateSpan } | null {
  const line = state.doc.lineAt(pos);
  for (const span of scanTemplate(line.text)) {
    if (span.kind === "var" && line.from + span.start <= pos && pos <= line.from + span.end) {
      return { from: line.from, span };
    }
  }
  return null;
}

/**
 * The hover's content for `{{name}}`. With `onShowResolution`, a button at
 * the end opens the request's resolution view on that name (DBG-08).
 */
export function variableTooltipDom(set: TemplateVariableSet, name: string, onShowResolution?: () => void): HTMLElement {
  const dom = variableTooltipContent(set, name);
  if (onShowResolution !== undefined) {
    const button = document.createElement("button");
    button.type = "button";
    button.className = "cm-templateVariableTooltip-resolution";
    button.textContent = t("editor.variableTooltip.showResolution");
    button.addEventListener("click", onShowResolution);
    dom.append(button);
  }
  return dom;
}

function variableTooltipContent(set: TemplateVariableSet, name: string): HTMLElement {
  const dom = document.createElement("div");
  dom.className = "cm-templateVariableTooltip";
  const head = document.createElement("div");
  head.className = "cm-templateVariableTooltip-name";
  head.textContent = `{{${name}}}`;
  dom.append(head);
  const v = lookupVariable(set, name);
  if (v !== undefined && v.resolved) {
    const value = document.createElement("div");
    value.className = "cm-templateVariableTooltip-value";
    value.textContent = shownValue(v) === "" ? t("editor.variableTooltip.empty") : shownValue(v);
    const scope = document.createElement("div");
    scope.className = "cm-templateVariableTooltip-scope";
    scope.textContent = v.secret ? t("editor.variableTooltip.secretScope", { scope: v.scope }) : scopeText(v);
    dom.append(value, scope);
    const overrides = overridesText(v);
    if (overrides !== "") {
      const line = document.createElement("div");
      line.className = "cm-templateVariableTooltip-overrides";
      line.textContent = overrides;
      dom.append(line);
    }
  } else {
    dom.dataset.unresolved = "true";
    const reason = document.createElement("div");
    reason.className = "cm-templateVariableTooltip-reason";
    reason.textContent = unresolvedReason(set, name);
    dom.append(reason);
  }
  return dom;
}

/**
 * Fills the hover of a {{$response}} reference (DYN-06) with what Go reads
 * from the stored response: the value (masked when secret), where it comes
 * from and whether the next send sends the request first, or why there is
 * no value. Returns the cancel of the call.
 */
export function fillResponseTooltip(dom: HTMLElement, requestId: string, name: string, now: () => number = Date.now): () => void {
  const line = document.createElement("div");
  line.className = "cm-templateVariableTooltip-response";
  line.textContent = t("editor.variableTooltip.responseLoading");
  const anchor = dom.querySelector(".cm-templateVariableTooltip-resolution");
  dom.insertBefore(line, anchor);
  let call: ReturnType<typeof loadResponseValue> | null = null;
  try {
    call = loadResponseValue(requestId, name);
  } catch {
    line.remove(); // the service is not bound (tests without it)
    return () => {};
  }
  call.then(
    (dto) => {
      line.replaceChildren();
      const value = document.createElement("div");
      value.className = "cm-templateVariableTooltip-value";
      if (dto.resolved) {
        value.textContent = dto.secret ? SECRET_MASK : dto.value === "" ? t("editor.variableTooltip.empty") : dto.value;
      } else {
        value.className = "cm-templateVariableTooltip-reason";
        value.textContent = dto.problem;
      }
      line.append(value);
      for (const text of responseValueLines(dto, now())) {
        const more = document.createElement("div");
        more.className = "cm-templateVariableTooltip-scope";
        more.textContent = text;
        line.append(more);
      }
    },
    () => line.remove(),
  );
  return () => call?.cancel();
}

const tooltip = hoverTooltip((view, pos) => {
  const set = view.state.field(variablesField, false) ?? null;
  if (set === null) return null;
  const at = spanAt(view.state, pos);
  if (at === null) return null;
  const { from, span } = at;
  return {
    pos: from + span.start,
    end: from + span.end,
    above: true,
    // In an editor with a Variables sub-tab, the hover links to it.
    create: (view) => {
      const dom = variableTooltipDom(
        set,
        span.name,
        hasResolutionHost(view.dom) ? () => requestShowResolution(view.dom, span.name) : undefined,
      );
      const response = responseVariable(set, span.name);
      if (response === undefined || !response.resolved || set.requestId === undefined) return { dom };
      return { dom, destroy: fillResponseTooltip(dom, set.requestId, span.name) };
    },
  };
});

const theme = EditorView.theme({
  ".cm-templateVariable": {
    color: "var(--tk-var)",
    backgroundColor: "color-mix(in srgb, var(--tk-var) 14%, transparent)",
    borderRadius: "var(--radius-xs)",
  },
  ".cm-templateVariable-unresolved": {
    color: "var(--bad)",
    backgroundColor: "color-mix(in srgb, var(--bad) 16%, transparent)",
  },
  ".cm-templateVariableTooltip": {
    display: "flex",
    flexDirection: "column",
    gap: "var(--space-1)",
    maxWidth: "360px",
    padding: "var(--space-3) var(--space-4)",
    fontFamily: "var(--font-mono)",
    fontSize: "var(--fs-mono-sm)",
  },
  ".cm-templateVariableTooltip-name": { color: "var(--tk-var)" },
  ".cm-templateVariableTooltip[data-unresolved] .cm-templateVariableTooltip-name": { color: "var(--bad)" },
  ".cm-templateVariableTooltip-value": { color: "var(--text)", overflowWrap: "anywhere" },
  ".cm-templateVariableTooltip-scope, .cm-templateVariableTooltip-reason, .cm-templateVariableTooltip-overrides": {
    color: "var(--text2)",
    fontFamily: "var(--font-sans)",
  },
  ".cm-templateVariableTooltip-resolution": {
    alignSelf: "flex-start",
    padding: "0",
    border: "0",
    background: "transparent",
    color: "var(--accent)",
    fontFamily: "var(--font-sans)",
    fontSize: "var(--fs-sm2)",
    textDecoration: "underline",
    cursor: "pointer",
  },
});

/** Completion of names inside `{{` and of filters after `|`. */
export function templateCompletionSource(context: CompletionContext): CompletionResult | null {
  const line = context.state.doc.lineAt(context.pos);
  const before = line.text.slice(0, context.pos - line.from);
  const set = context.state.field(variablesField, false) ?? null;
  const found = templateCompletionsAt(before, set);
  if (found === null) return null;
  const options: Completion[] = found.items.map((item) => ({
    label: item.label,
    detail: item.detail,
    info: item.info === "" ? undefined : item.info,
    // A request picked by its name inserts its ID ({{$response}}, DYN-06).
    apply: item.apply,
    type: item.kind === "filter" ? "function" : item.kind === "response" ? "keyword" : "variable",
    // The request's own variables first, then the dynamic values.
    boost: item.kind === "variable" ? 1 : 0,
  }));
  return { from: line.from + found.from, options, validFor: /^[$\w.-]*$/ };
}

const completion: Extension = [
  EditorState.languageData.of(() => [{ autocomplete: templateCompletionSource }]),
  autocompletion(),
  completionPopupTheme,
];

/** The chips, their hover, completion and the variable set they are checked against. */
export const templateVariables: Extension = [variablesField, chips, tooltip, theme, completion];
