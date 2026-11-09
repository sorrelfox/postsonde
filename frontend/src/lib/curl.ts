// cURL import helpers (IMP-01): recognising a pasted curl command, and the
// draft patch that replaces an http request with the one Go mapped from it
// (CollectionService.ParseCurl, docs/curl.md). Pure functions.

import type { CurlDiagnosticDTO, RequestDTO } from "./collection";

/**
 * A curl command line as pasted into the URL bar: optional shell prompt
 * (`$ `, `% `, `> `, `PS C:\…> `), the program (`curl` or `curl.exe`, bare or
 * with an absolute, `./` or `~/` path, not a URL), then an argument. Go's parser has the full rules; this
 * only decides whether to hand the text to it.
 */
const CURL_START = /^\s*(?:[$%>]\s+|PS\s[^\n>]*>\s*)?(?:(?:[A-Za-z]:|\.{1,2}|~)?[\\/](?:\S*[\\/])?)?curl(?:\.exe)?\s+\S/i;

/** Whether pasted text is a curl command (and not a URL or anything else). */
export function looksLikeCurl(text: string): boolean {
  return CURL_START.test(text.replace(/^\uFEFF/, ""));
}

/**
 * The draft patch that replaces `current` with `imported`: method, URL,
 * query and path rows, headers, body, auth and the http settings. The name,
 * description and members this version does not know are kept, as are the
 * settings of other kinds.
 */
export function importPatch(current: RequestDTO, imported: RequestDTO): Record<string, unknown> {
  const settings = {
    ...current.settings,
    timeoutMs: imported.settings.timeoutMs,
    followRedirects: imported.settings.followRedirects,
    maxRedirects: imported.settings.maxRedirects,
    followOriginalMethod: imported.settings.followOriginalMethod,
    forwardAuthorization: imported.settings.forwardAuthorization,
    verifyTls: imported.settings.verifyTls,
    // --http1.1 and --http2-prior-knowledge (HTTP-25); URL encoding and
    // the cookie switches have no cURL option and are kept.
    httpVersion: imported.settings.httpVersion,
  };
  return {
    method: imported.method,
    url: imported.url,
    params: imported.params,
    pathParams: imported.pathParams,
    headers: imported.headers,
    body: imported.body,
    auth: imported.auth,
    settings,
  };
}

/** The first error of a parse, or null. */
export function firstError(diagnostics: readonly CurlDiagnosticDTO[] | null): CurlDiagnosticDTO | null {
  return (diagnostics ?? []).find((d) => d.severity === "error") ?? null;
}

/** The warnings of a parse (what was not imported or may differ), in order. */
export function warningsOf(diagnostics: readonly CurlDiagnosticDTO[] | null): CurlDiagnosticDTO[] {
  return (diagnostics ?? []).filter((d) => d.severity === "warning");
}

/** "1 warning", "3 warnings". */
export function countLabel(n: number, noun: string): string {
  return `${n} ${noun}${n === 1 ? "" : "s"}`;
}
