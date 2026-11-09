import { useEffect, useState, type CSSProperties, type FormEvent, type KeyboardEvent } from "react";
import {
  createEnvironment,
  deleteEnvironment,
  loadEnvironments,
  loadGlobalVariables,
  renameEnvironment,
  saveEnvironmentVariables,
  saveGlobalVariables,
  setActiveEnvironment,
  type GlobalVariables,
} from "../../commands/environments";
import { confirm } from "../../commands/dialogs";
import { onExternalGlobalsChange } from "../../commands/externalChanges";
import { showToast } from "../../commands/toasts";
import {
  ENVIRONMENT_CODES,
  exportEnvironment,
  onEnvironmentsChanged,
  VARIABLE_PRECEDENCE,
  type EnvironmentExportFormat,
} from "../../lib/environments";
import { formatError, isCancelled, toAppError } from "../../lib/errors";
import type { MessageKey } from "../../i18n";
import { Trans, useT } from "../../i18n/react";
import {
  isEnvironmentColor,
  selectSelectedEnvironment,
  shallowEqual,
  useEnvironments,
  useStores,
  type Environment,
  type EnvironmentVariable,
} from "../../stores";
import { EmptyState } from "../common/EmptyState";
import { IconEnvironments, IconGlobe, IconPlay, IconPlus, IconTrash } from "../common/icons";
import { BaseUrlNote } from "./BaseUrlNote";
import { EnvironmentColorPicker } from "./EnvironmentColorPicker";
import styles from "./EnvironmentsPanel.module.css";
import { RuntimeVariables } from "./RuntimeVariables";
import { VariablesEditor } from "./VariablesEditor";

export interface EnvironmentsPanelProps {
  /** The sidebar heading. */
  label: string;
}

function dotStyle(env: Environment): CSSProperties | undefined {
  return isEnvironmentColor(env.color) ? ({ "--env-color": env.color } as CSSProperties) : undefined;
}

interface NameFormProps {
  label: string;
  initial: string;
  submitLabel: string;
  onSubmit: (name: string) => Promise<unknown>;
  onCancel: () => void;
}

/** An inline name field: Enter saves, Escape cancels. Shows the error of a failed save. */
function NameForm({ label, initial, submitLabel, onSubmit, onCancel }: NameFormProps) {
  const t = useT();
  const [name, setName] = useState(initial);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState<string | null>(null);

  const submit = (e: FormEvent) => {
    e.preventDefault();
    if (busy) return;
    if (name.trim() === "" || name.trim() === initial) {
      onCancel();
      return;
    }
    setBusy(true);
    onSubmit(name).then(
      () => onCancel(),
      (err: unknown) => {
        setBusy(false);
        setError(toAppError(err).message);
      },
    );
  };
  const onKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Escape") {
      e.preventDefault();
      onCancel();
    }
  };

  return (
    <form className={styles.nameForm} onSubmit={submit}>
      <div className={styles.nameRow}>
        <input
          className={styles.nameInput}
          aria-label={label}
          placeholder={t("environments.nameForm.placeholder")}
          value={name}
          maxLength={100}
          autoFocus
          spellCheck={false}
          autoComplete="off"
          disabled={busy}
          aria-invalid={error !== null || undefined}
          onChange={(e) => {
            setName(e.target.value);
            setError(null);
          }}
          onKeyDown={onKeyDown}
        />
        <button type="submit" className={styles.smallButton} disabled={busy}>
          {submitLabel}
        </button>
        <button type="button" className={styles.smallButton} onClick={onCancel} disabled={busy}>
          {t("common.cancel")}
        </button>
      </div>
      {error !== null && (
        <p className={styles.error} role="alert">
          {error}
        </p>
      )}
    </form>
  );
}

/**
 * The Environments sidebar view (ENV-01): the project's environments
 * with the active one marked, and the selected one's variables in the
 * key/value table. Environments are created, renamed and deleted here.
 */
