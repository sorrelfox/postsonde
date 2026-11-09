import { useEffect, useRef, useState } from "react";
import type { GraphqlSchema } from "../../commands/useGraphqlSchema";
import { formatDate, t as translate } from "../../i18n";
import { useT } from "../../i18n/react";
import { isCancelled, toAppError } from "../../lib/errors";
import { saveGraphqlSchemaSdl, schemaAge, schemaFileName, type SchemaInfoDTO, type SchemaProblemDTO } from "../../lib/graphqlSchema";
import { ariaShortcut, shortcutLabel } from "../../lib/shortcuts";
import { IconBook, IconDownload, IconFile, IconGlobe, IconRefresh, IconWarning } from "../common/icons";
import styles from "./GraphqlEditor.module.css";

// The schema strip of the GraphQL editor (GQL-10, GQL-21), under the URL
// bar as in the mockup: a status dot, "Schema · 14 types · introspected
// 2 min ago" (or that there is none yet), and the button that fetches it.
// Introspection runs only from that button. A request can load its schema
// from a local SDL file instead (GQL-11): then the strip says "from file
// schema.graphql", Reload reads the file again, the file's problems are
// listed, and "Use introspection" switches back. At its end, the Docs
// toggle of the docs explorer (GQL-14). Save SDL… writes the schema to a
// .graphql file (GQL-20), and after a fetch a note says whether the
// schema changed (GQL-19, also after the opt-in auto-refresh). See
// docs/graphql.md.

/** How often the age is redrawn, in ms. */
const AGE_TICK_MS = 30_000;

function useNow(enabled: boolean): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    if (!enabled) return;
    setNow(Date.now());
    const timer = setInterval(() => setNow(Date.now()), AGE_TICK_MS);
    return () => clearInterval(timer);
  }, [enabled]);
  return now;
}

/** "Ln 3, Col 5: message" for a schema file problem. */
export function problemText(p: SchemaProblemDTO): string {
  return p.line > 0 ? translate("editorProtocols.schema.problemAt", { line: p.line, column: p.column, message: p.message }) : p.message;
}

/** The tooltip listing a schema file's problems, with how many are not shown. */
function problemsTitle(info: SchemaInfoDTO): string {
  const list = info.problemList ?? [];
  const lines = list.map(problemText);
  if (info.problems > list.length) lines.push(translate("editorProtocols.schema.moreProblems", { count: info.problems - list.length }));
  return lines.join("\n");
}

/** A date and time as the strip shows them, in the language in use. */
function dateTime(iso: string): string {
  return formatDate(new Date(iso), { dateStyle: "short", timeStyle: "medium" });
}

export interface GraphqlSchemaStripProps {
  schema: GraphqlSchema;
  /**
   * Switching the schema source (GQL-11): loads a schema file chosen in
   * the native dialog, or goes back to introspection. Without it, or when
   * readOnly, the buttons do not show.
   */
  onLoadFile?: () => void;
  onUseIntrospection?: () => void;
  readOnly?: boolean;
  /** Whether the docs panel is open; with onToggleDocs, the Docs button shows. */
  docsOpen?: boolean;
  onToggleDocs?: () => void;
  /** The request's name, the suggested file name of Save SDL… (GQL-20). */
  requestName?: string;
}

