import { IconSettings, IconSidebar } from "../common/icons";
import styles from "./IconRail.module.css";
import { ariaShortcut, shortcutLabel } from "../../lib/shortcuts";
import { SIDEBAR_VIEWS, type SidebarView } from "./sidebarViews";
import { useT } from "../../i18n/react";

const TOP_VIEWS: readonly SidebarView[] = ["collections", "history", "environments", "servers"];

export interface IconRailProps {
  view: SidebarView;
  collapsed: boolean;
  /** The settings screen is showing: the gear is pressed and no view is. */
  settingsOpen: boolean;
  /** id of the sidebar element, for aria-controls. */
  sidebarId: string;
  onSelectView: (view: SidebarView) => void;
  onToggleSidebar: () => void;
  /** Hidden in focus mode (UI-09). */
  hidden?: boolean;
  /** The gear: opens the settings screen, or closes it when it is showing. */
  onToggleSettings: () => void;
}

/** The narrow column left of the sidebar: view switcher, sidebar toggle and settings. */
export function IconRail({
  view,
  collapsed,
  settingsOpen,
  sidebarId,
  onSelectView,
  onToggleSidebar,
  onToggleSettings,
  hidden = false,
}: IconRailProps) {
  const t = useT();
  const viewButton = (id: SidebarView) => {
    const { labelKey, Icon } = SIDEBAR_VIEWS[id];
    const label = t(labelKey);
    return (
      <button
        key={id}
        type="button"
        className={styles.button}
        aria-label={label}
        title={label}
        aria-pressed={!settingsOpen && !collapsed && view === id}
        onClick={() => onSelectView(id)}
      >
        <Icon size={17} />
      </button>
    );
  };

  const toggleLabel = collapsed ? t("shell.iconRail.showSidebar") : t("shell.iconRail.hideSidebar");
  const settingsLabel = t("shell.iconRail.settings");

  return (
    <nav className={styles.rail} aria-label={t("shell.iconRail.label")} hidden={hidden}>
      {TOP_VIEWS.map(viewButton)}
      <div className={styles.spacer} />
      <button
        type="button"
        className={styles.button}
        aria-label={toggleLabel}
        title={t("shell.iconRail.withShortcut", { label: toggleLabel, shortcut: shortcutLabel("sidebar.toggle") })}
        aria-expanded={!collapsed}
        aria-controls={sidebarId}
        aria-keyshortcuts={ariaShortcut("sidebar.toggle")}
        onClick={onToggleSidebar}
      >
        <IconSidebar size={17} />
      </button>
      <button
        type="button"
        className={styles.button}
        aria-label={settingsLabel}
        title={t("shell.iconRail.withShortcut", { label: settingsLabel, shortcut: shortcutLabel("settings.open") })}
        aria-pressed={settingsOpen}
        aria-keyshortcuts={ariaShortcut("settings.open")}
        onClick={onToggleSettings}
      >
        <IconSettings size={17} />
      </button>
    </nav>
  );
}
