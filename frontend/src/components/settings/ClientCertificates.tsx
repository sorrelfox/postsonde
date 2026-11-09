import { useEffect, useId, useState, type ReactNode } from "react";
import { formatError, isCancelled } from "../../lib/errors";
import {
  addClientCert,
  listClientCerts,
  onSettingsChanged,
  pickClientCertFile,
  removeClientCert,
  updateClientCert,
  type ClientCertDTO,
  type ClientCertFileKind,
  type ClientCertInputDTO,
} from "../../lib/settings";
import { Dialog } from "../common/Dialog";
import { IconWarning } from "../common/icons";
import styles from "./ClientCertificates.module.css";
import { LockBadge, RowControl, useSettingLocked } from "./Locked";
import { t as translate } from "../../i18n";
import { useT } from "../../i18n/react";
import screen from "./SettingsScreen.module.css";

/**
 * Per-host client certificates for mTLS (AUTH-30, NET-05,
 * network.clientCerts): each is a host pattern (api.example.com,
 * *.example.com, localhost:8443) and a PEM certificate and key or a
 * PKCS#12 file. Every TLS connection (HTTP, GraphQL, SSE, WebSocket, gRPC)
 * to a matching host sends it when the server asks. Go reads the files
 * when one is added or edited and refuses what does not decode or match;
 * an expired certificate is kept with a warning. Passphrases go to Go's
 * local secret storage and never come back: only whether one is stored.
 */

type Kind = "pem" | "pkcs12";

/** The common name in a Go subject string ("CN=api client,O=Corp"), or the whole subject. */
export function commonName(subject: string): string {
  const m = /(?:^|,)CN=((?:[^,\\]|\\.)+)/.exec(subject);
  return m ? m[1]!.replace(/\\(.)/g, "$1") : subject;
}

/** "api client, PKCS#12, expires 2027-03-01" for a certificate's row. */
export function describeClientCert(c: ClientCertDTO): string {
  const parts: string[] = [];
  if (c.subject) parts.push(commonName(c.subject));
  parts.push(c.kind === "pkcs12" ? "PKCS#12" : "PEM");
  if (c.notAfter) {
    const date = c.notAfter.slice(0, 10);
    parts.push(c.expired ? translate("settings.clientCert.expired", { date }) : translate("settings.clientCert.expires", { date }));
  }
  return parts.join(", ");
}

export function ClientCertRows({ shorten }: { shorten: (path: string) => string }) {
  const t = useT();
  const locked = useSettingLocked("network.clientCerts");
  const [certs, setCerts] = useState<ClientCertDTO[]>([]);
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);
  const [editing, setEditing] = useState<ClientCertDTO | "new" | null>(null);
  const errorId = useId();

  useEffect(() => {
    let live = true;
    const refresh = () => {
      listClientCerts().then(
        (next) => {
          if (live) setCerts(next);
        },
        () => {
          // No Go backend (the browser, tests without it): nothing to list.
        },
      );
    };
    refresh();
    const off = onSettingsChanged(refresh);
    return () => {
      live = false;
      off();
    };
  }, []);

  const remove = (id: string) => {
    if (busy) return;
    setBusy(true);
    setError(null);
    removeClientCert(id)
      .then(setCerts, (err: unknown) => {
        if (!isCancelled(err)) setError(formatError(err));
      })
      .finally(() => setBusy(false));
  };

  return (
    <>
      <Row
        label={t("settings.clientCert.label")}
        locked={locked}
        description={t("settings.clientCert.description")}
        error={error}
        errorId={errorId}
      >
        {certs.length === 0 && <span className={screen.unit}>{t("settings.none")}</span>}
        <button
          type="button"
          className={screen.smallButton}
          disabled={busy}
          aria-describedby={error ? errorId : undefined}
          onClick={() => setEditing("new")}
        >
          {t("settings.clientCert.add")}
        </button>
      </Row>
      {certs.map((c) => {
        const files = c.kind === "pkcs12" ? [c.pfxFile] : [c.certFile, c.keyFile].filter((f) => f !== "");
        return (
          <Row
            key={c.id}
            locked={locked}
            label={c.host}
            description={
              <span className={screen.bundleDetails}>
                {files.map((f) => (
                  <span key={f} className={screen.path} title={f}>
                    {shorten(f)}
                  </span>
                ))}
                {!c.error && <span>{describeClientCert(c)}</span>}
                {(c.warnings ?? []).map((w) => (
                  <span key={w.code + w.message} className={screen.rowWarning} role="note" title={w.hint || undefined}>
                    <IconWarning size={13} />
                    {w.message}
                  </span>
                ))}
              </span>
            }
            error={c.error ? formatError(c.error) : null}
          >
            <button
              type="button"
              className={screen.smallButton}
              disabled={busy}
              aria-label={t("settings.clientCert.editLabel", { host: c.host })}
              onClick={() => setEditing(c)}
            >
              {t("settings.clientCert.edit")}
            </button>
            <button
              type="button"
              className={screen.smallButton}
              disabled={busy}
              aria-label={t("settings.clientCert.removeLabel", { host: c.host })}
              title={t("settings.clientCert.removeTitle")}
              onClick={() => remove(c.id)}
            >
              {t("settings.remove")}
            </button>
          </Row>
        );
      })}
      {editing !== null && (
        <ClientCertDialog
          existing={editing === "new" ? null : editing}
          onCancel={() => setEditing(null)}
          onSaved={(next) => {
            setCerts(next);
            setEditing(null);
          }}
        />
      )}
    </>
  );
}

