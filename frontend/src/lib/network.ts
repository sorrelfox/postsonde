// The network activity indicator and connection log (PRV-06): calls into
// the Go NetworkService (internal/app/services/network) and the
// ConnectionFeed the indicator and the Connections tab read. Connections
// name their target by host:port only (docs/privacy.md); see
// docs/frontend.md (Network activity).
import { Events } from "@wailsio/runtime";
import {
  NetworkService,
  type ActivityEventDTO,
  type ConnDTO,
  type ListDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/network";
import { t } from "../i18n";
import type { CancellablePromise } from "./calls";
import { formatMinSec, formatUnit } from "./units";

export type { ActivityEventDTO, ConnDTO, ListDTO };

/** Sent by Go with the connections that changed (throttled). */
export const NETWORK_ACTIVITY_EVENT = "network:activity";

export type ConnState = "connecting" | "open" | "closed" | "failed" | "refused";

/** How many connections the feed keeps until Go says (ListDTO.capacity). */
export const DEFAULT_CONNECTION_CAPACITY = 500;

/** The kept connections, oldest first. */
export function listConnections(): CancellablePromise<ListDTO> {
  return NetworkService.List();
}

/** Drops the connections that have ended, in Go. Open ones stay. */
export function clearConnections(): CancellablePromise<void> {
  return NetworkService.Clear();
}

/** Calls `callback` with each activity event. Returns the unsubscribe function. */
export function onNetworkActivity(callback: (event: ActivityEventDTO) => void): () => void {
  return Events.On(NETWORK_ACTIVITY_EVENT, (ev) => callback(ev.data));
}

/** Whether a connection is connecting or open. */
export function isActive(c: ConnDTO): boolean {
  return c.state === "connecting" || c.state === "open";
}

// Connections only move forward: connecting, then open, then ended.
function stateRank(state: string): number {
  switch (state) {
    case "connecting":
      return 0;
    case "open":
      return 1;
    default:
      return 2;
  }
}

/** Whether `a` is a later view of the same connection than `b`. */
export function isNewer(a: ConnDTO, b: ConnDTO): boolean {
  const ra = stateRank(a.state);
  const rb = stateRank(b.state);
  if (ra !== rb) return ra > rb;
  return a.bytesIn + a.bytesOut >= b.bytesIn + b.bytesOut;
}

export interface ConnectionSnapshot {
  /** Bumped on every change. */
  readonly version: number;
  /** The connections, oldest first, at most `capacity`. */
  readonly conns: readonly ConnDTO[];
  /** How many of them are connecting or open. */
  readonly active: number;
}

/**
 * The connections the indicator and the log show: the ones Go kept when
 * the feed started, then every change it sends, merged by id. An older view
 * of a connection never replaces a newer one, so an event and the list may
 * arrive in any order. Framework-free; React reads it with
 * useSyncExternalStore.
 */
export class ConnectionFeed {
  private conns: ConnDTO[] = [];
  private capacity: number;
  private version = 0;
  private snapshot: ConnectionSnapshot | null = null;
  private readonly listeners = new Set<() => void>();

  constructor(capacity = DEFAULT_CONNECTION_CAPACITY) {
    this.capacity = Math.max(1, capacity);
  }

  getSnapshot = (): ConnectionSnapshot => {
    this.snapshot ??= { version: this.version, conns: this.conns, active: this.conns.filter(isActive).length };
    return this.snapshot;
  };

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };

  /** Merges connections from an event or the list. */
  apply(batch: readonly ConnDTO[]): void {
    if (batch.length === 0) return;
    const byId = new Map(this.conns.map((c) => [c.id, c]));
    let changed = false;
    for (const c of batch) {
      const old = byId.get(c.id);
      if (old === undefined || (old !== c && isNewer(c, old))) {
        byId.set(c.id, c);
        changed = true;
      }
    }
    if (!changed) return;
    let next = [...byId.values()].sort((a, b) => a.id - b.id);
    // Like Go: the oldest ended connections go first, so open ones stay listed.
    while (next.length > this.capacity) {
      const i = next.findIndex((c) => !isActive(c));
      next = next.filter((_, j) => j !== (i < 0 ? 0 : i));
    }
    this.conns = next;
    this.changed();
  }

  /** Takes Go's list. */
  load(list: ListDTO): void {
    if (list.capacity > 0) this.capacity = list.capacity;
    this.apply(list.conns ?? []);
  }

  /** Drops the connections that have ended; open ones stay, as in Go. */
  clearEnded(): void {
    const next = this.conns.filter(isActive);
    if (next.length === this.conns.length) return;
    this.conns = next;
    this.changed();
  }

  private changed(): void {
    this.version++;
    this.snapshot = null;
    for (const l of [...this.listeners]) l();
  }
}

/** A byte count for the log: 812 B, 5.0 KB, 1.2 MB. */
export function formatBytes(n: number): string {
  if (n < 1024) return formatUnit("B", n);
  if (n < 1024 * 1024) return formatUnit("KB", n / 1024, 1);
  if (n < 1024 * 1024 * 1024) return formatUnit("MB", n / (1024 * 1024), 1);
  return formatUnit("GB", n / (1024 * 1024 * 1024), 2);
}

/** A duration in ms for the log: 42 ms, 1.25 s, 3 min 5 s. */
export function formatDuration(ms: number): string {
  if (ms < 1000) return formatUnit("ms", Math.round(ms));
  if (ms < 60_000) return formatUnit("s", ms / 1000, 2);
  const s = Math.round(ms / 1000);
  return formatMinSec(Math.floor(s / 60), s % 60);
}

/**
 * The kind of proxy a connection went through (NET-06), from its `via`
 * ("socks5h://proxy.lan:1080"): the protocol's name, "SOCKS5", "SOCKS4",
 * "SOCKS4a", "HTTP" or "HTTPS"; "" for a direct connection.
 */
export function proxyKind(via: string): string {
  const scheme = via.slice(0, Math.max(0, via.indexOf("://"))).toLowerCase();
  switch (scheme) {
    case "socks5":
    case "socks5h":
      return "SOCKS5";
    case "socks4":
      return "SOCKS4";
    case "socks4a":
      return "SOCKS4a";
    case "http":
      return "HTTP";
    case "https":
      return "HTTPS";
  }
  return via === "" ? "" : scheme.toUpperCase();
}

/** The user action that caused a connection, for the log: "http.send op_1", or a note when there was none. */
export function connectionCause(c: ConnDTO): string {
  if (c.opKind === "") return t("lib.network.noUserAction");
  return c.opId === "" ? c.opKind : `${c.opKind} ${c.opId}`;
}

/** One connection as a text line, for copying. Host:port and addresses only. */
export function formatConnection(c: ConnDTO): string {
  const parts = [c.startedAt, c.state.toUpperCase().padEnd(10), c.network, c.address, `(${connectionCause(c)})`];
  if (c.remoteAddr !== "") parts.push(`remote=${c.remoteAddr}`);
  if ((c.via ?? "") !== "") parts.push(`via=${c.via}`);
  if (c.localAddr !== "") parts.push(`local=${c.localAddr}`);
  if (c.connectMs > 0) parts.push(`connect=${formatDuration(c.connectMs)}`);
  if (c.durationMs > 0) parts.push(`duration=${formatDuration(c.durationMs)}`);
  if (c.bytesIn > 0 || c.bytesOut > 0) parts.push(`in=${c.bytesIn}`, `out=${c.bytesOut}`);
  if (c.errorCode !== "") parts.push(`error=${c.errorCode}`);
  if (c.error !== "") parts.push(`message=${JSON.stringify(c.error)}`);
  return parts.join(" ");
}
