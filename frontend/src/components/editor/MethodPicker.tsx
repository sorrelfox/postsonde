import { useEffect, useId, useRef, useState, type ChangeEvent, type KeyboardEvent } from "react";
import { t as translate } from "../../i18n";
import { useT } from "../../i18n/react";
import { IconChevron, IconWarning } from "../common/icons";
import { methodBadge } from "../tree/badges";
import styles from "./RequestEditor.module.css";

/** The methods the picker offers (HTTP-01, HTTP-03 for QUERY), in the mockup's order. */
export const EDITOR_METHODS: readonly string[] = ["GET", "POST", "PUT", "PATCH", "DELETE", "QUERY", "HEAD", "OPTIONS"];

/** Methods whose other spellings get the case warning: Go's request.CanonicalMethod. */
const REGISTERED_METHODS: readonly string[] = [...EDITOR_METHODS, "TRACE", "CONNECT"];

/** The select's value of the "Custom…" entry; not a method token, so no method can collide with it. */
export const CUSTOM_OPTION = "custom…"; // i18n-ignore: an option value, never shown

const TOKEN = /^[A-Za-z0-9!#$%&'*+\-.^_`|~]+$/;

/**
 * Why `method` cannot be sent, or null when it can: the rule of Go's
 * request.Method (any RFC 9110 token except CONNECT).
 */
export function methodProblem(method: string): string | null {
  if (method === "") return translate("editorProtocols.method.empty");
  if (!TOKEN.test(method)) return translate("editorProtocols.method.invalid", { chars: "!#$%&'*+-.^_`|~" });
  if (method === "CONNECT") return translate("editorProtocols.method.connect");
  return null;
}

/** The registered method `method` is another spelling of ("GET" for "get"), or "". Mirrors request.CanonicalMethod. */
export function canonicalMethod(method: string): string {
  return REGISTERED_METHODS.find((m) => m !== method && m.toLowerCase() === method.toLowerCase()) ?? "";
}

function caseWarning(method: string): string | null {
  const canonical = canonicalMethod(method);
  return canonical === ""
    ? null
    : translate("editorProtocols.method.caseWarning", { method, canonical });
}

export interface MethodPickerProps {
  method: string;
  onChange?: (method: string) => void;
  disabled?: boolean;
}

/**
 * The method picker of the request editor (HTTP-01, HTTP-02): a native
 * select styled as the mockup's method label, in the method's colour. It
 * offers the standard methods and "Custom…", which turns it into a text
 * field for any method token. A method the file has that is not offered
 * (a custom one, a hand edit) is shown as it is and stays in the list while
 * the editor is open, so it is never changed unless the user picks another.
 * Methods are sent as typed; another spelling of a registered method (`get`)
 * shows a warning.
 */
export function MethodPicker({ method, onChange, disabled = false }: MethodPickerProps) {
  const t = useT();
  // The methods the editor showed that are not offered: keep them choosable.
  const [extras, setExtras] = useState<readonly string[]>(() => (EDITOR_METHODS.includes(method) ? [] : [method]));
  const [editing, setEditing] = useState(false);
  const [text, setText] = useState("");
  const selectRef = useRef<HTMLSelectElement>(null);
  const inputRef = useRef<HTMLInputElement>(null);
  const focusSelect = useRef(false);
  // False once finish ran, so the blur of the field being removed does not save twice.
  const editingRef = useRef(false);
  const baseId = useId();

  if (!EDITOR_METHODS.includes(method) && !extras.includes(method)) setExtras([...extras, method]);

  useEffect(() => {
    if (editing) {
      inputRef.current?.focus();
      inputRef.current?.select();
    } else if (focusSelect.current) {
      focusSelect.current = false;
      selectRef.current?.focus();
    }
  }, [editing]);

  const options = [...EDITOR_METHODS, ...extras];
  const shown = editing ? text : method;
  const badge = methodBadge(shown);
  const problem = editing && text !== "" ? methodProblem(text) : null;
  const warning = caseWarning(shown);
  const hintId = `${baseId}-hint`;
  const hint = problem ?? warning;

  const onSelect = (e: ChangeEvent<HTMLSelectElement>) => {
    const value = e.target.value;
    if (value === CUSTOM_OPTION) {
      setText(EDITOR_METHODS.includes(method) ? "" : method);
      editingRef.current = true;
      setEditing(true);
      return;
    }
    onChange?.(value);
  };

  /** Leaves the text field: saves a valid method, drops anything else. */
  const finish = (save: boolean, refocus: boolean) => {
    if (!editingRef.current) return;
    editingRef.current = false;
    if (save && methodProblem(text) === null && text !== method) onChange?.(text);
    focusSelect.current = refocus;
    setEditing(false);
  };

  const onInputKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Enter" && !e.metaKey && !e.ctrlKey) {
      e.preventDefault();
      if (methodProblem(text) === null) finish(true, true);
    } else if (e.key === "Escape") {
      e.preventDefault();
      e.stopPropagation();
      finish(false, true);
    }
  };

  return (
    <label
      className={styles.method}
      data-tone={badge.tone}
      data-disabled={disabled || undefined}
      data-invalid={problem !== null || undefined}
    >
      {hint !== null && (
        <span className={styles.methodHint} title={hint}>
          <IconWarning size={12} />
          <span id={hintId} className={styles.srOnly}>
            {hint}
          </span>
        </span>
      )}
      {editing ? (
        <input
          ref={inputRef}
          className={styles.methodInput}
          aria-label={t("editorProtocols.method.customLabel")}
          aria-invalid={problem !== null}
          aria-describedby={hint !== null ? hintId : undefined}
          title={hint ?? t("editorProtocols.method.customTitle")}
          value={text}
          size={Math.max(text.length, 7)}
          maxLength={64}
          spellCheck={false}
          autoComplete="off"
          autoCapitalize="off"
          autoCorrect="off"
          placeholder="METHOD"
          onChange={(e) => setText(e.target.value)}
          onKeyDown={onInputKeyDown}
          onBlur={() => finish(true, false)}
        />
      ) : (
        <>
          <select
            ref={selectRef}
            className={styles.methodSelect}
            aria-label={t("editorProtocols.method.label")}
            aria-describedby={warning !== null ? hintId : undefined}
            value={method}
            disabled={disabled}
            onChange={onSelect}
          >
            {options.map((m) => (
              <option key={m} value={m}>
                {m === "" ? t("editorProtocols.method.none") : m}
              </option>
            ))}
            {!disabled && <option value={CUSTOM_OPTION}>{t("editorProtocols.method.custom")}</option>}
          </select>
          <IconChevron size={11} className={styles.methodChevron} />
        </>
      )}
    </label>
  );
}
