// Folder settings (COL-03): what a request or folder inherits from the
// folders above it, for the read-only lists of the Headers and Variables
// tabs. Editing a folder's own settings goes through its draft
// (useFolderDraft, commands/autosave.ts). See docs/collection.md (Folder
// settings).

import { useEffect, useState } from "react";
import { getInheritedSettings, type InheritedFolderDTO, type KeyValueDTO } from "../lib/collection";
import { isCancelled, toAppError } from "../lib/errors";
import { isSecretExtra } from "../lib/secretExtra";
import { useCollection, type RequestId } from "../stores";

/** The folders above an item, the top-level folder first; empty until loaded or when loading failed. */
export type InheritedFolders = readonly InheritedFolderDTO[];

const NONE: InheritedFolders = [];

/**
 * Loads what item `id` (a request or folder) inherits from the folders
 * above it (CollectionService.InheritedSettings), again when it moves to
 * another folder. A failure is logged and shows nothing inherited.
 */
export function useInheritedSettings(id: RequestId): InheritedFolders {
  const [folders, setFolders] = useState<InheritedFolders>(NONE);
  const parentId = useCollection((s) => s.nodes[id]?.parentId ?? null);
  useEffect(() => {
    let live = true;
    let call: ReturnType<typeof getInheritedSettings> | undefined;
    try {
      call = getInheritedSettings(id);
    } catch {
      // the service is not bound (tests without it)
    }
    if (call === undefined || typeof call.then !== "function") return;
    const pending = call;
    pending.then(
      (dto) => {
        if (live) setFolders(dto?.folders ?? NONE);
      },
      (err: unknown) => {
        if (live && !isCancelled(err)) console.warn("folder settings: could not load the inherited settings", toAppError(err));
      },
    );
    return () => {
      live = false;
      if (typeof pending.cancel === "function") void pending.cancel();
    };
  }, [id, parentId]);
  return folders;
}

/** One inherited row: its folder, and whether a nearer level overrides it. */
export interface InheritedRow {
  readonly folderId: string;
  readonly folderName: string;
  readonly name: string;
  readonly value: string;
  /** A nearer folder or the item's own rows set the same name. */
  readonly overridden: boolean;
  /** A secret variable: its value is not sent to the UI ("") and is shown masked. */
  readonly secret: boolean;
}

/**
 * The enabled, named rows the folders pass on (their `headers` or
 * `variables`), the top-level folder first, each marked overridden when a
 * nearer folder or `own` (the item's own enabled rows) has one of the same
 * name. Header names compare ignoring case, as they are sent; variable
 * names exactly.
 */
export function inheritedRows(
  folders: InheritedFolders,
  kind: "headers" | "variables",
  own: readonly KeyValueDTO[] | null,
): InheritedRow[] {
  const key = (name: string) => (kind === "headers" ? name.toLowerCase() : name);
  const enabled = (rows: readonly KeyValueDTO[] | null) => (rows ?? []).filter((r) => r.enabled && r.name !== "");
  const taken = new Set(enabled(own).map((r) => key(r.name)));
  const levels: InheritedRow[][] = [];
  for (let i = folders.length - 1; i >= 0; i--) {
    const f = folders[i];
    if (f === undefined) continue;
    const rows = enabled(f[kind]);
    levels.unshift(
      rows.map((r) => ({
        folderId: f.folderId,
        folderName: f.folderName,
        name: r.name,
        value: r.value,
        overridden: taken.has(key(r.name)),
        secret: kind === "variables" && isSecretExtra(r.extra),
      })),
    );
    for (const r of rows) taken.add(key(r.name));
  }
  return levels.flat();
}
