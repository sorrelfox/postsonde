// Calls into the Go HTTPService (internal/app/services/http): sending an
// HTTP request (HTTP-20) with its timeout (HTTP-21). A send runs as an http
// operation (docs/events.md): the stage arrives as progress, the response
// as one data item, then the done event. See docs/http-client.md.
import {
  HTTPService,
  type BodyPageDTO,
  type BodyPreviewDTO,
  type CertificateDTO,
  type ConnInfoDTO,
  type CookieDTO,
  type CurlDTO,
  type HeaderDTO,
  type HeaderPreviewDTO,
  type PreviewHeaderDTO,
  type HopTimingDTO,
  type IncrementalDTO,
  type IncrementalEntryDTO,
  type IncrementalPartDTO,
  type PhaseDTO,
  type RepeatInputDTO,
  type RepeatResultDTO,
  type ResponseDTO,
  type SaveBodyResultDTO,
  type SaveExchangeResultDTO,
  type SendInputDTO,
  type SentRequestDTO,
  type SnippetDTO,
  type SnippetLanguageDTO,
  type TLSInfoDTO,
  type TimelineDTO,
  type TimelineEventDTO,
  type TimelineHopDTO,
  type WarningDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/http";
import { t, type MessageKey } from "../i18n";
import type { CancellablePromise } from "./calls";
import type { StartedDTO } from "./operations";
import { formatMinSec, formatUnit } from "./units";

export type {
  BodyPageDTO,
  BodyPreviewDTO,
  CertificateDTO,
  ConnInfoDTO,
  CookieDTO,
  CurlDTO,
  HeaderDTO,
  HeaderPreviewDTO,
  PreviewHeaderDTO,
  HopTimingDTO,
  IncrementalDTO,
  IncrementalEntryDTO,
  IncrementalPartDTO,
  PhaseDTO,
  RepeatInputDTO,
  RepeatResultDTO,
  ResponseDTO,
  SaveBodyResultDTO,
  SaveExchangeResultDTO,
  SendInputDTO,
  SentRequestDTO,
  SnippetDTO,
  SnippetLanguageDTO,
  TLSInfoDTO,
  TimelineDTO,
  TimelineEventDTO,
  TimelineHopDTO,
  WarningDTO,
};

/** The domain of send operations: their events are `http:op:data|progress|done`. */
export const HTTP_DOMAIN = "http";

/** The domain of repeat runs (HTTP-27): their events are `http.repeat:op:data|progress|done`. */
export const REPEAT_DOMAIN = "http.repeat";

/** The progress message of a run between two sends (Go's StageNextSend). */
export const REPEAT_WAIT_STAGE = "Waiting for the next send"; // i18n-ignore: Go's stage value, compared, not shown

// i18n-ignore-start: Go's stage values (StageConnecting…), matched to their messages
const STAGES: Readonly<Record<string, MessageKey>> = {
  Connecting: "lib.http.stage.connecting",
  "Sending the request": "lib.http.stage.sending",
  "Waiting for the response": "lib.http.stage.waiting",
  "Receiving the response": "lib.http.stage.receiving",
  "Getting an OAuth 2.0 token": "lib.http.stage.token",
  "Sending a referenced request first": "lib.http.stage.sendingFirst",
  [REPEAT_WAIT_STAGE]: "lib.http.stage.nextSend",
};
// i18n-ignore-end

/** A stage of Go's progress ("Waiting for the response") in the language in use; Go's text when it is not known. */
export function httpStageText(stage: string): string {
  const key = STAGES[stage];
  return key === undefined ? stage : t(key);
}

/** The most sends a run with a count makes (Go's MaxRepeatCount). */
export const MAX_REPEAT_COUNT = 100_000;

/** The longest interval of a run, a day (Go's MaxRepeatIntervalMs). */
export const MAX_REPEAT_INTERVAL_MS = 24 * 60 * 60 * 1000;

/** The code of the error Send rejects with when {{variables}} are undefined; details.names lists them. */
export const UNRESOLVED_CODE = "template.unresolved";

/**
 * The code of the error Send and Repeat reject with when the request has
 * {{$prompt}} values without an answer (DYN-07); details.prompts lists them
 * (promptsOf).
 */
export const PROMPT_CODE = "template.prompt";

/** A value to ask for before sending: Go's template.Prompt. */
export interface PromptField {
  /** Names the value; references with the same label share one answer. */
  readonly label: string;
  /** Fills the field when the dialog opens. */
  readonly default: string;
  /** The field is masked (and the value is masked in the timeline and the history). */
  readonly secret: boolean;
}

/** The prompts of a PROMPT_CODE error's details, in the order Go met them; [] when there are none. */
export function promptsOf(details: Record<string, string>): PromptField[] {
  let raw: unknown;
  try {
    raw = JSON.parse(details.prompts ?? "[]");
  } catch {
    return [];
  }
  if (!Array.isArray(raw)) return [];
  const out: PromptField[] = [];
  for (const p of raw as unknown[]) {
    if (typeof p !== "object" || p === null) continue;
    const { label, default: def, secret } = p as Record<string, unknown>;
    if (typeof label !== "string" || label === "") continue;
    out.push({ label, default: typeof def === "string" ? def : "", secret: secret === true });
  }
  return out;
}

/**
 * The user's answers to what a send cannot resolve alone: "Send anyway"
 * for undefined variables (ENV-11), and the values typed for {{$prompt}}
 * references by label (DYN-07), used by that send (or run) only.
 */
export interface SendAnswers {
  readonly allowUnresolved?: boolean;
  readonly prompts?: Readonly<Record<string, string>>;
}

/**
 * Starts sending request `requestId` as Go has it (the edits sent so far
 * included). Resolves with the operation ID at once; a request that
 * cannot be built (an undefined variable, a missing path parameter, …)
 * rejects instead. Undefined variables of every field reject together
 * (UNRESOLVED_CODE) unless `answers.allowUnresolved`, which sends them as
 * written (ENV-11); {{$prompt}} values without an answer in
 * `answers.prompts` reject together first (PROMPT_CODE).
 */
export function sendHttpRequest(requestId: string, answers: SendAnswers = {}): CancellablePromise<StartedDTO> {
  const input: SendInputDTO = { requestId };
  if (answers.allowUnresolved) input.allowUnresolved = true;
  if (answers.prompts !== undefined) input.prompts = { ...answers.prompts };
  return HTTPService.Send(input);
}

/**
 * Starts a run of request `requestId` (HTTP-27): `count` sends (0: until
 * stopped) with `intervalMs` from the start of one to the start of the
 * next (0: back to back). Resolves with the operation ID at once; each
 * send's outcome arrives as a RepeatResultDTO item of REPEAT_DOMAIN, and
 * cancelling the operation stops the run. Rejects as sendHttpRequest
 * does, and with `http.repeat_invalid` for a count or interval out of
 * range.
 */
export function repeatHttpRequest(
  requestId: string,
  count: number,
  intervalMs: number,
  answers: SendAnswers = {},
): CancellablePromise<StartedDTO> {
  const input: RepeatInputDTO = { requestId, count, intervalMs };
  if (answers.allowUnresolved) input.allowUnresolved = true;
  // Asked once, before the run: every send of it uses them.
  if (answers.prompts !== undefined) input.prompts = { ...answers.prompts };
  return HTTPService.Repeat(input);
}

/**
 * Request `requestId` as Go has it, written as a curl command for a POSIX
 * shell (GEN-01, EXP-01): variables, auth, folder headers, settings and
 * the cookie jar applied, as a send would. Secrets are masked (`masked`
 * set) unless `includeSecrets` (PRV-09). Undefined variables stay as
 * written and are listed in `unresolved`. Nothing is sent.
 */
export function curlCommand(requestId: string, includeSecrets = false): CancellablePromise<CurlDTO> {
  return HTTPService.CurlCommand({ requestId, includeSecrets });
}

/** How the snippet panel writes a request (GEN-12): variables filled in, or kept as `{{placeholders}}`. */
export type SnippetMode = "resolved" | "templated";

/**
 * Request `requestId` (as Go has it) as code in `language` (a
 * `SnippetLanguageDTO.id`), for the snippet panel (GEN-12, GEN-13). Resolved
 * is built as a send builds it, secrets masked unless `revealSecrets`;
 * templated keeps every `{{reference}}` and never holds a secret. Nothing is
 * sent, fetched or asked (no token request, no `{{$file}}` read, no
 * `{{$prompt}}`), and nothing is kept: a reveal lasts for this call.
 */
export function snippet(
  requestId: string,
  language: string,
  mode: SnippetMode,
  revealSecrets: boolean,
): CancellablePromise<SnippetDTO> {
  return HTTPService.Snippet({ requestId, language, mode, revealSecrets });
}

/** The languages of the snippet panel, in the order it lists them. */
export async function snippetLanguages(): Promise<SnippetLanguageDTO[]> {
  return (await HTTPService.SnippetLanguages()) ?? [];
}

/**
 * The header fields a send of request `requestId` (as Go has it) would
 * write, in HTTP/1.1 order, each with where it comes from, plus the
 * automatic headers turned off or overridden (HTTP-09). Built by the send's
 * own code, secrets masked; nothing is sent. Undefined variables stay as
 * written (`unresolved`).
 */
export function headerPreview(requestId: string): CancellablePromise<HeaderPreviewDTO> {
  return HTTPService.HeaderPreview({ requestId });
}

/**
 * Saves the bytes of a response body (`response.bodyId`) to a file the
 * user picks in the native save dialog (HTTP-34). Resolves with
 * `saved: false` when they cancel; rejects with `http.body_gone` once Go
 * no longer keeps the body.
 */
export function saveResponseBody(bodyId: string): CancellablePromise<SaveBodyResultDTO> {
  return HTTPService.SaveBody({ bodyId });
}

/**
 * Saves the whole exchange of a response (`response.bodyId`, EXP-06): the
 * request as sent and the full response, masked, as a HAR file the user
 * picks in the native save dialog. `name` (the request's) names the file.
 * Resolves with `saved: false` when they cancel; rejects with
 * `http.body_gone` once Go no longer keeps the response.
 */
export function saveResponseExchange(bodyId: string, name: string): CancellablePromise<SaveExchangeResultDTO> {
  return HTTPService.SaveExchange({ bodyId, name });
}

/**
 * A page of a kept response body (HTTP-37) from byte `offset`: at most
 * 1 MiB of text decoded with `charset`, or with `hex` 64 KiB as a hex
 * dump. Pages never split a character; the next one starts at `end`.
 * Rejects with `http.body_gone` once Go no longer keeps the body.
 */
export function readResponseBody(bodyId: string, offset: number, charset: string, hex: boolean): CancellablePromise<BodyPageDTO> {
  return HTTPService.ReadBody({ bodyId, offset, charset, hex });
}

/**
 * The bytes of a kept response body (`response.bodyId`) for the image
 * preview (HTTP-35), Base64. Rejects with `http.preview_too_large` for a
 * body over 16 MiB and `http.body_gone` once Go no longer keeps it.
 */
export function previewResponseBody(bodyId: string): CancellablePromise<BodyPreviewDTO> {
  return HTTPService.PreviewBody({ bodyId });
}

/** "30 s", "1.5 s", "250 ms", "2 min": a timeout for labels. */
export function formatTimeoutMs(ms: number): string {
  if (ms < 1000) return formatUnit("ms", ms);
  if (ms < 60_000 || ms % 1000 !== 0) return formatUnit("s", ms / 1000);
  const s = ms / 1000;
  const parts: string[] = [];
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const sec = s % 60;
  if (h > 0) parts.push(formatUnit("h", h));
  if (m > 0) parts.push(formatUnit("min", m));
  if (sec > 0) parts.push(formatUnit("s", sec));
  return parts.reduce((list, item) => t("lib.units.join", { list, item }));
}

/** "120 ms", "1.24 s", "2 min 5 s": how long a send took. */
export function formatDurationMs(ms: number): string {
  if (ms < 1000) return formatUnit("ms", ms);
  if (ms < 60_000) return formatUnit("s", ms / 1000, 2);
  const total = Math.round(ms / 1000);
  return formatMinSec(Math.floor(total / 60), total % 60);
}

/** The colour group of a status code: "ok" 2xx, "redirect" 3xx, "client" 4xx, "server" 5xx, else "other". */
export function statusTone(code: number): "ok" | "redirect" | "client" | "server" | "other" {
  if (code >= 200 && code < 300) return "ok";
  if (code >= 300 && code < 400) return "redirect";
  if (code >= 400 && code < 500) return "client";
  if (code >= 500 && code < 600) return "server";
  return "other";
}
