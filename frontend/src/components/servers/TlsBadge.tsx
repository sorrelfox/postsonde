import { useT } from "../../i18n/react";
import type { LocalServer } from "../../stores";
import { IconLock } from "../common/icons";
import tlsStyles from "./TlsSettings.module.css";
import { describeTls, tlsProtocolLabel } from "./TlsSettings";

/** A small lock badge on a server that serves TLS (SRV-05): HTTPS, wss or TLS. */
export function TlsBadge({ server }: { server: LocalServer }) {
  const t = useT();
  if (server.tls === null) return null;
  const label = tlsProtocolLabel(server.type);
  return (
    <span className={tlsStyles.badge} title={t("servers.tlsBadge.title", { protocol: label, tls: describeTls(server.tls) })}
      aria-label={t("servers.tlsBadge.label", { protocol: label })}>
      <IconLock size={10} />
      {label}
    </span>
  );
}
