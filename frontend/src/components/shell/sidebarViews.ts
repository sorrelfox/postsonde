// The views the icon rail switches the sidebar between.

import type { ComponentType } from "react";
import { IconCollections, IconEnvironments, IconHistory, IconServers, type IconProps } from "../common/icons";
import type { MessageKey } from "../../i18n";

// The rail's gear is not a sidebar view: it opens the settings screen in
// place of the sidebar and the work area (components/settings).
export type SidebarView = "collections" | "history" | "environments" | "servers";

/** A view's name and empty state, as message keys (shown with t()). */
export interface SidebarViewInfo {
  readonly labelKey: MessageKey;
  readonly Icon: ComponentType<IconProps>;
  readonly emptyTitleKey: MessageKey;
  readonly emptyBodyKey: MessageKey;
}

export const SIDEBAR_VIEWS: Readonly<Record<SidebarView, SidebarViewInfo>> = {
  collections: {
    labelKey: "shell.view.collections",
    Icon: IconCollections,
    emptyTitleKey: "shell.view.collections.emptyTitle",
    emptyBodyKey: "shell.view.collections.emptyBody",
  },
  history: {
    labelKey: "shell.view.history",
    Icon: IconHistory,
    emptyTitleKey: "shell.view.history.emptyTitle",
    emptyBodyKey: "shell.view.history.emptyBody",
  },
  environments: {
    labelKey: "shell.view.environments",
    Icon: IconEnvironments,
    emptyTitleKey: "shell.view.environments.emptyTitle",
    emptyBodyKey: "shell.view.environments.emptyBody",
  },
  servers: {
    labelKey: "shell.view.servers",
    Icon: IconServers,
    emptyTitleKey: "shell.view.servers.emptyTitle",
    emptyBodyKey: "shell.view.servers.emptyBody",
  },
};
