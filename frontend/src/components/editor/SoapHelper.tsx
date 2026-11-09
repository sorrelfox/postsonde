import { useEffect, useRef, useState } from "react";
import { applySoapEnvelope, SOAP_VERSIONS, type SoapVersion } from "../../commands/soap";
import { useT } from "../../i18n/react";
import { useStores, type RequestId } from "../../stores";
import styles from "./BodyEditor.module.css";

export interface SoapHelperProps {
  id: RequestId;
  readOnly: boolean;
  onClose: () => void;
}

/**
 * The SOAP helper of the Body section (HTTP-18): version, action and
 * "Apply", which puts the envelope in a raw XML body and sets Content-Type
 * and SOAPAction (commands/soap.ts). What it writes is an ordinary body and
 * ordinary headers, edited as usual afterwards; the version and action
 * fields are not saved.
 */
export function SoapHelper({ id, readOnly, onClose }: SoapHelperProps) {
  const t = useT();
  const stores = useStores();
  const [version, setVersion] = useState<SoapVersion>("1.1");
  const [action, setAction] = useState("");
  const [busy, setBusy] = useState(false);
  const [message, setMessage] = useState<string | null>(null);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  const apply = async () => {
    setBusy(true);
    setMessage(null);
    const outcome = await applySoapEnvelope(stores, id, version, action.trim());
    if (!mounted.current) return;
    setBusy(false);
    if (outcome.status === "failed") setMessage(outcome.message);
    else if (outcome.status === "skipped") setMessage(t("editor.soap.changed"));
    else setMessage(version === "1.1" ? t("editor.soap.applied11") : t("editor.soap.applied12"));
  };

  return (
    <div className={styles.soap} role="group" aria-label={t("editor.soap.label")}>
      <select
        className={styles.type}
        aria-label={t("editor.soap.version")}
        value={version}
        disabled={readOnly || busy}
        onChange={(e) => setVersion(e.target.value as SoapVersion)}
      >
        {SOAP_VERSIONS.map((v) => (
          <option key={v.version} value={v.version}>
            {v.label}
          </option>
        ))}
      </select>
      <input
        className={styles.soapAction}
        aria-label={t("editor.soap.action")}
        placeholder={t("editor.soap.action.placeholder")}
        value={action}
        readOnly={readOnly}
        onChange={(e) => setAction(e.target.value)}
      />
      <button type="button" className={styles.tool} disabled={readOnly || busy} onClick={() => void apply()}>
        {t("editor.soap.apply")}
      </button>
      <button type="button" className={styles.tool} onClick={onClose}>
        {t("common.close")}
      </button>
      <p className={styles.soapNote}>{message ?? t("editor.soap.note")}</p>
    </div>
  );
}
