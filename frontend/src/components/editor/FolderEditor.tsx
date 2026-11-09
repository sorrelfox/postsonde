import { useCallback, useId, useRef, useState, type KeyboardEvent } from "react";
import { useInheritedSettings } from "../../commands/folderSettings";
import { useAutoSave } from "../../commands/useAutoSave";
import { useFolderDraft } from "../../commands/useRequestDraft";
import type { FolderDTO, KeyValueDTO } from "../../lib/collection";
import { isSecretExtra, withSecretExtra } from "../../lib/secretExtra";
import type { MessageKey } from "../../i18n";
import { Trans, useT } from "../../i18n/react";
import { useCollection, useDrafts, useStores, type RequestId } from "../../stores";
import { EmptyState } from "../common/EmptyState";
import { FolderRunPanel } from "../runner/FolderRunPanel";
import { FolderServerTab } from "../servers/FolderServerTab";
import { IconEye, IconEyeOff, IconLock, IconWarning } from "../common/icons";
import { KeyValueTable, type KeyValueRow } from "../common/KeyValueTable";
import { AuthEditor } from "./AuthEditor";
import styles from "./FolderEditor.module.css";
import { HEADER_SUGGEST, HEADER_SUGGEST_LABELS } from "./headerSuggest";
import { InheritedRows } from "./InheritedRows";
import { useKeyValueRows } from "./keyValueRows";
import { NotesPanel } from "./NotesPanel";

export type FolderSubTab = "auth" | "headers" | "variables" | "server" | "notes" | "run";

const SUB_TABS: readonly { id: FolderSubTab; labelKey: MessageKey }[] = [
  { id: "auth", labelKey: "editor.folder.tab.auth" },
  { id: "headers", labelKey: "editor.folder.tab.headers" },
  { id: "variables", labelKey: "editor.folder.tab.variables" },
  { id: "server", labelKey: "editor.folder.tab.server" },
  { id: "notes", labelKey: "editor.folder.tab.notes" },
  { id: "run", labelKey: "editor.folder.tab.run" },
];

function isSubTab(value: string | undefined): value is FolderSubTab {
  return SUB_TABS.some((tab) => tab.id === value);
}

const FOLDER_READ_ONLY: Readonly<Record<string, MessageKey>> = {
  newer_version: "editor.folder.readOnly.newerVersion",
  symlink: "editor.folder.readOnly.symlink",
};

const PROJECT_READ_ONLY: Readonly<Record<string, MessageKey>> = {
  newer_version: "editor.folder.readOnly.project.newerVersion",
  invalid: "editor.folder.readOnly.project.invalid",
};

/** Why folder `id`'s settings cannot be edited, or null when they can. */
function useFolderReadOnlyNotice(id: RequestId): string | null {
  const t = useT();
  const itemReason = useCollection((s) => s.nodes[id]?.readOnly ?? "");
  const projectReason = useCollection((s) => s.readOnly);
  if (itemReason !== "") return t(FOLDER_READ_ONLY[itemReason] ?? "editor.folder.readOnly.folder");
  if (projectReason !== "") return t(PROJECT_READ_ONLY[projectReason] ?? "editor.request.readOnly.project");
  return null;
}

/** Rows that count in a sub-tab's badge: enabled, with a name. */
function countRows(rows: readonly KeyValueDTO[] | null): number {
  return (rows ?? []).filter((r) => r.enabled && r.name !== "").length;
}

export interface FolderEditorProps {
  id: RequestId;
}

/**
 * The folder settings view (COL-03), the content of a folder's `DIR` tab:
 * the folder's name and path, then the Auth, Headers, Variables, Server,
 * Notes and Run sub-tabs (Notes: the folder's Markdown description, COL-07;
 * Run: the folder runner, RUN-01, FolderRunPanel). The Server tab is the folder's saved local server (SRV-04,
 * FolderServerTab), saved through the Go ServersService at once rather than
 * through the draft. Everything set here applies to every request inside the
 * folder and its subfolders: requests with auth "Inherit" send the auth,
 * the headers are added to HTTP and GraphQL requests (a request's own
 * header of the same name wins, then the nearer folder's), and the
 * variables resolve after the environment's. Headers and variables the
 * folders above pass on are listed read-only above the tables.
 *
 * Bound to the folder's draft like a request editor: every change is
 * auto-saved to its `_folder.json` (useAutoSave) and can be undone
 * (NAV-07). Mount it with `key={id}`.
 */
