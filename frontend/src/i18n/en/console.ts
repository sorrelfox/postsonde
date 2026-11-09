// English messages of src/components/console/ (namespace "console"): t("console.<id>").
// See docs/frontend.md#localization.
import type { Messages } from "../messages";

export default {
  // Both views of the panel
  title: "Console",
  close: "Close the console",
  "filter.placeholder": "Filter",
  hidden: { one: "{number} hidden", other: "{number} hidden" },

  // The Log / Connections switch
  "views.label": "Console view",
  "views.log": "Log",
  "views.connections": "Connections",
  "views.active": { one: "{count} active", other: "{count} active" },

  // The log view (DBG-06)
  "log.levels": "Levels",
  "log.filter": "Filter the console",
  "log.pause": "Pause",
  "log.pause.title": "Stop following new records",
  "log.resume": "Resume",
  "log.resume.title": "Follow new records",
  "log.copy": "Copy the shown records",
  "log.copied": { one: "{number} record", other: "{number} records" },
  "log.save": "Save the shown records…",
  "log.clear": "Clear the console",
  "log.clearFailed": "Could not clear the console: {error}",
  "log.records": "Console records",
  "log.record": "Record",
  "log.empty": "Nothing logged yet.",
  "log.noMatch": "No records match the filter.",
  "level.debug": "Debug",
  "level.info": "Info",
  "level.warn": "Warnings",
  "level.error": "Errors",

  // The connections view (PRV-06)
  "connections.label": "Connections",
  "connections.logTitle": "Connection log",
  "connections.connection": "Connection",
  "connections.filter": "Filter the connections",
  "connections.activeOnly": "Active only",
  "connections.copy": "Copy the shown connections",
  "connections.copied": { one: "{number} connection", other: "{number} connections" },
  "connections.save": "Save the shown connections…",
  "connections.clear": "Clear the ended connections",
  "connections.clear.title": "Clear the ended connections (open ones stay)",
  "connections.clearFailed": "Could not clear the connection log: {error}",
  "connections.none": "No connections. PostSonde connects only when you send a request or connect a client.",
  "connections.noMatch": "No connections match the filter.",
  "connections.col.started": "Started",
  "connections.col.state": "State",
  "connections.col.cause": "Cause",
  "connections.col.target": "Target",
  "connections.col.remote": "Remote",
  "connections.col.local": "Local",
  "connections.col.connect": "Connect",
  "connections.col.duration": "Duration",
  "connections.col.in": "In",
  "connections.col.out": "Out",
  "connections.col.error": "Error",
  "connections.via": "via {kind}",
  "connections.viaTitle": "Through the proxy {proxy}; the remote address is the proxy's",

  // The status bar's Console button
  "button.show": "Show the console ({shortcut})",
  "button.hide": "Hide the console ({shortcut})",
  "button.badge": { one: "{badge} new warning or error", other: "{badge} new warnings or errors" },

  // The status bar's network indicator
  "network.idle": "Idle",
  "network.proxy": "via {kind} proxy",
  "network.proxyTitle": "Open connections go through {proxies}",
  "network.count": { one: "{count} connection", other: "{count} connections" },
  "network.show": "Show the connection log",
  "network.hide": "Hide the connection log",
  "network.label.idle.show": "Network: no open connections. Show the connection log",
  "network.label.idle.hide": "Network: no open connections. Hide the connection log",
  "network.label.open.show": {
    one: "Network: {count} connection open. Show the connection log",
    other: "Network: {count} connections open. Show the connection log",
  },
  "network.label.open.hide": {
    one: "Network: {count} connection open. Hide the connection log",
    other: "Network: {count} connections open. Hide the connection log",
  },
} as const satisfies Messages;
