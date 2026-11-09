// Editor theme and syntax colours, built only from the design tokens in
// styles/tokens.css. Because every colour is a var(--…), this one theme serves
// both the dark and the light token sets: switching data-theme on <html>
// restyles open editors. The editors also tell CodeMirror which of the two
// is showing (EditorView.darkTheme, reconfigured live by CodeEditor through
// appearanceExtension in extensions.ts; UI-04), which picks CodeMirror's
// light or dark base styles.
//
// Selectors that override CodeMirror's base theme (which has literal
// colours in its "&light" and "&dark" rules) use the same shape as the base
// rule, so they match its specificity and win by order, in both modes.

import { EditorView } from "@codemirror/view";
import { HighlightStyle } from "@codemirror/language";
import { tags as t } from "@lezer/highlight";
import { graphqlTags } from "./graphql";

// A see-through neutral for the active line. It must not be opaque: the
// selection layer is drawn behind the text, and an opaque line background
// would hide the selection on the current line.
const ACTIVE_LINE = "color-mix(in srgb, var(--bg3) 45%, transparent)";

export const editorTheme = EditorView.theme({
  "&": {
    height: "100%",
    color: "var(--tk-plain)",
    backgroundColor: "var(--bg1)",
    // The editor settings (UI-08) set these on <html>; tokens.css has the defaults.
    fontSize: "var(--fs-editor)",
  },
  "&.cm-focused": { outline: "none" },
  ".cm-scroller": {
    fontFamily: "var(--font-editor)",
    lineHeight: "var(--lh-editor)",
  },
  ".cm-content": {
    padding: "var(--space-2) 0",
    caretColor: "var(--accent)",
    // body sets user-select: none; code stays selectable, also when read-only.
    userSelect: "text",
    WebkitUserSelect: "text",
  },
  ".cm-line": { padding: "0 var(--space-6) 0 var(--space-3)" },

  // Cursor and selection
  ".cm-cursor, .cm-dropCursor": { borderLeft: "2px solid var(--accent)" },
  ".cm-selectionBackground": { backgroundColor: "var(--sel)" },
  "&.cm-focused > .cm-scroller > .cm-selectionLayer .cm-selectionBackground": {
    backgroundColor: "var(--accent-dim)",
  },
  ".cm-activeLine": { backgroundColor: ACTIVE_LINE },
  ".cm-specialChar": { color: "var(--tk-err)" },

  // Gutters: line numbers and fold markers
  ".cm-gutters": {
    backgroundColor: "var(--bg1)",
    color: "var(--text3)",
    border: "none",
    borderRight: "1px solid var(--line)",
  },
  ".cm-lineNumbers .cm-gutterElement": {
    padding: "0 var(--space-2) 0 var(--space-5)",
  },
  ".cm-activeLineGutter": {
    backgroundColor: "transparent",
    color: "var(--text2)",
  },
  ".cm-foldGutter .cm-gutterElement": {
    padding: "0 var(--space-2)",
    color: "var(--text3)",
    cursor: "pointer",
  },
  ".cm-foldGutter .cm-gutterElement:hover": { color: "var(--text)" },
  ".cm-foldPlaceholder": {
    margin: "0 var(--space-1)",
    padding: "0 var(--space-2)",
    backgroundColor: "var(--bg3)",
    border: "1px solid var(--line2)",
    borderRadius: "var(--radius-xs)",
    color: "var(--text2)",
  },

  // Brackets
  "&.cm-focused .cm-matchingBracket": {
    backgroundColor: "var(--accent-dim)",
    outline: "1px solid var(--accent)",
  },
  "&.cm-focused .cm-nonmatchingBracket": {
    backgroundColor: "transparent",
    color: "var(--tk-err)",
    outline: "1px solid var(--tk-err)",
  },

  // Search matches
  ".cm-searchMatch": {
    backgroundColor: "var(--accent-dim)",
    outline: "1px solid var(--line2)",
  },
  ".cm-searchMatch.cm-searchMatch-selected": {
    backgroundColor: "var(--accent-dim)",
    outline: "1px solid var(--accent)",
  },

  // Panels (find/replace)
  ".cm-panels": { backgroundColor: "var(--bg2)", color: "var(--text)" },
  ".cm-panels.cm-panels-top": { borderBottom: "1px solid var(--line)" },
  ".cm-panels.cm-panels-bottom": { borderTop: "1px solid var(--line)" },
  ".cm-panel.cm-search": {
    padding: "var(--space-2) var(--space-8) var(--space-2) var(--space-4)",
    fontFamily: "var(--font-sans)",
    fontSize: "var(--fs-md)",
  },
  ".cm-panel.cm-search label": {
    fontSize: "var(--fs-md)",
    color: "var(--text2)",
  },
  ".cm-panel.cm-search input[type=checkbox]": { accentColor: "var(--accent)" },
  ".cm-panel.cm-search [name=close]": {
    color: "var(--text2)",
    fontSize: "var(--fs-lg)",
    cursor: "pointer",
  },
  ".cm-panel.cm-search [name=close]:hover": { color: "var(--text)" },
  ".cm-textfield": {
    padding: "var(--space-1) var(--space-3)",
    backgroundColor: "var(--bg1)",
    color: "var(--text)",
    border: "1px solid var(--line2)",
    borderRadius: "var(--radius-sm)",
    fontFamily: "var(--font-mono)",
    fontSize: "var(--fs-mono)",
  },
  ".cm-textfield:focus": {
    outline: "none",
    borderColor: "var(--accent)",
    boxShadow: "var(--focus-ring)",
  },
  ".cm-button": {
    padding: "var(--space-1) var(--space-4)",
    backgroundColor: "var(--bg3)",
    backgroundImage: "none",
    color: "var(--text)",
    border: "1px solid var(--line2)",
    borderRadius: "var(--radius-sm)",
    fontSize: "var(--fs-md)",
    cursor: "pointer",
  },
  ".cm-button:hover": { backgroundColor: "var(--line2)" },
  ".cm-button:active": {
    backgroundColor: "var(--line2)",
    backgroundImage: "none",
  },
  ".cm-button:focus-visible, .cm-panel.cm-search input[type=checkbox]:focus-visible":
    { outline: "2px solid var(--accent)", outlineOffset: "1px" },

  // Tooltips (autocomplete, hover info)
  ".cm-tooltip": {
    backgroundColor: "var(--bg2)",
    color: "var(--text)",
    border: "1px solid var(--line2)",
    borderRadius: "var(--radius-md)",
    boxShadow: "var(--shadow)",
  },
  // Arrows point down (tooltip above) or up (below); the dark base theme
  // sets both colours, so both are overridden.
  ".cm-tooltip .cm-tooltip-arrow:before": {
    borderTopColor: "var(--line2)",
    borderBottomColor: "var(--line2)",
  },
  ".cm-tooltip .cm-tooltip-arrow:after": {
    borderTopColor: "var(--bg2)",
    borderBottomColor: "var(--bg2)",
  },
  ".cm-tooltip-autocomplete > ul > li[aria-selected]": {
    backgroundColor: "var(--accent-dim)",
    color: "var(--text)",
  },
});

