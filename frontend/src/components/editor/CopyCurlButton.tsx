import { copyAsCurl } from "../../commands/copyCurl";
import { ariaShortcut, shortcutLabel } from "../../lib/shortcuts";
import { useT } from "../../i18n/react";
import { useStores, type RequestId } from "../../stores";
import { IconCopy } from "../common/icons";
import { useShortcut } from "../shell/useShortcut";
import styles from "./RequestEditor.module.css";

/**
 * Copy as cURL (GEN-01, EXP-01) in the URL bar of the HTTP, GraphQL and
 * SSE editors, and its shortcut while the editor is open: the request as a
 * curl command on the clipboard, secrets masked (commands/copyCurl.ts).
 */
export function CopyCurlButton({ id }: { id: RequestId }) {
  const t = useT();
  const stores = useStores();
  useShortcut("request.copyCurl", () => {
    void copyAsCurl(stores, id);
  });
  return (
    <button
      type="button"
      className={styles.copyCurl}
      aria-label={t("editor.copyCurl")}
      title={t("editor.copyCurl.title", { shortcut: shortcutLabel("request.copyCurl") })}
      aria-keyshortcuts={ariaShortcut("request.copyCurl")}
      onClick={() => void copyAsCurl(stores, id)}
    >
      <IconCopy size={14} />
    </button>
  );
}
