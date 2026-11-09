import { useEffect, useId, useMemo, useRef, useState, type KeyboardEvent } from "react";
import { applySetting, isSettingLocked } from "../../commands/settings";
import { RECORD_SCOPE, SCOPE_ATTRIBUTE } from "../../commands/shortcuts";
import { formatError } from "../../lib/errors";
import {
  checkShortcutBinding,
  chordFromEvent,
  formatChord,
  IS_MAC,
  isValidChord,
  parseChord,
  scopeLabel,
  SHORTCUT_GROUPS,
  shortcutGroupLabel,
  SHORTCUTS,
  type BindingProblem,
  type ShortcutDefinition,
} from "../../lib/shortcuts";
import { useSettings, useStores, type ShortcutOverrides } from "../../stores";
import { IconSearch, IconWarning } from "../common/icons";
import { LockBadge, RowControl, useSettingsPolicy } from "./Locked";
import screen from "./SettingsScreen.module.css";
import styles from "./ShortcutSettings.module.css";
import { useT } from "../../i18n/react";

export interface ShortcutSettingsProps {
  /** Show macOS chords (⌘) instead of Ctrl. Default: the running platform. */
  mac?: boolean;
}

/** The chords as shown on the platform, without ones that are the same there. */
function chordLabels(keys: readonly string[], mac: boolean): string[] {
  const out: string[] = [];
  for (const k of keys) {
    if (!isValidChord(k)) continue;
    const label = formatChord(parseChord(k), mac);
    if (!out.includes(label)) out.push(label);
  }
  return out;
}

function Keys({ keys, mac }: { keys: readonly string[]; mac: boolean }) {
  return (
    <span className={styles.keys}>
      {chordLabels(keys, mac).map((k) => (
        <kbd key={k} className={styles.kbd}>
          {k}
        </kbd>
      ))}
    </span>
  );
}

/** Where a row's message comes from: a refused recording or save, or the warnings of a saved chord. */
interface RowMessage {
  readonly id: string;
  readonly problems: readonly BindingProblem[];
}

/**
 * The settings screen's Shortcuts page (UI-07): every shortcut of the
 * registry with its default and current chords. **Change** records a new
 * chord (press it; Escape cancels), checked against the other shortcuts of
 * the same scope and the keys the OS and text fields keep; a clash is
 * refused, a chord another scope takes where this one applies is saved with
 * a warning. **Reset** goes back to the default, **Reset all** for every
 * shortcut. Saved at once in settings.json ("shortcuts"), not in the
 * project, and applied live.
 */