/** A settings row, as SettingsScreen's (kept local to avoid an import cycle). */
function Row({
  label,
  description,
  error,
  errorId,
  locked = false,
  children,
}: {
  label: string;
  description?: ReactNode;
  error?: string | null;
  errorId?: string;
  /** Locked by the admin policy (SET-02): a lock, and the controls disabled. */
  locked?: boolean;
  children: ReactNode;
}) {
  return (
    <div className={screen.row} data-invalid={error ? "true" : undefined} data-locked={locked ? "true" : undefined}>
      <div className={screen.rowText}>
        <span className={screen.rowLabel}>
          {label}
          {locked && <LockBadge />}
        </span>
        {description && <span className={screen.rowDescription}>{description}</span>}
        {error && (
          <span id={errorId} className={screen.rowError} role="alert">
            <IconWarning size={13} />
            {error}
          </span>
        )}
      </div>
      <RowControl locked={locked}>{children}</RowControl>
    </div>
  );
}

interface DialogProps {
  existing: ClientCertDTO | null;
  onCancel: () => void;
  onSaved: (certs: ClientCertDTO[]) => void;
}

/**
 * Adds or edits one client certificate. The passphrase field starts empty:
 * when editing, leaving it untouched keeps the stored passphrase (sent as
 * null), and "Remove passphrase" clears it.
 */
