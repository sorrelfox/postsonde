import { createContext, useContext, useEffect, useMemo, useRef, useState, type ReactNode } from "react";
import { waitForQueuedEdits } from "../../commands/autosave";
import type { CancellablePromise } from "../../lib/calls";
import { onEnvironmentsChanged } from "../../lib/environments";
import { onRuntimeVariablesChanged } from "../../lib/runtimeVariables";
import { isCancelled, toAppError } from "../../lib/errors";
import {
  describeReferences,
  isResolved,
  loadRequestVariables,
  splitTemplate,
  toVariableSet,
  type TemplateVariableSet,
  type VariablesDTO,
} from "../../lib/templateVariables";
import type { ReferencedRequest } from "../../lib/templateResponse";
import { useCollection, useStores } from "../../stores";
import styles from "./TemplateVariables.module.css";

// The resolved {{variables}} of the request being edited (ENV-10, ENV-11),
// shared with every field of its editor: CodeEditor draws them as chips
// (editor/templateVariables.ts), KeyValueTable draws its value cells with
// TemplateText. The provider loads them from Go (HTTPService.Variables) and
// again after every environments:changed and runtimevars:changed event
// (runtime variables, ENV-15) and when the request moves
// to another folder, and, for a folder's editor (COL-03), after its own
// variables change (`reloadKey`). Outside a provider nothing is marked
// unresolved.

export interface TemplateVariablesValue {
  /** null until the first load (or when it failed): references are not marked unresolved then. */
  readonly variables: TemplateVariableSet | null;
}

export const TemplateVariablesContext = createContext<TemplateVariablesValue | null>(null);

/** The resolved variables around this component; null outside a TemplateVariablesProvider. */
export function useTemplateVariables(): TemplateVariablesValue | null {
  return useContext(TemplateVariablesContext);
}

/** How long after a change of `reloadKey` the variables are loaded again. */
const RELOAD_DELAY_MS = 300;

/** Loads request (or folder) `requestId`'s variables and keeps them current. */
function useRequestVariables(requestId: string, reloadKey: unknown): TemplateVariableSet | null {
  const stores = useStores();
  const [variables, setVariables] = useState<TemplateVariableSet | null>(null);
  // Folder variables depend on where the request is.
  const parentId = useCollection((s) => s.nodes[requestId]?.parentId ?? null);
  const lastKey = useRef(reloadKey);

  useEffect(() => {
    let alive = true;
    let call: CancellablePromise<VariablesDTO> | null = null;
    let timer: ReturnType<typeof setTimeout> | null = null;
    const load = () => {
      call?.cancel();
      try {
        call = loadRequestVariables(requestId);
      } catch {
        return; // the service is not bound (tests without it)
      }
      call.then(
        (dto) => {
          if (alive) setVariables(toVariableSet(dto));
        },
        (err: unknown) => {
          if (!alive || isCancelled(err)) return;
          // Keep what was shown: the chips stay as they were.
          console.warn("variables: could not load the request's variables", toAppError(err));
        },
      );
    };
    if (lastKey.current === reloadKey) {
      load();
    } else {
      lastKey.current = reloadKey;
      // An edit of the folder's own variables: once typing pauses and the
      // edit reached Go, which resolves with the pending edit.
      timer = setTimeout(() => {
        void waitForQueuedEdits(stores, requestId).then(() => {
          if (alive) load();
        });
      }, RELOAD_DELAY_MS);
    }
    const off = onEnvironmentsChanged(load);
    const offRuntime = onRuntimeVariablesChanged(load);
    return () => {
      alive = false;
      if (timer !== null) clearTimeout(timer);
      call?.cancel();
      off();
      offRuntime();
    };
  }, [stores, requestId, parentId, reloadKey]);

  return variables;
}

export interface TemplateVariablesProviderProps {
  /** The request, or the folder (its own variables included), whose variables are shown. */
  requestId: string;
  /** A change loads the variables again, shortly after: a folder editor passes its variables. */
  reloadKey?: unknown;
  children: ReactNode;
}

/**
 * The project's HTTP and GraphQL requests, which {{$response}} references
 * read (DYN-06): the hover and the picker show them by name.
 */
function useReferencedRequests(): ReadonlyMap<string, ReferencedRequest> {
  const nodes = useCollection((s) => s.nodes);
  return useMemo(() => {
    const out = new Map<string, ReferencedRequest>();
    for (const node of Object.values(nodes)) {
      if (node.kind !== "request" || (node.fileKind !== "http" && node.fileKind !== "graphql")) continue;
      out.set(node.id, { id: node.id, name: node.name, detail: node.path === "" ? node.method : `${node.method} ${node.path}` });
    }
    return out;
  }, [nodes]);
}

export function TemplateVariablesProvider({ requestId, reloadKey, children }: TemplateVariablesProviderProps) {
  const loaded = useRequestVariables(requestId, reloadKey);
  const requests = useReferencedRequests();
  const variables = useMemo(() => (loaded === null ? null : { ...loaded, requestId, requests }), [loaded, requestId, requests]);
  const value = useMemo(() => ({ variables }), [variables]);
  return <TemplateVariablesContext.Provider value={value}>{children}</TemplateVariablesContext.Provider>;
}

/**
 * `text` with its {{references}} as chips, red when they do not resolve
 * with `variables`: for fields that are not CodeMirror editors (the value
 * cells of KeyValueTable). Plain text without references.
 */
export function TemplateText({ text, variables }: { text: string; variables: TemplateVariableSet | null }) {
  return (
    <>
      {splitTemplate(text).map((piece, i) =>
        piece.kind === "text" ? (
          piece.text
        ) : (
          <span
            key={i}
            className={
              variables !== null && !isResolved(variables, piece.span.name)
                ? `${styles.chip} ${styles.unresolved}`
                : styles.chip
            }
            data-unresolved={(variables !== null && !isResolved(variables, piece.span.name)) || undefined}
          >
            {piece.text}
          </span>
        ),
      )}
    </>
  );
}

/** A title for a field showing `text`: each reference's value and scope, or why it does not resolve. */
export function templateTitle(text: string, variables: TemplateVariableSet | null): string | undefined {
  const title = describeReferences(text, variables);
  return title === "" ? undefined : title;
}