export function EnvironmentsPanel({ label }: EnvironmentsPanelProps) {
  const t = useT();
  const stores = useStores();
  const status = useEnvironments((s) => s.status);
  const loadError = useEnvironments((s) => s.error);
  const envs = useEnvironments((s) => s.ids.map((id) => s.byId[id]), shallowEqual);
  const activeId = useEnvironments((s) => s.activeId);
  const selected = useEnvironments(selectSelectedEnvironment);
  const problems = useEnvironments((s) => s.problems);
  const projectReadOnly = useEnvironments((s) => s.readOnly);
  const workspaceKey = useEnvironments((s) => s.workspaceKey);
  const [creating, setCreating] = useState(false);
  // The global variables (ENV-02) or the runtime variables (ENV-15) are
  // shown instead of the selected environment.
  const [shown, setShown] = useState<"environment" | "globals" | "runtime">("environment");
  const globalsOpen = shown === "globals";
  const runtimeOpen = shown === "runtime";

  const canCreate = status === "ready" && !projectReadOnly;

  return (
    <>
      <div className={styles.header}>
        <h2 className={styles.title}>{label}</h2>
        <div className={styles.headerActions}>
          <button
            type="button"
            className={styles.headerButton}
            aria-label={t("environments.panel.new")}
            title={t("environments.panel.new")}
            disabled={!canCreate}
            onClick={() => setCreating(true)}
          >
            <IconPlus size={15} />
          </button>
        </div>
      </div>

      <div className={styles.content}>
        {status === "error" && (
          <div className={styles.problem} role="alert">
            <p>{loadError ?? t("environments.panel.loadFailed")}</p>
            <button type="button" className={styles.smallButton} onClick={() => void loadEnvironments(stores)}>
              {t("environments.panel.retry")}
            </button>
          </div>
        )}

        {creating && (
          <NameForm
            label={t("environments.panel.newName")}
            initial=""
            submitLabel={t("environments.panel.create")}
            onSubmit={(name) => createEnvironment(stores, name)}
            onCancel={() => setCreating(false)}
          />
        )}

        {workspaceKey !== null && (
          <button
            type="button"
            className={globalsOpen ? `${styles.envRow} ${styles.envRowSelected}` : styles.envRow}
            aria-current={globalsOpen || undefined}
            title={t("environments.globals.rowTitle")}
            onClick={() => setShown("globals")}
          >
            <IconGlobe size={13} />
            <span className={styles.envName}>{t("environments.globals.label")}</span>
          </button>
        )}
        {workspaceKey !== null && (
          <button
            type="button"
            className={runtimeOpen ? `${styles.envRow} ${styles.envRowSelected}` : styles.envRow}
            aria-current={runtimeOpen || undefined}
            title={t("environments.runtime.rowTitle")}
            onClick={() => setShown("runtime")}
          >
            <IconPlay size={13} />
            <span className={styles.envName}>{t("environments.runtime.label")}</span>
          </button>
        )}

        {envs.length === 0 && !creating && status !== "error" && (
          <EmptyState
            placement="top"
            icon={<IconEnvironments size={28} strokeWidth={1.5} />}
            title={t("environments.panel.empty.title")}
          >
            {t("environments.panel.empty.body")}
          </EmptyState>
        )}

        {envs.length > 0 && (
          <ul className={styles.list} aria-label={t("environments.panel.list")}>
            {envs.map((env) => {
              const isActive = env.id === activeId;
              const isSelected = shown === "environment" && env.id === selected?.id;
              return (
                <li key={env.id}>
                  <button
                    type="button"
                    className={isSelected ? `${styles.envRow} ${styles.envRowSelected}` : styles.envRow}
                    aria-current={isSelected || undefined}
                    title={t("environments.panel.row.title", {
                      name: env.name,
                      // i18n-ignore: a path in the project folder
                      file: `environments/${env.fileName}`,
                    })}
                    onClick={() => {
                      setShown("environment");
                      stores.environments.dispatch({ type: "environments/select", id: env.id });
                    }}
                    onDoubleClick={() =>
                      setActiveEnvironment(stores, env.id).catch((err: unknown) =>
                        console.warn(`environments: ${formatError(err)}`),
                      )
                    }
                  >
                    <span
                      className={isActive ? `${styles.dot} ${styles.dotActive}` : styles.dot}
                      style={dotStyle(env)}
                    />
                    <span className={styles.envName}>{env.name}</span>
                    {isActive && <span className={styles.activeTag}>{t("environments.panel.active")}</span>}
                  </button>
                </li>
              );
            })}
          </ul>
        )}

        {globalsOpen && workspaceKey !== null ? (
          <>
            <div className={styles.divider} />
            <GlobalDetails key={workspaceKey} />
          </>
        ) : runtimeOpen && workspaceKey !== null ? (
          <>
            <div className={styles.divider} />
            <RuntimeVariables key={workspaceKey} />
          </>
        ) : (
          selected !== null && (
            <>
              <div className={styles.divider} />
              <EnvironmentDetails
                key={selected.id}
                environment={selected}
                active={selected.id === activeId}
                readOnly={projectReadOnly || selected.readOnly}
              />
            </>
          )
        )}

        {workspaceKey !== null && <Precedence />}

        {problems.length > 0 && (
          <div className={styles.problem}>
            <p>{t("environments.panel.problems", { count: problems.length })}</p>
            <ul>
              {problems.map((p) => (
                <li key={p.fileName}>
                  <span className={styles.mono}>{p.fileName}</span>: {p.message}
                </li>
              ))}
            </ul>
          </div>
        )}
      </div>
    </>
  );
}

interface EnvironmentDetailsProps {
  environment: Environment;
  active: boolean;
  readOnly: boolean;
}

