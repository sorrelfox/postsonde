import { useEffect, useId, useMemo, useRef, useState, type ReactNode } from "react";
import { openSearchPanel } from "@codemirror/search";
import { summarizeTests } from "../../lib/assertions";
import { summarizeExtractions } from "../../lib/extractions";
import type { IncrementalMode } from "../../lib/graphqlIncremental";
import { readGraphqlPayload, readGraphqlResponse, type QueryLocation } from "../../lib/graphqlResponse";
import { formatDurationMs, formatTimeoutMs, statusTone, type ResponseDTO } from "../../lib/http";
import { t as translate, type MessageKey } from "../../i18n";
import { useT } from "../../i18n/react";
import { IconDownload, IconFile, IconSearch, IconWarning } from "../common/icons";
import { onTabListKeyDown } from "../common/tabList";
import type { CodeEditorHandle } from "../editor/CodeEditor";
import { formatSize } from "../messagelog/format";
import { canFilter } from "../../lib/responseFilter";
import { proxyKind } from "../../lib/network";
import { GraphqlBody } from "./GraphqlBody";
import { IncrementalBody } from "./IncrementalBody";
import { ResponseBody, hasPrettyView, initialBodyView, type BodyView } from "./ResponseBody";
import { ResponsePreview } from "./ResponsePreview";
import { previewKind } from "../../lib/preview";
import { ResponseCookies } from "./ResponseCookies";
import { FilterBar, FilteredBody, useResponseFilter, type FilterPersistence } from "./ResponseFilter";
import { ResponseHeaders } from "./ResponseHeaders";
import { ResponseExtractions, extractionsLabel, extractionsTone } from "./ResponseExtractions";
import { ResponseTests, summaryLabel, summaryTone } from "./ResponseTests";
import { ResponseTimeline } from "./ResponseTimeline";
import styles from "./ResponseView.module.css";
import { useSaveBody } from "./saveBody";
import { useQuickAssertMenu } from "./QuickAssertMenu";
import { statusAssertion, type QuickAssertion } from "../../commands/quickAssert";

/** The save button's tooltip: the suggested name, and a warning for a body cut while receiving. */
function saveTitle(response: ResponseDTO): string {
  const bytes =
    response.contentEncoding !== ""
      ? translate("response.save.bytesDecompressed", { encoding: response.contentEncoding })
      : translate("response.save.bytesUnchanged");
  const title =
    response.fileName !== ""
      ? translate("response.save.titleAs", { name: response.fileName, bytes })
      : translate("response.save.title", { bytes });
  return response.bodyTruncated ? translate("response.save.titleCut", { title }) : title;
}

export interface ResponseViewProps {
  /** The response to show, as HTTPService sends it. */
  response: ResponseDTO;
  /**
   * Set for a GraphQL request's response (GQL-05): a GraphQL body (a JSON
   * object with `data` or `errors`) opens in the GraphQL view, and its
   * error locations call `onLocation`.
   */
  graphql?: { onLocation?: (location: QueryLocation) => void };
  /**
   * The request the response is for (HTTP-36): its body filter is
   * remembered per request in the workspace's local UI state. Without it
   * the filter lasts as long as the view.
   */
  filter?: FilterPersistence;
  /**
   * Set for a request's own response (TEST-06): adds the Tests tab with the
   * results of its assertions; `onJump` opens the request's Tests sub-tab
   * at an assertion (null: just opens it). A response whose request has
   * extraction rules (TEST-05) also gets the Variables tab, what they set;
   * `onExtractionJump` opens the request's Extract sub-tab at a rule.
   */
  tests?: {
    onJump?: (index: number | null) => void;
    onExtractionJump?: (index: number) => void;
    /** Adds an assertion picked from the response to the request (TEST-04): the body, headers and status offer it on right-click. */
    onQuickAssert?: (a: QuickAssertion) => void;
  };
  /** The request's name: a saved exchange (EXP-06) is named after it. */
  name?: string;
}

/** The body views: GraphQL only for a GraphQL response, Preview for an image or HTML body (HTTP-35). */
type View = BodyView | "graphql" | "preview";

type ResponseTab = "body" | "headers" | "cookies" | "tests" | "variables" | "timeline";

/** A tab of the response: its label, and the count badge with its accessible name. */
interface TabInfo<T extends string> {
  id: T;
  label: MessageKey;
  count: number;
  /** The badge's accessible name, "3 headers"; set when count > 0. */
  countLabel?: string;
}