export function ShortcutSettings({ mac = IS_MAC }: ShortcutSettingsProps) {
  const t = useT();
  const stores = useStores();
  const overrides = useSettings((s) => s.shortcuts);
  const [filter, setFilter] = useState("");
  const [recording, setRecording] = useState<string | null>(null);
  const [message, setMessage] = useState<RowMessage | null>(null);
  const [saveError, setSaveError] = useState<string | null>(null);
  const customized = Object.keys(overrides).filter((id) => SHORTCUTS.some((d) => d.id === id && d.fixed !== true));
  // The admin policy (SET-02) can lock every shortcut ("shortcuts") or one
  // ("shortcuts.request.send"); a locked one keeps its chords in every save.
  const policy = useSettingsPolicy();
  const allLocked = isSettingLocked("shortcuts", policy);
  const isLocked = (id: string) => isSettingLocked(`shortcuts.${id}`, policy);
  const keepLocked = (next: ShortcutOverrides): ShortcutOverrides => {
    const out: Record<string, readonly string[]> = { ...next };
    for (const id of Object.keys(out)) if (isLocked(id)) delete out[id];
    for (const [id, chords] of Object.entries(overrides)) if (isLocked(id)) out[id] = chords;
    return out as ShortcutOverrides;
  };

  const save = async (next: ShortcutOverrides): Promise<boolean> => {
    setSaveError(null);
    try {
      await applySetting(stores, { shortcuts: keepLocked(next) });
      return true;
    } catch (err: unknown) {
      setSaveError(formatError(err));
      return false;
    }
  };

  const without = (id: string): ShortcutOverrides => Object.fromEntries(Object.entries(overrides).filter(([k]) => k !== id));

  const record = async (def: ShortcutDefinition, chord: string): Promise<void> => {
    const problems = checkShortcutBinding(def.id, [chord], mac);
    if (problems.some((p) => p.level === "error")) {
      setMessage({ id: def.id, problems });
      return;
    }
    setRecording(null);
    // The default chords again: no override.
    const next = def.keys.length === 1 && def.keys[0] === chord ? without(def.id) : { ...overrides, [def.id]: [chord] };
    if (await save(next)) setMessage(problems.length > 0 ? { id: def.id, problems } : null);
  };

  const reset = (id: string) => {
    setMessage(null);
    void save(without(id));
  };

  const query = filter.trim().toLowerCase();
  const rows = useMemo(
    () =>
      SHORTCUTS.map((def) => {
        const current = overrides[def.id] ?? def.keys;
        const text = [
          t(def.labelKey),
          shortcutGroupLabel(def.group),
          scopeLabel(def.scope),
          ...chordLabels(current, mac),
          ...chordLabels(def.keys, mac),
        ]
          .join(" ")
          .toLowerCase();
        return { def, current, text };
      }),
    [overrides, mac, t],
  );
  const visible = rows.filter((r) => query === "" || r.text.includes(query));

  return (
    <div className={styles.page}>
      <div className={styles.toolbar}>
        <label className={styles.filter}>
          <IconSearch size={13} />
          <input
            className={styles.filterInput}
            type="search"
            placeholder={t("settings.shortcuts.filter")}
            aria-label={t("settings.shortcuts.filter")}
            value={filter}
            onChange={(e) => setFilter(e.target.value)}
          />
        </label>
        <span className={styles.count}>
          {customized.length === 0 ? t("settings.shortcuts.allDefaults") : t("settings.shortcuts.changed", { count: customized.length })}
        </span>
        <button
          type="button"
          className={screen.smallButton}
          disabled={allLocked || customized.every(isLocked)}
          onClick={() => {
            setMessage(null);
            void save({});
          }}
        >
          {t("settings.shortcuts.resetAll")}
        </button>
      </div>
      <p className={styles.hint}>{t("settings.shortcuts.hint")}</p>
      {saveError && (
        <p className={screen.rowError} role="alert">
          <IconWarning size={13} />
          {saveError}
        </p>
      )}
      {SHORTCUT_GROUPS.map((group) => {
        const groupRows = visible.filter((r) => r.def.group === group);
        if (groupRows.length === 0) return null;
        const heading = shortcutGroupLabel(group);
        return (
          <section key={group} aria-label={heading}>
            <h3 className={screen.groupTitle}>{heading}</h3>
            <div className={screen.group} role="list">
              {groupRows.map(({ def, current }) => (
                <ShortcutRow
                  key={def.id}
                  def={def}
                  current={current}
                  customized={overrides[def.id] !== undefined}
                  locked={allLocked || isLocked(def.id)}
                  mac={mac}
                  recording={recording === def.id}
                  message={message?.id === def.id ? message.problems : []}
                  onStart={() => {
                    setMessage(null);
                    setRecording(def.id);
                  }}
                  onCancel={() => {
                    setRecording(null);
                    if (message?.problems.some((p) => p.level === "error")) setMessage(null);
                  }}
                  onRecord={(chord) => void record(def, chord)}
                  onReset={() => reset(def.id)}
                />
              ))}
            </div>
          </section>
        );
      })}
      {visible.length === 0 && <p className={styles.empty}>{t("settings.shortcuts.empty", { query: filter.trim() })}</p>}
    </div>
  );
}

