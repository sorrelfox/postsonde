// Following a link from a note (COL-07, docs/collection.md#notes): after
// the user confirms, the URL opens in the system browser, never in the
// app. Notes come from project files, which may be someone else's, so the
// dialog shows the full URL before anything leaves the app.

import { t } from "../i18n";
import { toAppError } from "../lib/errors";
import { openInBrowser } from "../lib/browser";
import { linkTarget } from "../lib/markdown";
import { confirm } from "./dialogs";
import { showToast } from "./toasts";

/**
 * Asks, then opens `url` in the system browser. Resolves true when it was
 * opened; false when the user cancelled, the URL is not http(s) or mailto,
 * or opening failed (shown as a toast).
 */
export async function openExternalLink(url: string): Promise<boolean> {
  const target = linkTarget(url);
  if (target.kind !== "external") return false;
  const mail = target.url.toLowerCase().startsWith("mailto:");
  const ok = await confirm({
    title: mail ? t("commands.links.mailTitle") : t("commands.links.title"),
    message: target.url,
    confirmLabel: mail ? t("commands.links.openMail") : t("commands.links.open"),
  });
  if (!ok) return false;
  try {
    await openInBrowser(target.url);
    return true;
  } catch (err: unknown) {
    showToast({ message: t("commands.links.failed", { error: toAppError(err).message }) });
    return false;
  }
}
