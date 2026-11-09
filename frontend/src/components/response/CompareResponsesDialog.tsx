import { useEffect, useId, useMemo, useState, type ReactNode } from "react";
import { compareSources, parseSourceKey } from "../../commands/responseCompare";
import type { MessageKey } from "../../i18n";
import { useT } from "../../i18n/react";
import { toAppError } from "../../lib/errors";
import { FOLD_CONTEXT, foldRows, type DiffKind, type DiffLineDTO, type DiffRowDTO, type HeaderDiffDTO, type ResponseDiffDTO } from "../../lib/responseDiff";
import { useStores, type RequestId } from "../../stores";
import { Dialog } from "../common/Dialog";
import { IconWarning } from "../common/icons";
import styles from "./CompareResponses.module.css";

/** A response the selects offer: its select value (commands/responseCompare.ts sourceKey) and label. */
export interface CompareOption {
  readonly key: string;
  readonly label: string;
}

export interface CompareResponsesDialogProps {
  id: RequestId;
  /** Closed when false. */
  open: boolean;
  options: readonly CompareOption[];
  /** The select values shown first. */
  initialLeft: string;
  initialRight: string;
  onClose: () => void;
}

/** The visible marker of each kind; screen readers get the kind's name instead. */
const MARKERS: Readonly<Record<string, string>> = { added: "+", removed: "−", changed: "~", same: "" };

const KIND_LABELS: Readonly<Record<string, MessageKey>> = {
  added: "response.compare.kind.added",
  removed: "response.compare.kind.removed",
  changed: "response.compare.kind.changed",
  same: "response.compare.kind.same",
};

/**
 * Compare two responses (HTTP-40, docs/response-diff.md): two of the
 * request's latest response, earlier responses and examples, side by
 * side. Go compares them (commands/responseCompare.ts): the status line,
 * the header fields by name (added, removed, changed; unchanged ones on
 * request) and the body, JSON by path, pretty-printed, and other text by
 * line, with unchanged runs folded. Colour is never the only signal:
 * every changed line and header has a +, − or ~ marker and its kind for
 * screen readers.
 */
export function CompareResponsesDialog(props: CompareResponsesDialogProps) {
  if (!props.open) return null;
  return <Compare {...props} />;
}

function Compare({ id, options, initialLeft, initialRight, onClose }: CompareResponsesDialogProps) {
  const t = useT();
  const stores = useStores();
  const baseId = useId();
  const [left, setLeft] = useState(initialLeft);
  const [right, setRight] = useState(initialRight);
  const [diff, setDiff] = useState<ResponseDiffDTO | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);
  const [allHeaders, setAllHeaders] = useState(false);

  useEffect(() => {
    const l = parseSourceKey(left);
    const r = parseSourceKey(right);
    if (l === null || r === null) return;
    let live = true;
    setLoading(true);
    compareSources(stores, id, l, r).then(
      (d) => {
        if (!live) return;
        setDiff(d);
        setError(null);
        setLoading(false);
      },
      (err: unknown) => {
        if (!live) return;
        const e = toAppError(err);
        setError(e.hint ? `${e.message} ${e.hint}` : e.message);
        setDiff(null);
        setLoading(false);
      },
    );
    return () => {
      live = false;
    };
  }, [stores, id, left, right]);

  const select = (value: string, onChange: (v: string) => void, label: string, suffix: string) => (
    <div className={styles.pick}>
      <label className={styles.label} htmlFor={`${baseId}-${suffix}`}>
        {label}
      </label>
      <select id={`${baseId}-${suffix}`} className={styles.select} value={value} onChange={(e) => onChange(e.target.value)}>
        {options.map((o) => (
          <option key={o.key} value={o.key}>
            {o.label}
          </option>
        ))}
      </select>
    </div>
  );

  return (
    <Dialog open title={t("response.compare.title")} width={1100} onCancel={onClose} bodyClassName={styles.body}>
      <div className={styles.picks}>
        {select(left, setLeft, t("response.compare.left"), "left")}
        <button
          type="button"
          className={styles.swap}
          onClick={() => {
            setLeft(right);
            setRight(left);
          }}
        >
          {t("response.compare.swap")}
        </button>
        {select(right, setRight, t("response.compare.right"), "right")}
      </div>
      {left === right && <p className={styles.hint}>{t("response.compare.sameChosen")}</p>}
      {error !== null && (
        <p className={styles.error} role="alert">
          {t("response.compare.failed", { error })}
        </p>
      )}
      {loading && diff === null && error === null && (
        <p className={styles.hint} role="status">
          {t("response.compare.loading")}
        </p>
      )}
      {diff !== null && (
        <div className={styles.result} aria-busy={loading || undefined}>
          <p className={styles.status} data-kind={diff.status.changed ? "changed" : "same"}>
            {diff.status.changed ? (
              <>
                <Marker kind="changed" />
                {t("response.compare.statusChanged", { left: diff.status.left, right: diff.status.right })}
              </>
            ) : (
              t("response.compare.statusSame", { status: diff.status.left })
            )}
          </p>
          <Headers headers={diff.headers ?? []} diff={diff} all={allHeaders} onAll={setAllHeaders} baseId={baseId} />
          <Body diff={diff} baseId={baseId} />
        </div>
      )}
    </Dialog>
  );
}

