import { useId, useRef, useState } from "react";
import {
  chooseAndOpenWorkspace,
  chooseProjectFolder,
  closeWorkspace,
  createWorkspaceAt,
  openWorkspaceAt,
  refreshRecentWorkspaces,
  revealWorkspace,
} from "../../commands/workspace";
import { showToast } from "../../commands/toasts";
import { newWindow } from "../../commands/windows";
import { formatError, isCancelled, toAppError } from "../../lib/errors";
import { IS_MAC } from "../../lib/shortcuts";
import { folderName, WORKSPACE_CODES } from "../../lib/workspace";
import { shallowEqual, useStores, useWorkspace } from "../../stores";
import { t, type MessageKey } from "../../i18n";
import { useT } from "../../i18n/react";
import { ContextMenu, type ContextMenuItem } from "../common/ContextMenu";
import { Dialog } from "../common/Dialog";
import { IconChevron, IconFolder, IconWarning } from "../common/icons";
import fieldStyles from "../tree/NewItemDialog.module.css";
import styles from "./WorkspaceMenu.module.css";

/** At most this many recent projects are listed in the menu; the settings screen lists them all. */
export const MENU_RECENT_LIMIT = 10;

/** "Reveal in Finder" on macOS, else the generic wording. */
const REVEAL_KEY: MessageKey = IS_MAC ? "workspace.menu.revealFinder" : "workspace.menu.revealFileManager";

/** The reveal item's label in the language in use. */
export function revealLabel(): string {
  return t(REVEAL_KEY);
}

export interface WorkspaceMenuProps {
  /** Opens the settings screen, where the recent list is managed. Without it the menu has no such item. */
  onManageRecent?: () => void;
}

function reportFailure(err: unknown, onCreateHere?: (path: string) => void): void {
  if (isCancelled(err)) return;
  const appErr = toAppError(err);
  const path = appErr.details.path ?? "";
  const offerCreate = appErr.code === WORKSPACE_CODES.notAProject && path !== "" && onCreateHere !== undefined;
  showToast({
    message: formatError(appErr),
    icon: <IconWarning size={14} strokeWidth={1.9} />,
    action: offerCreate ? { label: t("workspace.menu.createHere"), run: () => onCreateHere(path) } : undefined,
  });
}

/**
 * The project switcher in the title bar (STO-06): the open project's name,
 * and a menu with the recent projects (choosing one switches to it; its
 * tabs, active tab, pinned tabs, environment and expanded folders come
 * back), Open folder…, New project…, Open in new window (another window on
 * this project, APP-06), reveal the folder, manage the recent list in the
 * settings, and Close project. With other windows open, Go refuses to
 * switch or close the project, and the toast says why.
 */