/** Lezer highlight tags mapped to the --tk-* syntax tokens. */
export const editorHighlightStyle = HighlightStyle.define([
  { tag: [t.string, t.special(t.string), t.regexp], color: "var(--tk-str)" },
  { tag: t.number, color: "var(--tk-num)" },
  { tag: [t.bool, t.null, t.atom], color: "var(--tk-bool)" },
  { tag: t.propertyName, color: "var(--tk-key)" },
  // XML and HTML element names (markup.ts), coloured like JSON keys.
  { tag: t.tagName, color: "var(--tk-key)" },
  { tag: t.character, color: "var(--tk-num)" },
  // GraphQL fields: a sub-tag of propertyName, so this wins over --tk-key.
  { tag: graphqlTags.field, color: "var(--tk-field)" },
  { tag: [t.punctuation, t.operator], color: "var(--tk-punct)" },
  { tag: t.comment, color: "var(--tk-cm)", fontStyle: "italic" },
  { tag: t.keyword, color: "var(--tk-kw)" },
  { tag: [t.typeName, t.className, t.namespace], color: "var(--tk-type)" },
  { tag: [t.variableName, t.special(t.variableName)], color: "var(--tk-var)" },
  { tag: [t.attributeName, t.labelName], color: "var(--tk-arg)" },
  { tag: [t.meta, t.annotation], color: "var(--tk-dir)" },
  { tag: t.invalid, color: "var(--tk-err)" },
  // Markdown notes (markdownLanguage.ts).
  { tag: t.heading, color: "var(--tk-key)", fontWeight: "bold" },
  { tag: t.strong, fontWeight: "bold" },
  { tag: t.emphasis, fontStyle: "italic" },
  { tag: t.strikethrough, textDecoration: "line-through" },
  { tag: [t.monospace, t.link, t.url], color: "var(--tk-str)" },
  { tag: t.quote, color: "var(--tk-cm)" },
  { tag: [t.list, t.contentSeparator], color: "var(--tk-punct)" },
]);
