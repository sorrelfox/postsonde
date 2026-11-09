import { useMemo, useState } from "react";
import {
  formatChord,
  IS_MAC,
  scopeLabel,
  SHORTCUT_GROUPS,
  shortcutGroupLabel,
  SHORTCUTS,
  shortcutChords,
  type ShortcutDefinition,
} from "../../lib/shortcuts";
import { Dialog } from "../common/Dialog";
import { IconSearch } from "../common/icons";
import { useT } from "../../i18n/react";
import styles from "./ShortcutHelp.module.css";
import { useShortcut } from "./useShortcut";
import { useShortcutOverrides } from "./useShortcutOverridesSync";

export interface ShortcutHelpProps {
  /** The shortcuts to list. Default: the registry. */
  shortcuts?: readonly ShortcutDefinition[];
  /** Show macOS chords (⌘) instead of Ctrl. Default: the running platform. */
  mac?: boolean;
}

/**
 * The keyboard shortcut help (UI-06): a modal overlay listing every
 * shortcut of the registry by group, opened and closed with ⌘/ (Ctrl+/) or
 * "?" outside text fields. Placeholders are listed as "Coming soon". The
 * frame is the shared Dialog (no footer: Esc, the close button or the scrim close it).
 */
export function ShortcutHelp({ shortcuts = SHORTCUTS, mac = IS_MAC }: ShortcutHelpProps) {
  const t = useT();
  const [open, setOpen] = useState(false);
  useShortcut("help.toggle", () => {
    setOpen((was) => !was);
  });
  return (
    <Dialog open={open} title={t("shell.shortcutHelp.title")} width={560} bodyClassName={styles.content} onCancel={() => setOpen(false)}>
      <ShortcutList shortcuts={shortcuts} mac={mac} />
    </Dialog>
  );
}

function ShortcutList({ shortcuts, mac }: Required<ShortcutHelpProps>) {
  const t = useT();
  const [filter, setFilter] = useState("");
  const overrides = useShortcutOverrides();
  const query = filter.trim().toLowerCase();
  const rows = useMemo(
    () =>
      shortcuts.map((def) => {
        const keys = shortcutChords(def, mac).map((c) => formatChord(c, mac));
        const label = t(def.labelKey);
        const scope = scopeLabel(def.scope);
        const text = [label, scope, ...keys].join(" ").toLowerCase();
        return { def, label, scope, keys, text };
      }),
    [shortcuts, mac, overrides, t],
  );
  const visible = rows.filter((r) => query === "" || r.text.includes(query));

  return (
    <>
      <label className={styles.filter}>
        <IconSearch size={13} />
        <input
          className={styles.filterInput}
          type="search"
          placeholder={t("shell.shortcutHelp.filter")}
          aria-label={t("shell.shortcutHelp.filter")}
          value={filter}
          onChange={(e) => setFilter(e.target.value)}
          data-autofocus
        />
      </label>
      <div className={styles.body}>
        {SHORTCUT_GROUPS.map((group) => {
          const groupRows = visible.filter((r) => r.def.group === group);
          if (groupRows.length === 0) return null;
          const heading = shortcutGroupLabel(group);
          return (
            <section key={group} className={styles.group} aria-label={heading}>
              <h3 className={styles.groupTitle}>{heading}</h3>
              <ul className={styles.list}>
                {groupRows.map(({ def, label, scope, keys }) => (
                  <li key={def.id} className={styles.row} data-shortcut-id={def.id}>
                    <span className={styles.label}>
                      <span className={def.placeholder ? styles.placeholderLabel : undefined}>{label}</span>
                      {scope !== "" && <span className={styles.scope}>{scope}</span>}
                      {def.placeholder && (
                        <span className={styles.soon} title={def.placeholder.issue ? t("shell.shortcutHelp.plannedIn", { issue: def.placeholder.issue }) : undefined}>
                          {t("shell.shortcutHelp.comingSoon")}
                        </span>
                      )}
                    </span>
                    <span className={styles.keys}>
                      {keys.map((k, i) => (
                        <span key={k} className={styles.chord}>
                          {i > 0 && <span className={styles.or}>{t("shell.shortcutHelp.or")}</span>}
                          <kbd className={styles.kbd}>{k}</kbd>
                        </span>
                      ))}
                    </span>
                  </li>
                ))}
              </ul>
            </section>
          );
        })}
        {visible.length === 0 && <p className={styles.empty}>{t("shell.shortcutHelp.empty", { query: filter.trim() })}</p>}
      </div>
    </>
  );
}
