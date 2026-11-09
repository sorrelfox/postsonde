import { useState } from "react";
import { closeWorkspace } from "./commands/workspace";
import { AppShell } from "./components/shell/AppShell";
import { AutoSaveStatus } from "./components/shell/AutoSaveStatus";
import { CookiesButton } from "./components/cookies/CookiesButton";
import { PolicyStatus } from "./components/shell/PolicyStatus";
import { SecretsStatus } from "./components/shell/SecretsStatus";
import { SecretsHost } from "./components/shell/UnlockDialog";
import { useAutoSaveSync } from "./components/shell/useAutoSaveSync";
import { useSettingsPersistence } from "./components/shell/useSettingsPersistence";
import { useThemeSync } from "./components/shell/useThemeSync";
import { useLocaleSync } from "./components/shell/useLocaleSync";
import { useCustomThemeSync } from "./components/shell/useCustomThemeSync";
import { useEditorPreferencesSync } from "./components/shell/useEditorPreferencesSync";
import { useShortcutOverridesSync } from "./components/shell/useShortcutOverridesSync";
import SystemDemo from "./components/SystemDemo";
import { StreamDemo } from "./components/StreamDemo";
import { KeyValuePreview } from "./components/editor/KeyValuePreview";
import { GrpcResponsePanel } from "./components/response/GrpcResponsePanel";
import { HttpResponsePanel } from "./components/response/HttpResponsePanel";
import { SseResponsePanel } from "./components/response/SseResponsePanel";
import { NoTabState } from "./components/tabs/NoTabState";
import { TabBar } from "./components/tabs/TabBar";
import { TabPanel } from "./components/tabs/TabPanel";
import { useTabsSync } from "./components/tabs/useTabsSync";
import { useEnvironmentsSync } from "./components/environments/useEnvironmentsSync";
import { useCollectionSync } from "./components/tree/useCollectionSync";
import { ServerDetails, ServerTraffic } from "./components/servers/ServerView";
import { useServersSync } from "./components/servers/useServersSync";
import { WorkspaceGate } from "./components/workspace/WorkspaceGate";
import { formatError } from "./lib/errors";
import { storedLocalePreference } from "./i18n";

import {
  createStores,
  initialSettingsState,
  selectFolderServer,
  selectSelectedServer,
  StoreProvider,
  useCollection,
  useDrafts,
  useServers,
  useStores,
  useTabs,
  useWorkspace,
  type Stores,
} from "./stores";
import { storedCustomTheme } from "./styles/customTheme";
import { storedThemePreference } from "./styles/theme";

// Root: providers, the theme and settings, then the welcome screen or the
// app shell (docs/frontend.md). Features mount into the shell's slots:
// <AppShell tabBar={…} request={…} response={…} statusEnd={…} />.

function createAppStores(): Stores {
  // Start from the theme (and custom theme, UI-05) and the UI language
  // (UI-10) cached for first paint, so the store, <html data-theme> and the
  // language main.tsx set agree until the settings file has loaded.
  const customTheme = storedCustomTheme()?.id ?? "";
  return createStores({
    settings: { ...initialSettingsState, theme: storedThemePreference(), customTheme, locale: storedLocalePreference() },
  });
}

/** The shell of the open project. */
function ProjectShell() {
  const stores = useStores();
  const name = useWorkspace((s) => s.current?.name);
  const path = useWorkspace((s) => s.current?.path);
  useEnvironmentsSync();
  useCollectionSync();
  useTabsSync();
  useServersSync();
  const activeId = useTabs((s) => s.activeId);
  const hasTab = activeId !== null;
  // An HTTP or GraphQL request's response pane shows its latest send (HTTP-20, GQL-02).
  const activeHttp = useCollection((s) => {
    const node = activeId === null ? undefined : s.nodes[activeId];
    return node?.kind === "request" && (node.fileKind === "http" || node.fileKind === "graphql");
  });
  const activeGraphql = useCollection((s) => {
    const node = activeId === null ? undefined : s.nodes[activeId];
    return node?.kind === "request" && node.fileKind === "graphql";
  });
  // A gRPC request's response pane shows its latest call (GRPC-11, GRPC-12, GRPC-19, GRPC-20).
  const activeGrpc = useCollection((s) => {
    const node = activeId === null ? undefined : s.nodes[activeId];
    return node?.kind === "request" && node.fileKind === "grpc";
  });
  // An sse request's response pane shows its latest event stream (SSE-01, SSE-02).
  const activeSse = useCollection((s) => {
    const node = activeId === null ? undefined : s.nodes[activeId];
    return node?.kind === "request" && node.fileKind === "sse";
  });
  // A server selected in the Servers view takes the work area (SRV-01) until it is closed or a tab is activated.
  const server = useServers(selectSelectedServer);
  // A folder tab on its Server sub-tab shows the traffic of the folder's saved server (SRV-04).
  const workspaceId = useCollection((s) => s.workspaceId);
  const activeFolder = useCollection((s) => activeId !== null && s.nodes[activeId]?.kind === "folder");
  const onServerTab = useDrafts((s) => activeId !== null && s.subTabs[activeId] === "server");
  const folderServerId = useServers((s) =>
    activeFolder && onServerTab && activeId !== null ? (selectFolderServer(s, workspaceId, activeId)?.id ?? null) : null,
  );
  const onCloseProject = () => {
    closeWorkspace(stores).catch((err: unknown) => console.warn(`workspace: ${formatError(err)}`));
  };

  return (
    // The active tab's editor. With no tab open, the request pane offers a
    // new scratch request and lists the scratch requests (COL-06); the
    // response pane keeps temporary content until real views land: the
    // bindings demo and the stream demo.
    <AppShell
      projectName={name}
      projectPath={path}
      onCloseProject={onCloseProject}
      tabBar={<TabBar />}
      statusStart={<AutoSaveStatus />}
      statusEnd={
        <>
          <PolicyStatus />
          <SecretsStatus />
          <CookiesButton />
        </>
      }
      request={
        server !== null ? (
          <ServerDetails server={server} />
        ) : hasTab ? (
          <TabPanel />
        ) : (
          <NoTabState />
        )
      }
      response={
        server !== null ? (
          <ServerTraffic serverId={server.id} />
        ) : activeId !== null ? (
          activeHttp ? (
            <HttpResponsePanel key={activeId} id={activeId} graphql={activeGraphql} />
          ) : activeGrpc ? (
            <GrpcResponsePanel key={activeId} id={activeId} />
          ) : activeSse ? (
            <SseResponsePanel key={activeId} id={activeId} />
          ) : folderServerId !== null ? (
            <ServerTraffic key={folderServerId} serverId={folderServerId} />
          ) : undefined
        ) : (
          <div style={{ flex: 1, minHeight: 0, overflow: "auto" }}>
            <KeyValuePreview />
            <SystemDemo />
            <StreamDemo />
          </div>
        )
      }
    />
  );
}

function AppRoot() {
  useThemeSync();
  useCustomThemeSync();
  useLocaleSync();
  useEditorPreferencesSync();
  useShortcutOverridesSync();
  useSettingsPersistence();
  useAutoSaveSync();
  return (
    <>
      <WorkspaceGate>
        <ProjectShell />
      </WorkspaceGate>
      <SecretsHost />
    </>
  );
}

export default function App() {
  const [stores] = useState(createAppStores);
  return (
    <StoreProvider stores={stores}>
      <AppRoot />
    </StoreProvider>
  );
}
