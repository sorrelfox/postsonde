// The look of the autocomplete popup shared by the GraphQL query editor
// (GQL-13, graphqlCompletion.ts) and the gRPC message editor (GRPC-17,
// grpcCompletion.ts): the list with its keyboard hints, a kind badge
// before each option and the side panel. The class names keep their
// cm-gql prefix, where they began.

import type { Completion } from "@codemirror/autocomplete";
import { EditorView } from "@codemirror/view";

/**
 * The kind badge before each option (position 20, where CodeMirror's icons
 * go): `kindOf` gives the option's kind, `letters` the letter of each.
 */
export function kindBadge(letters: Readonly<Record<string, string>>, kindOf: (c: Completion) => string) {
  return {
    position: 20,
    render(completion: Completion): Node {
      const kind = kindOf(completion);
      const el = document.createElement("span");
      el.className = `cm-gqlBadge cm-gqlBadge-${kind}`; // i18n-ignore: class names
      el.setAttribute("aria-hidden", "true");
      el.textContent = letters[kind] ?? "·";
      return el;
    },
  };
}

/** The popup's styles, as in the mockup. */
export const completionPopupTheme = EditorView.theme({
  ".cm-tooltip.cm-tooltip-autocomplete": {
    border: "1px solid var(--line2)",
    borderRadius: "var(--radius-lg)",
    background: "var(--bg0)",
    boxShadow: "var(--shadow)",
    overflow: "hidden",
    fontFamily: "var(--font-mono)",
    fontSize: "var(--fs-mono)",
  },
  ".cm-tooltip.cm-tooltip-autocomplete > ul": {
    maxHeight: "16em",
    minWidth: "230px",
    padding: "var(--space-2)",
    fontFamily: "var(--font-mono)",
  },
  ".cm-tooltip.cm-tooltip-autocomplete > ul > li": {
    display: "flex",
    alignItems: "center",
    gap: "var(--space-4)",
    height: "26px",
    padding: "0 var(--space-4)",
    borderRadius: "var(--radius-sm)",
    color: "var(--text)",
  },
  ".cm-tooltip.cm-tooltip-autocomplete > ul > li[aria-selected]": {
    background: "var(--bg2)",
    color: "var(--text)",
  },
  ".cm-tooltip-autocomplete .cm-completionLabel": { flex: "1" },
  ".cm-tooltip-autocomplete .cm-completionMatchedText": { color: "var(--accent)", textDecoration: "none" },
  ".cm-tooltip-autocomplete .cm-completionDetail": {
    marginLeft: "var(--space-6)",
    color: "var(--tk-type)",
    fontStyle: "normal",
    fontSize: "var(--fs-sm)",
  },
  ".cm-tooltip-autocomplete .cm-gqlDeprecated .cm-completionLabel": { textDecoration: "line-through", color: "var(--text3)" },
  // The keyboard hints under the list, as in the mockup.
  ".cm-tooltip.cm-tooltip-autocomplete::after": {
    content: '"↑↓ navigate · ↵ accept · esc close"',
    display: "block",
    padding: "var(--space-3) var(--space-5) var(--space-2)",
    color: "var(--text3)",
    fontFamily: "var(--font-sans)",
    fontSize: "var(--fs-xs)",
  },
  ".cm-gqlBadge": {
    flex: "none",
    width: "14px",
    height: "14px",
    display: "grid",
    placeItems: "center",
    borderRadius: "3px",
    background: "var(--accent-dim)",
    color: "var(--accent)",
    fontFamily: "var(--font-sans)",
    fontSize: "9px",
    fontWeight: "var(--fw-semibold)",
  },
  ".cm-gqlBadge-keyword, .cm-gqlBadge-fragment, .cm-gqlBadge-directive": {
    background: "var(--bg2)",
    color: "var(--tk-kw)",
  },
  ".cm-gqlBadge-type, .cm-gqlBadge-enum-value": { color: "var(--tk-type)" },
  ".cm-gqlBadge-argument, .cm-gqlBadge-input-field, .cm-gqlBadge-variable": { color: "var(--tk-arg)" },
  // gRPC message kinds (GRPC-17).
  ".cm-gqlBadge-extension, .cm-gqlBadge-map-key": { color: "var(--tk-arg)" },
  ".cm-tooltip.cm-completionInfo": {
    maxWidth: "280px",
    padding: "var(--space-5) var(--space-6)",
    border: "1px solid var(--line2)",
    borderRadius: "var(--radius-lg)",
    background: "var(--bg0)",
    color: "var(--text2)",
    fontFamily: "var(--font-sans)",
    fontSize: "var(--fs-md)",
    lineHeight: "1.5",
  },
  ".cm-gqlInfoHead": { marginBottom: "var(--space-2)", color: "var(--text)", fontFamily: "var(--font-mono)" },
  ".cm-gqlInfoType": { color: "var(--tk-type)" },
  ".cm-gqlInfoDeprecated": { marginTop: "var(--space-3)", color: "var(--warn)" },
  ".cm-gqlInfoFoot": { marginTop: "var(--space-4)", color: "var(--text3)", fontSize: "var(--fs-sm)" },
});
