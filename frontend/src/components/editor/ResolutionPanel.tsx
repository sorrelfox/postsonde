import { useEffect, useRef } from "react";
import { useResolution } from "../../commands/useResolution";
import type { MessageKey } from "../../i18n";
import { useT } from "../../i18n/react";
import type { ResolutionScopeDTO, ResolvedVariableDTO } from "../../lib/resolution";
import { parseResponseRef, sendText } from "../../lib/templateResponse";
import { SECRET_MASK } from "../../lib/templateVariables";
import type { RequestId } from "../../stores";
import styles from "./ResolutionPanel.module.css";

/** Labels of the kinds that are not plain variables. */
const KIND_LABELS: Readonly<Record<string, MessageKey>> = {
  path: "editor.resolution.kind.path",
  dynamic: "editor.resolution.kind.dynamic",
  prompt: "editor.resolution.kind.prompt",
  file: "editor.resolution.kind.file",
  filter: "editor.resolution.kind.filter",
  response: "editor.resolution.kind.response",
};

/** Why a reference does not resolve, by template.ProblemKind. */
const PROBLEMS: Readonly<Record<string, MessageKey>> = {
  unresolved: "editor.resolution.problem.unresolved",
  prompt: "editor.resolution.problem.prompt",
  file: "editor.resolution.problem.file",
  response: "editor.resolution.problem.response",
};

/** When a {{$response}} reference's request is sent first (DYN-06), read from the reference. */
function responseSendText(v: ResolvedVariableDTO): string {
  const ref = parseResponseRef(v.name);
  return ref === null || "error" in ref ? "—" : sendText(ref);
}

export interface ResolutionPanelProps {
  id: RequestId;
  /** The request's draft: the resolution is asked again when it changes. */
  request: unknown;
  /** A name to bring into view and mark (from a chip's hover), or null. */
  focus?: string | null;
}

/**
 * The Variables sub-tab (DBG-08, ENV-12): every {{reference}} the request
 * uses, the value it is sent with (secrets masked), the scope that wins
 * and the value of every scope that defines it, lowest precedence first,
 * the winner marked and the others struck through. Dynamic values, prompts
 * and files are marked as made when sent. The list comes from Go's
 * HTTPService.Resolution, which builds the request as a send does
 * (commands/useResolution.ts, docs/templates.md#resolution-view).
 */
export function ResolutionPanel({ id, request, focus = null }: ResolutionPanelProps) {
  const t = useT();
  const { resolution, error, loading } = useResolution(id, request);
  const list = useRef<HTMLTableElement>(null);

  useEffect(() => {
    if (focus === null || resolution === null) return;
    list.current?.querySelector<HTMLElement>(`[data-name="${CSS.escape(focus)}"]`)?.scrollIntoView?.({ block: "nearest" });
  }, [focus, resolution]);

  if (resolution === null) {
    return (
      <div className={styles.panel}>
        {error !== "" ? (
          <p className={styles.error} role="alert">
            {error}
          </p>
        ) : (
          loading && <p className={styles.note}>{t("editor.resolution.loading")}</p>
        )}
      </div>
    );
  }

  const variables = resolution.variables ?? [];

  const value = (v: ResolvedVariableDTO) => {
    if (!v.resolved) {
      const key = PROBLEMS[v.problem ?? ""] ?? "editor.resolution.problem.other";
      return (
        <span className={styles.problem} data-problem={v.problem}>
          {t(key)}
          {v.reason !== undefined && v.reason !== "" && ` ${v.reason}`}
        </span>
      );
    }
    if (v.secret) return <span className={styles.value}>{SECRET_MASK}</span>;
    if (v.generated) {
      return (
        <span className={styles.generated}>
          {v.description !== undefined && v.description !== ""
            ? t("editor.resolution.generatedWhat", { what: v.description })
            : t("editor.resolution.generated")}
        </span>
      );
    }
    return v.value === "" ? (
      <span className={styles.empty}>{t("editor.resolution.empty")}</span>
    ) : (
      <span className={styles.value}>{v.value}</span>
    );
  };

  const scope = (s: ResolutionScopeDTO, i: number) => (
    <li key={i} className={s.wins ? styles.wins : styles.overridden} data-wins={s.wins || undefined}>
      <span className={styles.scopeName}>
        {s.local === true ? t("editor.resolution.localScope", { scope: s.scope }) : s.scope}
      </span>
      <span className={styles.scopeValue}>{s.secret ? SECRET_MASK : s.value === "" ? t("editor.resolution.empty") : s.value}</span>
      <span className={styles.scopeState}>{s.wins ? t("editor.resolution.wins") : t("editor.resolution.overridden")}</span>
    </li>
  );

  return (
    <div className={styles.panel}>
      <p className={styles.note}>
        {resolution.environment === ""
          ? t("editor.resolution.noEnvironment")
          : t("editor.resolution.environment", { name: resolution.environment })}
      </p>
      {resolution.url !== "" && (
        <p className={styles.url}>
          <span className={styles.urlLabel}>{t("editor.resolution.url")}</span>
          <span className={styles.value}>{resolution.url}</span>
        </p>
      )}
      {variables.length === 0 ? (
        <p className={styles.note}>{t("editor.resolution.none")}</p>
      ) : (
        <table ref={list} className={styles.table} aria-label={t("editor.resolution.label")}>
          <thead>
            <tr>
              <th scope="col">{t("editor.resolution.col.name")}</th>
              <th scope="col">{t("editor.resolution.col.value")}</th>
              <th scope="col">{t("editor.resolution.col.from")}</th>
              <th scope="col">{t("editor.resolution.col.scopes")}</th>
            </tr>
          </thead>
          <tbody>
            {variables.map((v) => {
              const kind = KIND_LABELS[v.kind];
              return (
                <tr key={v.name} data-name={v.name} data-focused={v.name === focus || undefined} data-unresolved={!v.resolved || undefined}>
                  <th scope="row" className={styles.name}>
                    <span className={styles.ref}>{`{{${v.name}}}`}</span>
                    {kind !== undefined && <span className={styles.kind}>{t(kind)}</span>}
                    {(v.via ?? []).length > 0 && (
                      <span className={styles.via}>
                        {t("editor.resolution.via", { names: (v.via ?? []).map((n) => `{{${n}}}`).join(" → ") })}
                      </span>
                    )}
                  </th>
                  <td>{value(v)}</td>
                  <td className={styles.from}>
                    {v.scope === ""
                      ? "—"
                      : v.local === true
                        ? t("editor.resolution.localScope", { scope: v.scope })
                        : v.scope}
                  </td>
                  <td>
                    {(v.scopes ?? []).length === 0 ? (
                      <span className={styles.empty}>
                        {v.kind === "filter"
                          ? t("editor.resolution.scopesFilter")
                          : v.kind === "response"
                            ? responseSendText(v)
                            : "—"}
                      </span>
                    ) : (
                      <ol className={styles.scopes} aria-label={t("editor.resolution.scopesLabel", { name: v.name })}>
                        {(v.scopes ?? []).map(scope)}
                      </ol>
                    )}
                  </td>
                </tr>
              );
            })}
          </tbody>
        </table>
      )}
      <p className={styles.note}>{t("editor.resolution.note")}</p>
    </div>
  );
}