export function GraphqlSchemaStrip({
  schema,
  docsOpen = false,
  onToggleDocs,
  onLoadFile,
  onUseIntrospection,
  readOnly = false,
  requestName = "",
}: GraphqlSchemaStripProps) {
  const t = useT();
  const { info, fetching, stage, error } = schema;
  const cached = info?.cached === true;
  const fromFile = info?.source === "file";
  const now = useNow(cached && !fromFile);

  if (info !== null && fromFile) {
    return (
      <FileStrip
        info={info}
        schema={schema}
        docsOpen={docsOpen}
        onToggleDocs={onToggleDocs}
        onUseIntrospection={readOnly ? undefined : onUseIntrospection}
        onLoadFile={readOnly ? undefined : onLoadFile}
        requestName={requestName}
      />
    );
  }

  let status: string;
  let tone: "ok" | "warn" | "none";
  if (info === null) {
    status = t("editorProtocols.schema.none");
    tone = "none";
  } else if (!cached) {
    status = t("editorProtocols.schema.notYet");
    tone = "none";
  } else {
    const types = t("editorProtocols.schema.types", { count: info.typeCount });
    const age = schemaAge(info.fetchedAt, now);
    status =
      age === ""
        ? t("editorProtocols.schema.status", { types })
        : t("editorProtocols.schema.statusIntrospected", { types, age });
    tone = info.problems > 0 ? "warn" : "ok";
  }
  let title: string | undefined;
  if (cached && info !== null) {
    const params = { endpoint: info.endpoint, date: dateTime(info.fetchedAt) };
    title =
      info.query === "conservative"
        ? t("editorProtocols.schema.fetchedConservative", params)
        : t("editorProtocols.schema.fetched", params);
    if (info.problems > 0) title += t("editorProtocols.schema.brokenRefs", { count: info.problems });
  }
  const label = fetching
    ? cached
      ? t("editorProtocols.schema.refreshing")
      : t("editorProtocols.schema.fetching")
    : cached
      ? t("editorProtocols.schema.refresh")
      : t("editorProtocols.schema.fetch");

  return (
    <div className={styles.schemaStrip} role="group" aria-label={t("editorProtocols.schema.label")}>
      <span className={styles.schemaStatus} title={title}>
        <span className={styles.schemaDot} data-tone={tone} aria-hidden="true" />
        <span className={styles.schemaText}>{status}</span>
      </span>
      <button
        type="button"
        className={styles.schemaButton}
        disabled={fetching}
        title={fetching ? stage || t("editorProtocols.schema.fetchingTitle") : t("editorProtocols.schema.fetchTitle")}
        onClick={schema.fetch}
      >
        <IconRefresh size={12} className={fetching ? styles.spinning : undefined} />
        {label}
      </button>
      {onLoadFile !== undefined && !readOnly && (
        <button
          type="button"
          className={styles.schemaButton}
          title={t("editorProtocols.schema.loadFileTitle")}
          onClick={onLoadFile}
        >
          <IconFile size={12} />
          {t("editorProtocols.schema.loadFile")}
        </button>
      )}
      {cached && info !== null && <SaveSdlButton schemaKey={info.key} name={requestName} />}
      {!fetching && error === null && <ChangeNote schema={schema} />}
      {error !== null && !fetching && (
        <span className={styles.schemaError} role="alert" title={error.hint || undefined}>
          <IconWarning size={12} />
          <span>{error.message}</span>
        </span>
      )}
      <DocsToggle docsOpen={docsOpen} onToggleDocs={onToggleDocs} />
    </div>
  );
}

/** Whether the last fetch changed the schema (GQL-19): "Schema changed" or "No changes". */
function ChangeNote({ schema }: { schema: GraphqlSchema }) {
  const t = useT();
  const last = schema.lastFetch;
  if (last === null || last.key !== schema.info?.key || (last.change !== "changed" && last.change !== "unchanged")) return null;
  const changed = last.change === "changed";
  let text: string;
  if (last.auto) text = changed ? t("editorProtocols.schema.autoChanged") : t("editorProtocols.schema.autoUnchanged");
  else text = changed ? t("editorProtocols.schema.changed") : t("editorProtocols.schema.unchanged");
  return (
    <span className={styles.schemaNote} role="status" data-tone={changed ? "changed" : "same"}>
      {text}
    </span>
  );
}

/**
 * Save SDL… (GQL-20): Go prints the schema as SDL and asks where to save
 * it; then "Saved to …" or why it failed. Nothing is sent.
 */
function SaveSdlButton({ schemaKey, name }: { schemaKey: string; name: string }) {
  const t = useT();
  const [saving, setSaving] = useState(false);
  const [result, setResult] = useState<{ ok: boolean; text: string; hint: string } | null>(null);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  // A note about another schema would mislead.
  useEffect(() => setResult(null), [schemaKey]);
  const save = () => {
    if (saving) return;
    setSaving(true);
    setResult(null);
    void (async () => {
      try {
        const res = await saveGraphqlSchemaSdl(schemaKey, name);
        if (mounted.current && res.path !== "") {
          setResult({ ok: true, text: t("editorProtocols.schema.savedSdl", { file: schemaFileName(res.path) }), hint: res.path });
        }
      } catch (err: unknown) {
        if (mounted.current && !isCancelled(err)) {
          const e = toAppError(err);
          setResult({ ok: false, text: e.message, hint: e.hint });
        }
      } finally {
        if (mounted.current) setSaving(false);
      }
    })();
  };
  return (
    <>
      <button
        type="button"
        className={styles.schemaButton}
        disabled={saving}
        title={t("editorProtocols.schema.saveSdlTitle")}
        onClick={save}
      >
        <IconDownload size={12} />
        {t("editorProtocols.schema.saveSdl")}
      </button>
      {result !== null &&
        (result.ok ? (
          <span className={styles.schemaNote} role="status" title={result.hint || undefined}>
            {result.text}
          </span>
        ) : (
          <span className={styles.schemaError} role="alert" title={result.hint || undefined}>
            <IconWarning size={12} />
            <span>{result.text}</span>
          </span>
        ))}
    </>
  );
}