/**
 * The global variables, the base environment every environment inherits
 * (ENV-05): loaded when shown, again after every environments:changed
 * (a save of the global variables sends it) and after postsonde.json was
 * edited outside PostSonde. null until loaded, and when they cannot be.
 */
function useBaseVariables(): readonly EnvironmentVariable[] | null {
  const [variables, setVariables] = useState<readonly EnvironmentVariable[] | null>(null);
  const [attempt, setAttempt] = useState(0);
  useEffect(() => {
    let alive = true;
    loadGlobalVariables().then(
      (globals) => {
        if (alive) setVariables(globals.variables);
      },
      (err: unknown) => {
        if (alive) setVariables(null);
        console.warn(`environments: could not load the base variables: ${formatError(err)}`);
      },
    );
    return () => {
      alive = false;
    };
  }, [attempt]);
  useEffect(() => onExternalGlobalsChange(() => setAttempt((n) => n + 1)), []);
  useEffect(() => onEnvironmentsChanged(() => setAttempt((n) => n + 1)), []);
  return variables;
}

/** The selected environment: activate, rename, delete, export (EXP-05), its colour (ENV-08) and its variables. */
function EnvironmentDetails({ environment, active, readOnly }: EnvironmentDetailsProps) {
  const t = useT();
  const stores = useStores();
  const base = useBaseVariables();
  const [mode, setMode] = useState<"view" | "rename">("view");
  const [error, setError] = useState<string | null>(null);
  const [busy, setBusy] = useState(false);

  const activate = () => {
    setActiveEnvironment(stores, active ? null : environment.id).catch((err: unknown) =>
      setError(toAppError(err).message),
    );
  };

  // Asks in the shared danger dialog; the file is removed for good, so there is no undo.
  const remove = async () => {
    const { id, name, fileName } = environment;
    const confirmed = await confirm({
      title: t("environments.delete.title", { name }),
      message: (
        <Trans k="environments.delete.body" values={{ file: <span className={styles.mono}>{fileName}</span> }} />
      ),
      confirmLabel: t("environments.delete.confirm"),
      confirmIcon: <IconTrash size={14} />,
      variant: "danger",
    });
    if (!confirmed) return;
    setBusy(true);
    try {
      await deleteEnvironment(stores, id);
      showToast({ message: t("environments.delete.done", { name }), icon: <IconTrash size={14} /> });
    } catch (err: unknown) {
      const appErr = toAppError(err);
      setBusy(false);
      setError(appErr.message);
      // Deleted outside PostSonde: show what is there now.
      if (appErr.code === ENVIRONMENT_CODES.notFound) void loadEnvironments(stores);
    }
  };

  // EXP-05: Go writes the file the user picks, from the project's file only
  // (no secret or local value), and says how many secrets it left out.
  const exportAs = async (format: EnvironmentExportFormat) => {
    const { id, name } = environment;
    setError(null);
    try {
      const res = await exportEnvironment(id, format);
      if (res.saved) showToast({ message: t("environments.export.done", { name, path: res.path, count: res.secrets }) });
    } catch (err: unknown) {
      if (!isCancelled(err)) setError(toAppError(err).message);
    }
  };

  return (
    <section className={styles.details} aria-label={t("environments.details.label", { name: environment.name })}>
      {mode === "rename" ? (
        <NameForm
          label={t("environments.details.nameLabel")}
          initial={environment.name}
          submitLabel={t("environments.details.rename")}
          onSubmit={(name) => renameEnvironment(stores, environment.id, name)}
          onCancel={() => setMode("view")}
        />
      ) : (
        <div className={styles.detailsHeader}>
          <span className={styles.detailsTitle}>
            <Trans k="environments.details.title" values={{ name: <strong>{environment.name}</strong> }} />
          </span>
          {active ? (
            <button
              type="button"
              className={styles.activeButton}
              title={t("environments.details.activeTitle")}
              aria-label={t("environments.details.deactivate", { name: environment.name })}
              onClick={activate}
            >
              {t("environments.panel.active")}
            </button>
          ) : (
            <button type="button" className={styles.smallButton} onClick={activate}>
              {t("environments.details.activate")}
            </button>
          )}
        </div>
      )}

      {mode === "view" && (
        <div className={styles.buttons}>
          {!readOnly && (
            <>
              <button type="button" className={styles.linkButton} onClick={() => setMode("rename")}>
                {t("environments.details.rename")}
              </button>
              <button type="button" className={styles.linkButton} onClick={() => void remove()} disabled={busy}>
                {t("environments.details.delete")}
              </button>
            </>
          )}
          <button
            type="button"
            className={styles.linkButton}
            title={t("environments.export.postsondeTitle")}
            onClick={() => void exportAs("postsonde")}
          >
            {t("environments.export.postsonde")}
          </button>
          <button
            type="button"
            className={styles.linkButton}
            title={t("environments.export.postmanTitle")}
            onClick={() => void exportAs("postman")}
          >
            {t("environments.export.postman")}
          </button>
        </div>
      )}

      {mode === "view" && <EnvironmentColorPicker environment={environment} readOnly={readOnly} />}

      {error !== null && (
        <p className={styles.error} role="alert">
          {error}
        </p>
      )}

      {readOnly && (
        <p className={styles.note}>{t("environments.details.readOnly")}</p>
      )}

      <VariablesEditor
        label={t("environments.details.variablesLabel", { name: environment.name })}
        variables={environment.variables}
        onSave={(variables) => saveEnvironmentVariables(stores, environment.id, variables)}
        readOnly={readOnly}
        base={base}
      />
      <BaseUrlNote variables={environment.variables} base={base} />
      <p className={styles.note}>{t("environments.details.secretNote")}</p>
    </section>
  );
}