/** The +, − or ~ of a kind, and its name for screen readers. */
function Marker({ kind }: { kind: string }) {
  const t = useT();
  const labelKey = KIND_LABELS[kind];
  if (kind === "same" || labelKey === undefined) return null;
  return (
    <>
      <span className={styles.marker} data-kind={kind} aria-hidden="true">
        {MARKERS[kind]}
      </span>
      <span className={styles.srOnly}>{t(labelKey)} </span>
    </>
  );
}

function Headers({
  headers,
  diff,
  all,
  onAll,
  baseId,
}: {
  headers: readonly HeaderDiffDTO[];
  diff: ResponseDiffDTO;
  all: boolean;
  onAll: (all: boolean) => void;
  baseId: string;
}) {
  const t = useT();
  const shown = all ? headers : headers.filter((h) => h.kind !== "same");
  const differ = diff.headersAdded + diff.headersRemoved + diff.headersChanged;
  return (
    <section className={styles.section} aria-labelledby={`${baseId}-headers`}>
      <div className={styles.sectionHead}>
        <h3 className={styles.heading} id={`${baseId}-headers`}>
          {t("response.compare.headers")}
        </h3>
        <span className={styles.counts}>
          {differ === 0
            ? t("response.compare.headersSame")
            : t("response.compare.counts", { added: diff.headersAdded, removed: diff.headersRemoved, changed: diff.headersChanged })}
        </span>
        <label className={styles.toggle}>
          <input type="checkbox" checked={all} onChange={(e) => onAll(e.target.checked)} />
          {t("response.compare.showUnchanged")}
        </label>
      </div>
      {shown.length > 0 && (
        <div className={styles.tableWrap}>
          <table className={styles.headers}>
            <caption className={styles.srOnly}>{t("response.compare.headers")}</caption>
            <thead>
              <tr>
                <th scope="col">{t("response.compare.headerName")}</th>
                <th scope="col">{t("response.compare.left")}</th>
                <th scope="col">{t("response.compare.right")}</th>
              </tr>
            </thead>
            <tbody>
              {shown.map((h) => (
                <tr key={h.name.toLowerCase()} data-kind={h.kind}>
                  <th scope="row">
                    <Marker kind={h.kind} />
                    {h.name}
                  </th>
                  <td data-side="left">{values(h.left)}</td>
                  <td data-side="right">{values(h.right)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function values(list: readonly string[] | null): ReactNode {
  const v = list ?? [];
  if (v.length === 0) return <span className={styles.none}>—</span>;
  return v.map((x, i) => (
    <span key={i} className={styles.value}>
      {x}
    </span>
  ));
}

function Body({ diff, baseId }: { diff: ResponseDiffDTO; baseId: string }) {
  const t = useT();
  const body = diff.body;
  const rows = body.rows ?? [];
  const changes = body.changes ?? [];
  const notices = body.notices ?? [];
  const [expanded, setExpanded] = useState<ReadonlySet<number>>(new Set());
  // A new comparison folds again.
  useEffect(() => setExpanded(new Set()), [diff]);
  const shown = useMemo(() => foldRows(rows, FOLD_CONTEXT, expanded), [rows, expanded]);

  let summary: string;
  if (body.identical) summary = body.mode === "json" ? t("response.compare.bodySameJson") : t("response.compare.bodySame");
  else if (body.mode === "json") summary = t("response.compare.bodyJson", { added: body.added, removed: body.removed, changed: body.changed });
  else if (body.mode === "text") summary = t("response.compare.bodyText", { added: body.added, removed: body.removed, changed: body.changed });
  else summary = t("response.compare.bodyDiffer");

  return (
    <section className={styles.section} aria-labelledby={`${baseId}-body`}>
      <div className={styles.sectionHead}>
        <h3 className={styles.heading} id={`${baseId}-body`}>
          {t("response.compare.body")}
        </h3>
        <span className={styles.counts}>{summary}</span>
      </div>
      {notices.length > 0 && (
        <ul className={styles.notices} aria-label={t("response.compare.notices")}>
          {notices.map((n, i) => (
            <li key={`${n.code}-${i}`}>
              <IconWarning size={12} />
              <span>{n.message}</span>
            </li>
          ))}
        </ul>
      )}
      {body.mode === "json" && changes.length > 0 && (
        <details className={styles.changes}>
          <summary>{t("response.compare.changesCount", { count: changes.length })}</summary>
          <ul aria-label={t("response.compare.changes")}>
            {changes.map((c, i) => (
              <li key={`${c.path}-${i}`} data-kind={c.kind}>
                <Marker kind={c.kind} />
                <code className={styles.path}>{c.path}</code>{" "}
                {c.kind === "changed"
                  ? c.leftType !== ""
                    ? t("response.compare.changeType", { left: c.left, right: c.right, leftType: c.leftType, rightType: c.rightType })
                    : t("response.compare.changeValue", { left: c.left, right: c.right })
                  : c.kind === "added"
                    ? t("response.compare.changeAdded", { value: c.right })
                    : t("response.compare.changeRemoved", { value: c.left })}
              </li>
            ))}
          </ul>
        </details>
      )}
      {rows.length > 0 && (
        <div className={styles.tableWrap}>
          <table className={styles.diff}>
            <caption className={styles.srOnly}>{t("response.compare.sideBySide")}</caption>
            <thead>
              <tr>
                <th scope="col" className={styles.num}>
                  <span className={styles.srOnly}>{t("response.compare.leftLine")}</span>
                </th>
                <th scope="col">{t("response.compare.left")}</th>
                <th scope="col" className={styles.num}>
                  <span className={styles.srOnly}>{t("response.compare.rightLine")}</span>
                </th>
                <th scope="col">{t("response.compare.right")}</th>
              </tr>
            </thead>
            <tbody>
              {shown.map((s) =>
                s.kind === "fold" ? (
                  <tr key={`fold-${s.start}`} className={styles.fold}>
                    <td colSpan={4}>
                      <button type="button" onClick={() => setExpanded((prev) => new Set([...prev, s.start]))}>
                        {t("response.compare.unfold", { count: s.count })}
                      </button>
                    </td>
                  </tr>
                ) : (
                  <DiffRow key={s.index} row={s.row} />
                ),
              )}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function DiffRow({ row }: { row: DiffRowDTO }) {
  const kind = row.kind as DiffKind;
  const cell = (line: DiffLineDTO | null, side: "left" | "right") => {
    // A row's marker goes on the side that has the difference.
    const marked = kind === "changed" || (kind === "removed" && side === "left") || (kind === "added" && side === "right");
    return (
      <>
        <td className={styles.num} data-side={side}>
          {line?.n ?? ""}
        </td>
        <td className={styles.code} data-side={side} data-empty={line === null || undefined}>
          {line !== null && (
            <>
              {marked ? <Marker kind={kind} /> : <span className={styles.marker} aria-hidden="true" />}
              <span className={styles.text}>{line.text}</span>
            </>
          )}
        </td>
      </>
    );
  };
  return (
    <tr data-kind={kind} title={row.path || undefined}>
      {cell(row.left, "left")}
      {cell(row.right, "right")}
    </tr>
  );
}
