import { useEffect, useState } from "react";
import { isCancelled } from "../lib/errors";
import { validateJson, type JSONErrorDTO } from "../lib/format";

/** How long typing must pause before the JSON is checked, in ms. */
export const JSON_CHECK_DELAY_MS = 300;

export interface JsonCheck {
  /** The text the result is for. */
  readonly text: string;
  /** Where the JSON is invalid, or null when it is valid (or blank, or not checked). */
  readonly error: JSONErrorDTO | null;
}

const NO_ERROR: JsonCheck = { text: "", error: null };

/**
 * Checks `text` as JSON in Go (FormatService.ValidateJSON) once typing
 * pauses for `delayMs` (HTTP-12). Go is the only validator, so the
 * marker, beautify and minify always agree.
 *
 * The last result stays until the next one arrives, so the marker does not
 * blink while typing; the editor maps its position through the edits. A
 * check still running when the text changes is cancelled and its result
 * dropped. Blank text and `enabled` false give no error.
 */
export function useJsonCheck(text: string, enabled: boolean, delayMs: number = JSON_CHECK_DELAY_MS): JsonCheck {
  const [check, setCheck] = useState<JsonCheck>(NO_ERROR);

  useEffect(() => {
    if (!enabled || text.trim() === "") {
      setCheck((c) => (c.error === null ? c : NO_ERROR));
      return;
    }
    let call: ReturnType<typeof validateJson> | null = null;
    let live = true;
    const timer = setTimeout(() => {
      call = validateJson(text);
      call.then(
        (result) => {
          if (live) setCheck({ text, error: result.valid ? null : result.error });
        },
        (err: unknown) => {
          // A failed check marks nothing; the next edit tries again.
          if (live && !isCancelled(err)) setCheck({ text, error: null });
        },
      );
    }, delayMs);
    return () => {
      live = false;
      clearTimeout(timer);
      if (call !== null) void call.cancel();
    };
  }, [text, enabled, delayMs]);

  return check;
}