/**
 * A received HTTP response, as in the mockup: the status line (HTTP-30:
 * status coloured by class, total time, body and header sizes, protocol,
 * the save-to-file button, HTTP-34, the save-exchange button, EXP-06, and the search button), then the Body tab with its Pretty / Raw switch
 * (HTTP-31, HTTP-33, ResponseBody) and, for an image or HTML body, Preview
 * (HTTP-35, ResponsePreview: sandboxed, no scripts, no network), the Headers tab (HTTP-32, with the
 * header count), the Cookies tab (HTTP-32, the parsed Set-Cookie
 * headers, ResponseCookies), with `tests` the Tests tab (TEST-06, the
 * results of the request's assertions with a passed/total badge,
 * ResponseTests), the Variables tab when its request has extraction rules
 * (TEST-05, what they set, ResponseExtractions) and the Timeline tab (DBG-01, the request as
 * sent and each hop's response head, ResponseTimeline). Above the body, the
 * filter bar narrows it with JSONPath or XPath (HTTP-36, ResponseFilter). It takes the response as props only, so every request
 * kind that ends in an HTTP response (REST, GraphQL) can show it. With
 * `graphql`, a GraphQL body also has the GraphQL view (GQL-05, GraphqlBody),
 * and an incremental response (GQL-08, @defer and @stream) has it with a
 * Merged / Parts switch (IncrementalBody).
 */
