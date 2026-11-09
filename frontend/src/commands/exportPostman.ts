// Postman export (EXP-03): a folder, or the whole project, as a Postman
// Collection v2.1 file picked in the native save dialog
// (CollectionService.ExportPostman). The toast sums up what was written and
// opens the report of what was left out or changed. Secrets are never
// exported. See docs/import.md "Postman export".
import { createElement } from "react";
import { ExportReport } from "../components/tree/ExportReport";
import { t } from "../i18n";
import { toAppError } from "../lib/errors";
import { exportPostman, type ExportPostmanResultDTO } from "../lib/postmanExport";
import type { NodeId, Stores, TreeNode } from "../stores";
import { waitForQueuedEdits } from "./autosave";
import { confirm } from "./dialogs";
import { showToast } from "./toasts";

/** The requests under folder `id` (every folder for null), of any kind. */
function requestIds(nodes: Readonly<Record<NodeId, TreeNode>>, rootIds: readonly NodeId[], id: NodeId | null): NodeId[] {
  const out: NodeId[] = [];
  const visit = (nodeId: NodeId, depth: number) => {
    const node = nodes[nodeId];
    if (node === undefined || depth > 1000) return;
    if (node.kind === "folder") node.childIds.forEach((c) => visit(c, depth + 1));
    else if (node.kind === "request") out.push(node.id);
  };
  if (id === null) rootIds.forEach((r) => visit(r, 0));
  else visit(id, 0);
  return out;
}

/** The toast's one-line summary of an export. */
export function exportSummary(res: ExportPostmanResultDTO): string {
  const requests = t("commands.postman.requests", { count: res.requests });
  const what = res.folders > 0 ? t("commands.postman.inFolders", { requests, folders: t("commands.postman.folders", { count: res.folders }) }) : requests;
  const message = t("commands.postman.exported", { what });
  const extra: string[] = [];
  if (res.skipped > 0) extra.push(t("commands.postman.leftOut", { count: res.skipped }));
  const changed = res.warnings - (res.notes ?? []).filter((n) => n.skipped).length;
  if (changed > 0) extra.push(t("commands.postman.warnings", { count: changed }));
  if (extra.length === 0) return t("commands.postman.sentence", { text: message });
  return t("commands.postman.withExtra", { text: message, extra: extra.reduce((list, item) => t("commands.postman.list", { list, item })) });
}

/** Opens the report of an export: per item, what was left out or changed. */
export function showExportReport(res: ExportPostmanResultDTO): Promise<boolean> {
  return confirm({
    title: t("commands.postman.reportTitle"),
    message: t("commands.postman.reportMessage"),
    details: createElement(ExportReport, { notes: res.notes ?? [], collectionName: res.name }),
    confirmLabel: t("common.close"),
    hideCancel: true,
    width: 560,
  });
}

/**
 * Exports folder `id` (the whole project for null) as a Postman
 * collection. Queued edits reach Go first. The toast offers the report
 * when anything was left out or changed; a failure shows in a toast.
 * Never rejects.
 */
export async function exportAsPostman(stores: Stores, id: NodeId | null): Promise<void> {
  const { nodes, rootIds } = stores.collection.getState();
  try {
    await Promise.all(requestIds(nodes, rootIds, id).map((r) => waitForQueuedEdits(stores, r)));
    const res = await exportPostman(id ?? "");
    if (!res.saved) return;
    const notes = res.notes ?? [];
    showToast({
      message: exportSummary(res),
      action: notes.length > 0 ? { label: t("commands.postman.showReport"), run: () => showExportReport(res) } : undefined,
    });
  } catch (err: unknown) {
    showToast({ message: t("commands.postman.failed", { error: toAppError(err).message }) });
  }
}
