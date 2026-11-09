import { useEffect, useState } from "react";
import { formatDate } from "../../i18n";
import { useT } from "../../i18n/react";
import { formatUptime } from "./format";

export interface UptimeProps {
  /** Epoch ms the server started running. */
  since: number;
  /** Clock, for tests. Default Date.now. */
  now?: () => number;
}

/** A running server's uptime, updated every second. */
export function Uptime({ since, now = Date.now }: UptimeProps) {
  const t = useT();
  const [current, setCurrent] = useState(now);
  useEffect(() => {
    setCurrent(now());
    const id = setInterval(() => setCurrent(now()), 1000);
    return () => clearInterval(id);
  }, [since, now]);
  return (
    <span title={t("servers.uptime.since", { time: formatDate(since, { dateStyle: "medium", timeStyle: "medium" }) })}
      aria-label={t("servers.uptime.label", { uptime: formatUptime(current - since) })}>
      {formatUptime(current - since)}
    </span>
  );
}
