import {
  MATCHES_SCHEMA,
  describeResult,
  isInlineSchema,
  operatorInfo,
  summarizeTests,
  type SchemaErrorDTO,
  type StreamTestResultDTO,
  type TestResultDTO,
  type TestSummary,
} from "../../lib/assertions";
import { t, type MessageKey } from "../../i18n";
import { useT } from "../../i18n/react";
import { IconCheck, IconClose, IconWarning } from "../common/icons";
import styles from "./ResponseTests.module.css";

export interface ResponseTestsProps {
  /** The send's results, in the request's order (ResponseDTO.tests). */
  tests: readonly (TestResultDTO | StreamTestResultDTO)[];
  /**
   * Opens the request's Tests sub-tab at assertion `index` (its position in
   * the request's list), or just opens it with null.
   */
  onJump?: (index: number | null) => void;
}

const OUTCOME_LABELS: Readonly<Record<string, MessageKey>> = {
  passed: "response.tests.outcome.passed",
  failed: "response.tests.outcome.failed",
  invalid: "response.tests.outcome.invalid",
};

/** The tone of a summary: "ok" when all passed, "bad" when one failed, "warn" when only invalid ones did not pass. */
export function summaryTone(s: TestSummary): "ok" | "bad" | "warn" | "none" {
  if (s.total === 0) return "none";
  if (s.failed > 0) return "bad";
  if (s.invalid > 0) return "warn";
  return "ok";
}

/** "3 of 4 passed" */
export function summaryLabel(s: TestSummary): string {
  return t("response.tests.summary", { passed: s.passed, total: s.total });
}

/** The longest inline schema shown as the expected value. */
const MAX_SCHEMA_SHOWN = 300;

/** The expected value of a matches_schema result: the file path, or the inline schema, cut. */
function schemaShown(expected: string): string {
  const schema = expected.trim();
  if (!isInlineSchema(schema) || schema.length <= MAX_SCHEMA_SHOWN) return schema;
  return `${schema.slice(0, MAX_SCHEMA_SHOWN)}…`;
}

/** The ways a value does not match its JSON Schema (TEST-07): where in the value, what, and the schema keyword. */
function SchemaErrors({ errors }: { errors: readonly SchemaErrorDTO[] }) {
  const t = useT();
  return (
    <ul className={styles.schemaErrors} aria-label={t("response.tests.schemaErrors")}>
      {errors.map((e, i) => (
        <li key={`${e.schemaPath}-${e.path}-${i}`}>
          <code className={styles.schemaPath}>{e.path}</code> <span>{e.message}</span>{" "}
          <span className={styles.schemaKeyword} title={t("response.tests.schemaKeyword", { path: e.schemaPath })}>
            {e.schemaPath}
          </span>
        </li>
      ))}
    </ul>
  );
}

/**
 * The response's Tests tab (TEST-06): pass/fail per assertion of the
 * request, in its order, with the counts on top. Each result shows its
 * message, which names the expected and the actual value, and the two
 * values side by side; the assertion's description jumps to its row in the
 * request's Tests sub-tab (commands/assertionJump.ts). Failed assertions
 * never fail the send: the response is shown as it came.
 */
export function ResponseTests({ tests, onJump }: ResponseTestsProps) {
  const t = useT();
  const summary = summarizeTests(tests);
  if (tests.length === 0) {
    return (
      <div className={styles.empty}>
        <p>{t("response.tests.empty")}</p>
        {onJump !== undefined && (
          <button type="button" className={styles.linkButton} onClick={() => onJump(null)}>
            {t("response.tests.add")}
          </button>
        )}
      </div>
    );
  }
  return (
    <div className={styles.tests}>
      <p className={styles.summary} data-tone={summaryTone(summary)} role="status">
        <strong>{summaryLabel(summary)}</strong>
        {summary.failed > 0 && <span className={styles.failed}>{t("response.tests.failed", { n: summary.failed })}</span>}
        {summary.invalid > 0 && <span className={styles.invalid}>{t("response.tests.invalid", { n: summary.invalid })}</span>}
      </p>
      <ol className={styles.list} aria-label={t("response.tests.results")}>
        {tests.map((r, i) => {
          const outcome = OUTCOME_LABELS[r.outcome];
          const label = outcome === undefined ? r.outcome : t(outcome);
          const description = describeResult(r);
          const op = operatorInfo(r.op);
          const showExpected = r.outcome !== "invalid" && (op === undefined || op.expected);
          return (
            <li key={`${r.index}-${i}`} className={styles.item} data-outcome={r.outcome}>
              <span className={styles.mark} role="img" aria-label={label} title={label}>
                {r.outcome === "passed" ? <IconCheck size={13} /> : r.outcome === "invalid" ? <IconWarning size={13} /> : <IconClose size={13} />}
              </span>
              <div className={styles.body}>
                {onJump !== undefined ? (
                  <button
                    type="button"
                    className={styles.title}
                    title={t("response.tests.jumpTitle")}
                    onClick={() => onJump(r.index)}
                  >
                    {description}
                  </button>
                ) : (
                  <span className={styles.title}>{description}</span>
                )}
                <p className={styles.message}>{r.message}</p>
                {(showExpected || r.outcome !== "invalid") && (
                  <dl className={styles.values}>
                    {showExpected && (
                      <>
                        <dt>{t("response.tests.expected")}</dt>
                        <dd>{r.expected === "" ? "—" : r.op === MATCHES_SCHEMA ? schemaShown(r.expected) : r.expected}</dd>
                      </>
                    )}
                    {r.outcome !== "invalid" && (
                      <>
                        <dt>{t("response.tests.actual")}</dt>
                        <dd>{r.exists ? r.actual : t("response.tests.doesNotExist")}</dd>
                      </>
                    )}
                  </dl>
                )}
                {(r.schemaErrors ?? []).length > 0 && <SchemaErrors errors={r.schemaErrors ?? []} />}
                {r.code !== "" && <p className={styles.code}>{r.code}</p>}
              </div>
            </li>
          );
        })}
      </ol>
    </div>
  );
}
