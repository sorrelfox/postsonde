// Pasting a curl command into the URL bar of an http request (IMP-01):
// the request is replaced by the one Go maps from the command, and a toast
// offers Undo and lists the warnings. See docs/frontend.md (cURL import).

import { openImportCurlDialog, replaceWithCurl } from "../../commands/curlImport";
import { showToast } from "../../commands/toasts";
import { parseCurl, type CurlImportDTO } from "../../lib/collection";
import { firstError, warningsOf } from "../../lib/curl";
import { toAppError } from "../../lib/errors";
import { t } from "../../i18n";
import type { RequestId, Stores } from "../../stores";
import { IconInfo, IconWarning } from "../common/icons";
import styles from "./curlPaste.module.css";

/** How long the toast after a paste stays, in ms; longer when it has more to read. */
export const PASTE_TOAST_MS = 8000;
export const PASTE_TOAST_LONG_MS = 15000;
/** At most this many warnings are listed in the toast. */
export const PASTE_TOAST_MAX_WARNINGS = 3;

const warningIcon = <IconWarning size={14} strokeWidth={1.9} />;

/**
 * Replaces the http request `id` with the request mapped from the curl
 * command `text` (method, URL, params, headers, body, auth, settings; the
 * name stays) as one undoable edit, and shows a toast with Undo and the
 * warnings. A command that cannot be imported changes nothing: the toast
 * says why and offers the Import cURL dialog with the text. Resolves to
 * whether the request was replaced.
 */
export async function pasteCurlIntoRequest(stores: Stores, id: RequestId, text: string): Promise<boolean> {
  let result: CurlImportDTO;
  try {
    result = await parseCurl(text);
  } catch (err: unknown) {
    showToast({ message: t("editor.curl.readFailed", { error: toAppError(err).message }), icon: warningIcon });
    return false;
  }
  const error = firstError(result.diagnostics);
  if (!result.ok || error !== null) {
    showToast({
      message:
        error === null || error.message === ""
          ? t("editor.curl.cannotImport")
          : t("editor.curl.cannotImportWhy", { reason: error.message }),
      icon: warningIcon,
      action: { label: t("editor.curl.openImport"), run: () => void openImportCurlDialog(text) },
      duration: PASTE_TOAST_LONG_MS,
    });
    return false;
  }
  const undo = replaceWithCurl(stores, id, result.request);
  if (undo === null) return false;

  const warnings = warningsOf(result.diagnostics);
  const shown = warnings.slice(0, PASTE_TOAST_MAX_WARNINGS);
  showToast({
    message: (
      <>
        {warnings.length === 0 ? t("editor.curl.replaced") : t("editor.curl.replacedWarnings", { count: warnings.length })}
        {shown.length > 0 && (
          <ul className={styles.warnings}>
            {shown.map((w, i) => (
              <li key={i}>{w.message}</li>
            ))}
            {warnings.length > shown.length && <li>{t("editor.curl.more", { count: warnings.length - shown.length })}</li>}
          </ul>
        )}
      </>
    ),
    icon: warnings.length > 0 ? warningIcon : <IconInfo size={14} strokeWidth={1.9} />,
    action: { label: t("editor.curl.undo"), run: () => void undo() },
    duration: warnings.length > 0 ? PASTE_TOAST_LONG_MS : PASTE_TOAST_MS,
  });
  return true;
}
