// Files dropped on the window (IMP-11, docs/frontend.md "Dropped files",
// docs/import.md "Dropped files").
//
// Wails delivers OS file drops with their real paths when they land on an
// element with a data-file-drop-target attribute; Go forwards them as
// collection:files-dropped with that attribute's value. The app shell is
// the target IMPORT_DROP_TARGET. importDroppedFiles asks Go which format
// each file is in (detectImport, the same detection as the Import
// dialog's preview) and hands the importable ones to the mounted Import
// dialog host (useNewItemDialog), which opens the Import dialog for each
// in turn; files that cannot be imported are named in a toast.
//
// The dialog listeners are callbacks, not plain data, so they live in this
// module instead of a store (as commands/curlImport.ts does).

import { t } from "../i18n";
import { detectImport, IMPORT_CODES, type DetectedImportDTO } from "../lib/collection";
import { toAppError } from "../lib/errors";
import type { Stores } from "../stores";
import { showToast } from "./toasts";

/** The data-file-drop-target value of the app shell: a drop there imports. */
export const IMPORT_DROP_TARGET = "import";

/** A dropped file an importer recognised. */
export interface DroppedImport {
  readonly path: string;
  readonly fileName: string;
  /** The detected format's ID. */
  readonly format: string;
}

type DialogListener = (files: readonly DroppedImport[]) => void;
const dialogListeners = new Set<DialogListener>();

/** For the Import dialog host: called with the files to import, in order. */
export function onImportDroppedFiles(listener: DialogListener): () => void {
  dialogListeners.add(listener);
  return () => {
    dialogListeners.delete(listener);
  };
}

function openImportDialogs(files: readonly DroppedImport[]): boolean {
  if (dialogListeners.size === 0) return false;
  dialogListeners.forEach((l) => l(files));
  return true;
}

/** The toast text for files that cannot be imported. */
export function refusedMessage(refused: readonly DetectedImportDTO[]): string {
  if (refused.length === 1) {
    const f = refused[0];
    return t("commands.fileDrop.refusedOne", { file: f.fileName, reason: f.error?.message ?? t("commands.fileDrop.cannotImport") });
  }
  const names = refused.map((f) => f.fileName).join(", ");
  const unknown = refused.every((f) => f.error?.code === IMPORT_CODES.unknownFormat);
  return unknown
    ? t("commands.fileDrop.unknownFormats", { count: refused.length, names })
    : t("commands.fileDrop.refused", { count: refused.length, names });
}

/**
 * Imports the files of a drop on the window. Drops on another target
 * (`target` not IMPORT_DROP_TARGET) are left to it. Needs a writable open
 * project; says so otherwise. Resolves once the dialogs were asked for.
 */
export async function importDroppedFiles(stores: Stores, paths: readonly string[], target: string): Promise<void> {
  if (target !== IMPORT_DROP_TARGET || paths.length === 0) return;
  const state = stores.collection.getState();
  if (state.status !== "ready") {
    showToast({ message: t("commands.fileDrop.noProject") });
    return;
  }
  if (state.readOnly !== "") {
    showToast({ message: t("commands.fileDrop.readOnly") });
    return;
  }
  let found: DetectedImportDTO[];
  try {
    found = await detectImport(paths);
  } catch (err: unknown) {
    const e = toAppError(err);
    showToast({ message: e.hint === "" ? e.message : t("lib.errors.withHint", { message: e.message, hint: e.hint }) });
    return;
  }
  const importable: DroppedImport[] = [];
  const refused: DetectedImportDTO[] = [];
  for (const f of found) {
    if (f.error === null && f.format.id !== "") importable.push({ path: f.path, fileName: f.fileName, format: f.format.id });
    else refused.push(f);
  }
  if (refused.length > 0) showToast({ message: refusedMessage(refused) });
  if (importable.length > 0 && !openImportDialogs(importable)) {
    showToast({ message: t("commands.fileDrop.notHere") });
  }
}
