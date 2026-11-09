import { useCallback, useMemo, useState, type ReactNode } from "react";
import { createScratchRequest, deleteScratchRequest } from "../../commands/collection";
import { confirm } from "../../commands/dialogs";
import { showToast } from "../../commands/toasts";
import { useT } from "../../i18n/react";
import type { NewRequestKind } from "../../lib/collection";
import { formatError } from "../../lib/errors";
import { useCollection, useStores, type NodeId } from "../../stores";
import { ContextMenu, type ContextMenuItem } from "../common/ContextMenu";
import { IconTrash, IconWarning } from "../common/icons";
import { NewItemDialog, type NewItemRequest } from "../tree/NewItemDialog";
import { REQUEST_KINDS } from "../tree/newItem";

/** What the tab bar's "+" and the empty state do with scratch requests (COL-06). */
export interface ScratchActions {
  /** Creates a scratch request of `kind` and opens it. */
  readonly create: (kind?: NewRequestKind) => void;
  /** Opens the menu of request kinds at a point (viewport coordinates). */
  readonly openNewMenu: (x: number, y: number) => void;
  /** Whether "Save to…" can run now: a tree is loaded and the project is writable. */
  readonly canSave: boolean;
  /** Opens the Save to… dialog of a scratch request. */
  readonly saveTo: (id: NodeId) => void;
  /** Deletes a scratch request for good, after a confirmation. */
  readonly remove: (id: NodeId) => void;
  /** The menu and dialogs; render them anywhere (they are portals). */
  readonly elements: ReactNode;
}

function showFailure(message: string): void {
  showToast({ message, icon: <IconWarning size={14} strokeWidth={1.9} /> });
}

/**
 * Scratch requests (COL-06): quick requests kept in the workspace's local
 * side files until "Save to…" moves one into a folder. The New scratch
 * request menu offers every request kind (HTTP first); Save to… is the
 * New request dialog's form (folder picker and name check) in save mode.
 */
export function useScratchActions(): ScratchActions {
  const t = useT();
  const stores = useStores();
  const status = useCollection((s) => s.status);
  const readOnly = useCollection((s) => s.readOnly);
  const [menu, setMenu] = useState<{ x: number; y: number } | null>(null);
  const [saving, setSaving] = useState<NewItemRequest | null>(null);
  const canSave = status === "ready" && readOnly === "";

  const create = useCallback(
    (kind: NewRequestKind = "http") => {
      createScratchRequest(stores, kind).catch((err: unknown) =>
        showFailure(t("tabs.scratch.createFailed", { error: formatError(err) })),
      );
    },
    [stores, t],
  );

  const openNewMenu = useCallback((x: number, y: number) => setMenu({ x, y }), []);

  const saveTo = useCallback(
    (id: NodeId) => {
      if (!canSave) return;
      setSaving({ type: "saveScratch", from: null, scratchId: id });
    },
    [canSave],
  );

  const remove = useCallback(
    (id: NodeId) => {
      const name = stores.collection.getState().nodes[id]?.name ?? "";
      void (async () => {
        const ok = await confirm({
          title: t("tabs.scratch.deleteTitle", { name }),
          message: t("tabs.scratch.deleteBody"),
          confirmLabel: t("tabs.scratch.delete"),
          variant: "danger",
          confirmIcon: <IconTrash size={14} />,
        });
        if (!ok) return;
        try {
          await deleteScratchRequest(stores, id);
        } catch (err: unknown) {
          showFailure(t("tabs.scratch.deleteFailed", { name, error: formatError(err) }));
        }
      })();
    },
    [stores, t],
  );

  const items = useMemo<ContextMenuItem[]>(
    () =>
      REQUEST_KINDS.map((k) => ({
        id: k.kind,
        label: t("tabs.scratch.kindRequest", { kind: k.label }),
        title: t(k.descriptionKey),
        onSelect: () => create(k.kind),
      })),
    [create, t],
  );

  const close = useCallback(() => setSaving(null), []);

  const elements = (
    <>
      {menu !== null && (
        <ContextMenu label={t("tabs.scratch.menu")} x={menu.x} y={menu.y} items={items} onClose={() => setMenu(null)} />
      )}
      <NewItemDialog request={saving} onClose={close} />
    </>
  );

  return { create, openNewMenu, canSave, saveTo, remove, elements };
}
