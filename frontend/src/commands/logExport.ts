// Copy and save of timeline and log entries (DBG-07). Every copy and save
// goes through Go (lib/logExport.ts), which masks the secrets the app keeps,
// so the clipboard and saved files never hold one, whatever a view shows.
// Results and failures show as toasts; nothing here rejects.
// See docs/frontend.md (Copying and saving logs) and docs/privacy.md.
import { t } from "../i18n";
import { copyText } from "../lib/clipboard";
import { formatError } from "../lib/errors";
import { maskLogText, saveLogFile, type LogFormat } from "../lib/logExport";
import { showToast } from "./toasts";

const COPY_TOAST_MS = 3000;

/**
 * A log's title inside a sentence: the first letter in lower case
 * ("Console" → "console"), unless the first word has another capital
 * ("WebSocket messages", "TCP messages" stay).
 */
export function inSentence(title: string): string {
  const first = title.split(" ")[0].slice(1);
  if (first !== first.toLowerCase()) return title;
  return title.charAt(0).toLowerCase() + title.slice(1);
}

/**
 * Masks `text` (an entry, or several) in Go and puts it on the clipboard.
 * `what` names it in the toast ("entry", "3 records"). Resolves true when
 * it was copied.
 */
export async function copyLogText(text: string, format: LogFormat = "text", what = t("commands.log.entry")): Promise<boolean> {
  let masked = false;
  try {
    const res = await maskLogText(text, format);
    masked = res.masked;
    await copyText(res.text);
  } catch (err: unknown) {
    showToast({ message: t("commands.log.copyFailed", { what, error: formatError(err) }), duration: COPY_TOAST_MS });
    return false;
  }
  const key =
    format === "json"
      ? masked
        ? "commands.log.copiedJsonMasked"
        : "commands.log.copiedJson"
      : masked
        ? "commands.log.copiedMasked"
        : "commands.log.copied";
  showToast({ message: t(key, { what }), duration: COPY_TOAST_MS });
  return true;
}

export interface SaveLogOptions {
  /** Names the log: "Console", "WebSocket messages". */
  title: string;
  /** Suggested file name without extension. */
  fileName: string;
  format: LogFormat;
  /** Builds the whole log in `format`; called once, when saving. */
  build: (format: LogFormat) => string;
}

/**
 * Builds a log, masks it in Go and saves it where the user picks in the
 * native save dialog. Resolves with the path written, or null when the
 * user cancelled or it failed (a toast says why).
 */
export async function saveLog({ title, fileName, format, build }: SaveLogOptions): Promise<string | null> {
  try {
    const res = await saveLogFile({ title, fileName, format, content: build(format) });
    if (!res.saved) return null;
    showToast({ message: t(res.masked ? "commands.log.savedMasked" : "commands.log.saved", { log: inSentence(title), path: res.path }) });
    return res.path;
  } catch (err: unknown) {
    showToast({ message: t("commands.log.saveFailed", { log: inSentence(title), error: formatError(err) }) });
    return null;
  }
}