export function FolderEditor({ id }: FolderEditorProps) {
  const t = useT();
  const draft = useFolderDraft(id);
  if (draft.status === "loading") {
    return <div className={styles.editor} aria-busy="true" />;
  }
  if (draft.status === "error") {
    return (
      <div className={styles.editor}>
        <FolderHeading id={id} />
        <EmptyState icon={<IconWarning size={20} />} title={t("editor.folder.openFailed")}>
          {draft.error.message}{" "}
          <button type="button" className={styles.linkButton} onClick={draft.retry}>
            {t("editor.request.tryAgain")}
          </button>
        </EmptyState>
      </div>
    );
  }
  return <LoadedFolderEditor id={id} folder={draft.request} />;
}

function FolderHeading({ id }: { id: RequestId }) {
  const node = useCollection((s) => s.nodes[id]);
  return (
    <div className={styles.heading}>
      <span className={styles.badge}>DIR</span>
      <h2 className={styles.name}>{node?.name ?? ""}</h2>
      {node !== undefined && node.path !== "" && <span className={styles.path}>{node.path}</span>}
    </div>
  );
}

function LoadedFolderEditor({ id, folder }: { id: RequestId; folder: FolderDTO }) {
  const t = useT();
  const stores = useStores();
  const { edit } = useAutoSave(id);
  const notice = useFolderReadOnlyNotice(id);
  const readOnly = notice !== null;
  const inherited = useInheritedSettings(id);
  // A top-level folder's variables are the collection variables (ENV-03).
  const isCollection = useCollection((s) => s.nodes[id]?.parentId === null);

  const stored = useDrafts((s) => s.subTabs[id]);
  const subTab: FolderSubTab = isSubTab(stored) ? stored : "auth";
  const selectSubTab = (next: FolderSubTab) => stores.drafts.dispatch({ type: "drafts/setSubTab", id, subTab: next });

  const onHeaders = useCallback((headers: KeyValueDTO[]) => edit({ headers }), [edit]);
  const onVariables = useCallback((variables: KeyValueDTO[]) => edit({ variables }), [edit]);
  const [headerRows, setHeaderRows] = useKeyValueRows(folder.headers, onHeaders);
  const [variableRows, setVariableRows] = useKeyValueRows(folder.variables, onVariables);
  const [revealed, setRevealed] = useState<Readonly<Record<string, true>>>({});

  // A variable's secret flag is in its row's extra members (lib/secretExtra.ts);
  // table rows and file rows match by position.
  const secretAt = (index: number) => isSecretExtra(folder.variables?.[index]?.extra ?? "");
  const rowIndex = (row: KeyValueRow) => variableRows.findIndex((r) => r.id === row.id);
  const toggleSecret = (index: number) => {
    const next = (folder.variables ?? []).map((v, i) =>
      i === index ? { ...v, extra: withSecretExtra(v.extra, !isSecretExtra(v.extra)) } : v,
    );
    edit({ variables: next });
  };
  const toggleRevealed = (row: KeyValueRow) =>
    setRevealed((r) => {
      const next = { ...r };
      if (next[row.id]) delete next[row.id];
      else next[row.id] = true;
      return next;
    });
  const variableActions = (row: KeyValueRow, index: number) => {
    const n = index + 1;
    const isSecret = secretAt(index);
    const shown = revealed[row.id] === true;
    return (
      <>
        {isSecret && (
          <button
            type="button"
            className={styles.rowButton}
            aria-label={shown ? t("editor.folder.hideValueRow", { n }) : t("editor.folder.showValueRow", { n })}
            title={shown ? t("editor.folder.hideValue") : t("editor.folder.showValue")}
            onClick={() => toggleRevealed(row)}
          >
            {shown ? <IconEyeOff size={12} /> : <IconEye size={12} />}
          </button>
        )}
        <button
          type="button"
          className={isSecret ? `${styles.rowButton} ${styles.rowButtonOn}` : styles.rowButton}
          aria-label={t("editor.folder.secretRow", { n })}
          aria-pressed={isSecret}
          title={isSecret ? t("editor.folder.secret.on") : t("editor.folder.secret.off")}
          disabled={readOnly}
          onClick={() => toggleSecret(index)}
        >
          <IconLock size={12} />
        </button>
      </>
    );
  };

  const baseId = useId();
  const tabId = (tab: FolderSubTab) => `${baseId}-tab-${tab}`;
  const panelId = `${baseId}-panel`;
  const tabRefs = useRef<Partial<Record<FolderSubTab, HTMLButtonElement | null>>>({});

  const counts: Partial<Record<FolderSubTab, number>> = {
    headers: countRows(folder.headers),
    variables: countRows(folder.variables),
  };

  const onTabKeyDown = (e: KeyboardEvent<HTMLButtonElement>) => {
    const at = SUB_TABS.findIndex((tab) => tab.id === subTab);
    let next: number;
    if (e.key === "ArrowRight") next = (at + 1) % SUB_TABS.length;
    else if (e.key === "ArrowLeft") next = (at - 1 + SUB_TABS.length) % SUB_TABS.length;
    else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = SUB_TABS.length - 1;
    else return;
    e.preventDefault();
    const target = SUB_TABS[next];
    if (target === undefined) return;
    selectSubTab(target.id);
    tabRefs.current[target.id]?.focus();
  };

  return (
    <div className={styles.editor} data-folder-id={id}>
      {notice !== null && (
        <p className={styles.notice} role="note">
          <IconLock size={13} />
          {notice}
        </p>
      )}
      <FolderHeading id={id} />
      <p className={styles.intro}>{t("editor.folder.intro")}</p>

      <div className={styles.subTabs} role="tablist" aria-label={t("editor.folder.sections")}>
        {SUB_TABS.map((tab) => {
          const selected = tab.id === subTab;
          const count = counts[tab.id] ?? 0;
          return (
            <button
              key={tab.id}
              ref={(el) => {
                tabRefs.current[tab.id] = el;
              }}
              type="button"
              role="tab"
              id={tabId(tab.id)}
              className={styles.subTab}
              aria-selected={selected}
              aria-controls={selected ? panelId : undefined}
              tabIndex={selected ? 0 : -1}
              onClick={() => selectSubTab(tab.id)}
              onKeyDown={onTabKeyDown}
            >
              {t(tab.labelKey)}
              {count > 0 && <span className={styles.count}>{count}</span>}
            </button>
          );
        })}
      </div>

      <div className={styles.section} role="tabpanel" id={panelId} aria-labelledby={tabId(subTab)}>
        {subTab === "auth" && <AuthEditor id={id} auth={folder.auth} readOnly={readOnly} folder />}
        {subTab === "headers" && (
          <>
            <p className={styles.hint}>{t("editor.folder.headers.hint")}</p>
            <InheritedRows folders={inherited} kind="headers" own={folder.headers} />
            <KeyValueTable
              label={t("editor.folder.headers")}
              bulkEdit
              rows={headerRows}
              onChange={readOnly ? undefined : setHeaderRows}
              readOnly={readOnly}
              placeholders={{ key: t("editor.request.placeholder.header") }}
              suggest={HEADER_SUGGEST}
              suggestLabels={HEADER_SUGGEST_LABELS}
            />
          </>
        )}
        {subTab === "variables" && (
          <>
            <p className={styles.hint}>
              {isCollection ? (
                <Trans
                  k="editor.folder.variables.hint.collection"
                  values={{
                    collection: <strong>{t("editor.folder.collectionVariables")}</strong>,
                    name: <code>{"{{name}}"}</code>,
                  }}
                />
              ) : (
                <Trans k="editor.folder.variables.hint.folder" values={{ name: <code>{"{{name}}"}</code> }} />
              )}
            </p>
            <InheritedRows folders={inherited} kind="variables" own={folder.variables} />
            <KeyValueTable
              label={isCollection ? t("editor.folder.collectionVariables") : t("editor.folder.variables")}
              rows={variableRows}
              onChange={readOnly ? undefined : setVariableRows}
              readOnly={readOnly}
              placeholders={{ key: t("editor.folder.placeholder.name") }}
              isValueMasked={(row) => {
                const i = rowIndex(row);
                return i >= 0 && secretAt(i) && revealed[row.id] !== true;
              }}
              valueActions={variableActions}
            />
          </>
        )}
        {subTab === "server" && <FolderServerTab folderId={id} readOnly={readOnly} />}
        {subTab === "run" && <FolderRunPanel folderId={id} />}
        {subTab === "notes" && (
          <NotesPanel
            value={folder.description ?? ""}
            subject="folder"
            readOnly={readOnly}
            onChange={(description) => edit({ description })}
          />
        )}
      </div>
    </div>
  );
}
