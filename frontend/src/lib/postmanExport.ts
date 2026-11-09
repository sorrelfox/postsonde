// Calls into Go for the Postman export (EXP-03, docs/import.md "Postman
// export"): CollectionService.ExportPostman writes a folder, or the whole
// project, as a Postman Collection v2.1 file picked in the native save
// dialog, and resolves with `saved: false` when the user cancels it.
import {
  CollectionService,
  type ExportNoteDTO,
  type ExportPostmanResultDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection";
import type { CancellablePromise } from "./calls";

export type { ExportNoteDTO, ExportPostmanResultDTO };

/** Exports folder `id` ("" for the whole project) as a Postman collection. */
export function exportPostman(id: string): CancellablePromise<ExportPostmanResultDTO> {
  return CollectionService.ExportPostman({ id });
}

/** The report's notes grouped by item, in order: the item's path ("" for the collection) and its notes. */
export function groupNotes(notes: readonly ExportNoteDTO[]): { path: string; notes: ExportNoteDTO[] }[] {
  const groups: { path: string; notes: ExportNoteDTO[] }[] = [];
  for (const note of notes) {
    const last = groups[groups.length - 1];
    if (last !== undefined && last.path === note.path) last.notes.push(note);
    else groups.push({ path: note.path, notes: [note] });
  }
  return groups;
}
