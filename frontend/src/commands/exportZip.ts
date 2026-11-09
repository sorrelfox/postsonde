// Folder zip (EXP-02): a folder, with its subfolders and requests, as a
// zip of PostSonde project files picked in the native save dialog
// (CollectionService.ExportFolderZip). Secrets are never in it; the toast
// says so, counts the secrets typed into files by hand that were left
// out, and opens the report of what was left out. See docs/import.md
// "Folder zips".
import { createElement } from "react";
import { ExportReport } from "../components/tree/ExportReport";
import { t } from "../i18n";
import { toAppError } from "../lib/errors";
import { exportFolderZip, type ExportZipResultDTO } from "../lib/zipExport";
import type { NodeId, Stores, TreeNode } from "../stores";
import { waitForQueuedEdits } from "./autosave";
import { confirm } from "./dialogs";
import { showToast } from "./toasts";

/** The requests under folder `id`, of any kind. */
function requestIds(nodes: Readonly<Record<NodeId, TreeNode>>, id: NodeId): NodeId[] {
  const out: NodeId[] = [];
  const visit = (nodeId: NodeId, depth: number) => {
    const node = nodes[nodeId];
    if (node === undefined || depth > 1000) return;
    if (node.kind === "folder") node.childIds.forEach((c) => visit(c, depth + 1));
    else if (node.kind === "request") out.push(node.id);
  };
  visit(id, 0);
  return out;
}

/** The toast's one-line summary of a zip export. */
export function zipExportSummary(res: ExportZipResultDTO): string {
  const requests = t("commands.zip.requests", { count: res.requests });
  const what = res.folders > 1 ? t("commands.zip.inFolders", { requests, folders: t("commands.zip.folders", { count: res.folders }) }) : requests;
  const message = t("commands.zip.exported", { what });
  const extra: string[] = [];
  if (res.secretsRemoved > 0) extra.push(t("commands.zip.secretsRemoved", { count: res.secretsRemoved }));
  if (res.skipped > 0) extra.push(t("commands.zip.leftOut", { count: res.skipped }));
  const other = res.warnings - (res.notes ?? []).filter((n) => n.skipped).length;
  if (other > 0) extra.push(t("commands.zip.warnings", { count: other }));
  if (extra.length === 0) return t("commands.zip.sentence", { text: message });
  return t("commands.zip.withExtra", { text: message, extra: extra.reduce((list, item) => t("commands.zip.list", { list, item })) });
}

/** Opens the report of a zip export: per item, what was left out. */
export function showZipExportReport(res: ExportZipResultDTO): Promise<boolean> {
  return confirm({
    title: t("commands.zip.reportTitle"),
    message: t("commands.zip.reportMessage"),
    details: createElement(ExportReport, { notes: res.notes ?? [], collectionName: res.name }),
    confirmLabel: t("common.close"),
    hideCancel: true,
    width: 560,
  });
}

/**
 * Exports folder `id` as a zip. Queued edits reach Go first. The toast
 * offers the report when anything was left out; a failure shows in a
 * toast. Never rejects.
 */
export async function exportAsZip(stores: Stores, id: NodeId): Promise<void> {
  const { nodes } = stores.collection.getState();
  try {
    await Promise.all(requestIds(nodes, id).map((r) => waitForQueuedEdits(stores, r)));
    const res = await exportFolderZip(id);
    if (!res.saved) return;
    const notes = res.notes ?? [];
    showToast({
      message: zipExportSummary(res),
      action: notes.length > 0 ? { label: t("commands.zip.showReport"), run: () => showZipExportReport(res) } : undefined,
    });
  } catch (err: unknown) {
    showToast({ message: t("commands.zip.failed", { error: toAppError(err).message }) });
  }
}
