import { useCallback, useEffect, useId, useRef, useState, type ReactNode } from "react";
import { showDataDirStartupNotice } from "../../commands/dataDir";
import { IMPORT_DROP_TARGET } from "../../commands/fileDrop";
import { useSettings, useStores } from "../../stores";
import { ConfirmHost } from "../common/Dialog";
import { PromptHost } from "../common/PromptHost";
import { ConsoleButton } from "../console/ConsoleButton";
import { ConnectionsPanel } from "../console/ConnectionsPanel";
import { ConsolePanel } from "../console/ConsolePanel";
import type { ConsoleView } from "../console/ConsoleViews";
import { NetworkIndicator } from "../console/NetworkIndicator";
import { ToastHost } from "../common/Toast";
import { SettingsScreen } from "../settings/SettingsScreen";
import styles from "./AppShell.module.css";
import { CommandPalette } from "./CommandPalette";
import { FocusModeButton } from "./FocusModeButton";
import { FileDropOverlay, useFileDrop } from "./FileDrop";
import { IconRail } from "./IconRail";
import { ShortcutHelp } from "./ShortcutHelp";
import { Sidebar } from "./Sidebar";
import type { SidebarView } from "./sidebarViews";
import { StatusBar } from "./StatusBar";
import { TabBarSlot } from "./TabBarSlot";
import { TitleBar } from "./TitleBar";
import { useEditHistory } from "./useEditHistory";
import { useGlobalShortcuts, useShortcut } from "./useShortcut";
import { useShortcutOverrides } from "./useShortcutOverridesSync";
import { WorkArea } from "./WorkArea";

export interface AppShellProps {
  /** The open project's name, shown in the title bar. */
  projectName?: string;
  /** The open project's folder: the title bar's tooltip. A placeholder path without a project. */
  projectPath?: string;
  /** Closes the project; shows a close button in the title bar when set. */
  onCloseProject?: () => void;
  /** Content of the tab row above the panes (the tab bar). Empty by default. */
  tabBar?: ReactNode;
  /** Request pane content; fills the pane and owns its own scrolling. Default: token preview. */
  request?: ReactNode;
  /** Response pane content; fills the pane and owns its own scrolling. Default: empty state. */
  response?: ReactNode;
  /** Left side of the status bar. Default: "Ready". */
  statusStart?: ReactNode;
  /** Right side of the status bar (app version, info). Default: version placeholder. */
  statusEnd?: ReactNode;
}

/**
 * The main window frame (UI-01): title bar on top; below it the icon
 * rail, the collapsible sidebar and the main column (tab bar, request and
 * response panes, status bar). Features plug in through the props. The
 * rail's gear (or ⌘,) shows the settings screen in place of the sidebar and
 * the main column.
 *
 * Focus mode (UI-09, ⇧⌘↩) hides the rail, the sidebar and the console so
 * the request and response fill the window. It is a flag on top of the
 * layout, not a change of it: the sidebar's collapsed setting and whether
 * the console shows are left alone, so leaving focus mode brings back
 * exactly what was there. It lasts for this session only (local state).
 *
 * The theme and the settings file are applied above it, by useThemeSync and
 * useSettingsPersistence in App, so they also cover the welcome screen.
 */
