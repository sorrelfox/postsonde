import { useCallback, useEffect, useRef, useState } from "react";
import type { EditorView } from "@codemirror/view";
import { Trans, useT } from "../../i18n/react";
import { isCancelled, toAppError } from "../../lib/errors";
import { exampleMessage, messageInput, validateMessage, type MessageErrorDTO, type ProtoSetup } from "../../lib/grpc";
import { IconWarning } from "../common/icons";
import { CodeEditor, type CodeEditorHandle } from "./CodeEditor";
import { installErrorMarker, markerRange, setErrorMarker } from "./errorMarker";
import { completionTarget, installGrpcCompletion, sameTarget, setCompletionTarget, type CompletionTarget } from "./grpcCompletion";
import styles from "./GrpcEditor.module.css";

/** How long typing must pause before the message is checked, in ms. */
export const MESSAGE_CHECK_DELAY_MS = 300;

/** The method whose request message is edited: its service, name and input type. */
export interface MessageTarget {
  service: string;
  method: string;
  inputType: string;
}

interface MessageCheck {
  /** The text the result is for. */
  readonly text: string;
  readonly error: MessageErrorDTO | null;
}

const NO_ERROR: MessageCheck = { text: "", error: null };

/**
 * Checks `text` against the input type in Go (GRPCService.ValidateMessage)
 * once typing pauses, as useJsonCheck does for bodies. The last result
 * stays until the next one arrives; a check still running when anything
 * changes is cancelled. No target gives no error; a rejected check (the
 * method is gone) marks nothing.
 */
function useMessageCheck(setup: ProtoSetup | null, target: MessageTarget | null, text: string, output: boolean): MessageCheck {
  const [check, setCheck] = useState<MessageCheck>(NO_ERROR);
  const setupKey = setup === null ? "" : JSON.stringify([setup.files, setup.importPaths]);
  const latest = useRef(setup);
  latest.current = setup;
  const service = target?.service ?? "";
  const method = target?.method ?? "";

  useEffect(() => {
    const current = latest.current;
    if (current === null || service === "" || method === "") {
      setCheck((c) => (c.error === null ? c : NO_ERROR));
      return;
    }
    let live = true;
    let call: ReturnType<typeof validateMessage> | null = null;
    const timer = setTimeout(() => {
      call = validateMessage(messageInput(current, service, method, text, output));
      call.then(
        (result) => {
          if (live) setCheck({ text, error: result.valid ? null : result.error });
        },
        (err: unknown) => {
          if (live && !isCancelled(err)) setCheck({ text, error: null });
        },
      );
    }, MESSAGE_CHECK_DELAY_MS);
    return () => {
      live = false;
      clearTimeout(timer);
      call?.cancel?.();
    };
  }, [setupKey, service, method, text, output]);

  return check;
}

export interface GrpcMessageEditorProps {
  message: string;
  setup: ProtoSetup | null;
  /** The chosen method, null while none is chosen or it is not in the loaded files. */
  target: MessageTarget | null;
  readOnly: boolean;
  onChange: (message: string) => void;
  /**
   * Edits the method's response message instead (the gRPC mock server's
   * canned responses, SSRV-07): checked, completed and generated from its
   * output type; `target.inputType` then names that type.
   */
  output?: boolean;
  /** The editor's accessible name; "Message" by default. */
  ariaLabel?: string;
}

/**
 * The Message sub-tab of the gRPC editor (GRPC-15, GRPC-16, GRPC-17): the
 * request message as JSON in a CodeEditor, checked against the method's
 * input type in Go as typing pauses (the first problem is marked in the
 * text and shown below with its position), with field-name and enum
 * autocomplete from the descriptors (grpcCompletion.ts), and Generate
 * example, which replaces the text with a template of the type.
 */