export function ResponseView({ response, graphql, filter: filterFor, tests: testsFor, name = "" }: ResponseViewProps) {
  const t = useT();
  const [chosenTab, setTab] = useState<ResponseTab>("body");
  const extractions = response.extractions ?? [];
  const hasVariables = testsFor !== undefined && extractions.length > 0;
  const tab: ResponseTab =
    (chosenTab === "tests" && testsFor === undefined) || (chosenTab === "variables" && !hasVariables) ? "body" : chosenTab;
  const isGraphql = graphql !== undefined;
  const gql = useMemo(() => (isGraphql ? readGraphqlResponse(response) : null), [isGraphql, response]);
  // An incremental response (GQL-08) has the GraphQL view whatever its body.
  const incremental = isGraphql ? (response.incremental ?? null) : null;
  const mergedGql = useMemo(() => (incremental !== null && incremental.merged !== "" ? readGraphqlPayload(incremental.merged) : null), [incremental]);
  const hasGraphqlView = gql !== null || incremental !== null;
  const preview = previewKind(response);
  // An image opens as the image: its hex dump says little.
  const initialView = (): View => (hasGraphqlView ? "graphql" : preview === "image" ? "preview" : initialBodyView(response));
  // The view is chosen per response: a new one opens in its initial view.
  // The partial responses of an incremental send and its final one count
  // as one.
  const [view, setView] = useState<{ for: ResponseDTO; view: View }>(() => ({ for: response, view: initialView() }));
  const sameSend = view.for === response || (view.for.incremental?.receiving === true && incremental !== null);
  const chosen = sameSend ? view.view : initialView();
  const bodyView: View =
    (chosen === "graphql" && !hasGraphqlView) || (chosen === "preview" && preview === null) ? initialBodyView(response) : chosen;
  useEffect(() => {
    // Follow the send's next responses, so the choice ends with its final one.
    if (sameSend && view.for !== response) setView({ for: response, view: view.view });
  }, [sameSend, view, response]);
  // Kept across responses: whoever hides extensions wants them hidden on the next send too.
  const [hideExtensions, setHideExtensions] = useState(false);
  // Kept across responses too: the merged result or the parts (GQL-08).
  const [incrementalMode, setIncrementalMode] = useState<IncrementalMode>("merged");
  const shownGql = incremental !== null ? (incrementalMode === "merged" ? mergedGql : null) : gql;
  const [searchWanted, setSearchWanted] = useState(0);
  const bodyRef = useRef<CodeEditorHandle>(null);
  const saving = useSaveBody(response);
  const filter = useResponseFilter(response, filterFor);
  const filterable = canFilter(response);
  const filtered = filterable && filter.active;
  const baseId = useId();
  const headers = response.headers ?? [];
  const cookies = response.cookies ?? [];
  const pretty = hasPrettyView(response);
  const tests = response.tests ?? [];
  const testSummary = summarizeTests(tests);
  const extractionSummary = summarizeExtractions(extractions);
  const tabs: TabInfo<ResponseTab>[] = [
    { id: "body", label: "response.tab.body", count: 0 },
    { id: "headers", label: "response.tab.headers", count: headers.length, countLabel: t("response.tab.headersCount", { count: headers.length }) },
    { id: "cookies", label: "response.tab.cookies", count: cookies.length, countLabel: t("response.tab.cookiesCount", { count: cookies.length }) },
    ...(testsFor !== undefined ? [{ id: "tests" as const, label: "response.tab.tests" as const, count: 0 }] : []),
    ...(hasVariables ? [{ id: "variables" as const, label: "response.tab.variables" as const, count: 0 }] : []),
    { id: "timeline", label: "response.tab.timeline", count: 0 },
  ];

  // Search (HTTP-33): CodeMirror's panel on the body editor, opened once
  // the Body tab shows it.
  useEffect(() => {
    if (searchWanted === 0) return;
    const editor = bodyRef.current?.view;
    if (editor) openSearchPanel(editor);
  }, [searchWanted]);
  const search = () => {
    setTab("body");
    // The preview has no text to search: show the body's.
    if (bodyView === "preview") setView({ for: response, view: initialBodyView(response) });
    setSearchWanted((n) => n + 1);
  };

  return (
    <div className={styles.view}>
      <StatusLine response={response} onQuickAssert={testsFor?.onQuickAssert}>
        {saving.canSave && response.size > 0 && (
          <button
            type="button"
            className={styles.iconButton}
            title={saveTitle(response)}
            aria-label={t("response.save.label")}
            disabled={saving.busy}
            onClick={saving.save}
          >
            <IconDownload size={13} />
          </button>
        )}
        {saving.canSave && (
          <button
            type="button"
            className={styles.iconButton}
            title={t("response.saveExchange.title")}
            aria-label={t("response.saveExchange.label")}
            disabled={saving.busy}
            onClick={() => saving.saveExchange(name)}
          >
            <IconFile size={13} />
          </button>
        )}
        {response.body !== "" && (
          <button
            type="button"
            className={styles.iconButton}
            title={t("response.search.bodyTitle")}
            aria-label={t("response.search.body")}
            onClick={search}
          >
            <IconSearch size={13} />
          </button>
        )}
      </StatusLine>
      {(response.warnings ?? []).map((w) => (
        <p key={w.code} className={styles.warning} role="note">
          <IconWarning size={13} />
          {w.message}
        </p>
      ))}
      {saving.note !== null && (
        <p
          className={saving.note.tone === "ok" ? styles.saved : styles.warning}
          role={saving.note.tone === "ok" ? "status" : "alert"}
        >
          {saving.note.tone === "error" && <IconWarning size={13} />}
          {saving.note.text}
          {saving.note.hint !== "" && ` ${saving.note.hint}`}
        </p>
      )}

      <div className={styles.tabs}>
        <div className={styles.tabList} role="tablist" aria-label={t("response.sections")} onKeyDown={onTabListKeyDown}>
          {tabs.map((info) => (
            <button
              key={info.id}
              type="button"
              role="tab"
              id={`${baseId}-tab-${info.id}`}
              className={styles.tab}
              aria-selected={info.id === tab}
              aria-controls={info.id === tab ? `${baseId}-panel` : undefined}
              tabIndex={info.id === tab ? 0 : -1}
              onClick={() => setTab(info.id)}
            >
              {t(info.label)}
              {info.count > 0 && (
                <span className={styles.count} aria-label={info.countLabel}>
                  {info.count}
                </span>
              )}
              {info.id === "tests" && testSummary.total > 0 && (
                <span className={styles.count} data-tone={summaryTone(testSummary)} aria-label={summaryLabel(testSummary)} data-tests-badge="">
                  {testSummary.passed}/{testSummary.total}
                </span>
              )}
              {info.id === "variables" && (
                <span
                  className={styles.count}
                  data-tone={extractionsTone(extractionSummary)}
                  aria-label={extractionsLabel(extractionSummary)}
                  data-variables-badge=""
                >
                  {extractionSummary.set}/{extractionSummary.total}
                </span>
              )}
            </button>
          ))}
        </div>
        {tab === "body" && (
          <div className={styles.tools}>
            <Charset response={response} />
            {!filtered && bodyView === "graphql" && incremental !== null && (
              <Segmented<IncrementalMode>
                label={t("response.incremental.view")}
                value={incrementalMode}
                options={[
                  { value: "merged", label: t("response.incremental.merged") },
                  { value: "parts", label: t("response.incremental.parts") },
                ]}
                onChange={setIncrementalMode}
              />
            )}
            {!filtered && bodyView === "graphql" && shownGql?.hasExtensions === true && (
              <button
                type="button"
                className={styles.toggle}
                aria-pressed={hideExtensions}
                title={t("response.graphql.hideExtensionsTitle")}
                onClick={() => setHideExtensions((h) => !h)}
              >
                {t("response.graphql.hideExtensions")}
              </button>
            )}
            {(pretty || incremental !== null || preview !== null) && !filtered && (
              <Segmented<View>
                label={t("response.bodyView")}
                value={bodyView}
                options={[
                  ...(hasGraphqlView ? [{ value: "graphql" as const, label: "GraphQL" }] : []),
                  ...(pretty ? [{ value: "pretty" as const, label: t("response.bodyView.pretty") }] : []),
                  { value: "raw", label: t("response.bodyView.raw") },
                  ...(preview !== null ? [{ value: "preview" as const, label: t("response.bodyView.preview") }] : []),
                ]}
                onChange={(v) => setView({ for: response, view: v })}
              />
            )}
          </div>
        )}
      </div>

      <div
        className={tab === "body" ? `${styles.section} ${styles.bodySection}` : styles.section}
        role="tabpanel"
        id={`${baseId}-panel`}
        aria-labelledby={`${baseId}-tab-${tab}`}
      >
        {tab === "body" && filterable && <FilterBar filter={filter} onSearch={search} />}
        {tab === "body" && filtered && <FilteredBody ref={bodyRef} filter={filter} />}
        {tab === "body" && !filtered && bodyView === "graphql" && incremental !== null && (
          <IncrementalBody
            ref={bodyRef}
            incremental={incremental}
            mode={incrementalMode}
            hideExtensions={hideExtensions}
            onLocation={graphql?.onLocation}
          />
        )}
        {tab === "body" && !filtered && bodyView === "graphql" && incremental === null && gql !== null && (
          <GraphqlBody ref={bodyRef} graphql={gql} hideExtensions={hideExtensions} onLocation={graphql?.onLocation} />
        )}
        {tab === "body" && !filtered && bodyView === "preview" && <ResponsePreview response={response} />}
        {tab === "body" && !filtered && bodyView !== "graphql" && bodyView !== "preview" && (
          <ResponseBody ref={bodyRef} response={response} view={bodyView} onQuickAssert={testsFor?.onQuickAssert} />
        )}
        {tab === "headers" && <ResponseHeaders headers={headers} onQuickAssert={testsFor?.onQuickAssert} />}
        {tab === "cookies" && <ResponseCookies cookies={cookies} />}
        {tab === "tests" && <ResponseTests tests={tests} onJump={testsFor?.onJump} />}
        {tab === "variables" && <ResponseExtractions extractions={extractions} onJump={testsFor?.onExtractionJump} />}
        {tab === "timeline" && <ResponseTimeline timeline={response.timeline} requestProxy={response.requestProxy} />}
      </div>
    </div>
  );
}