export function AppShell({
  projectName,
  projectPath,
  onCloseProject,
  tabBar,
  request,
  response,
  statusStart,
  statusEnd,
}: AppShellProps) {
  const stores = useStores();
  const collapsed = useSettings((s) => s.sidebarCollapsed);
  // Only the rail and the sidebar read the active view, and only the shell
  // whether the settings screen shows: local state (docs/frontend.md).
  const [view, setView] = useState<SidebarView>("collections");
  const [settingsOpen, setSettingsOpen] = useState(false);
  // The console panel under the panes (DBG-06): local state, like the settings screen.
  const [consoleOpen, setConsoleOpen] = useState(false);
  // Its view: the app log or the connection log (PRV-06).
  const [consoleView, setConsoleView] = useState<ConsoleView>("log");
  const sidebarId = useId();
  // Files dropped anywhere on the shell are imported (IMP-11); modal dialogs are outside it.
  const shellRef = useRef<HTMLDivElement>(null);
  const dropping = useFileDrop(shellRef);
  // Focus mode (UI-09): local state, never saved.
  const [focusMode, setFocusMode] = useState(false);
  const mainRef = useRef<HTMLElement>(null);

  // With the settings screen showing, the sidebar toggle goes back to the
  // sidebar (shown) rather than toggling what cannot be seen.
  const toggleSidebar = useCallback(() => {
    // In focus mode the toggle brings the sidebar back: leave focus mode, with the sidebar shown.
    if (focusMode) {
      setFocusMode(false);
      stores.settings.dispatch({ type: "settings/setSidebarCollapsed", collapsed: false });
      return;
    }
    if (settingsOpen) {
      setSettingsOpen(false);
      stores.settings.dispatch({ type: "settings/setSidebarCollapsed", collapsed: false });
      return;
    }
    stores.settings.dispatch({ type: "settings/toggleSidebar" });
  }, [stores, settingsOpen, focusMode]);

  // Clicking the open view hides the sidebar; any other view opens it. Any
  // view closes the settings screen.
  const selectView = useCallback(
    (next: SidebarView) => {
      if (next === view && !settingsOpen) {
        toggleSidebar();
        return;
      }
      setSettingsOpen(false);
      setView(next);
      stores.settings.dispatch({ type: "settings/setSidebarCollapsed", collapsed: false });
    },
    [view, settingsOpen, stores, toggleSidebar],
  );

  // The global keyboard shortcuts run while the project shell is open.
  useGlobalShortcuts();
  // Custom shortcuts (UI-07) changed: render the shell again, so tooltips and hints show the new keys.
  useShortcutOverrides();
  // Undo and redo in request editors (NAV-07).
  useEditHistory();
  useShortcut("sidebar.toggle", toggleSidebar);
  // The settings screen needs the rail to go back: it leaves focus mode.
  const openSettings = useCallback(() => {
    setFocusMode(false);
    setSettingsOpen(true);
  }, []);
  useShortcut("settings.open", openSettings);
  // From the settings screen, focus mode goes back to the work area in focus mode.
  const toggleFocusMode = useCallback(() => {
    if (settingsOpen) {
      setSettingsOpen(false);
      setFocusMode(true);
      return;
    }
    setFocusMode((on) => !on);
  }, [settingsOpen]);
  useShortcut("view.focusMode", toggleFocusMode);
  // Focus inside what focus mode hides would be lost: move it to the work area.
  useEffect(() => {
    if (!focusMode) return;
    const active = document.activeElement;
    if (active === null || active === document.body || active.closest("[hidden]") !== null) {
      mainRef.current?.focus({ preventScroll: true });
    }
  }, [focusMode]);
  // Leaving the settings screen from the keyboard (⌘B, focus mode, the
  // console) drops focus with the screen: give it to the work area, as focus
  // mode does (UI-11).
  const settingsWasOpen = useRef(settingsOpen);
  useEffect(() => {
    const closed = settingsWasOpen.current && !settingsOpen;
    settingsWasOpen.current = settingsOpen;
    if (!closed) return;
    const active = document.activeElement;
    if (active === null || active === document.body || !active.isConnected || active.closest("[hidden]") !== null) {
      mainRef.current?.focus({ preventScroll: true });
    }
  }, [settingsOpen]);
  // Once per session: a data directory problem or a finished move (SET-03).
  useEffect(() => {
    void showDataDirStartupNotice(openSettings);
  }, [openSettings]);

  // Showing the console leaves the settings screen, which hides the main column.
  const toggleConsole = useCallback(() => {
    // In focus mode the console is hidden: show it, leaving focus mode.
    if (focusMode) {
      setFocusMode(false);
      setConsoleOpen(true);
      return;
    }
    if (settingsOpen) {
      setSettingsOpen(false);
      setConsoleOpen(true);
      return;
    }
    setConsoleOpen((open) => !open);
  }, [settingsOpen, focusMode]);
  useShortcut("console.toggle", toggleConsole);
  // The network activity indicator opens and closes the console on its Connections view.
  const toggleConnections = useCallback(() => {
    const showing = consoleOpen && !settingsOpen && !focusMode && consoleView === "connections";
    setFocusMode(false);
    setSettingsOpen(false);
    setConsoleView("connections");
    setConsoleOpen(!showing);
  }, [consoleOpen, settingsOpen, focusMode, consoleView]);
  const shown = consoleOpen && !settingsOpen && !focusMode;
  const statusTools = (
    <>
      {focusMode && (
        <FocusModeButton
          onExit={() => {
            setFocusMode(false);
            // The button goes away with focus mode: keep focus in the work area.
            mainRef.current?.focus({ preventScroll: true });
          }}
        />
      )}
      <NetworkIndicator open={shown && consoleView === "connections"} onToggle={toggleConnections} />
      <ConsoleButton open={shown} onToggle={toggleConsole} />
    </>
  );

  // The sidebar filter (NAV-06): shows the Collections view and focuses its filter field.
  const [filterFocus, setFilterFocus] = useState(0);
  useShortcut("collection.filter", () => {
    setFocusMode(false);
    setSettingsOpen(false);
    setView("collections");
    stores.settings.dispatch({ type: "settings/setSidebarCollapsed", collapsed: false });
    setFilterFocus((n) => n + 1);
  });

  return (
    <div ref={shellRef} className={styles.shell} data-file-drop-target={IMPORT_DROP_TARGET}>
      <TitleBar
        projectName={projectName}
        projectPath={projectPath}
        onCloseProject={onCloseProject}
        onOpenSettings={openSettings}
      />
      <div className={styles.body} data-focus-mode={focusMode ? "true" : undefined}>
        <IconRail
          hidden={focusMode}
          view={view}
          collapsed={collapsed}
          settingsOpen={settingsOpen}
          sidebarId={sidebarId}
          onSelectView={selectView}
          onToggleSidebar={toggleSidebar}
          onToggleSettings={() => setSettingsOpen((open) => !open)}
        />
        {settingsOpen && (
          <div className={styles.settingsColumn}>
            <SettingsScreen />
            <StatusBar start={statusStart} end={statusEnd} tools={statusTools} />
          </div>
        )}
        {/* Hidden, not unmounted, behind the settings screen: open editors keep their state. */}
        <Sidebar id={sidebarId} view={view} hidden={collapsed || settingsOpen || focusMode} filterFocus={filterFocus} />
        <main ref={mainRef} className={styles.main} hidden={settingsOpen} tabIndex={-1}>
          <TabBarSlot>{tabBar}</TabBarSlot>
          <WorkArea request={request} response={response} />
          {/* Hidden, not unmounted, in focus mode: the panel keeps its filter and scroll position. */}
          {consoleOpen && (
            <div className={styles.consoleSlot} hidden={focusMode}>
              {consoleView === "connections" ? (
                <ConnectionsPanel onClose={() => setConsoleOpen(false)} onViewChange={setConsoleView} />
              ) : (
                <ConsolePanel onClose={() => setConsoleOpen(false)} onViewChange={setConsoleView} />
              )}
            </div>
          )}
          <StatusBar start={statusStart} end={statusEnd} tools={statusTools} />
          <ToastHost />
        </main>
      </div>
      <ShortcutHelp />
      <CommandPalette />
      <ConfirmHost />
      <PromptHost />
      <FileDropOverlay active={dropping} />
    </div>
  );
}
