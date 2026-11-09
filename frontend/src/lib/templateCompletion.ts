// What to offer while typing a `{{reference}}` (ENV-11, DYN-04, DYN-05):
// right after `{{` the request's variables and every dynamic value (with
// its description, so the fake data and timestamps are discoverable), and
// after a `|` the filters. The editors show these in CodeMirror's
// completion popup (components/editor/templateVariables.ts).

import { t } from "../i18n";
import { filterDescription, TEMPLATE_FILTERS } from "./templateFilters";
import { RESPONSE_NAME, responseCompletions } from "./templateResponse";
import { scopeText, shownValue, type TemplateVariableSet } from "./templateVariables";

/** `response`: an argument of a {{$response}} reference (the request, the source, an option). */
export type TemplateCompletionKind = "variable" | "dynamic" | "filter" | "response";

export interface TemplateCompletionItem {
  readonly label: string;
  readonly kind: TemplateCompletionKind;
  /** A few words next to the label: the scope, "dynamic" or "filter". */
  readonly detail: string;
  /** The value (masked when secret) or what the dynamic value or filter does. */
  readonly info: string;
  /** The text inserted when it is not the label: a request's ID for its name. */
  readonly apply?: string;
}

export interface TemplateCompletions {
  /** Where the word being completed starts in `before`. */
  readonly from: number;
  readonly items: readonly TemplateCompletionItem[];
}

/** A word being typed as a name: letters, digits, `$`, `.`, `-` and `_`. */
const NAME_TAIL = /[$\w.-]*$/;
const FILTER_TAIL = /^[ \t]*([A-Za-z0-9]*)$/;

/**
 * The completions for a cursor after `before` (the text of the line up to
 * the cursor), or null when the cursor is not inside an open `{{` or is
 * past the name or filter name.
 */
export function templateCompletionsAt(
  before: string,
  set: TemplateVariableSet | null,
): TemplateCompletions | null {
  const open = before.lastIndexOf("{{");
  if (open < 0 || before.includes("}}", open)) return null;
  let slashes = 0;
  while (before[open - 1 - slashes] === "\\") slashes++;
  if (slashes % 2 === 1) return null; // \{{ is literal text
  const inner = before.slice(open + 2);

  let inQuote = false;
  let lastPipe = -1;
  for (let i = 0; i < inner.length; i++) {
    if (inner[i] === '"') inQuote = !inQuote;
    else if (inner[i] === "|" && !inQuote) lastPipe = i;
  }
  if (inQuote) return null;

  if (lastPipe >= 0) {
    const m = FILTER_TAIL.exec(inner.slice(lastPipe + 1));
    if (m === null) return null;
    const items = TEMPLATE_FILTERS.map((f) => ({
      label: f.name,
      kind: "filter" as const,
      detail: f.keys > 0 ? t("lib.templateCompletion.filterWithKey") : t("lib.templateCompletion.filter"),
      info: filterDescription(f),
    }));
    return { from: before.length - m[1].length, items };
  }

  const word = NAME_TAIL.exec(inner)?.[0] ?? "";
  const head = inner.trimStart();
  if (head.startsWith(RESPONSE_NAME) && /^\s/.test(head.slice(RESPONSE_NAME.length))) {
    // The arguments of a response reference (DYN-06): the request by its
    // name (its ID is inserted), the source, then send= and secret.
    const found = responseCompletions(head.slice(RESPONSE_NAME.length), word, set?.requests);
    if (found === null || found.length === 0) return null;
    const items = found.map((c) => ({ label: c.label, kind: "response" as const, detail: c.detail, info: c.info, apply: c.apply }));
    return { from: before.length - word.length, items };
  }
  if (inner.slice(0, inner.length - word.length).trim() !== "") return null; // in the arguments
  if (set === null) return null;
  const variables: TemplateCompletionItem[] = [];
  const dynamic: TemplateCompletionItem[] = [];
  for (const v of set.byName.values()) {
    if (v.source === "dynamic") {
      dynamic.push({ label: v.name, kind: "dynamic", detail: t("lib.templateCompletion.dynamic"), info: v.value });
    } else {
      variables.push({ label: v.name, kind: "variable", detail: scopeText(v), info: shownValue(v) });
    }
  }
  const byLabel = (a: TemplateCompletionItem, b: TemplateCompletionItem) => a.label.localeCompare(b.label);
  const items = [...variables.sort(byLabel), ...dynamic.sort(byLabel)];
  if (items.length === 0) return null;
  return { from: before.length - word.length, items };
}
