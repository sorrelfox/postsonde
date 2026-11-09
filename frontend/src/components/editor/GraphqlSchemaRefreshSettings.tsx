import { useId } from "react";
import { useT } from "../../i18n/react";
import type { RequestDTO } from "../../lib/collection";
import { SCHEMA_REFRESH_INTERVALS, schemaRefreshOf, schemaRefreshPatch, type SchemaRefresh } from "../../lib/graphqlSchemaRefresh";
import type { RequestData } from "../../stores";
import styles from "./RequestSettings.module.css";

// The schema auto-refresh of a GraphQL request (GQL-19), at the top of its
// Settings tab: refresh after a send whose response shows the server's
// schema changed, and every so many minutes while the editor is open.
// Both are off by default and stored as nothing; the help says that
// each one sends introspection queries by itself (PRV-05). A request
// whose schema comes from a file has nothing to refresh. See
// docs/graphql.md.

export interface GraphqlSchemaRefreshSettingsProps {
  request: RequestDTO;
  readOnly: boolean;
  edit: (patch: RequestData) => void;
}

export function GraphqlSchemaRefreshSettings({ request, readOnly, edit }: GraphqlSchemaRefreshSettingsProps) {
  const t = useT();
  const intervalId = useId();
  const helpId = useId();
  const current = schemaRefreshOf(request.settings);
  const fromFile = request.schemaFile.trim() !== "";
  const disabled = readOnly || fromFile;
  const set = (next: Partial<SchemaRefresh>) =>
    edit({ settings: { ...request.settings, graphqlSchemaRefresh: schemaRefreshPatch(request.settings.graphqlSchemaRefresh, { ...current, ...next }) } });
  const known = (SCHEMA_REFRESH_INTERVALS as readonly number[]).includes(current.intervalMinutes);
  return (
    <section className={styles.group} aria-label={t("editorProtocols.schemaRefresh.label")}>
      <h3 className={styles.heading}>{t("editorProtocols.schemaRefresh.label")}</h3>
      <label className={styles.check}>
        <input
          type="checkbox"
          checked={current.onSend}
          disabled={disabled}
          aria-describedby={helpId}
          onChange={(e) => set({ onSend: e.currentTarget.checked })}
        />
        {t("editorProtocols.schemaRefresh.onSend")}
        <span className={styles.checkHelp}>{t("editorProtocols.schemaRefresh.onSendHelp")}</span>
      </label>
      <div className={styles.row}>
        <label className={styles.label} htmlFor={intervalId}>
          {t("editorProtocols.schemaRefresh.interval")}
        </label>
        <select
          id={intervalId}
          className={styles.select}
          value={current.intervalMinutes}
          disabled={disabled}
          aria-describedby={helpId}
          onChange={(e) => set({ intervalMinutes: Number(e.currentTarget.value) })}
        >
          {SCHEMA_REFRESH_INTERVALS.map((m) => (
            <option key={m} value={m}>
              {m === 0 ? t("editorProtocols.schemaRefresh.never") : t("editorProtocols.schemaRefresh.minutes", { count: m })}
            </option>
          ))}
          {!known && <option value={current.intervalMinutes}>{t("editorProtocols.schemaRefresh.minutes", { count: current.intervalMinutes })}</option>}
        </select>
      </div>
      <p className={styles.help} id={helpId}>
        {fromFile ? t("editorProtocols.schemaRefresh.fromFile") : t("editorProtocols.schemaRefresh.help")}
      </p>
    </section>
  );
}
