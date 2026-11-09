import { useEffect, useState } from "react";
import {
  copySnippet,
  DEFAULT_SNIPPET_LANGUAGE,
  fallbackSnippetLanguages,
  loadSnippetLanguages,
  type SnippetLanguageDTO,
  type SnippetMode,
  type SnippetSource,
} from "../../commands/snippets";
import { useSnippet, useSourceSnippet, type SnippetState } from "../../commands/useSnippet";
import type { MessageKey } from "../../i18n";
import { useT } from "../../i18n/react";
import type { RequestId } from "../../stores";
import { IconCopy, IconEye, IconEyeOff, IconWarning } from "../common/icons";
import styles from "./SnippetPanel.module.css";

/**
 * Names of the languages Go writes, by `SnippetLanguageDTO.id`. A language
 * missing here shows Go's name: adding one in Go (snippet.Language) is
 * enough for it to be offered; its entry here makes the name translatable.
 */
const LANGUAGE_NAMES: Readonly<Record<string, MessageKey>> = {
  curl: "editor.snippet.language.curl",
  curlCmd: "editor.snippet.language.curlCmd",
  curlPowerShell: "editor.snippet.language.curlPowerShell",
  httpie: "editor.snippet.language.httpie",
  wget: "editor.snippet.language.wget",
  http: "editor.snippet.language.http",
  fetch: "editor.snippet.language.fetch",
  python: "editor.snippet.language.python",
  go: "editor.snippet.language.go",
  java: "editor.snippet.language.java",
  csharp: "editor.snippet.language.csharp",
  grpcurl: "editor.snippet.language.grpcurl",
  websocat: "editor.snippet.language.websocat",
  wscat: "editor.snippet.language.wscat",
};

const MODES: readonly { id: SnippetMode; labelKey: MessageKey; titleKey: MessageKey }[] = [
  { id: "resolved", labelKey: "editor.snippet.mode.resolved", titleKey: "editor.snippet.mode.resolved.title" },
  { id: "templated", labelKey: "editor.snippet.mode.templated", titleKey: "editor.snippet.mode.templated.title" },
];

export interface SnippetPanelProps {
  id: RequestId;
  /** The request's draft (or, with `source`, the editor's input): the code is asked again when it changes. */
  request: unknown;
  /**
   * Why the request cannot be written as code now (a GraphQL subscription
   * over WebSocket), or null.
   */
  unavailable?: string | null;
  /**
   * Where the code comes from in the gRPC and WebSocket editors (GEN-10,
   * GEN-11): the editor's input, written by its service. Without it, the
   * saved request `id`, written by HTTPService.Snippet.
   */
  source?: SnippetSource;
}

/**
 * The Code sub-tab of the HTTP, SSE, GraphQL, gRPC and WebSocket editors
 * (GEN-10 to GEN-13): the request as code in a language Go writes
 * (docs/snippets.md), **Resolved** (variables filled in as a send fills
 * them) or **Templated** (`{{variables}}` kept as written; not offered for
 * gRPC, which has no variables), and Copy. In resolved mode secrets are
 * masked; **Show secrets** asks Go for the real values for this view
 * only: the choice lives in this component's state, is never saved, and is
 * dropped when the panel closes, the request changes or the mode goes to
 * templated. Templated code never holds a secret. Nothing is sent or
 * fetched: no token request, no `{{$file}}` read, no `{{$prompt}}` dialog
 * (commands/snippets.ts, docs/snippets.md).
 */
export function SnippetPanel({ id, request, unavailable = null, source }: SnippetPanelProps) {
  const [languages, setLanguages] = useState<readonly SnippetLanguageDTO[]>(source?.fallback ?? fallbackSnippetLanguages);
  const [language, setLanguage] = useState(source?.defaultLanguage ?? DEFAULT_SNIPPET_LANGUAGE);
  const [mode, setMode] = useState<SnippetMode>("resolved");
  const [reveal, setReveal] = useState(false);
  const loadLanguages = source?.languages ?? loadSnippetLanguages;

  useEffect(() => {
    let live = true;
    void loadLanguages().then((list) => {
      if (live) setLanguages(list);
    });
    return () => {
      live = false;
    };
  }, [loadLanguages]);

  if (unavailable !== null) {
    return (
      <div className={styles.panel}>
        <p className={styles.note} role="note">
          <IconWarning size={13} />
          {unavailable}
        </p>
      </div>
    );
  }
  const modes = source?.modes ?? true;
  const view: ViewProps = {
    languages,
    language,
    mode: modes ? mode : "resolved",
    modes,
    reveal,
    onLanguage: setLanguage,
    onMode: (next) => {
      setMode(next);
      // A reveal is for the resolved view it was asked in.
      setReveal(false);
    },
    onReveal: setReveal,
  };
  if (source !== undefined) return <SourceCode source={source} request={request} view={view} />;
  return <HttpCode id={id} request={request} view={view} />;
}

