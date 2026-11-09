import {
  forwardRef,
  useContext,
  useImperativeHandle,
  useLayoutEffect,
  useRef,
  type MutableRefObject,
} from "react";
import { isolateHistory } from "@codemirror/commands";
import { Annotation, EditorState, type Extension } from "@codemirror/state";
import { EditorView } from "@codemirror/view";
import { currentTheme, subscribeTheme } from "../../styles/theme";
import { useEditorStateScope } from "./editorHistory";
import {
  appearanceExtension,
  buildExtensions,
  createCompartments,
  labelExtension,
  modeExtension,
  preferencesFor,
  type EditorCompartments,
  type EditorOptions,
} from "./extensions";
import { languageExtension, type EditorLanguage } from "./languages";
import { currentEditorPreferences, subscribeEditorPreferences } from "./preferences";
import { setTemplateVariables, templateVariablesOf } from "./templateVariables";
import { TemplateVariablesContext } from "../common/TemplateVariables";
import styles from "./CodeEditor.module.css";

// Thin React wrapper around a CodeMirror 6 EditorView.
//
// CodeMirror owns the text while the editor is open (docs/frontend.md):
// the view is created once per mount and never re-created on render.
// `value` is not a controlled value. The editor takes it:
// - when `docKey` changes: a fresh state (new document, empty undo history),
//   e.g. when another request's tab becomes active;
// - when `value` differs from the editor's text: the text is replaced, e.g. a
//   new response or a beautify. Passing back what `onChange` reported is a
//   no-op, so a parent may keep `value` in sync with a draft. The
//   replacement is an undo step of its own.
// `readOnly`, `language` and `ariaLabel` switch at runtime through compartments,
// and so does light/dark, which follows the app theme (styles/theme.ts), and
// the editor settings (font, tab size, word wrap, line numbers, bracket
// matching; preferences.ts, UI-08), which follow the settings file.
// `singleLine` (a one-line field such as the URL, singleLine.ts) and
// `placeholder` are fixed at mount.
//
// `{{variable}}` chips (templateVariables.ts, ENV-11): single-line fields
// always draw them; a code editor does when it mounts inside a
// TemplateVariablesProvider (a request's editor, not the response views).
// The provider's resolved variables are passed to the editor whenever they
// change, so unresolved references turn red.
//
// Inside an EditHistoryScope (the tab panel, editorHistory.tsx) an editable
// editor keeps its state (text, selection, undo history) there when it
// unmounts, and takes it back when it mounts again, so undo works across
// tab and sub-tab switches (NAV-07). A state that is taken back comes with
// the compartments and change listener it was built with (a Session).

/** Marks transactions that apply `value` from the parent; they don't fire onChange. */
const fromParent = Annotation.define<boolean>();

export interface CodeEditorProps {
  /** The document. Taken on mount, when `docKey` changes, and when it differs from the editor's text. */
  value: string;
  /** Change it to load `value` as a new document with a fresh undo history. */
  docKey?: string | number;
  /** Called with the full text after each change made in the editor (not for `value` updates). */
  onChange?: (doc: string) => void;
  /** Response views: selectable, searchable and foldable, but not editable. */
  readOnly?: boolean;
  language?: EditorLanguage;
  /** Accessible name of the text area. */
  ariaLabel: string;
  className?: string;
  /** A one-line field (no line breaks, `{{variables}}` highlighted) instead of a code editor. Fixed at mount. */
  singleLine?: boolean;
  /** Shown while a single-line field is empty. Fixed at mount. */
  placeholder?: string;
  /** Called with each view this component creates (twice on mount in React StrictMode dev). */
  onReady?: (view: EditorView) => void;
  /**
   * Under which name the editor keeps its state in the EditHistoryScope
   * around it (NAV-07). By default an editor that is editable when it
   * mounts keeps it under its `ariaLabel` (and `docKey`), which is unique
   * within a request's editor; null keeps nothing. Read at mount and when
   * `docKey` changes.
   */
  historyKey?: string | null;
}

export interface CodeEditorHandle {
  /** The live view, or null before mount and after unmount. */
  readonly view: EditorView | null;
  focus(): void;
}

interface LatestProps {
  value: string;
  readOnly: boolean;
  language: EditorLanguage;
  ariaLabel: string;
  singleLine: boolean;
  placeholder?: string;
  /** Chips for `{{variables}}` in a code editor; fixed at mount. */
  templates: boolean;
  onChange?: (doc: string) => void;
  onReady?: (view: EditorView) => void;
  historyKey?: string | null;
}

