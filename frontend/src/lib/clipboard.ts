// Writing text to the system clipboard. Through the Wails runtime first:
// it goes through Go, so it works after an await, when the webview no
// longer counts the write as part of the user's click or key press. The
// browser's clipboard API is the fallback (a browser preview, tests).
import { Clipboard } from "@wailsio/runtime";

/** Puts `text` on the clipboard. Rejects when neither the runtime nor the browser can. */
export async function copyText(text: string): Promise<void> {
  try {
    await Clipboard.SetText(text);
    return;
  } catch (err: unknown) {
    if (typeof navigator === "undefined" || !navigator.clipboard) throw err;
  }
  await navigator.clipboard.writeText(text);
}
