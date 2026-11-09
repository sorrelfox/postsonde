import { useEffect, useId, useRef, useState, type FormEvent } from "react";
import { confirm } from "../../commands/dialogs";
import {
  clearRuntimeVariables,
  deleteRuntimeVariable,
  listRuntimeVariables,
  onRuntimeVariablesChanged,
  setRuntimeVariable,
  type RuntimeVariableDTO,
  type RuntimeVariablesDTO,
} from "../../lib/runtimeVariables";
import { toAppError } from "../../lib/errors";
import { Trans, useT } from "../../i18n/react";
import { IconEye, IconEyeOff, IconLock, IconPin, IconTrash } from "../common/icons";
import panel from "./EnvironmentsPanel.module.css";
import styles from "./RuntimeVariables.module.css";

/** What a masked value shows, as the timeline masks it. */
const MASK = "••••••";

type ListState =
  | { readonly status: "loading" }
  | { readonly status: "error"; readonly message: string }
  | { readonly status: "ready"; readonly variables: readonly RuntimeVariableDTO[] };

interface Draft {
  /** The name of the variable being edited; "" when adding one. */
  readonly previousName: string;
  readonly name: string;
  readonly value: string;
  readonly secret: boolean;
  readonly persist: boolean;
}

const EMPTY_DRAFT: Draft = { previousName: "", name: "", value: "", secret: false, persist: false };

/**
 * The open project's runtime variables (ENV-15): set while working, here
 * or (later) from a response, they win over every scope a project file
 * defines. Each is kept for the session or persisted in PostSonde's local
 * data, and may be secret (masked like every secret). Loaded when shown,
 * again after every runtimevars:changed event; every call answers with the
 * whole list. Remount it (key) for another project.
 */
