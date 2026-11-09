// Copying and saving timeline and log entries (DBG-07, docs/privacy.md).
// The UI formats an entry or a whole log as text or JSON; LogExportService
// masks every secret the app keeps before the text reaches the clipboard
// (Mask) or the file the user picks in the native save dialog (Save).
import {
  LogExportService,
  type MaskResultDTO,
  type SaveResultDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/logexport";
import { t } from "../i18n";
import type { CancellablePromise } from "./calls";

export type { MaskResultDTO, SaveResultDTO };

/** How a copied entry or a saved log is laid out. */
export type LogFormat = "text" | "json";

/** `format` of a saved JSON log, so a reader can tell what it is. */
export const LOG_DOCUMENT_FORMAT = "postsonde-log";
export const LOG_DOCUMENT_VERSION = 1;

/** `text` with the known secrets masked; JSON stays valid. */
export function maskLogText(text: string, format: LogFormat): CancellablePromise<MaskResultDTO> {
  return LogExportService.Mask({ text, format });
}

export interface SaveLogInput {
  /** Names the log in the save dialog: "Console", "Timeline". */
  title: string;
  /** Suggested file name; Go adds the extension of the format. */
  fileName: string;
  format: LogFormat;
  content: string;
}

/** Masks `content` and writes it where the user picks; `saved: false` when they cancel. */
export function saveLogFile(input: SaveLogInput): CancellablePromise<SaveResultDTO> {
  return LogExportService.Save(input);
}

/** What a saved log is about: the log kind ("console", "timeline", …) and its title. */
export interface LogDocumentInfo {
  kind: string;
  title: string;
  /** When it was saved. Default now. */
  savedAt?: Date;
}

/**
 * A saved log as JSON: `{ format: "postsonde-log", version: 1, kind, title,
 * savedAt, count, entries }`, entries in the order shown, pretty-printed.
 */
export function jsonLogDocument(info: LogDocumentInfo, entries: readonly unknown[]): string {
  return JSON.stringify(
    {
      format: LOG_DOCUMENT_FORMAT,
      version: LOG_DOCUMENT_VERSION,
      kind: info.kind,
      title: info.title,
      savedAt: (info.savedAt ?? new Date()).toISOString(),
      count: entries.length,
      entries,
    },
    null,
    2,
  );
}

/**
 * A saved log as plain text: a header (title, time, entry count), a blank
 * line, then the entries, one per line when `oneLine`, else separated by
 * blank lines.
 */
export function textLogDocument(info: LogDocumentInfo, entries: readonly string[], oneLine = false): string {
  // In the language of the UI, like the entries (docs/frontend.md, Copying and saving logs); the time stays ISO 8601.
  const header = [
    t("lib.logExport.title", { title: info.title }),
    t("lib.logExport.saved", { time: (info.savedAt ?? new Date()).toISOString(), count: entries.length }),
  ];
  return `${header.join("\n")}\n\n${entries.join(oneLine ? "\n" : "\n\n")}\n`;
}

/** An ISO 8601 time from epoch milliseconds, or the text as it came when it is not a number. */
export function isoTime(ms: number): string {
  return Number.isFinite(ms) ? new Date(ms).toISOString() : String(ms);
}
