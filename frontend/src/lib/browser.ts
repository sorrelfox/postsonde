// Opening a URL in the system's default browser, outside the app (links in
// notes, COL-07). Through the Wails runtime, which hands the URL to the OS;
// the app's webview never navigates to it and the CSP is not involved.
// Only absolute http, https and mailto URLs are passed on (linkTarget in
// lib/markdown.ts); anything else is refused here too.
import { Browser } from "@wailsio/runtime";
import { linkTarget } from "./markdown";

/** Opens `url` in the system browser. Rejects for a URL that is not http(s) or mailto, or when the OS refuses. */
export async function openInBrowser(url: string): Promise<void> {
  const target = linkTarget(url);
  if (target.kind !== "external") throw new Error(`Only http, https and mailto links can be opened: ${url}`);
  await Browser.OpenURL(target.url);
}
