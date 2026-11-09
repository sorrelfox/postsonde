// The data directory (SET-03) and portable mode (APP-04) seen from the UI:
// restarting to carry out a scheduled change, and the notice shown once at
// startup after a move or when the data directory has a problem. See
// docs/settings.md (Data directory).

import { t } from "../i18n";
import { getDataDirInfo, restartApp, type DataDirInfo } from "../lib/dataDir";
import { formatError } from "../lib/errors";
import type { Stores } from "../stores";
import { flushSettings } from "./settings";
import { showToast } from "./toasts";
import { leaveWorkspace } from "./workspaceLeave";

/**
 * Saves what the UI still holds (queued settings, the open project's edits
 * and view state), then asks Go to quit and start again. Go's services
 * save their own state as on any quit.
 */
export async function restartToApply(stores: Stores): Promise<void> {
  await flushSettings();
  await leaveWorkspace(stores);
  await restartApp();
}

/** What the startup notice says, or null when there is nothing to say. */
export function startupNotice(info: DataDirInfo): string | null {
  const problems = info.problems ?? [];
  if (problems.length > 0) {
    const first = problems[0]!;
    const error = formatError(first);
    return problems.length > 1
      ? t("commands.dataDir.problemMore", { error, count: problems.length - 1 })
      : t("commands.dataDir.problem", { error });
  }
  if (info.moved) return t("commands.dataDir.moved", { path: info.dataDir });
  return null;
}

let noticeShown = false;

/**
 * Shows the startup notice once per app session as a toast that stays
 * until dismissed, with `open` (the settings screen) as its action.
 */
export async function showDataDirStartupNotice(open: () => void): Promise<void> {
  if (noticeShown) return;
  noticeShown = true;
  let info: DataDirInfo;
  try {
    info = await getDataDirInfo();
  } catch {
    return;
  }
  const message = startupNotice(info);
  if (message === null) return;
  showToast({ message, duration: 0, action: { label: t("commands.dataDir.settings"), run: open } });
}

/** Test hook: lets the notice show again. */
export function resetDataDirNoticeForTests(): void {
  noticeShown = false;
}
