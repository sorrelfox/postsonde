import { useId, useState } from "react";
import type { HttpRunOptions } from "../../commands/httpSends";
import { MAX_REPEAT_COUNT, MAX_REPEAT_INTERVAL_MS } from "../../lib/http";
import { useT } from "../../i18n/react";
import { Dialog } from "../common/Dialog";
import styles from "./RepeatDialog.module.css";

type Mode = "times" | "interval";

export interface RepeatDialogProps {
  open: boolean;
  /** The options of the latest run, shown first. */
  initial: HttpRunOptions;
  onCancel: () => void;
  onStart: (options: HttpRunOptions) => void;
}

/** "1.5" for 1500 ms: seconds for a field. */
function secondsText(ms: number): string {
  return ms === 0 ? "0" : String(ms / 1000);
}

/** Milliseconds from a seconds field, or null when it is not a number in [min, a day]. */
function parseSeconds(text: string, minMs: number): number | null {
  const s = text.trim();
  if (s === "" || !/^\d*\.?\d*$/.test(s)) return null;
  const ms = Math.round(Number(s) * 1000);
  return Number.isFinite(ms) && ms >= minMs && ms <= MAX_REPEAT_INTERVAL_MS ? ms : null;
}

/** A count from a field, or null when it is not a whole number in [min, MAX_REPEAT_COUNT]. */
function parseCount(text: string, min: number): number | null {
  const s = text.trim();
  if (!/^\d+$/.test(s)) return null;
  const n = Number(s);
  return n >= min && n <= MAX_REPEAT_COUNT ? n : null;
}

/**
 * The Repeat dialog of the Send button (HTTP-27): send the request N
 * times, with an optional wait between the sends, or every X seconds until
 * stopped or after N sends. Both are one run: `count` sends (0 until
 * stopped) with `intervalMs` from the start of one send to the start of
 * the next. Enter starts it; Esc closes.
 */
export function RepeatDialog({ open, initial, onCancel, onStart }: RepeatDialogProps) {
  if (!open) return null;
  return <RepeatForm initial={initial} onCancel={onCancel} onStart={onStart} />;
}

function RepeatForm({ initial, onCancel, onStart }: Omit<RepeatDialogProps, "open">) {
  const t = useT();
  const baseId = useId();
  const intervalFirst = initial.mode === undefined ? initial.count === 0 : initial.mode === "interval";
  const [mode, setMode] = useState<Mode>(intervalFirst ? "interval" : "times");
  const [times, setTimes] = useState(initial.count > 0 ? String(initial.count) : "10");
  const [wait, setWait] = useState(intervalFirst ? "0" : secondsText(initial.intervalMs));
  const [every, setEvery] = useState(intervalFirst && initial.intervalMs > 0 ? secondsText(initial.intervalMs) : "5");
  const [cap, setCap] = useState(intervalFirst && initial.count > 0 ? String(initial.count) : "");

  const timesValue = parseCount(times, 1);
  const waitValue = parseSeconds(wait, 0);
  const everyValue = parseSeconds(every, 100);
  const capValue = cap.trim() === "" ? 0 : parseCount(cap, 1);

  let options: HttpRunOptions | null = null;
  if (mode === "times" && timesValue !== null && waitValue !== null) options = { mode, count: timesValue, intervalMs: waitValue };
  if (mode === "interval" && everyValue !== null && capValue !== null) options = { mode, count: capValue, intervalMs: everyValue };

  const start = () => {
    if (options !== null) onStart(options);
  };

  const field = (name: string) => `${baseId}-${name}`;
  const error = (name: string, bad: boolean, text: string) =>
    bad ? (
      <p id={field(`${name}-error`)} className={styles.error}>
        {text}
      </p>
    ) : null;
  const timesBad = mode === "times" && timesValue === null;
  const waitBad = mode === "times" && waitValue === null;
  const everyBad = mode === "interval" && everyValue === null;
  const capBad = mode === "interval" && capValue === null;

  return (
    <Dialog
      open
      title={t("editor.repeat.title")}
      onCancel={onCancel}
      onConfirm={start}
      confirmLabel={t("editor.repeat.start")}
      confirmDisabled={options === null}
      describedBy={field("help")}
      width={420}
    >
      <p id={field("help")} className={styles.help}>
        {t("editor.repeat.help")}
      </p>
      <fieldset className={styles.modes}>
        <legend className={styles.legend}>{t("editor.repeat.send")}</legend>
        <label className={styles.mode}>
          <input type="radio" name={field("mode")} checked={mode === "times"} onChange={() => setMode("times")} />
          <span>{t("editor.repeat.times.mode")}</span>
        </label>
        <div className={styles.fields} aria-disabled={mode !== "times"}>
          <label className={styles.row}>
            <span className={styles.label}>{t("editor.repeat.times")}</span>
            <input
              className={styles.input}
              inputMode="numeric"
              value={times}
              disabled={mode !== "times"}
              aria-invalid={timesBad}
              aria-describedby={timesBad ? field("times-error") : undefined}
              onChange={(e) => setTimes(e.target.value)}
            />
          </label>
          {error("times", timesBad, t("editor.repeat.times.invalid", { max: MAX_REPEAT_COUNT }))}
          <label className={styles.row}>
            <span className={styles.label}>{t("editor.repeat.wait")}</span>
            <input
              className={styles.input}
              inputMode="decimal"
              value={wait}
              disabled={mode !== "times"}
              aria-invalid={waitBad}
              aria-describedby={waitBad ? field("wait-error") : undefined}
              onChange={(e) => setWait(e.target.value)}
            />
            <span className={styles.unit}>{t("editor.repeat.seconds")}</span>
          </label>
          {error("wait", waitBad, t("editor.repeat.wait.invalid"))}
        </div>
        <label className={styles.mode}>
          <input type="radio" name={field("mode")} checked={mode === "interval"} onChange={() => setMode("interval")} />
          <span>{t("editor.repeat.interval.mode")}</span>
        </label>
        <div className={styles.fields} aria-disabled={mode !== "interval"}>
          <label className={styles.row}>
            <span className={styles.label}>{t("editor.repeat.every")}</span>
            <input
              className={styles.input}
              inputMode="decimal"
              value={every}
              disabled={mode !== "interval"}
              aria-invalid={everyBad}
              aria-describedby={everyBad ? field("every-error") : undefined}
              onChange={(e) => setEvery(e.target.value)}
            />
            <span className={styles.unit}>{t("editor.repeat.seconds")}</span>
          </label>
          {error("every", everyBad, t("editor.repeat.every.invalid"))}
          <label className={styles.row}>
            <span className={styles.label}>{t("editor.repeat.cap")}</span>
            <input
              className={styles.input}
              inputMode="numeric"
              value={cap}
              placeholder={t("editor.repeat.cap.placeholder")}
              disabled={mode !== "interval"}
              aria-invalid={capBad}
              aria-describedby={capBad ? field("cap-error") : undefined}
              onChange={(e) => setCap(e.target.value)}
            />
            <span className={styles.unit}>{t("editor.repeat.sends")}</span>
          </label>
          {error("cap", capBad, t("editor.repeat.cap.invalid", { max: MAX_REPEAT_COUNT }))}
        </div>
      </fieldset>
    </Dialog>
  );
}
