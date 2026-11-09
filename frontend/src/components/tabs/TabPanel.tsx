import type { ComponentType } from "react";
import { useT } from "../../i18n/react";
import { useCollection, useDrafts, useTabs, type RequestId } from "../../stores";
import { TemplateVariablesProvider } from "../common/TemplateVariables";
import { EditHistoryScope } from "../editor/editorHistory";
import { ExternalChangeBanner } from "./ExternalChangeBanner";
import { FolderEditor } from "../editor/FolderEditor";
import { GraphqlEditor } from "../editor/GraphqlEditor";
import { GrpcEditor } from "../editor/GrpcEditor";
import { RequestEditor } from "../editor/RequestEditor";
import { SocketIoEditor } from "../editor/SocketIoEditor";
import { TcpEditor } from "../editor/TcpEditor";
import { UdpEditor } from "../editor/UdpEditor";
import { WebSocketEditor } from "../editor/WebSocketEditor";
import { nodeKindLabel, tabBadge } from "./tabBadge";
import { TAB_PANEL_ID, tabElementId } from "./TabBar";
import styles from "./TabPanel.module.css";

/** The editor of each request file kind. Each is mounted per item (keyed by its id). */
const EDITORS: ReadonlyMap<string, ComponentType<{ id: RequestId }>> = new Map<string, ComponentType<{ id: RequestId }>>([
  ["http", RequestEditor],
  ["tcp", TcpEditor],
  ["udp", UdpEditor],
  ["websocket", WebSocketEditor],
  ["grpc", GrpcEditor],
  ["graphql", GraphqlEditor],
  // An sse request has an http request's members (SSE-01).
  ["sse", RequestEditor],
  ["socketio", SocketIoEditor],
]);

/** The kinds sent with the variables of HTTPService.Variables (ENV-10, WS-11): their editors draw resolved chips. */
const TEMPLATED_KINDS: ReadonlySet<string> = new Set(["http", "graphql", "websocket", "sse", "socketio"]);

/**
 * The content of the active tab: the request editor for an HTTP request,
 * the TCP editor for a tcp request (TCP-01), the WebSocket editor
 * for a websocket request (WS-01), the gRPC editor for a grpc request
 * (GRPC-10), the GraphQL editor for a graphql request (GQL-01), the
 * request editor, with Connect instead of Send, for an sse request
 * (SSE-01), the Socket.IO editor for a socketio request (SIO-01), the folder
 * settings view for a folder (COL-03); for other items a placeholder with
 * the item's name, kind and path. Renders nothing without an active
 * tab.
 *
 * An editor sits in an EditHistoryScope and an element marked
 * data-edit-history, so its edits can be undone and redone, across tab
 * switches too (NAV-07, commands/undoRedo.ts). HTTP, GraphQL and WebSocket
 * editors also sit in a TemplateVariablesProvider, which marks their {{variables}}
 * resolved or not (ENV-11); so does the folder view, whose own variables
 * count (and reload the chips when they change). Above the editor,
 * ExternalChangeBanner shows a conflict with the file on disk or a file
 * deleted outside PostSonde (STO-05).
 */
export function TabPanel() {
  const t = useT();
  const activeId = useTabs((s) => s.activeId);
  const node = useCollection((s) => (activeId === null ? undefined : s.nodes[activeId]));
  if (activeId === null || node === undefined) return null;
  const badge = tabBadge(node);

  const Editor = node.kind === "request" ? EDITORS.get(node.fileKind) : undefined;
  if (Editor !== undefined) {
    return (
      <div
        className={styles.editorPanel}
        role="tabpanel"
        id={TAB_PANEL_ID}
        aria-labelledby={tabElementId(activeId)}
        data-node-id={node.id}
        data-edit-history={node.id}
      >
        <ExternalChangeBanner id={node.id} />
        <EditHistoryScope key={node.id} id={node.id}>
          {TEMPLATED_KINDS.has(node.fileKind) ? (
            <TemplateVariablesProvider requestId={node.id}>
              <Editor id={node.id} />
            </TemplateVariablesProvider>
          ) : (
            <Editor id={node.id} />
          )}
        </EditHistoryScope>
      </div>
    );
  }

  if (node.kind === "folder") {
    return (
      <div
        className={styles.editorPanel}
        role="tabpanel"
        id={TAB_PANEL_ID}
        aria-labelledby={tabElementId(activeId)}
        data-node-id={node.id}
        data-edit-history={node.id}
      >
        <ExternalChangeBanner id={node.id} />
        <EditHistoryScope key={node.id} id={node.id}>
          <FolderPanel id={node.id} />
        </EditHistoryScope>
      </div>
    );
  }

  return (
    <div
      className={styles.panel}
      role="tabpanel"
      id={TAB_PANEL_ID}
      aria-labelledby={tabElementId(activeId)}
      tabIndex={0}
      data-node-id={node.id}
    >
      <div className={styles.heading}>
        <span className={styles.badge} data-tone={badge.tone}>
          {badge.label}
        </span>
        <h2 className={styles.name}>{node.name}</h2>
      </div>
      <dl className={styles.facts}>
        <dt>{t("tabs.panel.kind")}</dt>
        <dd>{nodeKindLabel(node)}</dd>
        <dt>{t("tabs.panel.path")}</dt>
        <dd className={styles.path}>{node.path}</dd>
      </dl>
      <p className={styles.note}>{t("tabs.panel.notAvailable")}</p>
    </div>
  );
}

/** The folder view with its variables' chips, reloaded when its own variables change. */
function FolderPanel({ id }: { id: RequestId }) {
  const variables = useDrafts((s) => s.byId[id]?.request.variables);
  return (
    <TemplateVariablesProvider requestId={id} reloadKey={variables}>
      <FolderEditor id={id} />
    </TemplateVariablesProvider>
  );
}
