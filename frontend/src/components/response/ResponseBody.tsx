import { forwardRef, useRef, useState, type MouseEvent, type ReactNode } from "react";
import type { EditorView } from "@codemirror/view";
import { jsonAssertions, type QuickAssertion } from "../../commands/quickAssert";
import { jsonValueAt } from "../../lib/jsonPathAt";
import { readResponseBody, type BodyPageDTO, type ResponseDTO } from "../../lib/http";
import { beautifyJson, indentXml } from "../../lib/format";
import { formatError } from "../../lib/errors";
import { useT } from "../../i18n/react";
import { CodeEditor, type CodeEditorHandle } from "../editor/CodeEditor";
import type { EditorLanguage } from "../editor/languages";
import { formatSize } from "../messagelog/format";
import { useQuickAssertMenu } from "./QuickAssertMenu";
import styles from "./ResponseView.module.css";

export type BodyView = "pretty" | "raw";

export interface ResponseBodyProps {
  response: ResponseDTO;
  view: BodyView;
  /** Whose body it is, for the labels: a response's (the default) or a received request's (MOCK-08). */
  subject?: "response" | "request";
  /**
   * Adds an assertion to the request (TEST-04): a JSON body's context menu
   * then offers "Assert … equals this value" on the value under the
   * pointer (or the cursor, from the keyboard), with its JSONPath.
   */
  onQuickAssert?: (a: QuickAssertion) => void;
}

/** The body's languages that have a pretty view distinct from the raw one. */
export function hasPrettyView(response: ResponseDTO): boolean {
  return response.body !== "" && !response.bodyHex && response.bodyLanguage !== "text" && response.bodyLanguage !== "";
}

/** The view a new response opens in: raw when Go did not pretty-print it because of its size. */
export function initialBodyView(response: ResponseDTO): BodyView {
  return response.prettyState === "too_large" || response.prettyState === "incomplete" ? "raw" : "pretty";
}

/** A pretty-print asked for by the user (a body over Go's size limit): its state for one response. */
type OnDemand =
  | { status: "idle" }
  | { status: "running" }
  | { status: "done"; text: string }
  | { status: "failed"; message: string };

const LANGUAGES: Record<string, EditorLanguage> = { json: "json", xml: "xml", html: "html" };

/**
 * The response body (HTTP-31, HTTP-33) in a read-only code editor.
 * Pretty: JSON and XML as Go pretty-printed them (or as received, with a
 * note, when they were too large, cut short or invalid), highlighted;
 * HTML highlighted as received. Raw: the text Go decoded from the bytes
 * received, not highlighted. A body Go could not decode is a hex dump.
 * Search (Mod-F, or the search button) is CodeMirror's: text, case,
 * whole word or regular expression, every match highlighted.
 */
