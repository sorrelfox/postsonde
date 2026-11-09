import type { ComponentType, CSSProperties } from "react";
import { useSettings, useStores, useWorkspace, type Theme } from "../../stores";
import { useActiveEnvironmentColor } from "../environments/useActiveEnvironmentColor";
import { WorkspaceMenu } from "../workspace/WorkspaceMenu";
import {
  IconClose,
  IconFolder,
  IconMonitor,
  IconMoon,
  IconSignal,
  IconSun,
  type IconProps,
} from "../common/icons";
import { EnvironmentSwitcher } from "../environments/EnvironmentSwitcher";
import { useSettingLocked } from "../settings/Locked";
import { useAppliedTheme } from "../../styles/useAppliedTheme";
import type { MessageKey } from "../../i18n";
import { useT } from "../../i18n/react";
import { QuickSwitcher } from "./QuickSwitcher";
import styles from "./TitleBar.module.css";

export const PLACEHOLDER_PROJECT_PATH = "~/Projects/shop-api";

const THEME_ORDER: readonly Theme[] = ["system", "light", "dark"];
const THEME_LABEL: Readonly<Record<Theme, MessageKey>> = {
  system: "common.theme.system",
  light: "common.theme.light",
  dark: "common.theme.dark",
};
const THEME_ICON: Readonly<Record<Theme, ComponentType<IconProps>>> = {
  system: IconMonitor,
  light: IconSun,
  dark: IconMoon,
};

export interface TitleBarProps {
  /** The open project's name, shown after the brand. Default: `projectPath`. */
  projectName?: string;
  /** The project folder, shown as the tooltip (and as the label without a name). */
  projectPath?: string;
  /** Shows a close button next to the project that calls this. */
  onCloseProject?: () => void;
  /** Opens the settings screen (the project menu's "Manage recent projects…"). */
  onOpenSettings?: () => void;
}

/**
 * The app's top toolbar row (not the native window title bar): brand and
 * project, the jump box (the quick switcher, NAV-04), the environment
 * switcher and the theme toggle (UI-04), which cycles System, Light,
 * Dark and is saved like the settings screen's switch.
 */
export function TitleBar({
  projectName,
  projectPath = PLACEHOLDER_PROJECT_PATH,
  onCloseProject,
  onOpenSettings,
}: TitleBarProps) {
  const t = useT();
  const { settings } = useStores();
  // With a project open, its name is the project switcher (STO-06).
  const hasWorkspace = useWorkspace((s) => s.current !== null);
  const theme = useSettings((s) => s.theme);
  const customTheme = useSettings((s) => s.customTheme);
  const next = THEME_ORDER[(THEME_ORDER.indexOf(theme) + 1) % THEME_ORDER.length];
  const applied = useAppliedTheme();
  const ThemeIcon = THEME_ICON[theme];
  const current =
    theme === "system" ? t("shell.titleBar.theme.systemApplied", { applied: t(THEME_LABEL[applied]) }) : t(THEME_LABEL[theme]);
  const shown = customTheme !== "" ? t("shell.titleBar.theme.custom", { file: customTheme }) : current;
  // A theme locked by the admin policy (SET-02) cannot be switched here.
  const themeLockedBuiltIn = useSettingLocked("ui.theme");
  const themeLockedCustom = useSettingLocked("ui.customTheme");
  const themeLocked = themeLockedBuiltIn || themeLockedCustom;
  // With a custom theme (UI-05) the button turns it off and goes on with
  // the next built-in theme.
  const themeLabel = themeLocked
    ? t("shell.titleBar.theme.locked", { theme: shown })
    : customTheme !== ""
      ? t("shell.titleBar.theme.leaveCustom", { file: customTheme, next: t(THEME_LABEL[next]) })
      : t("shell.titleBar.theme.switch", { theme: current, next: t(THEME_LABEL[next]) });
  const onThemeClick = () => {
    if (customTheme !== "") settings.dispatch({ type: "settings/setCustomTheme", id: "" });
    settings.dispatch({ type: "settings/setTheme", theme: next });
  };

  // The active environment's colour (ENV-08) as a thin stripe along the top.
  const envColor = useActiveEnvironmentColor();
  const envStyle = envColor === null ? undefined : ({ "--env-color": envColor } as CSSProperties);

  return (
    <header className={styles.titleBar} data-env-color={envColor === null ? undefined : ""} style={envStyle}>
      <div className={styles.brand}>
        <IconSignal size={18} className={styles.logo} />
        <span className={styles.name}>PostSonde</span>
        <span className={styles.separator} aria-hidden="true">
          /
        </span>
        {hasWorkspace ? (
          <WorkspaceMenu onManageRecent={onOpenSettings} />
        ) : (
          <span className={styles.project} title={projectPath}>
            <IconFolder size={13} />
            <span className={styles.projectPath}>{projectName ?? projectPath}</span>
          </span>
        )}
        {onCloseProject !== undefined && (
          <button
            type="button"
            className={styles.closeProject}
            aria-label={t("shell.titleBar.closeProject")}
            title={t("shell.titleBar.closeProject")}
            onClick={onCloseProject}
          >
            <IconClose size={11} />
          </button>
        )}
      </div>

      <div className={styles.center}>
        <QuickSwitcher />
      </div>

      <div className={styles.actions}>
        <EnvironmentSwitcher />
        <button
          type="button"
          className={styles.iconButton}
          aria-label={themeLabel}
          title={themeLabel}
          disabled={themeLocked}
          onClick={onThemeClick}
        >
          <ThemeIcon size={14} />
        </button>
      </div>
    </header>
  );
}
