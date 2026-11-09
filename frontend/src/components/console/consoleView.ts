// Filtering and formatting of console rows (DBG-06). Pure functions, tested
// without React.
import type { MessageKey } from "../../i18n";
import type { ConsoleLevel, EntryDTO } from "../../lib/console";

export type LevelFilter = Readonly<Record<ConsoleLevel, boolean>>;

/** Debug records are hidden until asked for; the log file never has them. */
export const DEFAULT_LEVELS: LevelFilter = { debug: false, info: true, warn: true, error: true };

/** The level toggles' labels, as message keys: shown with t(LEVEL_LABELS[level]). */
export const LEVEL_LABELS: Readonly<Record<ConsoleLevel, MessageKey>> = {
  debug: "console.level.debug",
  info: "console.level.info",
  warn: "console.level.warn",
  error: "console.level.error",
};

/** The level of an entry as one of the four; unknown levels are info. */
export function entryLevel(e: EntryDTO): ConsoleLevel {
  return e.level === "debug" || e.level === "warn" || e.level === "error" ? e.level : "info";
}

/** Whether the entry matches the text filter: a case-insensitive substring of its message, source, operation or fields. */
export function matchesText(e: EntryDTO, query: string): boolean {
  const q = query.trim().toLowerCase();
  if (q === "") return true;
  if (e.message.toLowerCase().includes(q)) return true;
  if (e.source.toLowerCase().includes(q) || e.opKind.toLowerCase().includes(q) || e.opId.toLowerCase().includes(q)) return true;
  return (e.fields ?? []).some((f) => `${f.key}=${f.value}`.toLowerCase().includes(q));
}

/** The entries shown for the level toggles and the text filter, in order. */
export function filterEntries(entries: readonly EntryDTO[], levels: LevelFilter, query: string): EntryDTO[] {
  return entries.filter((e) => levels[entryLevel(e)] && matchesText(e, query));
}

/** Entries per level, for the toggle counts. */
export function countLevels(entries: readonly EntryDTO[]): Record<ConsoleLevel, number> {
  const counts = { debug: 0, info: 0, warn: 0, error: 0 };
  for (const e of entries) counts[entryLevel(e)]++;
  return counts;
}

/** HH:MM:SS.mmm in local time, or the raw text if it does not parse. */
export function formatClock(at: string): string {
  const t = new Date(at);
  if (Number.isNaN(t.getTime())) return at;
  const pad = (n: number, w = 2) => String(n).padStart(w, "0");
  return `${pad(t.getHours())}:${pad(t.getMinutes())}:${pad(t.getSeconds())}.${pad(t.getMilliseconds(), 3)}`;
}

/** The fields shown after the message: key=value, without the ones the row shows already. */
export function fieldsText(e: EntryDTO): string {
  const hidden = new Set<string>();
  if (e.source !== "") SOURCE_FIELDS.forEach((k) => hidden.add(k));
  if (e.opId !== "") ID_FIELDS.forEach((k) => hidden.add(k));
  if (e.opKind !== "") KIND_FIELDS.forEach((k) => hidden.add(k));
  return (e.fields ?? [])
    .filter((f) => !hidden.has(f.key))
    .map((f) => `${f.key}=${f.value}`)
    .join(" ");
}

// The attributes Go reads the source and the operation from (logging.Ring).
const SOURCE_FIELDS = ["component", "service"];
const ID_FIELDS = ["opId", "conn"];
const KIND_FIELDS = ["op", "domain", "kind"];