export const ResponseBody = forwardRef<CodeEditorHandle, ResponseBodyProps>(function ResponseBody(
  { response, view, subject = "response", onQuickAssert },
  ref,
) {
  const t = useT();
  const editorView = useRef<EditorView | null>(null);
  const quick = useQuickAssertMenu(onQuickAssert);
  const [onDemand, setOnDemand] = useState<{ for: ResponseDTO; state: OnDemand }>({ for: response, state: { status: "idle" } });
  const demand = onDemand.for === response ? onDemand.state : ({ status: "idle" } as const);
  const pager = useBodyPager(response);

  if (response.body === "") {
    return (
      <>
        <BodyNotes response={response} />
        <p className={styles.empty}>{subject === "request" ? t("response.body.emptyRequest") : t("response.body.emptyResponse")}</p>
      </>
    );
  }

  const pretty = view === "pretty" && hasPrettyView(response);
  // A body larger than what came with the response is paged (HTTP-37).
  let text = pager.paged ? pager.window.text : response.body;
  if (pretty && demand.status === "done") text = demand.text;
  else if (pretty && response.bodyPretty !== "") text = response.bodyPretty;
  const language: EditorLanguage = pretty ? (LANGUAGES[response.bodyLanguage] ?? "text") : "text";

  const prettyPrint = () => {
    setOnDemand({ for: response, state: { status: "running" } });
    const call = response.bodyLanguage === "xml" ? indentXml(response.body) : beautifyJson(response.body);
    call.then(
      (r) => {
        let state: OnDemand;
        if (r.valid) state = { status: "done", text: r.text };
        else if (r.error !== null) {
          state = { status: "failed", message: t("response.body.prettyError", { message: r.error.message, line: r.error.line, column: r.error.column }) };
        } else state = { status: "failed", message: t("response.body.prettyFailed") };
        setOnDemand((s) => (s.for === response ? { for: response, state } : s));
      },
      (err: unknown) => setOnDemand((s) => (s.for === response ? { for: response, state: { status: "failed", message: formatError(err) } } : s)),
    );
  };

  // Quick assertions (TEST-04) on a JSON body: the value under the pointer,
  // or at the cursor when the menu is opened from the keyboard.
  const onContextMenu = (e: MouseEvent) => {
    const ev = editorView.current;
    if (!quick.enabled || response.bodyLanguage !== "json" || response.bodyHex || ev === null) return;
    const pos = e.button === 2 ? ev.posAtCoords({ x: e.clientX, y: e.clientY }) : ev.state.selection.main.head;
    if (pos === null) return;
    const value = jsonValueAt(ev.state.doc.toString(), pos);
    if (value === null) return;
    const a = jsonAssertions(value);
    quick.open(e, [
      { id: "equals", label: t("response.quickAssert.equals", { path: value.path }), assertion: a.equals },
      { id: "exists", label: t("response.quickAssert.exists", { path: value.path }), assertion: a.exists },
      { id: "type", label: t("response.quickAssert.type", { path: value.path, type: value.kind }), assertion: a.type },
    ]);
  };

  return (
    <div className={styles.bodyPane} onContextMenu={onContextMenu}>
      <BodyNotes response={response} paged={pager.paged}>
        {pretty && <PrettyNote response={response} demand={demand} onPrettyPrint={prettyPrint} />}
        {pager.paged && <PagerBar pager={pager} />}
      </BodyNotes>
      <CodeEditor
        ref={ref}
        className={styles.bodyEditor}
        value={text}
        readOnly
        language={language}
        ariaLabel={subject === "request" ? t("response.body.requestLabel") : t("response.body.responseLabel")}
        onReady={(v) => {
          editorView.current = v;
        }}
      />
      {quick.element}
    </div>
  );
});

/** A part of the body shown (HTTP-37): its bytes start to end, decoded or as a hex dump. */
interface BodyWindow {
  readonly start: number;
  readonly end: number;
  readonly text: string;
}

/** How much of the body the view holds at most: "Load more" stops there and paging takes over. */
export const MAX_LOADED_TEXT_BYTES = 8 * 1024 * 1024;
export const MAX_LOADED_HEX_BYTES = 1024 * 1024;
/** One page, as Go reads it (MaxBodyText, MaxHexBytes). */
const PAGE_BYTES = 1024 * 1024;
const HEX_PAGE_BYTES = 64 * 1024;

export interface BodyPager {
  /** Whether the body is paged: it is kept by Go and larger than what came with the response. */
  readonly paged: boolean;
  readonly window: BodyWindow;
  /** The size of the whole body. */
  readonly size: number;
  readonly loading: boolean;
  readonly error: string | null;
  /** Appends the next page to the window, while it stays under the cap. */
  readonly canLoadMore: boolean;
  loadMore(): void;
  first(): void;
  previous(): void;
  next(): void;
  last(): void;
}

/**
 * Pages through a body larger than the response carries (HTTP-37): the
 * response has the first page; the others are read from Go's copy
 * (ReadBody) a page at a time, so the editor never holds more than
 * MAX_LOADED_TEXT_BYTES of it, whatever the body's size.
 */
function useBodyPager(response: ResponseDTO): BodyPager {
  const initial: BodyWindow = { start: 0, end: response.bodyShown, text: response.body };
  const [state, setState] = useState<{ for: ResponseDTO; window: BodyWindow; loading: boolean; error: string | null }>({
    for: response,
    window: initial,
    loading: false,
    error: null,
  });
  const current = state.for === response ? state : { for: response, window: initial, loading: false, error: null };
  const size = response.bodySize > 0 ? response.bodySize : response.size;
  const paged = response.bodyClipped && response.bodyId !== "" && response.body !== "";
  const hex = response.bodyHex;
  const page = hex ? HEX_PAGE_BYTES : PAGE_BYTES;
  const cap = hex ? MAX_LOADED_HEX_BYTES : MAX_LOADED_TEXT_BYTES;
  const w = current.window;

  const read = (offset: number, place: (page: BodyPageDTO) => BodyWindow) => {
    setState({ ...current, loading: true, error: null });
    readResponseBody(response.bodyId, Math.max(0, offset), response.charset, hex).then(
      (p) => setState((s) => (s.for === response ? { for: response, window: place(p), loading: false, error: null } : s)),
      (err: unknown) => setState((s) => (s.for === response ? { ...s, loading: false, error: formatError(err) } : s)),
    );
  };
  const replace = (p: BodyPageDTO): BodyWindow => ({ start: p.start, end: p.end, text: p.text });

  return {
    paged,
    window: w,
    size,
    loading: current.loading,
    error: current.error,
    canLoadMore: w.end < size && w.end - w.start + page <= cap,
    loadMore: () =>
      read(w.end, (p) => ({ start: w.start, end: p.end, text: hex && w.text !== "" && p.text !== "" ? `${w.text}\n${p.text}` : w.text + p.text })),
    first: () => setState({ for: response, window: initial, loading: false, error: null }),
    previous: () => read(w.start - page, replace),
    next: () => read(w.end, replace),
    last: () => read(size - page, replace),
  };
}

