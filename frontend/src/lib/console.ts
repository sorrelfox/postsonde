// The in-app console (DBG-06): calls into the Go ConsoleService
// (internal/app/services/console) and the ConsoleFeed that keeps the
// records the console shows. Records come redacted from Go
// (docs/privacy.md); see docs/frontend.md (Console).
import { Events } from "@wailsio/runtime";
import {
  ConsoleService,
  type EntriesEventDTO,
  type EntryDTO,
  type FieldDTO,
  type ListDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/console";
import type { CancellablePromise } from "./calls";

export type { EntriesEventDTO, EntryDTO, FieldDTO, ListDTO };

/** Sent by Go with the records logged since the last one (batched). */
export const CONSOLE_ENTRIES_EVENT = "console:entries";

export type ConsoleLevel = "debug" | "info" | "warn" | "error";

/** The levels, least severe first. */
export const CONSOLE_LEVELS: readonly ConsoleLevel[] = ["debug", "info", "warn", "error"];

/** How many records the feed keeps until Go says (ListDTO.capacity). */
export const DEFAULT_CONSOLE_CAPACITY = 2000;

/** The kept records, oldest first. */
export function listConsole(): CancellablePromise<ListDTO> {
  return ConsoleService.List();
}

/** Drops the records Go keeps. The log file is not touched. */
export function clearConsole(): CancellablePromise<void> {
  return ConsoleService.Clear();
}

/** Calls `callback` with each batch of new records. Returns the unsubscribe function. */
export function onConsoleEntries(callback: (event: EntriesEventDTO) => void): () => void {
  return Events.On(CONSOLE_ENTRIES_EVENT, (ev) => callback(ev.data));
}

/** Rank of a level, for "this level and above" filters; unknown levels count as info. */
export function levelRank(level: string): number {
  const i = CONSOLE_LEVELS.indexOf(level as ConsoleLevel);
  return i < 0 ? 1 : i;
}

export interface ConsoleSnapshot {
  /** Bumped on every change. */
  readonly version: number;
  /** The records, oldest first, at most `capacity`. */
  readonly entries: readonly EntryDTO[];
  /** Warnings and errors added since markSeen. */
  readonly unseenProblems: number;
}

/**
 * The records the console shows: the ones Go kept when the feed started,
 * then every batch it streams. Records are ordered and deduplicated by seq,
 * so a batch that overlaps the initial list is harmless. Framework-free;
 * React reads it with useSyncExternalStore.
 */
export class ConsoleFeed {
  private entries: EntryDTO[] = [];
  private lastSeq = 0;
  private capacity: number;
  private unseen = 0;
  private version = 0;
  private snapshot: ConsoleSnapshot | null = null;
  private readonly listeners = new Set<() => void>();

  constructor(capacity = DEFAULT_CONSOLE_CAPACITY) {
    this.capacity = Math.max(1, capacity);
  }

  getSnapshot = (): ConsoleSnapshot => {
    this.snapshot ??= { version: this.version, entries: this.entries, unseenProblems: this.unseen };
    return this.snapshot;
  };

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };

  /** Adds records; ones not newer than the last one kept are dropped. */
  add(batch: readonly EntryDTO[], countUnseen = true): void {
    const fresh = batch.filter((e) => e.seq > this.lastSeq).sort((a, b) => a.seq - b.seq);
    if (fresh.length === 0) return;
    this.lastSeq = fresh[fresh.length - 1].seq;
    let next = this.entries.concat(fresh);
    if (next.length > this.capacity) next = next.slice(next.length - this.capacity);
    this.entries = next;
    if (countUnseen) this.unseen += fresh.filter((e) => levelRank(e.level) >= 2).length;
    this.changed();
  }

  /** Takes Go's list: merges it before anything streamed meanwhile. */
  load(list: ListDTO): void {
    if (list.capacity > 0) this.capacity = list.capacity;
    const known = new Set(this.entries.map((e) => e.seq));
    const merged = (list.entries ?? []).filter((e) => !known.has(e.seq)).concat(this.entries);
    merged.sort((a, b) => a.seq - b.seq);
    this.entries = merged.length > this.capacity ? merged.slice(merged.length - this.capacity) : merged;
    if (merged.length > 0) this.lastSeq = Math.max(this.lastSeq, merged[merged.length - 1].seq);
    this.changed();
  }

  /** Empties the feed; later records still arrive. */
  clear(): void {
    if (this.entries.length === 0 && this.unseen === 0) return;
    this.entries = [];
    this.unseen = 0;
    this.changed();
  }

  /** The console was looked at: resets the unseen warnings and errors. */
  markSeen(): void {
    if (this.unseen === 0) return;
    this.unseen = 0;
    this.changed();
  }

  private changed(): void {
    this.version++;
    this.snapshot = null;
    for (const l of [...this.listeners]) l();
  }
}

/** One console line as text, for copying: time, level, operation, message, fields. */
export function formatConsoleEntry(e: EntryDTO): string {
  const parts = [e.at, e.level.toUpperCase().padEnd(5)];
  if (e.source !== "") parts.push(`[${e.source}]`);
  if (e.opKind !== "" || e.opId !== "") parts.push(`(${[e.opKind, e.opId].filter((s) => s !== "").join(" ")})`);
  parts.push(e.message);
  for (const f of e.fields ?? []) parts.push(`${f.key}=${quoteValue(f.value)}`);
  return parts.join(" ");
}

function quoteValue(v: string): string {
  return v === "" || /[\s"=]/.test(v) ? JSON.stringify(v) : v;
}