export function WorkspaceMenu({ onManageRecent }: WorkspaceMenuProps) {
  const t = useT();
  const stores = useStores();
  const current = useWorkspace((s) => s.current);
  const recent = useWorkspace((s) => s.recent, shallowEqual);
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null);
  const [newProject, setNewProject] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const buttonRef = useRef<HTMLButtonElement>(null);

  if (current === null) return null;

  const run = (action: () => Promise<unknown>) => {
    if (busy) return;
    setBusy(true);
    action()
      .catch((err: unknown) => reportFailure(err, setNewProject))
      .finally(() => setBusy(false));
  };

  const openMenu = () => {
    const rect = buttonRef.current?.getBoundingClientRect();
    setMenu({ x: rect?.left ?? 0, y: (rect?.bottom ?? 0) + 4 });
    // The list may be stale (a folder deleted meanwhile): refresh it while the menu shows.
    void refreshRecentWorkspaces(stores);
  };

  const others = recent.filter((r) => r.path !== current.path).slice(0, MENU_RECENT_LIMIT);
  const items: ContextMenuItem[] = [
    ...others.map(
      (r): ContextMenuItem => ({
        id: `recent:${r.path}`,
        label: r.available
          ? r.name || folderName(r.path)
          : t("workspace.menu.recentNotFound", { name: r.name || folderName(r.path) }),
        title: r.path,
        disabled: !r.available,
        onSelect: () => run(() => openWorkspaceAt(stores, r.path)),
      }),
    ),
    {
      id: "open",
      label: t("workspace.menu.open"),
      separatorBefore: true,
      onSelect: () => run(() => chooseAndOpenWorkspace(stores)),
    },
    {
      id: "new",
      label: t("workspace.menu.new"),
      onSelect: () =>
        run(async () => {
          const path = await chooseProjectFolder();
          if (path !== "") setNewProject(path);
        }),
    },
    {
      id: "newWindow",
      label: t("workspace.menu.newWindow"),
      separatorBefore: true,
      onSelect: () => run(() => newWindow()),
    },
    {
      id: "reveal",
      label: t(REVEAL_KEY),
      title: current.path,
      onSelect: () => run(() => revealWorkspace(current.path)),
    },
    ...(onManageRecent !== undefined
      ? [{ id: "manage", label: t("workspace.menu.manageRecent"), onSelect: onManageRecent } satisfies ContextMenuItem]
      : []),
    {
      id: "close",
      label: t("workspace.menu.close"),
      separatorBefore: true,
      onSelect: () => run(() => closeWorkspace(stores)),
    },
  ];

  return (
    <>
      <button
        ref={buttonRef}
        type="button"
        className={styles.button}
        title={current.path}
        aria-haspopup="menu"
        aria-expanded={menu !== null}
        aria-label={t("workspace.menu.button", { name: current.name })}
        disabled={busy}
        onClick={() => (menu === null ? openMenu() : setMenu(null))}
      >
        <IconFolder size={13} />
        <span className={styles.name}>{current.name}</span>
        <IconChevron direction="down" size={11} className={styles.chevron} />
      </button>
      {menu !== null && (
        <ContextMenu
          label={t("workspace.menu.label")}
          x={menu.x}
          y={menu.y}
          items={items}
          onClose={() => setMenu(null)}
        />
      )}
      {newProject !== null && (
        <NewProjectDialog
          path={newProject}
          onCancel={() => setNewProject(null)}
          onCreate={(name) => createWorkspaceAt(stores, newProject, name).then(() => setNewProject(null))}
        />
      )}
    </>
  );
}

interface NewProjectDialogProps {
  /** The chosen folder. */
  path: string;
  onCancel: () => void;
  /** Creates and opens the project; rejects with the error to show. */
  onCreate: (name: string) => Promise<void>;
}

/** Names the project about to be created in `path` (default: the folder name). */
function NewProjectDialog({ path, onCancel, onCreate }: NewProjectDialogProps) {
  const t = useT();
  const [name, setName] = useState(() => folderName(path));
  const [busy, setBusy] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  const nameRef = useRef<HTMLInputElement>(null);
  const baseId = useId();

  const confirm = async () => {
    if (busy) return;
    setBusy(true);
    setFailure(null);
    try {
      await onCreate(name);
    } catch (err: unknown) {
      if (!isCancelled(err)) setFailure(formatError(err));
      setBusy(false);
    }
  };

  return (
    <Dialog
      open
      title={t("workspace.newProject.title")}
      confirmLabel={t("workspace.newProject.create")}
      onCancel={onCancel}
      onConfirm={() => void confirm()}
      busy={busy}
      initialFocus={nameRef}
    >
      <div className={fieldStyles.field}>
        <label className={fieldStyles.label} htmlFor={`${baseId}-name`}>
          {t("workspace.form.projectName")}
        </label>
        <input
          id={`${baseId}-name`}
          ref={nameRef}
          className={fieldStyles.input}
          value={name}
          placeholder={folderName(path)}
          maxLength={200}
          spellCheck={false}
          autoComplete="off"
          readOnly={busy}
          aria-describedby={`${baseId}-path`}
          onChange={(e) => {
            setName(e.target.value);
            setFailure(null);
          }}
        />
        <span className={fieldStyles.help} id={`${baseId}-path`} title={path}>
          {path}
        </span>
        {failure !== null && (
          <span className={fieldStyles.error} role="alert">
            {failure}
          </span>
        )}
      </div>
    </Dialog>
  );
}
