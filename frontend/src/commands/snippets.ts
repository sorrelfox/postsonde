// The snippet panel (GEN-12, GEN-13): a request as code, resolved or
// templated, secrets masked unless the user reveals them in the panel. Go
// builds and writes the code (HTTPService.Snippet; for the gRPC and
// WebSocket editors GRPCService.Snippet and WebSocketService.Snippet, from
// the editor's input: SnippetSource); the UI asks for it, shows it and
// copies it. Nothing here is saved: the language, the mode
// and a reveal live in the panel while it is shown. See docs/snippets.md.
import { t } from "../i18n";
import { copyText } from "../lib/clipboard";
import { toAppError } from "../lib/errors";
import { snippet, snippetLanguages, type SnippetDTO, type SnippetLanguageDTO, type SnippetMode } from "../lib/http";
import type { CancellablePromise } from "../lib/calls";
import { grpcSnippet, grpcSnippetLanguages, type InvokeInputDTO } from "../lib/grpc";
import { wsSnippet, wsSnippetLanguages, type ConnectInputDTO } from "../lib/websocket";
import type { RequestId, Stores } from "../stores";
import { waitForQueuedEdits } from "./autosave";
import { showToast } from "./toasts";

export type { SnippetDTO, SnippetLanguageDTO, SnippetMode };

/** The language the panel opens with: the POSIX shell curl command (Go's snippet.LanguageCurl). */
export const DEFAULT_SNIPPET_LANGUAGE = "curl";

/** What the panel lists before Go answers, and if it cannot. */
const FALLBACK_LANGUAGES: readonly SnippetLanguageDTO[] = [{ id: DEFAULT_SNIPPET_LANGUAGE, name: "cURL", syntax: "shell" }];

let languages: Promise<readonly SnippetLanguageDTO[]> | null = null;

/** The languages Go writes (asked once per session), in the order the panel lists them. Never rejects. */
export function loadSnippetLanguages(): Promise<readonly SnippetLanguageDTO[]> {
  languages ??= snippetLanguages().then(
    (list) => (list.length > 0 ? list : FALLBACK_LANGUAGES),
    () => FALLBACK_LANGUAGES,
  );
  return languages;
}

/** The languages known before Go answers. */
export function fallbackSnippetLanguages(): readonly SnippetLanguageDTO[] {
  return FALLBACK_LANGUAGES;
}

/** Forgets the languages asked for (tests). */
export function resetSnippetLanguages(): void {
  languages = null;
  grpcLanguages = null;
  wsLanguages = null;
}

export interface SnippetOptions {
  language: string;
  mode: SnippetMode;
  /** Resolved mode only: the real values of the secrets, for this call. */
  reveal: boolean;
}

/**
 * Request `id` as code: the editor's queued edits reach Go first, so the
 * code matches the screen. Resolves with null when `isStale()` says the
 * answer is no longer wanted before the call starts; `onCall` gets the
 * call so the caller can cancel it.
 */
export async function requestSnippet(
  stores: Stores,
  id: RequestId,
  options: SnippetOptions,
  isStale: () => boolean,
  onCall: (call: CancellablePromise<SnippetDTO>) => void,
): Promise<SnippetDTO | null> {
  await waitForQueuedEdits(stores, id);
  if (isStale()) return null;
  const call = snippet(id, options.language, options.mode, options.mode === "resolved" && options.reveal);
  onCall(call);
  return call;
}

/**
 * Where the panel of a gRPC or WebSocket editor gets its code (GEN-10,
 * GEN-11): the editor's own input, written by its service, instead of the
 * saved request HTTPService.Snippet builds.
 */
export interface SnippetSource {
  /** The languages to offer (asked once per session per kind). Never rejects. */
  languages: () => Promise<readonly SnippetLanguageDTO[]>;
  /** What the panel lists before they arrive. */
  fallback: readonly SnippetLanguageDTO[];
  /** The language the panel opens with. */
  defaultLanguage: string;
  /** Whether Resolved / Templated applies (a gRPC call has no variables). */
  modes: boolean;
  /** The code for `options`. */
  write: (options: SnippetOptions) => CancellablePromise<SnippetDTO>;
}

const GRPC_FALLBACK: readonly SnippetLanguageDTO[] = [{ id: "grpcurl", name: "grpcurl", syntax: "shell" }];
const WS_FALLBACK: readonly SnippetLanguageDTO[] = [
  { id: "websocat", name: "websocat", syntax: "shell" },
  { id: "wscat", name: "wscat", syntax: "shell" },
];
let grpcLanguages: Promise<readonly SnippetLanguageDTO[]> | null = null;
let wsLanguages: Promise<readonly SnippetLanguageDTO[]> | null = null;

function orFallback(
  list: Promise<readonly SnippetLanguageDTO[]>,
  fallback: readonly SnippetLanguageDTO[],
): Promise<readonly SnippetLanguageDTO[]> {
  return list.then(
    (l) => (l.length > 0 ? l : fallback),
    () => fallback,
  );
}

/** The gRPC editor's source: `call` as Invoke would take it, written by GRPCService.Snippet. */
export function grpcSnippetSource(call: InvokeInputDTO): SnippetSource {
  return {
    languages: () => (grpcLanguages ??= orFallback(grpcSnippetLanguages(), GRPC_FALLBACK)),
    fallback: GRPC_FALLBACK,
    defaultLanguage: "grpcurl",
    modes: false,
    write: ({ language, reveal }) => grpcSnippet(call, language, reveal),
  };
}

/** The WebSocket editor's source: `connect` as Connect would take it, written by WebSocketService.Snippet. */
export function wsSnippetSource(connect: ConnectInputDTO): SnippetSource {
  return {
    languages: () => (wsLanguages ??= orFallback(wsSnippetLanguages(), WS_FALLBACK)),
    fallback: WS_FALLBACK,
    defaultLanguage: "websocat",
    modes: true,
    write: ({ language, mode, reveal }) => wsSnippet(connect, language, mode, mode === "resolved" && reveal),
  };
}

/**
 * Copies the code the panel shows and says so in a toast: whether secrets
 * were masked or shown. A failure shows in the toast. Never rejects.
 */
export async function copySnippet(dto: SnippetDTO): Promise<void> {
  try {
    await copyText(dto.code);
    let message = t("commands.snippet.copied");
    if (dto.masked) message = t("commands.snippet.copiedMasked");
    if (dto.revealed) message = t("commands.snippet.copiedRevealed");
    showToast({ message });
  } catch (err: unknown) {
    showToast({ message: t("commands.snippet.copyFailed", { error: toAppError(err).message }) });
  }
}
