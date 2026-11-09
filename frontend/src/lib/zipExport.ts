// Calls into Go for the folder zip (EXP-02, docs/import.md "Folder zips"):
// CollectionService.ExportFolderZip writes a folder as a zip of PostSonde
// project files picked in the native save dialog, without secrets, and
// resolves with `saved: false` when the user cancels it. The zip is
// imported back through the Import dialog (format "postsonde-folder").
import { CollectionService, type ExportZipResultDTO } from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection";
import type { CancellablePromise } from "./calls";

export type { ExportZipResultDTO };

/** Exports folder `id` as a zip of project files. */
export function exportFolderZip(id: string): CancellablePromise<ExportZipResultDTO> {
  return CollectionService.ExportFolderZip({ id });
}