export function RuntimeVariables() {
  const t = useT();
  const baseId = useId();
  const [state, setState] = useState<ListState>({ status: "loading" });
  const [attempt, setAttempt] = useState(0);
  const [draft, setDraft] = useState<Draft>(EMPTY_DRAFT);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [revealed, setRevealed] = useState<Record<string, true>>({});
  const nameInput = useRef<HTMLInputElement>(null);
  const mounted = useRef(true);

  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  useEffect(() => {
    let alive = true;
    listRuntimeVariables().then(
      (list) => {
        if (alive) setState({ status: "ready", variables: list.variables ?? [] });
      },
      (err: unknown) => {
        if (alive) setState({ status: "error", message: toAppError(err).message });
      },
    );
    return () => {
      alive = false;
    };
  }, [attempt]);
  // Set elsewhere too (extraction from a response): read the list again.
  useEffect(() => onRuntimeVariablesChanged(() => setAttempt((n) => n + 1)), []);

  const apply = (list: RuntimeVariablesDTO) => {
    if (mounted.current) setState({ status: "ready", variables: list.variables ?? [] });
  };

  /** Runs a change; its result is the new list, its failure the error shown. */
  const run = async (change: () => Promise<RuntimeVariablesDTO>): Promise<boolean> => {
    setBusy(true);
    setError(null);
    try {
      apply(await change());
      return true;
    } catch (err: unknown) {
      if (mounted.current) setError(toAppError(err).message);
      return false;
    } finally {
      if (mounted.current) setBusy(false);
    }
  };

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (busy) return;
    const d = { ...draft, name: draft.name.trim() };
    void run(() => setRuntimeVariable(d)).then((ok) => {
      if (!ok || !mounted.current) return;
      setDraft(EMPTY_DRAFT);
      nameInput.current?.focus();
    });
  };

  const toggle = (v: RuntimeVariableDTO, flag: "secret" | "persist") => {
    const next = flag === "secret" ? { ...v, secret: !v.secret } : { ...v, persist: !v.persist };
    void run(() => setRuntimeVariable({ previousName: v.name, ...next }));
  };

  const remove = (v: RuntimeVariableDTO) => {
    if (draft.previousName === v.name) setDraft(EMPTY_DRAFT);
    void run(() => deleteRuntimeVariable(v.name));
  };

  const clearAll = async () => {
    const confirmed = await confirm({
      title: t("environments.runtime.clear.title"),
      message: t("environments.runtime.clear.body"),
      confirmLabel: t("environments.runtime.clear.confirm"),
      confirmIcon: <IconTrash size={14} />,
      variant: "danger",
    });
    if (!confirmed) return;
    setDraft(EMPTY_DRAFT);
    await run(() => clearRuntimeVariables());
  };

  const edit = (v: RuntimeVariableDTO) => {
    setDraft({ previousName: v.name, name: v.name, value: v.value, secret: v.secret, persist: v.persist });
    setError(null);
    nameInput.current?.focus();
  };

  const toggleRevealed = (name: string) =>
    setRevealed((r) => {
      const next = { ...r };
      if (next[name]) delete next[name];
      else next[name] = true;
      return next;
    });

  const editing = draft.previousName !== "";
  const variables = state.status === "ready" ? state.variables : [];
  const formTitleId = `${baseId}-form`;

  return (
    <section className={panel.details} aria-label={t("environments.runtime.label")}>
      <div className={panel.detailsHeader}>
        <span className={panel.detailsTitle}>
          <Trans k="environments.details.title" values={{ name: <strong>{t("environments.runtime.titleName")}</strong> }} />
        </span>
        <button
          type="button"
          className={panel.smallButton}
          disabled={busy || variables.length === 0}
          onClick={() => void clearAll()}
        >
          {t("environments.runtime.clearAll")}
        </button>
      </div>

      {state.status === "loading" && <p className={panel.note}>{t("environments.runtime.loading")}</p>}
      {state.status === "error" && (
        <div className={panel.problem} role="alert">
          <p>{state.message}</p>
          <div className={panel.buttons}>
            <button
              type="button"
              className={panel.smallButton}
              onClick={() => {
                setState({ status: "loading" });
                setAttempt((n) => n + 1);
              }}
            >
              {t("environments.panel.retry")}
            </button>
            <button type="button" className={panel.smallButton} disabled={busy} onClick={() => void clearAll()}>
              {t("environments.runtime.clearAll")}
            </button>
          </div>
        </div>
      )}

      {state.status === "ready" && variables.length === 0 && (
        <p className={panel.note}>{t("environments.runtime.empty")}</p>
      )}

      {variables.length > 0 && (
        <ul className={styles.list} aria-label={t("environments.runtime.listLabel")}>
          {variables.map((v) => {
            const shown = !v.secret || revealed[v.name] === true;
            return (
              <li
                key={v.name}
                className={draft.previousName === v.name ? `${styles.row} ${styles.rowEditing}` : styles.row}
              >
                <button
                  type="button"
                  className={`${panel.linkButton} ${styles.name}`}
                  title={t("environments.runtime.editTitle", { name: v.name })}
                  aria-label={t("environments.runtime.edit", { name: v.name })}
                  onClick={() => edit(v)}
                >
                  {v.name}
                </button>
                <span className={v.value === "" ? `${styles.value} ${styles.valueEmpty}` : styles.value}>
                  {v.value === "" ? t("environments.runtime.emptyValue") : shown ? v.value : MASK}
                </span>
                <span className={styles.actions}>
                  <span
                    className={v.persist ? `${styles.tag} ${styles.tagKept}` : styles.tag}
                    title={v.persist ? t("environments.runtime.keptTitle") : t("environments.runtime.sessionTitle")}
                  >
                    {v.persist ? t("environments.runtime.kept") : t("environments.runtime.session")}
                  </span>
                  {v.secret && (
                    <button
                      type="button"
                      className={panel.rowButton}
                      aria-label={
                        shown
                          ? t("environments.runtime.hideValue", { name: v.name })
                          : t("environments.runtime.showValue", { name: v.name })
                      }
                      title={shown ? t("environments.variables.hideValue") : t("environments.variables.showValue")}
                      onClick={() => toggleRevealed(v.name)}
                    >
                      {shown ? <IconEyeOff size={12} /> : <IconEye size={12} />}
                    </button>
                  )}
                  <button
                    type="button"
                    className={v.secret ? `${panel.rowButton} ${panel.rowButtonOn}` : panel.rowButton}
                    aria-label={t("environments.runtime.secret", { name: v.name })}
                    aria-pressed={v.secret}
                    title={v.secret ? t("environments.runtime.secretOn") : t("environments.runtime.secretOff")}
                    disabled={busy}
                    onClick={() => toggle(v, "secret")}
                  >
                    <IconLock size={12} />
                  </button>
                  <button
                    type="button"
                    className={v.persist ? `${panel.rowButton} ${panel.rowButtonLocal}` : panel.rowButton}
                    aria-label={t("environments.runtime.persist", { name: v.name })}
                    aria-pressed={v.persist}
                    title={v.persist ? t("environments.runtime.persistOn") : t("environments.runtime.persistOff")}
                    disabled={busy}
                    onClick={() => toggle(v, "persist")}
                  >
                    <IconPin size={12} />
                  </button>
                  <button
                    type="button"
                    className={panel.rowButton}
                    aria-label={t("environments.runtime.delete", { name: v.name })}
                    title={t("environments.runtime.deleteTitle")}
                    disabled={busy}
                    onClick={() => remove(v)}
                  >
                    <IconTrash size={12} />
                  </button>
                </span>
              </li>
            );
          })}
        </ul>
      )}

      {state.status === "ready" && (
        <form className={styles.form} aria-labelledby={formTitleId} onSubmit={submit}>
          <p id={formTitleId} className={styles.formTitle}>
            {editing
              ? t("environments.runtime.form.editTitle", { name: draft.previousName })
              : t("environments.runtime.form.addTitle")}
          </p>
          <label className={styles.field}>
            {t("environments.runtime.form.name")}
            <input
              ref={nameInput}
              className={styles.input}
              value={draft.name}
              placeholder={t("environments.variables.placeholder.name")}
              spellCheck={false}
              autoComplete="off"
              autoCapitalize="off"
              autoCorrect="off"
              aria-invalid={error !== null || undefined}
              onChange={(e) => setDraft({ ...draft, name: e.target.value })}
            />
          </label>
          <label className={styles.field}>
            {t("environments.runtime.form.value")}
            <input
              className={styles.input}
              type={draft.secret ? "password" : "text"}
              value={draft.value}
              placeholder={t("common.keyValue.placeholder.value")}
              spellCheck={false}
              autoComplete="off"
              autoCapitalize="off"
              autoCorrect="off"
              onChange={(e) => setDraft({ ...draft, value: e.target.value })}
            />
          </label>
          <label className={styles.check}>
            <input
              type="checkbox"
              checked={draft.secret}
              onChange={(e) => setDraft({ ...draft, secret: e.target.checked })}
            />
            {t("environments.runtime.form.secret")}
          </label>
          <label className={styles.check}>
            <input
              type="checkbox"
              checked={draft.persist}
              onChange={(e) => setDraft({ ...draft, persist: e.target.checked })}
            />
            {t("environments.runtime.form.persist")}
          </label>
          <div className={styles.formButtons}>
            {editing && (
              <button
                type="button"
                className={panel.smallButton}
                disabled={busy}
                onClick={() => {
                  setDraft(EMPTY_DRAFT);
                  setError(null);
                }}
              >
                {t("common.cancel")}
              </button>
            )}
            <button type="submit" className={panel.smallButton} disabled={busy || draft.name.trim() === ""}>
              {editing ? t("environments.runtime.form.save") : t("environments.runtime.form.set")}
            </button>
          </div>
        </form>
      )}

      {error !== null && (
        <p className={panel.error} role="alert">
          {error}
        </p>
      )}

      <p className={panel.note}>{t("environments.runtime.note")}</p>
    </section>
  );
}