function DocsToggle({ docsOpen, onToggleDocs }: { docsOpen: boolean; onToggleDocs?: () => void }) {
  const t = useT();
  if (onToggleDocs === undefined) return null;
  const shortcut = shortcutLabel("graphql.docs");
  return (
    <button
      type="button"
      className={`${styles.schemaButton} ${styles.docsButton}`}
      aria-pressed={docsOpen}
      title={docsOpen ? t("editorProtocols.schema.hideDocs", { shortcut }) : t("editorProtocols.schema.showDocs", { shortcut })}
      aria-keyshortcuts={ariaShortcut("graphql.docs")}
      onClick={onToggleDocs}
    >
      <IconBook size={12} />
      {t("editorProtocols.schema.docs")}
    </button>
  );
}

interface FileStripProps {
  info: SchemaInfoDTO;
  schema: GraphqlSchema;
  docsOpen: boolean;
  onToggleDocs?: () => void;
  onLoadFile?: () => void;
  onUseIntrospection?: () => void;
  requestName: string;
}

/** The strip of a request whose schema comes from a local SDL file (GQL-11). */
function FileStrip({ info, schema, docsOpen, onToggleDocs, onLoadFile, onUseIntrospection, requestName }: FileStripProps) {
  const t = useT();
  const { fetching, error } = schema;
  const name = schemaFileName(info.file);
  const problems = info.problemList ?? [];
  const loadedTitle =
    info.modifiedAt === ""
      ? t("editorProtocols.schema.loadedFrom", { file: info.file })
      : t("editorProtocols.schema.loadedFromModified", { file: info.file, date: dateTime(info.modifiedAt) });
  let status: string;
  let tone: "ok" | "warn" | "bad";
  if (info.error !== null) {
    status = t("editorProtocols.schema.fileStatusBad", { name });
    tone = "bad";
  } else {
    const types = t("editorProtocols.schema.types", { count: info.typeCount });
    status = t("editorProtocols.schema.fileStatus", { types, name });
    tone = info.problems > 0 ? "warn" : "ok";
  }
  const first = problems[0];
  return (
    <div className={styles.schemaStrip} role="group" aria-label={t("editorProtocols.schema.label")}>
      <span className={styles.schemaStatus} title={loadedTitle}>
        <span className={styles.schemaDot} data-tone={tone} aria-hidden="true" />
        <span className={styles.schemaText}>{status}</span>
      </span>
      <button
        type="button"
        className={styles.schemaButton}
        disabled={fetching}
        title={t("editorProtocols.schema.reloadTitle", { file: info.file })}
        onClick={schema.fetch}
      >
        <IconRefresh size={12} className={fetching ? styles.spinning : undefined} />
        {fetching ? t("editorProtocols.schema.reloading") : t("editorProtocols.schema.reload")}
      </button>
      {onLoadFile !== undefined && (
        <button
          type="button"
          className={styles.schemaButton}
          title={t("editorProtocols.schema.changeFileTitle")}
          onClick={onLoadFile}
        >
          <IconFile size={12} />
          {t("editorProtocols.schema.changeFile")}
        </button>
      )}
      {onUseIntrospection !== undefined && (
        <button
          type="button"
          className={styles.schemaButton}
          title={t("editorProtocols.schema.useIntrospectionTitle")}
          onClick={onUseIntrospection}
        >
          <IconGlobe size={12} />
          {t("editorProtocols.schema.useIntrospection")}
        </button>
      )}
      {info.cached && <SaveSdlButton schemaKey={info.key} name={requestName} />}
      {info.error !== null ? (
        <span
          className={styles.schemaError}
          role="alert"
          title={[info.error.hint, problemsTitle(info)].filter((s) => s !== "").join("\n") || undefined}
        >
          <IconWarning size={12} />
          <span>{info.error.message}</span>
        </span>
      ) : (
        first !== undefined && (
          <span className={styles.schemaProblems} title={problemsTitle(info)}>
            <IconWarning size={12} />
            <span>{t("editorProtocols.schema.fileProblems", { count: info.problems, problem: problemText(first) })}</span>
          </span>
        )
      )}
      {error !== null && !fetching && (
        <span className={styles.schemaError} role="alert" title={error.hint || undefined}>
          <IconWarning size={12} />
          <span>{error.message}</span>
        </span>
      )}
      <DocsToggle docsOpen={docsOpen} onToggleDocs={onToggleDocs} />
    </div>
  );
}
