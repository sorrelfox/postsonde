import { useId, useMemo, useState, useSyncExternalStore } from "react";
import { applySetting } from "../../commands/settings";
import {
  findTheme,
  getThemesState,
  openThemesFolder,
  reloadThemes,
  subscribeThemes,
  type CustomTheme,
} from "../../commands/themes";
import { toAppError } from "../../lib/errors";
import { useSettings, useStores } from "../../stores";
import { customThemeContrast } from "../../styles/themeContrast";
import { IconWarning } from "../common/icons";
import styles from "./CustomThemes.module.css";
import { LockBadge, useSettingLocked } from "./Locked";
import { t as translate } from "../../i18n";
import { useT } from "../../i18n/react";
import screen from "./SettingsScreen.module.css";

/**
 * The custom theme row of the settings screen (UI-05, docs/themes.md): pick
 * a theme file from the data directory's themes folder or none, open that
 * folder, read it again, and see what is wrong with the files. Choosing one
 * saves ui.customTheme at once; the theme is applied by useCustomThemeSync.
 * Self-contained, so it only touches settings through applySetting.
 */
export function CustomThemeRows() {
  const t = useT();
  const stores = useStores();
  const id = useSettings((s) => s.customTheme);
  const themes = useSyncExternalStore(subscribeThemes, getThemesState);
  const [error, setError] = useState<string | null>(null);
  const selectId = useId();
  const errorId = useId();

  const selected = id === "" ? undefined : findTheme(themes.themes, id);
  const loaded = themes.status === "ready" || themes.status === "error";
  // Why the chosen theme is not applied, when it is not.
  const selectedProblem =
    id === "" || !loaded
      ? null
      : selected === undefined
        ? t("settings.customTheme.notFound", { file: id })
        : !selected.usable
          ? t("settings.customTheme.hasErrors", { file: id })
          : null;

  const choose = (next: string) => {
    if (next === id) return;
    setError(null);
    applySetting(stores, { customTheme: next }).catch((err: unknown) => setError(toAppError(err).message));
  };

  const openFolder = () => {
    setError(null);
    openThemesFolder().catch((err: unknown) => {
      const appErr = toAppError(err);
      setError(appErr.hint ? `${appErr.message} ${appErr.hint}` : appErr.message);
    });
  };

  // Low contrast is the user's choice: a warning, never a refusal (UI-11).
  const contrast = useMemo(() => (selected?.usable === true ? customThemeContrast(selected) : []), [selected]);

  const shownError = error ?? (themes.status === "error" ? (themes.error?.message ?? t("settings.customTheme.readFailed")) : null);
  const withProblems = themes.themes.filter((t) => t.problems !== null && t.problems.length > 0);

  // Locked by the admin policy (SET-02): the choice cannot change here.
  const locked = useSettingLocked("ui.customTheme");
  return (
    <div className={screen.row} data-invalid={shownError || selectedProblem ? "true" : undefined}>
      <div className={screen.rowText}>
        <label className={screen.rowLabel} htmlFor={selectId}>
          {t("settings.customTheme.label")}
          {locked && <LockBadge />}
        </label>
        <span className={screen.rowDescription}>
          {t("settings.customTheme.description")}{" "}
          {id === ""
            ? t("settings.customTheme.noneUses")
            : selected?.usable
              ? t("settings.customTheme.basedOn", { base: selected.base })
              : ""}
        </span>
        {selectedProblem && (
          <span className={screen.rowWarning} role="status">
            <IconWarning size={13} />
            {selectedProblem}
          </span>
        )}
        {contrast.length > 0 && (
          <span className={screen.rowWarning} role="status">
            <IconWarning size={13} />
            {contrastWarning(contrast)}
          </span>
        )}
        {shownError && (
          <span id={errorId} className={screen.rowError} role="alert">
            <IconWarning size={13} />
            {shownError}
          </span>
        )}
        {withProblems.length > 0 && <ThemeProblems themes={withProblems} />}
      </div>
      <div className={`${screen.rowControl} ${styles.controls}`}>
        <select
          id={selectId}
          className={screen.select}
          value={id}
          disabled={locked}
          title={locked ? t("settings.locked.tooltip") : undefined}
          aria-describedby={shownError ? errorId : undefined}
          onChange={(e) => choose(e.currentTarget.value)}
        >
          <option value="">{t("settings.none")}</option>
          {themes.themes.map((theme) => (
            <option key={theme.id} value={theme.id} disabled={!theme.usable && theme.id !== id}>
              {theme.usable ? theme.name : t("settings.customTheme.optionErrors", { name: theme.name })}
            </option>
          ))}
          {id !== "" && selected === undefined && (
            <option value={id}>{t("settings.customTheme.optionNotFound", { file: id })}</option>
          )}
        </select>
        <div className={styles.buttons}>
          <button type="button" className={screen.smallButton} onClick={openFolder}>
            {t("settings.customTheme.openFolder")}
          </button>
          <button
            type="button"
            className={screen.smallButton}
            onClick={() => void reloadThemes()}
            disabled={themes.status === "loading"}
          >
            {t("settings.customTheme.reload")}
          </button>
        </div>
      </div>
    </div>
  );
}

/** The custom theme's pairs below WCAG AA, the worst three named. */
function contrastWarning(failures: ReturnType<typeof customThemeContrast>): string {
  const named = failures.slice(0, 3).map((f) => {
    const [top, bottom] = f.on.split("/");
    const on = bottom === undefined ? f.on : translate("settings.customTheme.contrastOver", { top: top ?? "", bottom });
    return translate("settings.customTheme.contrastPair", { fg: f.fg, on, ratio: f.ratio.toFixed(1), min: f.min });
  });
  const pairs = named.join(", ");
  const listed = failures.length > 3 ? translate("settings.customTheme.contrastMore", { pairs, count: failures.length - 3 }) : pairs;
  return translate("settings.customTheme.contrast", { pairs: listed });
}

/** The errors and warnings of the theme files, per file. */
function ThemeProblems({ themes }: { themes: readonly CustomTheme[] }) {
  const t = useT();
  return (
    <ul className={styles.problems} aria-label={t("settings.customTheme.problems")}>
      {themes.map((theme) => (
        <li key={theme.id}>
          <span className={styles.file}>{theme.id}</span>
          <ul className={styles.problemList}>
            {(theme.problems ?? []).map((p, i) => (
              <li key={i} data-severity={p.severity}>
                {p.severity === "error" ? t("settings.customTheme.error") : t("settings.customTheme.warning")}
                {p.message}
              </li>
            ))}
          </ul>
        </li>
      ))}
    </ul>
  );
}