/** The panel's choices, and what changes them. */
interface ViewProps {
  languages: readonly SnippetLanguageDTO[];
  language: string;
  mode: SnippetMode;
  /** Whether Resolved / Templated is offered. */
  modes: boolean;
  reveal: boolean;
  onLanguage: (id: string) => void;
  onMode: (mode: SnippetMode) => void;
  onReveal: (reveal: boolean) => void;
}

/** The code of saved request `id` (HTTP, SSE, GraphQL). */
function HttpCode({ id, request, view }: { id: RequestId; request: unknown; view: ViewProps }) {
  const state = useSnippet(id, request, { language: view.language, mode: view.mode, reveal: view.reveal });
  return <SnippetView state={state} view={view} />;
}

/** The code of a gRPC or WebSocket editor's input. */
function SourceCode({ source, request, view }: { source: SnippetSource; request: unknown; view: ViewProps }) {
  const state = useSourceSnippet(source, request, { language: view.language, mode: view.mode, reveal: view.reveal });
  return <SnippetView state={state} view={view} />;
}

function SnippetView({ state, view }: { state: SnippetState; view: ViewProps }) {
  const t = useT();
  const { languages, language, mode, modes, reveal, onLanguage, onMode, onReveal } = view;
  const { snippet, options, error, loading } = state;
  const languageName = (l: SnippetLanguageDTO) => {
    const key = LANGUAGE_NAMES[l.id];
    return key !== undefined ? t(key) : l.name;
  };
  const selected = languages.find((l) => l.id === language);
  const shownName = selected !== undefined ? languageName(selected) : language;
  // Only the answer for the options shown is shown: after Hide secrets,
  // never the revealed code while the masked one is on its way.
  const current =
    snippet !== null && options !== null && options.language === language && options.mode === mode && options.reveal === reveal
      ? snippet
      : null;
  const revealed = mode === "resolved" && reveal;
  const canReveal = mode === "resolved" && (revealed || current?.masked === true);

  let status = "";
  if (current !== null) {
    if (mode === "templated") {
      status = current.masked ? t("editor.snippet.status.templatedMasked") : t("editor.snippet.status.templated");
    } else if (current.revealed) {
      status = t("editor.snippet.status.revealed");
    } else if (current.masked) {
      status = t("editor.snippet.status.masked");
    } else {
      status = modes ? t("editor.snippet.status.resolved") : t("editor.snippet.status.asWritten");
    }
  }
  const unresolved = current?.unresolved ?? [];

  return (
    <div className={styles.panel} data-snippet-mode={mode}>
      <div className={styles.toolbar}>
        <label className={styles.field}>
          <span className={styles.fieldLabel}>{t("editor.snippet.language")}</span>
          <select className={styles.select} value={language} onChange={(e) => onLanguage(e.target.value)}>
            {languages.map((l) => (
              <option key={l.id} value={l.id}>
                {languageName(l)}
              </option>
            ))}
          </select>
        </label>
        {modes && (
          <div className={styles.segmented} role="group" aria-label={t("editor.snippet.mode")}>
            {MODES.map((m) => (
              <button
                key={m.id}
                type="button"
                className={styles.segment}
                aria-pressed={mode === m.id}
                title={t(m.titleKey)}
                onClick={() => onMode(m.id)}
              >
                {t(m.labelKey)}
              </button>
            ))}
          </div>
        )}
        {mode === "resolved" && (
          <button
            type="button"
            className={styles.action}
            aria-pressed={revealed}
            disabled={!canReveal}
            title={revealed ? t("editor.snippet.hideSecrets.title") : t("editor.snippet.showSecrets.title")}
            onClick={() => onReveal(!revealed)}
          >
            {revealed ? <IconEyeOff size={13} /> : <IconEye size={13} />}
            {t("editor.snippet.showSecrets")}
          </button>
        )}
        <button
          type="button"
          className={styles.action}
          disabled={current === null || loading}
          onClick={() => {
            if (current !== null) void copySnippet(current);
          }}
        >
          <IconCopy size={13} />
          {t("editor.snippet.copy")}
        </button>
      </div>
      {error !== "" ? (
        <p className={styles.error} role="alert">
          <IconWarning size={13} />
          {t("editor.snippet.failed", { error })}
        </p>
      ) : (
        <>
          <p className={styles.status} role="status" data-revealed={current?.revealed === true}>
            <span>{status}</span>
            {unresolved.length > 0 && (
              <span>
                {t("editor.snippet.unresolved", {
                  count: unresolved.length,
                  list: unresolved.map((n) => `{{${n}}}`).join(", "),
                })}
              </span>
            )}
          </p>
          <pre
            className={styles.code}
            tabIndex={0}
            aria-label={t("editor.snippet.code", { language: shownName })}
            aria-busy={loading || current === null}
          >
            {current?.code ?? ""}
          </pre>
        </>
      )}
    </div>
  );
}
