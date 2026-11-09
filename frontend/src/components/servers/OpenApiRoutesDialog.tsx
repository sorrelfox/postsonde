import { useEffect, useId, useState } from "react";
import { toMockConfig, toMockConfigDTO } from "../../commands/servers";
import { t as translate } from "../../i18n";
import { useT } from "../../i18n/react";
import { toAppError } from "../../lib/errors";
import { generateMockRoutes, type MockOpenAPIDiagnosticDTO, type MockOpenAPIResultDTO } from "../../lib/servers";
import type { MockConfig } from "../../stores";
import { Dialog } from "../common/Dialog";
import { IconInfo, IconWarning } from "../common/icons";
import { methodBadge } from "../tree/badges";
import styles from "./OpenApiRoutes.module.css";

/** How long the prefix must stay unchanged before the preview is made again, in ms. */
export const PREFIX_DELAY_MS = 300;

export type OpenApiMode = "merge" | "replace";

export interface OpenApiRoutesDialogProps {
  /** The OpenAPI file; null: closed. */
  path: string | null;
  /** The server's routes now. */
  config: MockConfig;
  /** Apply: the routes with the generated ones, and the first added route's id. */
  onApply: (config: MockConfig, firstAdded: string | null) => void;
  onClose: () => void;
}

/**
 * The "Generate routes from OpenAPI" preview (MOCK-09, docs/frontend.md):
 * the routes Go generated from the chosen file (ServersService
 * GenerateMockRoutes), the path prefix, merge or replace, and every note
 * about the document. Nothing changes until Apply; the routes then go
 * through the editor's usual update.
 */
export function OpenApiRoutesDialog(props: OpenApiRoutesDialogProps) {
  if (props.path === null) return null;
  return <Preview key={props.path} {...props} path={props.path} />;
}

