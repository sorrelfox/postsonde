// HAR export (EXP-04): a folder's requests (HTTPService.ExportHAR, built as
// they would be sent, not sent) or the request history
// (HistoryService.ExportHAR, as recorded), to a file picked in the native
// save dialog. A folder's secrets are masked unless the user asks for them
// from the toast (PRV-09), as for Copy as cURL; the history is always
// masked. See docs/har.md.
import { t } from "../i18n";
import { toAppError } from "../lib/errors";
import { exportHistoryHar, exportRequestsHar } from "../lib/har";
import type { NodeId, Stores, TreeNode } from "../stores";
import { waitForQueuedEdits } from "./autosave";
import { showToast } from "./toasts";

/** File kinds sent over HTTP, which a HAR entry can hold. */
const HAR_KINDS: ReadonlySet<string> = new Set(["http", "graphql", "sse"]);

/** Whether a tree item is a request whose sends the history records (HTTP, GraphQL). */
export function hasHistory(node: TreeNode | undefined): boolean {
  return node !== undefined && node.kind === "request" && (node.fileKind === "http" || node.fileKind === "graphql");
}

/** The HTTP, GraphQL and SSE requests under folder `id`, depth first in tree order. */
export function harRequestIds(nodes: Readonly<Record<NodeId, TreeNode>>, id: NodeId): NodeId[] {
  const out: NodeId[] = [];
  const visit = (nodeId: NodeId, depth: number) => {
    const node = nodes[nodeId];
    if (node === undefined || depth > 1000) return;
    if (node.kind === "folder") {
      for (const child of node.childIds) visit(child, depth + 1);
    } else if (node.kind === "request" && HAR_KINDS.has(node.fileKind)) {
      out.push(node.id);
    }
  };
  visit(id, 0);
  return out;
}

/**
 * Exports folder `id`'s HTTP, GraphQL and SSE requests as a HAR file.
 * Queued edits reach Go first. Masked secrets are offered with an
 * "Export with secrets" action; skipped requests are counted. A failure
 * shows in a toast. Never rejects.
 */
export async function exportFolderAsHar(stores: Stores, id: NodeId, includeSecrets = false): Promise<void> {
  const { nodes } = stores.collection.getState();
  const folder = nodes[id];
  const requestIds = harRequestIds(nodes, id);
  if (requestIds.length === 0) {
    showToast({ message: t("commands.har.nothing") });
    return;
  }
  try {
    await Promise.all(requestIds.map((r) => waitForQueuedEdits(stores, r)));
    const res = await exportRequestsHar(folder?.name ?? "", requestIds, includeSecrets);
    if (!res.saved) return;
    const exported = t(res.masked ? "commands.har.exportedMasked" : "commands.har.exported", { count: res.entries });
    const skipped = res.skipped ?? [];
    const message =
      skipped.length > 0
        ? t("commands.har.skipped", { text: exported, count: skipped.length, reason: (skipped[0]?.message ?? "").replace(/\.$/, "") })
        : t("commands.har.sentence", { text: exported });
    showToast({
      message,
      action: res.masked ? { label: t("commands.har.withSecrets"), run: () => exportFolderAsHar(stores, id, true) } : undefined,
    });
  } catch (err: unknown) {
    showToast({ message: t("commands.har.failed", { error: toAppError(err).message }) });
  }
}

/**
 * Exports the request history as a HAR file: request `requestId`'s entries,
 * or every entry when it is null. Entries are masked as recorded. A
 * failure shows in a toast. Never rejects.
 */
export async function exportHistoryAsHar(stores: Stores, requestId: NodeId | null): Promise<void> {
  const node = requestId === null ? undefined : stores.collection.getState().nodes[requestId];
  try {
    const res = await exportHistoryHar(requestId ?? "", node === undefined ? "" : t("commands.har.historyName", { name: node.name }));
    if (!res.saved) return;
    showToast({ message: t(res.masked ? "commands.har.historyExportedMasked" : "commands.har.historyExported", { count: res.entries }) });
  } catch (err: unknown) {
    showToast({ message: t("commands.har.historyFailed", { error: toAppError(err).message }) });
  }
}