interface ShortcutRowProps {
  def: ShortcutDefinition;
  current: readonly string[];
  customized: boolean;
  /** Locked by the admin policy (SET-02): a lock, and no Change or Reset. */
  locked: boolean;
  mac: boolean;
  recording: boolean;
  message: readonly BindingProblem[];
  onStart: () => void;
  onCancel: () => void;
  onRecord: (chord: string) => void;
  onReset: () => void;
}

function ShortcutRow({
  def,
  current,
  customized,
  locked,
  mac,
  recording,
  message,
  onStart,
  onCancel,
  onRecord,
  onReset,
}: ShortcutRowProps) {
  const t = useT();
  const recorderRef = useRef<HTMLButtonElement>(null);
  const changeRef = useRef<HTMLButtonElement>(null);
  const wasRecording = useRef(false);
  const labelId = useId();
  const messageId = useId();

  useEffect(() => {
    if (recording) recorderRef.current?.focus();
    else if (wasRecording.current && document.activeElement === document.body) changeRef.current?.focus();
    wasRecording.current = recording;
  }, [recording]);

  const onKeyDown = (e: KeyboardEvent<HTMLButtonElement>) => {
    const plain = !e.altKey && !e.ctrlKey && !e.metaKey && !e.shiftKey;
    if (plain && e.key === "Tab") {
      onCancel();
      return;
    }
    e.preventDefault();
    e.stopPropagation();
    if (plain && e.key === "Escape") {
      onCancel();
      return;
    }
    if (e.repeat || e.nativeEvent.isComposing) return;
    const chord = chordFromEvent(e, mac);
    if (chord !== null) onRecord(chord);
  };

  const errors = message.some((p) => p.level === "error");
  const scope = scopeLabel(def.scope);
  const label = t(def.labelKey);

  return (
    <div className={screen.row} role="listitem" data-shortcut-id={def.id} data-invalid={errors ? "true" : undefined}>
      <div className={screen.rowText}>
        <span id={labelId} className={screen.rowLabel}>
          {label}
          {locked && <LockBadge />}
        </span>
        <span className={screen.rowDescription}>
          {[scope, def.fixed ? t("settings.shortcuts.editorOwn") : "", customized ? t("settings.shortcuts.default") : ""]
            .filter((part) => part !== "")
            .join(" · ")}
          {customized && (
            <>
              {" "}
              <Keys keys={def.keys} mac={mac} />
            </>
          )}
        </span>
        {message.length > 0 && (
          <span id={messageId} className={styles.messages}>
            {message.map((p) => (
              <span
                key={p.message}
                className={p.level === "error" ? screen.rowError : screen.rowWarning}
                role={p.level === "error" ? "alert" : "note"}
              >
                <IconWarning size={13} />
                {p.message}
              </span>
            ))}
          </span>
        )}
      </div>
      <RowControl locked={locked}>
        {recording ? (
          <button
            ref={recorderRef}
            type="button"
            className={styles.recorder}
            aria-labelledby={labelId}
            aria-describedby={message.length > 0 ? messageId : undefined}
            aria-live="polite"
            {...{ [SCOPE_ATTRIBUTE]: RECORD_SCOPE }}
            onKeyDown={onKeyDown}
            onBlur={onCancel}
          >
            {t("settings.shortcuts.recording")}
          </button>
        ) : (
          <Keys keys={current} mac={mac} />
        )}
        {!def.fixed && !recording && (
          <button
            ref={changeRef}
            type="button"
            className={screen.smallButton}
            aria-label={t("settings.shortcuts.changeLabel", { shortcut: label })}
            onClick={onStart}
          >
            {t("settings.shortcuts.change")}
          </button>
        )}
        {customized && !recording && (
          <button type="button" className={screen.smallButton} aria-label={t("settings.shortcuts.resetLabel", { shortcut: label })} onClick={onReset}>
            {t("settings.shortcuts.reset")}
          </button>
        )}
      </RowControl>
    </div>
  );
}