/**
 * The status line: 200 OK, 132 ms, 2.1 KB, 312 B headers, HTTP/2, then
 * timeout and redirects, a warning badge when TLS verification was off
 * (HTTP-23); `children` at the end.
 */
function StatusLine({
  response,
  children,
  onQuickAssert,
}: {
  response: ResponseDTO;
  children?: ReactNode;
  onQuickAssert?: (a: QuickAssertion) => void;
}) {
  const t = useT();
  const quick = useQuickAssertMenu(onQuickAssert);
  let timeout = t("response.status.noTimeout");
  if (response.timeoutMs > 0) {
    const value = formatTimeoutMs(response.timeoutMs);
    timeout = response.timeoutSource === "request" ? t("response.status.timeoutRequest", { value }) : t("response.status.timeoutDefault", { value });
  }
  const protocol = response.protocol !== "" ? response.protocol : response.proto;
  // NET-06: the proxy the final exchange went through.
  const hops = response.timeline?.hops ?? [];
  const via = hops[hops.length - 1]?.conn?.via ?? "";
  // The request's own proxy setting, when it has one: "none", or its URL.
  const own = response.requestProxy ?? "";
  return (
    <div className={styles.statusLine} data-response-status="">
      <span
        className={styles.status}
        data-tone={statusTone(response.statusCode)}
        tabIndex={quick.enabled ? 0 : undefined}
        title={quick.enabled ? t("response.quickAssert.hint") : undefined}
        onContextMenu={(e) =>
          quick.open(e, [
            {
              id: "status",
              label: t("response.quickAssert.statusEquals", { code: response.statusCode }),
              assertion: statusAssertion(response.statusCode),
            },
          ])
        }
      >
        <span className={styles.dot} aria-hidden="true" />
        {response.status !== "" ? response.status : String(response.statusCode)}
      </span>
      {quick.element}
      <Meta title={t("response.status.totalTime")}>{formatDurationMs(response.durationMs)}</Meta>
      <Meta
        title={
          response.contentEncoding !== ""
            ? t("response.status.bodyEncoded", { size: response.size, encoding: response.contentEncoding, decoded: response.bodySize })
            : t("response.status.body", { count: response.size })
        }
      >
        {formatSize(response.size)}
      </Meta>
      <Meta quiet title={t("response.status.headSizeTitle", { size: response.headerSize })}>
        {t("response.status.headSize", { size: formatSize(response.headerSize) })}
      </Meta>
      {protocol !== "" && (
        <Meta quiet sans title={t("response.status.protocol")}>
          {protocol}
        </Meta>
      )}
      {via !== "" && (
        <span
          className={`${styles.meta} ${styles.quiet} ${styles.sans}`}
          data-proxy={via}
          data-request-proxy={own !== "" ? "" : undefined}
          title={own !== "" ? t("response.status.proxyOwnTitle", { proxy: via }) : t("response.status.proxyTitle", { proxy: via })}
        >
          {t("response.status.proxy", { kind: proxyKind(via) })}
        </span>
      )}
      {via === "" && own === "none" && (
        <span className={`${styles.meta} ${styles.quiet} ${styles.sans}`} data-request-proxy="none" title={t("response.status.noProxyOwnTitle")}>
          {t("response.status.noProxyOwn")}
        </span>
      )}
      <Meta quiet sans>
        {timeout}
      </Meta>
      {response.redirects > 0 && (
        <Meta quiet sans title={response.finalUrl}>
          {t("response.status.redirects", { count: response.redirects })}
        </Meta>
      )}
      {response.insecureTls && (
        <span
          className={styles.tlsOff}
          data-tls-off={response.insecureTlsSource}
          title={
            response.insecureTlsSource === "settings"
              ? t("response.status.tlsOffSettings")
              : t("response.status.tlsOffRequest")
          }
        >
          <IconWarning size={12} />
          {t("response.status.tlsOff")}
        </span>
      )}
      {children !== undefined && <span className={styles.statusEnd}>{children}</span>}
    </div>
  );
}

