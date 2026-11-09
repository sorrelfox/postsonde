import { useState } from "react";
import { useT } from "../../i18n/react";
import { IconCookie } from "../common/icons";
import { CookieManager } from "./CookieManager";
import styles from "./CookieManager.module.css";

/**
 * The status bar's "Cookies" button: opens the cookie manager (CK-02) of
 * the open project. Mounted by the project shell in the status bar.
 */
export function CookiesButton() {
  const t = useT();
  const [open, setOpen] = useState(false);
  return (
    <>
      <button
        type="button"
        className={styles.statusButton}
        aria-haspopup="dialog"
        title={t("cookies.button.title")}
        onClick={() => setOpen(true)}
      >
        <IconCookie size={13} />
        {t("cookies.button.label")}
      </button>
      <CookieManager open={open} onClose={() => setOpen(false)} />
    </>
  );
}