export function GrpcMessageEditor({
  message,
  setup,
  target,
  readOnly,
  onChange,
  output = false,
  ariaLabel,
}: GrpcMessageEditorProps) {
  const t = useT();
  const editorRef = useRef<CodeEditorHandle>(null);
  const [view, setView] = useState<EditorView | null>(null);
  const onReady = useCallback((v: EditorView) => {
    installErrorMarker(v);
    installGrpcCompletion(v);
    setView(v);
  }, []);

  // Autocomplete (GRPC-17) reads the method from the editor state.
  const setupKey = setup === null ? "" : JSON.stringify([setup.files, setup.importPaths]);
  const latestSetup = useRef(setup);
  latestSetup.current = setup;
  const service = target?.service ?? null;
  const method = target?.method ?? null;
  useEffect(() => {
    if (view === null) return;
    const current = latestSetup.current;
    const next: CompletionTarget | null =
      current === null || service === null || method === null
        ? null
        : { files: current.files, importPaths: current.importPaths, service, method, output };
    if (!sameTarget(completionTarget(view.state), next)) view.dispatch({ effects: setCompletionTarget.of(next) });
  }, [view, setupKey, service, method, output]);

  const check = useMessageCheck(setup, target, message, output);
  useEffect(() => {
    if (view === null) return;
    const error = check.error !== null && check.text === view.state.doc.toString() ? check.error : null;
    view.dispatch({ effects: setErrorMarker.of(error === null ? null : { offset: error.offset, message: error.message }) });
  }, [view, check]);

  const goTo = useCallback((offset: number) => {
    const v = editorRef.current?.view;
    if (!v) return;
    const range = markerRange(v.state.doc, offset);
    v.dispatch({ selection: { anchor: range?.from ?? 0 }, scrollIntoView: true });
    v.focus();
  }, []);

  const [generating, setGenerating] = useState(false);
  const [failure, setFailure] = useState<string | null>(null);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);
  useEffect(() => setFailure(null), [target?.service, target?.method]);

  const generate = async () => {
    if (setup === null || target === null) return;
    setGenerating(true);
    setFailure(null);
    try {
      const example = await exampleMessage(messageInput(setup, target.service, target.method, "", output));
      if (mounted.current) onChange(example.json);
    } catch (err) {
      if (mounted.current && !isCancelled(err)) setFailure(toAppError(err).message);
    } finally {
      if (mounted.current) setGenerating(false);
    }
  };

  const error = target !== null && check.text === message ? check.error : null;
  const blocker =
    target === null
      ? t("editorProtocols.grpcMessage.chooseMethod")
      : readOnly
        ? t("editorProtocols.grpcMessage.readOnly")
        : null;

  return (
    <div className={styles.messagePanel}>
      <div className={styles.messageBar}>
        <p className={styles.messageNote}>
          {target !== null ? (
            <Trans
              k="editorProtocols.grpcMessage.noteType"
              values={{ type: <code title={target.inputType}>{target.inputType}</code> }}
            />
          ) : output ? (
            t("editorProtocols.grpcMessage.noteResponse")
          ) : (
            t("editorProtocols.grpcMessage.noteRequest")
          )}
        </p>
        <button
          type="button"
          className={styles.tool}
          disabled={blocker !== null || generating}
          title={blocker ?? t("editorProtocols.grpcMessage.generateTitle")}
          onClick={() => void generate()}
        >
          {t("editorProtocols.grpcMessage.generate")}
        </button>
      </div>
      <div className={styles.messageFrame}>
        <CodeEditor
          ref={editorRef}
          value={message}
          language="json"
          ariaLabel={ariaLabel ?? t("editorProtocols.grpcMessage.label")}
          readOnly={readOnly}
          onChange={readOnly ? undefined : onChange}
          onReady={onReady}
        />
      </div>
      {(error !== null || failure !== null) && (
        <p className={styles.messageStatus} role="status">
          <IconWarning size={12} />
          {error !== null && (
            <>
              <button type="button" className={styles.position} onClick={() => goTo(error.offset)}>
                {t("editorProtocols.grpcMessage.position", { line: error.line, column: error.column })}
              </button>
              <span>{error.message}</span>
            </>
          )}
          {error === null && <span>{failure}</span>}
        </p>
      )}
    </div>
  );
}
