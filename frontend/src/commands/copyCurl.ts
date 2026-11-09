// Copy as cURL (GEN-01, EXP-01): request → curl command → clipboard, from
// the request editors (button and shortcut) and the tree's context menu.
// Go builds the command from the request as it would send it
// (HTTPService.CurlCommand); secrets are masked unless the user asks for
// them from the toast (PRV-09). See docs/curl.md (Copy as cURL).
import { t } from "../i18n";
import { copyText } from "../lib/clipboard";
import { toAppError } from "../lib/errors";
import { curlCommand } from "../lib/http";
import type { RequestId, Stores, TreeNode } from "../stores";
import { waitForQueuedEdits } from "./autosave";
import { showToast } from "./toasts";

/** File kinds that are sent over HTTP, which a curl command can express. */
const CURL_KINDS: ReadonlySet<string> = new Set(["http", "graphql", "sse"]);

/** Whether a tree item can be copied as cURL: an HTTP, GraphQL or SSE request. */
export function canCopyAsCurl(node: TreeNode | undefined): boolean {
  return node !== undefined && node.kind === "request" && CURL_KINDS.has(node.fileKind);
}

/**
 * Copies request `id` as a curl command and says so in a toast. The
 * editor's queued edits reach Go first, so the command matches the
 * screen. Masked secrets are offered with a "Copy with secrets" action;
 * undefined variables are named. A failure shows in the toast. Never
 * rejects.
 */
export async function copyAsCurl(stores: Stores, id: RequestId, includeSecrets = false): Promise<void> {
  try {
    await waitForQueuedEdits(stores, id);
    const dto = await curlCommand(id, includeSecrets);
    await copyText(dto.command);
    const unresolved = dto.unresolved ?? [];
    const copied = dto.masked ? t("commands.curl.copiedMasked") : t("commands.curl.copied");
    const message =
      unresolved.length > 0
        ? t("commands.curl.unresolved", { text: copied, count: unresolved.length, list: unresolved.map((n) => `{{${n}}}`).join(", ") })
        : t("commands.curl.sentence", { text: copied });
    showToast({
      message,
      action: dto.masked ? { label: t("commands.curl.withSecrets"), run: () => copyAsCurl(stores, id, true) } : undefined,
    });
  } catch (err: unknown) {
    showToast({ message: t("commands.curl.failed", { error: toAppError(err).message }) });
  }
}