function Preview({ path, config, onApply, onClose }: OpenApiRoutesDialogProps & { path: string }) {
  const t = useT();
  const baseId = useId();
  const [mode, setMode] = useState<OpenApiMode>("merge");
  // The prefix typed (null: untouched, so Go uses the document's first
  // server) and the one the preview was asked with.
  const [prefix, setPrefix] = useState<string | null>(null);
  const [sent, setSent] = useState<string | null>(null);
  const [result, setResult] = useState<MockOpenAPIResultDTO | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [loading, setLoading] = useState(true);

  // A typed prefix is sent once typing pauses.
  useEffect(() => {
    if (prefix === null || prefix === sent) return;
    const timer = setTimeout(() => setSent(prefix), PREFIX_DELAY_MS);
    return () => clearTimeout(timer);
  }, [prefix, sent]);

  useEffect(() => {
    setLoading(true);
    const call = generateMockRoutes({ path, prefix: sent, mode, config: toMockConfigDTO(config) });
    let live = true;
    call.then(
      (res) => {
        if (!live) return;
        setResult(res);
        setError(null);
        setLoading(false);
      },
      (err: unknown) => {
        if (!live) return;
        const e = toAppError(err);
        setError(e.hint ? `${e.message} ${e.hint}` : e.message);
        setResult(null);
        setLoading(false);
      },
    );
    return () => {
      live = false;
      call.cancel();
    };
  }, [path, sent, mode, config]);

  const pending = loading || (prefix !== null && prefix !== sent);
  const shownPrefix = prefix ?? result?.prefix ?? "";
  const canApply = result !== null && !pending && (mode === "replace" || result.added > 0);
  const apply = () => {
    if (!canApply || result === null) return;
    const next = toMockConfig(result.config);
    if (next === null) return;
    const first = (result.routes ?? []).find((r) => r.status === "added")?.id ?? null;
    onApply(next, first);
  };

  const routes = result?.routes ?? [];
  const diagnostics = result?.diagnostics ?? [];
  const problems = diagnostics.filter((d) => d.severity !== "info");
  const notes = diagnostics.filter((d) => d.severity === "info");
  const fileName = path.split(/[\\/]/).pop() ?? path;

  return (
    <Dialog
      open
      title={t("servers.openapi.title")}
      width={720}
      confirmLabel={mode === "replace" ? t("servers.openapi.replace") : t("servers.openapi.add", { count: result?.added ?? 0 })}
      confirmDisabled={!canApply}
      onCancel={onClose}
      onConfirm={apply}
    >
      <div className={styles.body} data-testid="openapi-routes">
        <p className={styles.source}>
          <span className={styles.file} title={path}>
            {fileName}
          </span>
          {result !== null && (
            <span className={styles.meta}>
              {[result.title, result.version].filter(Boolean).join(" ")}
              {" · "}
              {/* i18n-ignore: specification names and versions */}
              {result.swagger ? `Swagger ${result.swagger}` : `OpenAPI ${result.openapi}`}
            </span>
          )}
        </p>

        <div className={styles.row}>
          <label className={styles.label} htmlFor={`${baseId}-prefix`}>
            {t("servers.openapi.prefix")}
          </label>
          <input
            id={`${baseId}-prefix`}
            className={styles.input}
            spellCheck={false}
            placeholder="/"
            value={shownPrefix}
            onChange={(e) => setPrefix(e.target.value)}
          />
          {result !== null && shownPrefix !== result.defaultPrefix && (
            <button type="button" className={styles.link} onClick={() => setPrefix(result.defaultPrefix)}>
              {t("servers.openapi.useDocument", { prefix: result.defaultPrefix || "/" })}
            </button>
          )}
        </div>
        <p className={styles.hint}>{t("servers.openapi.prefixHint")}</p>

        <div className={styles.modes} role="radiogroup" aria-label={t("servers.openapi.existing")}>
          <label className={styles.mode}>
            <input
              type="radio"
              name={`${baseId}-mode`}
              checked={mode === "merge"}
              onChange={() => setMode("merge")}
            />
            {t("servers.openapi.merge")}
            <span className={styles.modeHint}>{t("servers.openapi.mergeHint")}</span>
          </label>
          <label className={styles.mode}>
            <input
              type="radio"
              name={`${baseId}-mode`}
              checked={mode === "replace"}
              onChange={() => setMode("replace")}
            />
            {t("servers.openapi.replaceAll")}
            <span className={styles.modeHint}>{t("servers.openapi.replaceHint", { count: config.routes.length })}</span>
          </label>
        </div>

        {loading && result === null && error === null && <p className={styles.hint}>{t("servers.openapi.reading")}</p>}
        {error !== null && (
          <p className={styles.error} role="alert">
            {error}
          </p>
        )}

        {result !== null && (
          <>
            <p className={styles.summary} aria-live="polite">
              {summary(result, mode)}
            </p>
            {routes.length > 0 && (
              <ul className={styles.list} aria-label={t("servers.openapi.generated")}>
                {routes.map((r, i) => {
                  const badge = methodBadge(r.route.method);
                  return (
                    <li key={`${r.operation}-${i}`} className={styles.route} data-status={r.status}>
                      <span className={styles.badge} data-tone={badge.tone}>
                        {badge.label}
                      </span>
                      <span className={styles.path} title={r.route.path}>
                        {r.route.path}
                      </span>
                      <span className={styles.status}>{r.route.status}</span>
                      <span className={styles.what} title={r.operation}>
                        {r.summary || r.operationId || r.operation}
                      </span>
                      {r.status === "existing" && <span className={styles.tag}>{t("servers.openapi.alreadyThere")}</span>}
                      {r.status === "over_limit" && <span className={styles.tag}>{t("servers.openapi.overLimit")}</span>}
                    </li>
                  );
                })}
              </ul>
            )}
            <Diagnostics items={problems} label={t("servers.openapi.problems")} />
            {notes.length > 0 && (
              <details className={styles.notes}>
                <summary>{t("servers.openapi.notesCount", { count: notes.length })}</summary>
                <Diagnostics items={notes} label={t("servers.openapi.notes")} />
              </details>
            )}
          </>
        )}
      </div>
    </Dialog>
  );
}

function summary(r: MockOpenAPIResultDTO, mode: OpenApiMode): string {
  const routes = (count: number) => translate("servers.openapi.routes", { count });
  const total = (r.routes ?? []).length;
  if (total === 0) return translate("servers.openapi.noOperations");
  if (mode === "replace") {
    return translate("servers.openapi.summaryReplace", {
      added: routes(r.added),
      operations: translate("servers.openapi.operations", { count: total }),
      removed: routes(r.removed),
    });
  }
  const skipped = total - r.added;
  const added = translate("servers.openapi.summaryMerge", { count: r.added, kept: routes(r.kept) });
  if (skipped === 0) return added;
  return translate("servers.openapi.sentences", { first: added, second: translate("servers.openapi.skipped", { count: skipped }) });
}

function Diagnostics({ items, label }: { items: readonly MockOpenAPIDiagnosticDTO[]; label: string }) {
  const t = useT();
  if (items.length === 0) return null;
  return (
    <ul className={styles.diagnostics} aria-label={label}>
      {items.map((d, i) => (
        <li key={`${d.code}-${i}`} className={styles.diagnostic} data-severity={d.severity}>
          {d.severity === "info" ? <IconInfo size={12} /> : <IconWarning size={12} />}
          <span>
            {d.operation !== "" && <span className={styles.operation}>{t("servers.openapi.operation", { operation: d.operation })} </span>}
            {d.message}
            {d.line > 0 && <span className={styles.line}> {t("servers.openapi.line", { line: d.line })}</span>}
          </span>
        </li>
      ))}
    </ul>
  );
}
