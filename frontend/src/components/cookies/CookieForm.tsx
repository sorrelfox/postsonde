import { useId, useRef } from "react";
import { useT } from "../../i18n/react";
import { cookieFromDraft, SAME_SITE_VALUES, type CookieDraft, type DraftResult } from "../../lib/cookies";
import { Dialog } from "../common/Dialog";
import styles from "./CookieManager.module.css";

export interface CookieFormProps {
  /** Adding a new cookie rather than editing one. */
  adding: boolean;
  draft: CookieDraft;
  onChange: (draft: CookieDraft) => void;
  /** The last save's failure; `field` names the field it is about, or "". */
  error: { message: string; field: string } | null;
  busy: boolean;
  /** Back to the list, without saving. */
  onCancel: () => void;
  /** Save: the draft checked by cookieFromDraft. */
  onSave: (result: DraftResult) => void;
}


/**
 * The add and edit form of the cookie manager (CK-02), in the manager's
 * dialog: name and value, domain with "include subdomains" (not
 * host-only), path, expiry or session, SameSite, Secure and HttpOnly. The
 * service checks the cookie; its error is shown under the field it names.
 */
export function CookieForm({ adding, draft, onChange, error, busy, onCancel, onSave }: CookieFormProps) {
  const t = useT();
  const id = useId();
  const nameRef = useRef<HTMLInputElement>(null);
  const set = <K extends keyof CookieDraft>(key: K, value: CookieDraft[K]) => onChange({ ...draft, [key]: value });
  const errorId = `${id}-error`;
  const invalid = (field: string) => (error !== null && error.field === field) || undefined;
  const describedBy = (field: string) => (error !== null && error.field === field ? errorId : undefined);
  const errorText = error !== null && (
    <span className={styles.error} id={errorId} role="alert">
      {error.message}
    </span>
  );
  const fieldError = (field: string) => error !== null && error.field === field && errorText;
  const knownField = error !== null && ["name", "value", "domain", "path", "expires", "sameSite"].includes(error.field);

  return (
    <Dialog
      open
      title={adding ? t("cookies.form.addTitle") : t("cookies.form.editTitle")}
      confirmLabel={adding ? t("cookies.form.add") : t("cookies.form.save")}
      cancelLabel={t("cookies.form.back")}
      onCancel={onCancel}
      onConfirm={() => onSave(cookieFromDraft(draft))}
      busy={busy}
      width={560}
      initialFocus={nameRef}
    >
      <div className={styles.formRow}>
        <div className={styles.field}>
          <label className={styles.label} htmlFor={`${id}-name`}>
            {t("cookies.form.name")}
          </label>
          <input
            id={`${id}-name`}
            ref={nameRef}
            className={`${styles.input} ${styles.mono}`}
            value={draft.name}
            spellCheck={false}
            autoComplete="off"
            readOnly={busy}
            aria-invalid={invalid("name")}
            aria-describedby={describedBy("name")}
            onChange={(e) => set("name", e.target.value)}
          />
          {fieldError("name")}
        </div>
        <div className={styles.field}>
          <label className={styles.label} htmlFor={`${id}-path`}>
            {t("cookies.form.path")}
          </label>
          <input
            id={`${id}-path`}
            className={`${styles.input} ${styles.mono}`}
            value={draft.path}
            placeholder="/"
            spellCheck={false}
            autoComplete="off"
            readOnly={busy}
            aria-invalid={invalid("path")}
            aria-describedby={describedBy("path")}
            onChange={(e) => set("path", e.target.value)}
          />
          {fieldError("path")}
        </div>
      </div>

      <div className={styles.field}>
        <label className={styles.label} htmlFor={`${id}-value`}>
          {t("cookies.form.value")}
        </label>
        <textarea
          id={`${id}-value`}
          className={`${styles.input} ${styles.textarea} ${styles.mono}`}
          value={draft.value}
          rows={3}
          spellCheck={false}
          autoComplete="off"
          readOnly={busy}
          aria-invalid={invalid("value")}
          aria-describedby={describedBy("value")}
          onChange={(e) => set("value", e.target.value)}
        />
        {fieldError("value")}
      </div>

      <div className={styles.field}>
        <label className={styles.label} htmlFor={`${id}-domain`}>
          {t("cookies.form.domain")}
        </label>
        <input
          id={`${id}-domain`}
          className={`${styles.input} ${styles.mono}`}
          value={draft.domain}
          placeholder="api.example.com"
          spellCheck={false}
          autoComplete="off"
          readOnly={busy}
          aria-invalid={invalid("domain")}
          aria-describedby={describedBy("domain")}
          onChange={(e) => set("domain", e.target.value)}
        />
        {fieldError("domain")}
        <label className={styles.check}>
          <input
            type="checkbox"
            checked={draft.includeSubdomains}
            disabled={busy}
            onChange={(e) => set("includeSubdomains", e.target.checked)}
          />
          {t("cookies.form.subdomains")}
        </label>
      </div>

      <div className={styles.formRow}>
        <div className={styles.field}>
          <label className={styles.label} htmlFor={`${id}-expires`}>
            {t("cookies.form.expires")}
          </label>
          <input
            id={`${id}-expires`}
            className={styles.input}
            type="datetime-local"
            value={draft.expires}
            disabled={busy || draft.session}
            aria-invalid={invalid("expires")}
            aria-describedby={describedBy("expires")}
            onChange={(e) => set("expires", e.target.value)}
          />
          {fieldError("expires")}
          <label className={styles.check}>
            <input type="checkbox" checked={draft.session} disabled={busy} onChange={(e) => set("session", e.target.checked)} />
            {t("cookies.form.session")}
          </label>
        </div>
        <div className={styles.field}>
          <label className={styles.label} htmlFor={`${id}-samesite`}>
            {/* i18n-ignore: the SameSite cookie attribute name */}
            SameSite
          </label>
          <select
            id={`${id}-samesite`}
            className={styles.input}
            value={draft.sameSite}
            disabled={busy}
            aria-invalid={invalid("sameSite")}
            onChange={(e) => set("sameSite", e.target.value as CookieDraft["sameSite"])}
          >
            {SAME_SITE_VALUES.map((v) => (
              <option key={v} value={v}>
                {/* The values (Lax, Strict, None) are the attribute's tokens; only "not set" is text. */}
                {v === "" ? t("cookies.form.sameSite.notSet") : v}
              </option>
            ))}
          </select>
          {fieldError("sameSite")}
        </div>
      </div>

      <div className={styles.checks}>
        <label className={styles.check}>
          <input type="checkbox" checked={draft.secure} disabled={busy} onChange={(e) => set("secure", e.target.checked)} />
          {/* i18n-ignore: the Secure cookie attribute name */}
          Secure <span className={styles.help}>{t("cookies.form.secure.help")}</span>
        </label>
        <label className={styles.check}>
          <input type="checkbox" checked={draft.httpOnly} disabled={busy} onChange={(e) => set("httpOnly", e.target.checked)} />
          {/* i18n-ignore: the HttpOnly cookie attribute name */}
          HttpOnly <span className={styles.help}>{t("cookies.form.httpOnly.help")}</span>
        </label>
      </div>

      {error !== null && !knownField && errorText}
    </Dialog>
  );
}