export function Meta({ children, title, quiet = false, sans = false }: { children: ReactNode; title?: string; quiet?: boolean; sans?: boolean }) {
  const className = [styles.meta, quiet ? styles.quiet : "", sans ? styles.sans : ""].filter(Boolean).join(" ");
  return (
    <span className={className} title={title}>
      {children}
    </span>
  );
}

interface SegmentedProps<T extends string> {
  label: string;
  value: T;
  options: { value: T; label: string }[];
  onChange: (value: T) => void;
}

/** A small segmented switch (the mockup's Pretty / Raw). */
export function Segmented<T extends string>({ label, value, options, onChange }: SegmentedProps<T>) {
  return (
    <div className={styles.segmented} role="group" aria-label={label}>
      {options.map((o) => (
        <button key={o.value} type="button" className={styles.segment} aria-pressed={value === o.value} onClick={() => onChange(o.value)}>
          {o.label}
        </button>
      ))}
    </div>
  );
}

const CHARSET_SOURCES: Record<string, MessageKey> = {
  bom: "response.charset.bom",
  header: "response.charset.header",
  declaration: "response.charset.declaration",
  meta: "response.charset.meta",
  default: "response.charset.default",
};

/** The charset the body was decoded with, or HEX for a hex dump. */
function Charset({ response }: { response: ResponseDTO }) {
  const t = useT();
  if (response.body === "") return null;
  if (response.bodyHex) {
    return (
      <span className={styles.charset} title={t("response.charset.hex")}>
        HEX
      </span>
    );
  }
  if (response.charset === "") return null;
  const source = CHARSET_SOURCES[response.charsetSource];
  const title =
    source === undefined
      ? t("response.charset.decoded", { charset: response.charset })
      : t("response.charset.decodedFrom", { charset: response.charset, source: t(source) });
  return (
    <span className={styles.charset} title={title}>
      {response.charset}
    </span>
  );
}