/** What an editor state is built with: its compartments and its change listener. */
interface Session {
  readonly compartments: EditorCompartments;
  readonly listener: Extension;
  /** The props of the component showing the state now; the listener reads them. */
  latest: MutableRefObject<LatestProps>;
}

/** What an editor keeps in the edit-history scope. */
interface KeptEditor {
  readonly state: EditorState;
  readonly session: Session;
}

function createSession(latest: MutableRefObject<LatestProps>): Session {
  const session: { compartments: EditorCompartments; listener: Extension; latest: MutableRefObject<LatestProps> } = {
    compartments: createCompartments(),
    listener: [],
    latest,
  };
  session.listener = EditorView.updateListener.of((update) => {
    if (!update.docChanged) return;
    if (update.transactions.every((tr) => tr.annotation(fromParent))) return;
    session.latest.current.onChange?.(update.state.doc.toString());
  });
  return session;
}

function keyFor(p: LatestProps, docKey: string | number | undefined): string | null {
  if (p.historyKey !== undefined) return p.historyKey;
  if (p.readOnly) return null;
  return `${p.singleLine ? "line" : "doc"}:${p.ariaLabel}:${docKey ?? ""}`;
}

export const CodeEditor = forwardRef<CodeEditorHandle, CodeEditorProps>(
  function CodeEditor(
    {
      value,
      docKey,
      onChange,
      readOnly = false,
      language = "text",
      ariaLabel,
      className,
      singleLine = false,
      placeholder,
      onReady,
      historyKey,
    },
    ref,
  ) {
    const hostRef = useRef<HTMLDivElement>(null);
    const viewRef = useRef<EditorView | null>(null);
    const sessionRef = useRef<Session | null>(null);
    const docKeyRef = useRef(docKey);
    // The edit-history scope as it was at mount (EditHistoryScope remounts
    // its editors when it changes), and the key this editor holds in it.
    const scope = useEditorStateScope();
    const scopeRef = useRef(scope);
    const heldKeyRef = useRef<string | null>(null);
    // Brings the view's theme and editor settings up to date; set at mount.
    const syncRef = useRef<() => void>(() => {});
    const templateContext = useContext(TemplateVariablesContext);
    const templatesRef = useRef(templateContext !== null);
    const templates = templatesRef.current;

    // Latest props, read by the long-lived view and listener without
    // re-creating them. Updated first, before the effects below run.
    const latest = useRef<LatestProps>({ value, readOnly, language, ariaLabel, singleLine, placeholder, templates, onChange, onReady, historyKey });
    useLayoutEffect(() => {
      latest.current = { value, readOnly, language, ariaLabel, singleLine, placeholder, templates, onChange, onReady, historyKey };
    });

    const freshState = (doc: string, options: EditorOptions): KeptEditor => {
      const session = createSession(latest);
      const state = EditorState.create({
        doc,
        extensions: buildExtensions(session.compartments, options, session.listener),
      });
      return { state, session };
    };

    // The state kept under this editor's key, or a fresh one. Holds the key.
    const claimState = (key: string | null, p: LatestProps): KeptEditor => {
      const s = scopeRef.current;
      heldKeyRef.current = null;
      if (s !== null && key !== null && s.acquire(key)) {
        heldKeyRef.current = key;
        const kept = s.get(key) as KeptEditor | undefined;
        if (kept !== undefined) {
          kept.session.latest = latest;
          return kept;
        }
      }
      return freshState(p.value, p);
    };

    // Keeps the view's state under the held key and frees it.
    const releaseState = (view: EditorView) => {
      const s = scopeRef.current;
      const key = heldKeyRef.current;
      const session = sessionRef.current;
      heldKeyRef.current = null;
      if (s === null || key === null || session === null) return;
      const kept: KeptEditor = { state: view.state, session };
      s.release(key, kept);
    };

    // Create the view once per mount; destroy it on unmount.
    useLayoutEffect(() => {
      scopeRef.current = scope;
      const p = latest.current;
      const initial = claimState(keyFor(p, docKeyRef.current), p);
      sessionRef.current = initial.session;
      const view = new EditorView({ parent: hostRef.current!, state: initial.state });
      viewRef.current = view;
      // Follow the app theme (UI-04): light/dark switches with no new view.
      const followTheme = () => {
        const c = sessionRef.current!.compartments;
        const next = appearanceExtension(currentTheme());
        if (c.appearance.get(view.state) !== next) {
          view.dispatch({ effects: c.appearance.reconfigure(next) });
        }
      };
      // Follow the editor settings (UI-08). The font lives in CSS
      // variables, so the view is measured again for new metrics.
      const followPreferences = () => {
        const c = sessionRef.current!.compartments;
        const next = preferencesFor(currentEditorPreferences(), latest.current.singleLine);
        if (c.preferences.get(view.state) !== next) {
          view.dispatch({ effects: c.preferences.reconfigure(next) });
        }
        view.requestMeasure();
      };
      syncRef.current = () => {
        followTheme();
        followPreferences();
      };
      // A kept state may have been made in the other theme or with other settings.
      syncRef.current();
      const stopTheme = subscribeTheme(followTheme);
      const stopPreferences = subscribeEditorPreferences(followPreferences);
      p.onReady?.(view);
      return () => {
        syncRef.current = () => {};
        stopTheme();
        stopPreferences();
        releaseState(view);
        view.destroy();
        viewRef.current = null;
      };
      // Mount only: later prop changes go through the effects below.
    }, []);

    // A new docKey: new document and fresh history (or the kept state of that document), same view.
    useLayoutEffect(() => {
      const view = viewRef.current;
      if (!view || docKeyRef.current === docKey) return;
      docKeyRef.current = docKey;
      releaseState(view);
      const p: LatestProps = { ...latest.current, value, readOnly, language, ariaLabel, singleLine, placeholder };
      const next = claimState(keyFor(p, docKey), p);
      sessionRef.current = next.session;
      view.setState(next.state);
      syncRef.current();
      // Only on docKey: the other props are read as they are right now.
    }, [docKey]);

    // `value` changed from outside: replace the text if it differs. Only the
    // part that differs is replaced, so the selection outside it stays put.
    // Also runs on mount and after a docKey change, for a kept state whose
    // text is not the value any more (e.g. undone while the editor was away).
    useLayoutEffect(() => {
      const view = viewRef.current;
      if (!view) return;
      const doc = view.state.doc.toString();
      if (doc === value) return;
      let from = 0;
      const max = Math.min(doc.length, value.length);
      while (from < max && doc.charCodeAt(from) === value.charCodeAt(from)) from++;
      let tail = 0;
      while (
        tail < max - from &&
        doc.charCodeAt(doc.length - 1 - tail) === value.charCodeAt(value.length - 1 - tail)
      ) {
        tail++;
      }
      view.dispatch({
        changes: { from, to: doc.length - tail, insert: value.slice(from, value.length - tail) },
        annotations: [fromParent.of(true), isolateHistory.of("full")],
      });
    }, [value, docKey]);

    // The request's resolved variables, for the chips. Also after a docKey
    // change: a kept state may hold an older set.
    const templateVariables = templateContext?.variables ?? null;
    useLayoutEffect(() => {
      const view = viewRef.current;
      if (!view) return;
      const current = templateVariablesOf(view.state);
      if (current === undefined || current === templateVariables) return;
      view.dispatch({ effects: setTemplateVariables.of(templateVariables) });
    }, [templateVariables, docKey]);

    // Runtime reconfiguration, without re-creating the view. Also runs after
    // a docKey change, for a kept state made with other settings.
    useLayoutEffect(() => {
      const view = viewRef.current;
      const c = sessionRef.current?.compartments;
      if (!view || !c) return;
      const effects = [];
      const mode = modeExtension(readOnly, latest.current.singleLine);
      if (c.mode.get(view.state) !== mode) effects.push(c.mode.reconfigure(mode));
      const lang = languageExtension(language);
      if (c.language.get(view.state) !== lang) effects.push(c.language.reconfigure(lang));
      if (view.contentDOM.getAttribute("aria-label") !== ariaLabel) {
        effects.push(c.label.reconfigure(labelExtension(ariaLabel)));
      }
      if (effects.length > 0) view.dispatch({ effects });
    }, [readOnly, language, ariaLabel, docKey]);

    useImperativeHandle(
      ref,
      () => ({
        get view() {
          return viewRef.current;
        },
        focus() {
          viewRef.current?.focus();
        },
      }),
      [],
    );

    return (
      <div
        ref={hostRef}
        className={className ? `${styles.root} ${className}` : styles.root}
        data-shortcut-scope={singleLine ? undefined : "editor"}
      />
    );
  },
);