type Scope = (typeof VARIABLE_PRECEDENCE)[number];

/** Each scope's name and its explanation (the title). */
const PRECEDENCE_TEXT: Record<Scope, { labelKey: MessageKey; titleKey: MessageKey }> = {
  Global: { labelKey: "environments.precedence.scope.global", titleKey: "environments.precedence.title.global" },
  Environment: {
    labelKey: "environments.precedence.scope.environment",
    titleKey: "environments.precedence.title.environment",
  },
  Collection: {
    labelKey: "environments.precedence.scope.collection",
    titleKey: "environments.precedence.title.collection",
  },
  Folder: { labelKey: "environments.precedence.scope.folder", titleKey: "environments.precedence.title.folder" },
  Runtime: { labelKey: "environments.precedence.scope.runtime", titleKey: "environments.precedence.title.runtime" },
};

/** The fixed order in which a {{name}} defined in several scopes is looked up (ENV-06). */
function Precedence() {
  const t = useT();
  return (
    <div className={styles.precedence}>
      <span className={styles.precedenceLabel}>{t("environments.precedence.label")}</span>
      <ol className={styles.precedenceList} aria-label={t("environments.precedence.listLabel")}>
        {VARIABLE_PRECEDENCE.map((scope) => (
          <li key={scope} title={t(PRECEDENCE_TEXT[scope].titleKey)}>
            {t(PRECEDENCE_TEXT[scope].labelKey)}
          </li>
        ))}
      </ol>
      <span className={styles.note}>{t("environments.precedence.note")}</span>
    </div>
  );
}

type GlobalsState =
  | { readonly status: "loading" }
  | { readonly status: "error"; readonly message: string }
  | { readonly status: "ready"; readonly globals: GlobalVariables };

/** The project's global variables (ENV-02): loaded when shown, saved as they are edited. */
function GlobalDetails() {
  const t = useT();
  const [state, setState] = useState<GlobalsState>({ status: "loading" });
  const [attempt, setAttempt] = useState(0);

  useEffect(() => {
    let alive = true;
    loadGlobalVariables().then(
      (globals) => {
        if (alive) setState({ status: "ready", globals });
      },
      (err: unknown) => {
        if (alive) setState({ status: "error", message: toAppError(err).message });
      },
    );
    return () => {
      alive = false;
    };
  }, [attempt]);
  // postsonde.json edited outside PostSonde (STO-05): read the variables again.
  useEffect(() => onExternalGlobalsChange(() => setAttempt((n) => n + 1)), []);

  return (
    <section className={styles.details} aria-label={t("environments.globals.label")}>
      <div className={styles.detailsHeader}>
        <span className={styles.detailsTitle}>
          <Trans
            k="environments.details.title"
            values={{ name: <strong>{t("environments.globals.titleName")}</strong> }}
          />
        </span>
      </div>
      {state.status === "loading" && <p className={styles.note}>{t("environments.globals.loading")}</p>}
      {state.status === "error" && (
        <div className={styles.problem} role="alert">
          <p>{state.message}</p>
          <button
            type="button"
            className={styles.smallButton}
            onClick={() => {
              setState({ status: "loading" });
              setAttempt((n) => n + 1);
            }}
          >
            {t("environments.panel.retry")}
          </button>
        </div>
      )}
      {state.status === "ready" && (
        <>
          {state.globals.readOnly && (
            <p className={styles.note}>{t("environments.globals.readOnly")}</p>
          )}
          <VariablesEditor
            key={attempt}
            label={t("environments.globals.label")}
            variables={state.globals.variables}
            onSave={(variables) =>
              saveGlobalVariables(variables).then((globals) => {
                setState({ status: "ready", globals });
                return globals;
              })
            }
            readOnly={state.globals.readOnly}
          />
          <BaseUrlNote variables={state.globals.variables} />
          <p className={styles.note}>{t("environments.globals.note")}</p>
        </>
      )}
    </section>
  );
}
