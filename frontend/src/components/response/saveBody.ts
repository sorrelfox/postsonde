// Save body to file (HTTP-34) and save the whole exchange (EXP-06): the
// state behind the response view's save buttons. Go keeps the bytes
// received and the request as sent (HTTPService.SaveBody, SaveExchange);
// the view only asks it to show the save dialog and reports what happened.
import { useCallback, useState } from "react";
import { t } from "../../i18n";
import { toAppError } from "../../lib/errors";
import { saveResponseBody, saveResponseExchange, type ResponseDTO } from "../../lib/http";
import { formatSize } from "../messagelog/format";

export interface SaveNote {
  tone: "ok" | "error";
  text: string;
  hint: string;
}

export interface SaveBodyState {
  /** Whether this response's body (and its exchange) can be saved. */
  canSave: boolean;
  /** A save is in progress (the dialog is open or the file is being written). */
  busy: boolean;
  /** What the last save of this response did; null before one, or after a cancel. */
  note: SaveNote | null;
  /** Saves the body as received. */
  save: () => void;
  /** Saves the request as sent and the whole response as a HAR file, masked; `name` names the file. */
  saveExchange: (name: string) => void;
}

/** The save state of `response`; a new response starts afresh. */
export function useSaveBody(response: ResponseDTO): SaveBodyState {
  const [state, setState] = useState<{ for: ResponseDTO; busy: boolean; note: SaveNote | null }>({
    for: response,
    busy: false,
    note: null,
  });
  const current = state.for === response ? state : { for: response, busy: false, note: null };

  const run = useCallback(
    (action: () => Promise<{ saved: boolean; size: number; path: string; masked?: boolean }>) => {
      if (response.bodyId === "") return;
      setState({ for: response, busy: true, note: null });
      action().then(
        (res) => {
          let note: SaveNote | null = null;
          if (res.saved) {
            const values = { size: formatSize(res.size), path: res.path };
            const text = res.masked === true ? t("response.save.savedMasked", values) : t("response.save.saved", values);
            note = { tone: "ok", text, hint: "" };
          }
          setState((s) => (s.for === response ? { for: response, busy: false, note } : s));
        },
        (reason: unknown) => {
          const err = toAppError(reason);
          const note: SaveNote = { tone: "error", text: err.message, hint: err.hint };
          setState((s) => (s.for === response ? { for: response, busy: false, note } : s));
        },
      );
    },
    [response],
  );

  const save = useCallback(() => run(() => saveResponseBody(response.bodyId)), [run, response]);
  const saveExchange = useCallback(
    (name: string) => run(() => saveResponseExchange(response.bodyId, name)),
    [run, response],
  );

  return { canSave: response.bodyId !== "", busy: current.busy, note: current.note, save, saveExchange };
}
