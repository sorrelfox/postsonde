import { useRef, type ChangeEvent, type CSSProperties } from "react";
import { setActiveEnvironment } from "../../commands/environments";
import { formatError } from "../../lib/errors";
import { isEnvironmentColor, shallowEqual, useEnvironments, useStores } from "../../stores";
import { ariaShortcut, shortcutLabel } from "../../lib/shortcuts";
import { useT } from "../../i18n/react";
import { IconChevron } from "../common/icons";
import { useShortcut } from "../shell/useShortcut";
import styles from "./EnvironmentSwitcher.module.css";

/**
 * The title-bar environment switcher (ENV-01): the active environment's
 * colour dot and a native select of the project's environments. Choosing one
 * makes it active; the choice is saved in the project's local data.
 *
 * ⌘E (Ctrl+E) focuses the select and opens its list where the web view
 * supports showPicker; with no environments the key press is left alone.
 */
export function EnvironmentSwitcher() {
  const t = useT();
  const stores = useStores();
  const options = useEnvironments((s) => s.ids.map((id) => s.byId[id]), shallowEqual);
  const activeId = useEnvironments((s) => s.activeId);
  const active = options.find((e) => e.id === activeId) ?? null;
  const activeName = active?.name ?? t("environments.switcher.none");
  const selectRef = useRef<HTMLSelectElement>(null);

  useShortcut("environment.switch", () => {
    const select = selectRef.current;
    if (select === null || select.disabled) return false;
    select.focus();
    try {
      (select as HTMLSelectElement & { showPicker?: () => void }).showPicker?.();
    } catch {
      // Not supported for selects, or no user activation: focus is enough.
    }
  });

  const onChange = (e: ChangeEvent<HTMLSelectElement>) => {
    const id = e.target.value === "" ? null : e.target.value;
    setActiveEnvironment(stores, id).catch((err: unknown) =>
      console.warn(`environments: could not switch the environment: ${formatError(err)}`),
    );
  };

  // The active environment's colour (ENV-08) marks the whole pill: a bar
  // on its left, a light tint and the dot. The name stays in the text
  // colour, never in the accent, so its contrast does not depend on the
  // colour chosen.
  const color = active !== null && isEnvironmentColor(active.color) ? active.color : null;
  const colorStyle = color === null ? undefined : ({ "--env-color": color } as CSSProperties);

  return (
    <label
      className={styles.switcher}
      data-env-color={color === null ? undefined : ""}
      style={colorStyle}
      title={t("environments.switcher.title", { name: activeName, shortcut: shortcutLabel("environment.switch") })}
    >
      <span className={active === null ? styles.dot : `${styles.dot} ${styles.dotActive}`} aria-hidden="true" />
      <select
        ref={selectRef}
        className={styles.select}
        aria-keyshortcuts={ariaShortcut("environment.switch")}
        aria-label={t("environments.switcher.label")}
        value={activeId ?? ""}
        disabled={options.length === 0}
        onChange={onChange}
      >
        <option value="">{t("environments.switcher.none")}</option>
        {options.map((e) => (
          <option key={e.id} value={e.id}>
            {e.name}
          </option>
        ))}
      </select>
      <IconChevron direction="down" size={12} className={styles.chevron} />
    </label>
  );
}
