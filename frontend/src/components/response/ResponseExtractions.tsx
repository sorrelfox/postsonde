import { describeSource, scopeInfo, summarizeExtractions, type ExtractionResultDTO, type ExtractionSummary } from "../../lib/extractions";
import { t, type MessageKey } from "../../i18n";
import { useT } from "../../i18n/react";
import { IconCheck, IconClose, IconInfo, IconWarning } from "../common/icons";
import tests from "./ResponseTests.module.css";
import styles from "./ResponseExtractions.module.css";

export interface ResponseExtractionsProps {
  /** The send's extraction results, in the request's order (ResponseDTO.extractions). */
  extractions: readonly ExtractionResultDTO[];
  /**
   * Opens the request's Extract sub-tab at rule `index` (its position in
   * the request's list).
   */
  onJump?: (index: number) => void;
}

const OUTCOME_LABELS: Readonly<Record<string, MessageKey>> = {
  set: "response.extractions.outcome.set",
  failed: "response.extractions.outcome.failed",
  invalid: "response.extractions.outcome.invalid",
  skipped: "response.extractions.outcome.skipped",
};

/** The tone of a summary: "ok" when every rule applied set its variable, "bad" when one failed. */
export function extractionsTone(s: ExtractionSummary): "ok" | "bad" | "none" {
  if (s.total === 0) return "none";
  return s.failed > 0 ? "bad" : "ok";
}

/** "2 of 3 variables set" */
export function extractionsLabel(s: ExtractionSummary): string {
  return t("response.extractions.summary", { set: s.set, total: s.total });
}

/**
 * The response's Variables tab (TEST-05): what each of the request's
 * extraction rules did with this response, in the request's order, with
 * the counts on top. A rule that set its variable shows the value (masked
 * for a secret one: Go never sends it), one that failed says why, and the
 * variable's name jumps to its rule in the request's Extract sub-tab
 * (commands/extractionJump.ts). A failed rule leaves its variable as it
 * was and does not fail the send; in a folder run it fails the result.
 */
export function ResponseExtractions({ extractions, onJump }: ResponseExtractionsProps) {
  const t = useT();
  const summary = summarizeExtractions(extractions);
  return (
    <div className={tests.tests}>
      <p className={tests.summary} data-tone={extractionsTone(summary)} role="status">
        <strong>{extractionsLabel(summary)}</strong>
        {summary.failed > 0 && <span className={tests.failed}>{t("response.extractions.failed", { n: summary.failed })}</span>}
        {summary.skipped > 0 && <span className={styles.skipped}>{t("response.extractions.skipped", { n: summary.skipped })}</span>}
      </p>
      <ol className={tests.list} aria-label={t("response.extractions.results")}>
        {extractions.map((r, i) => {
          const outcome = OUTCOME_LABELS[r.outcome];
          const label = outcome === undefined ? r.outcome : t(outcome);
          const name = `{{${r.variable}}}`;
          const scope = scopeInfo(r.scope);
          return (
            <li key={`${r.index}-${i}`} className={`${tests.item} ${styles.item}`} data-outcome={r.outcome}>
              <span className={tests.mark} role="img" aria-label={label} title={label}>
                {r.outcome === "set" ? (
                  <IconCheck size={13} />
                ) : r.outcome === "skipped" ? (
                  <IconInfo size={13} />
                ) : r.outcome === "invalid" ? (
                  <IconWarning size={13} />
                ) : (
                  <IconClose size={13} />
                )}
              </span>
              <div className={tests.body}>
                <span className={styles.head}>
                  {onJump !== undefined ? (
                    <button type="button" className={tests.title} title={t("response.extractions.jumpTitle")} onClick={() => onJump(r.index)}>
                      {name}
                    </button>
                  ) : (
                    <span className={tests.title}>{name}</span>
                  )}
                  <span className={styles.tag} title={scope?.title}>
                    {scope?.label ?? r.scope}
                  </span>
                  {r.secret && <span className={styles.tag}>{t("response.extractions.secret")}</span>}
                  {r.persist && r.scope !== "run" && <span className={styles.tag}>{t("response.extractions.kept")}</span>}
                </span>
                <dl className={tests.values}>
                  <dt>{t("response.extractions.from")}</dt>
                  <dd>{describeSource(r)}</dd>
                  {r.outcome === "set" && (
                    <>
                      <dt>{t("response.extractions.value")}</dt>
                      <dd>
                        {r.secret ? (
                          <span title={t("response.extractions.maskedTitle")}>{r.value}</span>
                        ) : r.value === "" ? (
                          t("response.extractions.emptyValue")
                        ) : (
                          r.value
                        )}
                        {r.valueClipped && <span className={styles.clipped}> {t("response.extractions.clipped")}</span>}
                      </dd>
                    </>
                  )}
                </dl>
                {r.outcome !== "set" && <p className={tests.message}>{r.message}</p>}
                {r.code !== "" && <p className={tests.code}>{r.code}</p>}
              </div>
            </li>
          );
        })}
      </ol>
    </div>
  );
}