function ClientCertDialog({ existing, onCancel, onSaved }: DialogProps) {
  const t = useT();
  const [host, setHost] = useState(existing?.host ?? "");
  const [kind, setKind] = useState<Kind>(existing?.kind === "pkcs12" ? "pkcs12" : "pem");
  const [certFile, setCertFile] = useState(existing?.certFile ?? "");
  const [keyFile, setKeyFile] = useState(existing?.keyFile ?? "");
  const [pfxFile, setPfxFile] = useState(existing?.pfxFile ?? "");
  const [passphrase, setPassphrase] = useState("");
  const [touched, setTouched] = useState(existing === null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [field, setField] = useState<string | null>(null);
  const ids = { host: useId(), cert: useId(), key: useId(), pfx: useId(), pass: useId(), help: useId(), error: useId() };
  const stored = existing?.hasPassphrase === true && !touched;

  const pick = (which: ClientCertFileKind, set: (path: string) => void) => {
    setError(null);
    pickClientCertFile(which).then(
      (path) => {
        if (path) set(path);
      },
      (err: unknown) => {
        if (!isCancelled(err)) setError(formatError(err));
      },
    );
  };

  const save = () => {
    if (busy) return;
    const input: ClientCertInputDTO = {
      host: host.trim(),
      certFile: kind === "pem" ? certFile.trim() : "",
      keyFile: kind === "pem" ? keyFile.trim() : "",
      pfxFile: kind === "pkcs12" ? pfxFile.trim() : "",
      passphrase: touched ? passphrase : null,
    };
    setBusy(true);
    setError(null);
    setField(null);
    const call = existing ? updateClientCert(existing.id, input) : addClientCert(input);
    call.then(onSaved, (err: unknown) => {
      setBusy(false);
      if (isCancelled(err)) return;
      setError(formatError(err));
      const details = (err as { details?: Record<string, string> }).details;
      setField(details?.field ?? null);
    });
  };

  const invalid = (...names: string[]) => (field !== null && names.includes(field) ? true : undefined);
  const ready = host.trim() !== "" && (kind === "pem" ? certFile.trim() !== "" : pfxFile.trim() !== "");

  return (
    <Dialog
      open
      title={existing ? t("settings.clientCert.titleEdit", { host: existing.host }) : t("settings.clientCert.titleAdd")}
      onCancel={onCancel}
      onConfirm={save}
      confirmLabel={existing ? t("settings.clientCert.save") : t("settings.clientCert.addConfirm")}
      confirmDisabled={!ready}
      busy={busy}
      width={520}
      describedBy={ids.help}
    >
      <div className={styles.form}>
        <p id={ids.help} className={styles.help}>
          {t("settings.clientCert.help")}
        </p>
        <div className={styles.field}>
          <label className={styles.label} htmlFor={ids.host}>
            {t("settings.clientCert.host")}
          </label>
          <input
            id={ids.host}
            className={styles.input}
            value={host}
            placeholder={t("settings.clientCert.hostPlaceholder")}
            spellCheck={false}
            autoComplete="off"
            aria-invalid={invalid("host")}
            onChange={(e) => setHost(e.target.value)}
            data-autofocus
          />
          <span className={styles.help}>{t("settings.clientCert.hostHelp")}</span>
        </div>
        <div className={styles.field}>
          <span className={styles.label}>{t("settings.clientCert.format")}</span>
          <div className={screen.segmented} role="group" aria-label={t("settings.clientCert.formatGroup")}>
            <button type="button" className={screen.segment} aria-pressed={kind === "pem"} onClick={() => setKind("pem")}>
              {t("settings.clientCert.formatPem")}
            </button>
            <button
              type="button"
              className={screen.segment}
              aria-pressed={kind === "pkcs12"}
              onClick={() => setKind("pkcs12")}
            >
              {t("settings.clientCert.formatPkcs12")}
            </button>
          </div>
        </div>
        {kind === "pem" ? (
          <>
            <FileField
              id={ids.cert}
              label={t("settings.clientCert.certificate")}
              pickLabel={t("settings.clientCert.pickCertificate")}
              value={certFile}
              onChange={setCertFile}
              onPick={() => pick("cert", setCertFile)}
              invalid={invalid("certFile")}
            />
            <FileField
              id={ids.key}
              label={t("settings.clientCert.privateKey")}
              pickLabel={t("settings.clientCert.pickKey")}
              help={t("settings.clientCert.keyHelp")}
              value={keyFile}
              onChange={setKeyFile}
              onPick={() => pick("key", setKeyFile)}
              invalid={invalid("keyFile")}
            />
          </>
        ) : (
          <FileField
            id={ids.pfx}
            label={t("settings.clientCert.pkcs12File")}
            pickLabel={t("settings.clientCert.pickPkcs12")}
            value={pfxFile}
            onChange={setPfxFile}
            onPick={() => pick("pfx", setPfxFile)}
            invalid={invalid("pfxFile")}
          />
        )}
        <div className={styles.field}>
          <label className={styles.label} htmlFor={ids.pass}>
            {t("settings.clientCert.passphrase")}
          </label>
          <div className={styles.fileLine}>
            <input
              id={ids.pass}
              type="password"
              className={styles.input}
              value={passphrase}
              placeholder={
                stored
                  ? t("settings.clientCert.passphraseStored")
                  : kind === "pkcs12"
                    ? t("settings.clientCert.passphrasePkcs12")
                    : t("settings.clientCert.passphrasePem")
              }
              autoComplete="off"
              onChange={(e) => {
                setPassphrase(e.target.value);
                setTouched(true);
              }}
            />
            {stored && (
              <button
                type="button"
                className={screen.smallButton}
                onClick={() => {
                  setPassphrase("");
                  setTouched(true);
                }}
              >
                {t("settings.clientCert.removePassphrase")}
              </button>
            )}
          </div>
          <span className={styles.help}>{t("settings.clientCert.passphraseHelp")}</span>
        </div>
        {error && (
          <p id={ids.error} className={styles.error} role="alert">
            <IconWarning size={13} />
            {error}
          </p>
        )}
      </div>
    </Dialog>
  );
}

function FileField({
  id,
  label,
  pickLabel,
  help,
  value,
  onChange,
  onPick,
  invalid,
}: {
  id: string;
  label: string;
  /** The accessible name of the Choose button. */
  pickLabel: string;
  help?: string;
  value: string;
  onChange: (value: string) => void;
  onPick: () => void;
  invalid?: boolean;
}) {
  const t = useT();
  return (
    <div className={styles.field}>
      <label className={styles.label} htmlFor={id}>
        {label}
      </label>
      <div className={styles.fileLine}>
        <input
          id={id}
          className={styles.input}
          value={value}
          placeholder={t("settings.clientCert.pathPlaceholder")}
          spellCheck={false}
          autoComplete="off"
          title={value || undefined}
          aria-invalid={invalid}
          onChange={(e) => onChange(e.target.value)}
        />
        <button type="button" className={screen.smallButton} aria-label={pickLabel} onClick={onPick}>
          {t("settings.clientCert.choose")}
        </button>
      </div>
      {help && <span className={styles.help}>{help}</span>}
    </div>
  );
}