/** Where the view is in a paged body, and the buttons that move it (HTTP-37). */
function PagerBar({ pager }: { pager: BodyPager }) {
  const t = useT();
  const { window: w, size, loading } = pager;
  const atStart = w.start === 0;
  const atEnd = w.end >= size;
  return (
    <p className={styles.bodyNote} data-body-pager="">
      {t("response.pager.showing", { from: formatSize(w.start), to: formatSize(w.end), size: formatSize(size) })}{" "}
      {pager.canLoadMore && (
        <button type="button" className={styles.noteButton} onClick={pager.loadMore} disabled={loading}>
          {t("response.pager.loadMore")}
        </button>
      )}{" "}
      <button type="button" className={styles.noteButton} onClick={pager.first} disabled={loading || atStart}>
        {t("response.pager.start")}
      </button>{" "}
      <button type="button" className={styles.noteButton} onClick={pager.previous} disabled={loading || atStart}>
        {t("response.pager.previous")}
      </button>{" "}
      <button type="button" className={styles.noteButton} onClick={pager.next} disabled={loading || atEnd}>
        {t("response.pager.next")}
      </button>{" "}
      <button type="button" className={styles.noteButton} onClick={pager.last} disabled={loading || atEnd}>
        {t("response.pager.end")}
      </button>
      {loading && ` ${t("response.pager.loading")}`}
      {pager.error !== null && ` ${pager.error}`}
    </p>
  );
}

/** Why the body is shown the way it is: charset, hex, clipped, truncated, and the pretty view's state. */
function BodyNotes({ response, paged = false, children }: { response: ResponseDTO; paged?: boolean; children?: ReactNode }) {
  const t = useT();
  const notes: string[] = [];
  if (response.bodyNote !== "") notes.push(response.bodyNote);
  if (response.bodyBinary) {
    notes.push(
      response.charset !== "" ? t("response.body.invalidBytesIn", { charset: response.charset }) : t("response.body.invalidBytes"),
    );
  }
  if (response.bodyClipped && !paged) notes.push(t("response.body.clipped", { size: formatSize(response.bodyShown) }));
  if (response.bodyTruncated) {
    notes.push(
      response.bodyLimit > 0
        ? t("response.body.truncatedLimit", { limit: formatSize(response.bodyLimit) })
        : t("response.body.truncated"),
    );
  }
  if (notes.length === 0 && children === undefined) return null;
  return (
    <div className={styles.bodyNotes}>
      {notes.map((n) => (
        <p key={n} className={styles.bodyNote}>
          {n}
        </p>
      ))}
      {children}
    </div>
  );
}

function PrettyNote({ response, demand, onPrettyPrint }: { response: ResponseDTO; demand: OnDemand; onPrettyPrint: () => void }) {
  const t = useT();
  const kind = response.bodyLanguage === "xml" ? "XML" : "JSON";
  switch (response.prettyState) {
    case "invalid":
      return <p className={styles.bodyNote}>{t("response.body.asReceived", { reason: response.prettyError })}</p>;
    case "incomplete":
      return <p className={styles.bodyNote}>{t("response.body.cutShort")}</p>;
    case "too_large":
      if (demand.status === "done") return null;
      return (
        <p className={styles.bodyNote}>
          {demand.status === "failed"
            ? t("response.body.asReceived", { reason: demand.message })
            : t("response.body.tooLarge", { kind, size: formatSize(response.bodyShown) })}{" "}
          {demand.status !== "failed" && (
            <button type="button" className={styles.noteButton} onClick={onPrettyPrint} disabled={demand.status === "running"}>
              {demand.status === "running" ? t("response.body.prettyPrinting") : t("response.body.prettyPrint")}
            </button>
          )}
        </p>
      );
  }
  return null;
}
