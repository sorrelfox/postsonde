import { useEffect, useRef, useState } from "react";
import { useT } from "../../i18n/react";
import type { LocalServer } from "../../stores";
import { IconCopy } from "../common/icons";
import { canCopyAddress, serverAddress } from "./format";
import styles from "./Servers.module.css";

/** The server's address; while it runs, a click copies it. */
export function CopyAddress({ server }: { server: LocalServer }) {
  const t = useT();
  const [copied, setCopied] = useState<"ok" | "failed" | null>(null);
  const timer = useRef<ReturnType<typeof setTimeout> | null>(null);
  useEffect(
    () => () => {
      if (timer.current !== null) clearTimeout(timer.current);
    },
    [],
  );
  const address = serverAddress(server);

  if (!canCopyAddress(server)) {
    return <span className={`${styles.address} ${styles.addressIdle}`}>{address}</span>;
  }

  const copy = () => {
    const done = (state: "ok" | "failed") => {
      setCopied(state);
      if (timer.current !== null) clearTimeout(timer.current);
      timer.current = setTimeout(() => setCopied(null), 1500);
    };
    if (!navigator.clipboard) {
      done("failed");
      return;
    }
    navigator.clipboard.writeText(address).then(
      () => done("ok"),
      () => done("failed"),
    );
  };

  const all = server.addresses.join(", ");
  return (
    <button
      type="button"
      className={styles.address}
      title={server.addresses.length > 1 ? t("servers.copyAddress.titleMany", { all, address }) : t("servers.copyAddress.title", { address })}
      aria-label={t("servers.copyAddress.label", { address })}
      onClick={copy}
    >
      <span className={styles.addressText}>{address}</span>
      <IconCopy size={11} />
      {copied !== null && (
        <span className={styles.copied} role="status">
          {copied === "ok" ? t("servers.copyAddress.copied") : t("servers.copyAddress.failed")}
        </span>
      )}
    </button>
  );
}
