import { useCallback, useEffect, useId, useMemo, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { grpcSnippetSource } from "../../commands/snippets";
import { useAutoSave } from "../../commands/useAutoSave";
import { useStreamTests } from "../../commands/streamTests";
import { countAssertions } from "../../lib/assertions";
import { useGrpcCall } from "../../commands/useGrpcCall";
import { useRequestDraft } from "../../commands/useRequestDraft";
import { type MessageKey } from "../../i18n";
import { Trans, useT } from "../../i18n/react";
import { onCollectionChanged, type KeyValueDTO, type RequestDTO } from "../../lib/collection";
import type { AppError } from "../../lib/errors";
import { isCancelled, toAppError } from "../../lib/errors";
import {
  canInvoke,
  describeProto,
  diagnosticLocation,
  exampleMessage,
  filterServices,
  findMethod,
  getInheritedProto,
  grpcCompression,
  grpcTransport,
  invokeInput,
  messageInput,
  methodKindLabel,
  pickImportPath,
  pickProtoFiles,
  sameJson,
  setupKey,
  streamsFromClient,
  transportAllows,
  updateFolderProto,
  type DescribeResultDTO,
  type GrpcTransport,
  type DiagnosticDTO,
  type InheritedGRPCProtoDTO,
  type MethodDTO,
  type InvokeInputDTO,
  type ProtoSetup,
  type ServiceDTO,
} from "../../lib/grpc";
import { useCollection, useDrafts, useStores, type RequestData, type RequestId } from "../../stores";
import { EmptyState } from "../common/EmptyState";
import { IconChevron, IconClose, IconFile, IconFolder, IconInfo, IconLock, IconPlay, IconPlus, IconSearch, IconWarning } from "../common/icons";
import { KeyValueTable } from "../common/KeyValueTable";
import { FileAccessNotice, useFileAccessCheck } from "../common/FileAccessNotice";
import { GrpcStreamControls } from "../response/GrpcStreamControls";
import { AssertionsEditor } from "./AssertionsEditor";
import { AuthEditor } from "./AuthEditor";
import { GrpcMessageEditor } from "./GrpcMessageEditor";
import styles from "./GrpcEditor.module.css";
import { useKeyValueRows } from "./keyValueRows";
import { NotesPanel } from "./NotesPanel";
import { SnippetPanel } from "./SnippetPanel";

const READ_ONLY: Readonly<Record<string, MessageKey>> = {
  newer_version: "editorProtocols.readOnly.newerVersion",
  symlink: "editorProtocols.readOnly.symlink",
};

/** Why request `id` cannot be edited, or null when it can. */
function useReadOnlyNotice(id: RequestId): string | null {
  const t = useT();
  const itemReason = useCollection((s) => s.nodes[id]?.readOnly ?? "");
  const projectReason = useCollection((s) => s.readOnly);
  if (itemReason !== "") return t(READ_ONLY[itemReason] ?? "editorProtocols.readOnly.request");
  if (projectReason !== "") return t("editorProtocols.readOnly.project");
  return null;
}

export type GrpcSubTab = "proto" | "message" | "metadata" | "auth" | "tests" | "settings" | "notes" | "code";

const SUB_TABS: readonly { id: GrpcSubTab; labelKey: MessageKey }[] = [
  { id: "proto", labelKey: "editorProtocols.tab.proto" },
  { id: "message", labelKey: "editorProtocols.tab.message" },
  { id: "metadata", labelKey: "editorProtocols.tab.metadata" },
  { id: "auth", labelKey: "editorProtocols.tab.auth" },
  // Assertions on the response messages and the status (TEST-08); the results are in the response's Tests tab.
  { id: "tests", labelKey: "editorProtocols.tab.tests" },
  { id: "settings", labelKey: "editorProtocols.tab.settings" },
  { id: "notes", labelKey: "editorProtocols.tab.notes" },
  // The call as a grpcurl command (GEN-10, SnippetPanel).
  { id: "code", labelKey: "editorProtocols.tab.code" },
];

function isSubTab(value: string | undefined): value is GrpcSubTab {
  return SUB_TABS.some((t) => t.id === value);
}

/** How long the setup must stay unchanged before it is described again. */
export const DESCRIBE_DELAY_MS = 150;

function errorText(err: AppError): string {
  return err.hint ? `${err.message} ${err.hint}` : err.message;
}

/** The last part of a dotted name ("shop.v1.Orders" → "Orders"). */
function shortName(fullName: string): string {
  const at = fullName.lastIndexOf(".");
  return at < 0 ? fullName : fullName.slice(at + 1);
}

function ownSetup(request: RequestDTO): ProtoSetup {
  return { files: request.proto.files ?? [], importPaths: request.proto.importPaths ?? [] };
}

type InheritedState = { status: "loading" } | { status: "ready"; value: InheritedGRPCProtoDTO } | { status: "error"; error: AppError };

/** The setup request `id` would inherit, loaded again after every tree change. */
function useInheritedProto(id: RequestId): [InheritedState, () => void] {
  const [state, setState] = useState<InheritedState>({ status: "loading" });
  const [tick, setTick] = useState(0);
  const reload = useCallback(() => setTick((n) => n + 1), []);
  useEffect(() => onCollectionChanged(() => reload()), [reload]);
  useEffect(() => {
    let live = true;
    const call = getInheritedProto(id);
    call.then(
      (value) => {
        if (live) setState({ status: "ready", value });
      },
      (err: unknown) => {
        if (live && !isCancelled(err)) setState({ status: "error", error: toAppError(err) });
      },
    );
    return () => {
      live = false;
      call.cancel?.();
    };
  }, [id, tick]);
  return [state, reload];
}

type DescribeState =
  | { status: "empty" }
  | { status: "loading"; previous: DescribeResultDTO | null }
  | { status: "ready"; result: DescribeResultDTO }
  | { status: "error"; error: AppError };

/** Describes `setup` in Go, again whenever it changes (debounced) or `reload` is called. Null waits. */
function useDescribe(setup: ProtoSetup | null): [DescribeState, () => void] {
  const [state, setState] = useState<DescribeState>({ status: "empty" });
  const [reloadTick, setReloadTick] = useState(0);
  const key = setup === null ? null : setupKey(setup);
  const latest = useRef(setup);
  latest.current = setup;
  const reloadRequested = useRef(false);
  const reload = useCallback(() => {
    reloadRequested.current = true;
    setReloadTick((n) => n + 1);
  }, []);

  useEffect(() => {
    const current = latest.current;
    if (key === null || current === null) return;
    if (current.files.every((f) => f.trim() === "")) {
      setState({ status: "empty" });
      return;
    }
    setState((s) => ({ status: "loading", previous: s.status === "ready" ? s.result : s.status === "loading" ? s.previous : null }));
    let live = true;
    let call: ReturnType<typeof describeProto> | null = null;
    const timer = setTimeout(() => {
      const reloadNow = reloadRequested.current;
      reloadRequested.current = false;
      call = describeProto(current, reloadNow);
      call.then(
        (result) => {
          if (live) setState({ status: "ready", result });
        },
        (err: unknown) => {
          if (live && !isCancelled(err)) setState({ status: "error", error: toAppError(err) });
        },
      );
    }, DESCRIBE_DELAY_MS);
    return () => {
      live = false;
      clearTimeout(timer);
      call?.cancel?.();
    };
  }, [key, reloadTick]);

  return [state, reload];
}

export interface GrpcEditorProps {
  id: RequestId;
}

/**
 * The editor of a gRPC request (GRPC-01, GRPC-02, GRPC-10): the server
 * address with TLS, the service and method picker built from the request's
 * .proto setup (its own files and import paths, or its folder's), the
 * setup with its diagnostics, and the message, metadata and settings.
 * Invoke makes unary and server-streaming calls (GRPC-11, GRPC-12); the
 * latest call belongs to the request's tab (commands/grpcCalls.ts) and is
 * shown in the response pane (components/response/GrpcResponsePanel).
 * Everything but the call is auto-saved.
 */
export function GrpcEditor({ id }: GrpcEditorProps) {
  const t = useT();
  const draft = useRequestDraft(id);
  if (draft.status === "loading") {
    return <div className={styles.editor} aria-busy="true" />;
  }
  if (draft.status === "error") {
    return (
      <div className={styles.editor}>
        <EmptyState icon={<IconWarning size={20} />} title={t("editorProtocols.openFailed")}>
          {draft.error.message}{" "}
          <button type="button" className={styles.linkButton} onClick={draft.retry}>
            {t("editorProtocols.tryAgain")}
          </button>
        </EmptyState>
      </div>
    );
  }
  return <LoadedGrpcEditor id={id} request={draft.request} />;
}

function LoadedGrpcEditor({ id, request }: { id: RequestId; request: RequestDTO }) {
  const t = useT();
  const stores = useStores();
  const { edit } = useAutoSave(id);
  const streamTests = useStreamTests(id);
  const notice = useReadOnlyNotice(id);
  const readOnly = notice !== null;
  const baseId = useId();

  const inherit = request.proto.inherit;
  const [inherited, reloadInherited] = useInheritedProto(id);
  const own = ownSetup(request);
  const effective: ProtoSetup | null = !inherit
    ? own
    : inherited.status === "ready"
      ? { files: inherited.value.files ?? [], importPaths: inherited.value.importPaths ?? [] }
      : null;
  const [described, reloadDescribe] = useDescribe(effective);

  const stored = useDrafts((s) => s.subTabs[id]);
  const hasSetup = inherit || own.files.length > 0;
  const subTab: GrpcSubTab = isSubTab(stored) ? stored : hasSetup ? "message" : "proto";
  const selectSubTab = (next: GrpcSubTab) => stores.drafts.dispatch({ type: "drafts/setSubTab", id, subTab: next });
  const tabRefs = useRef<Partial<Record<GrpcSubTab, HTMLButtonElement | null>>>({});

  const onMetadata = useCallback((metadata: KeyValueDTO[]) => edit({ metadata }), [edit]);
  const [metadataRows, setMetadataRows] = useKeyValueRows(request.metadata, onMetadata);

  const settings = request.settings;
  const tls = settings.tls === true;
  const transport = grpcTransport(request);
  const web = transport !== "grpc";

  const result = described.status === "ready" ? described.result : described.status === "loading" ? described.previous : null;
  const services = result?.services ?? [];
  const errors = result?.errors ?? 0;

  const { call, state: callState } = useGrpcCall(id);
  const running = callState.status === "starting" || callState.status === "running";
  const chosen = request.service !== "" && request.method !== "" ? findMethod(services, request.service, request.method) : null;
  let invokeBlocker: string | null = null;
  if (request.address.trim() === "") invokeBlocker = t("editorProtocols.grpc.blocker.address");
  else if (effective === null) invokeBlocker = t("editorProtocols.grpc.blocker.loading");
  else if (chosen === null) invokeBlocker = t("editorProtocols.grpc.blocker.method");
  else if (!canInvoke(chosen.method.kind))
    invokeBlocker = t("editorProtocols.grpc.blocker.kind", { kind: methodKindLabel(chosen.method.kind) });
  else if (!transportAllows(transport, chosen.method.kind))
    invokeBlocker = t("editorProtocols.grpc.blocker.web", { kind: methodKindLabel(chosen.method.kind) });

  const streaming = chosen !== null && streamsFromClient(chosen.method.kind);
  const currentMessage = () => ((stores.drafts.getState().byId[id]?.request as unknown as RequestDTO | undefined) ?? request).message;

  const invoke = () => {
    if (invokeBlocker !== null || effective === null || call.running) return;
    const current = (stores.drafts.getState().byId[id]?.request as unknown as RequestDTO | undefined) ?? request;
    call.invoke(invokeInput(current, effective), { kind: chosen?.method.kind ?? "" });
  };

  const onMessage = useCallback((message: string) => edit({ message }), [edit]);
  const mounted = useRef(true);
  useEffect(() => {
    mounted.current = true;
    return () => {
      mounted.current = false;
    };
  }, []);

  // Choosing a method fills in its example message while the message is
  // untouched: blank, {}, or the example of the method chosen before
  // (GRPC-16). Edits made while the example loads win.
  const pickMethod = (service: string, method: string) => {
    const current = (stores.drafts.getState().byId[id]?.request as unknown as RequestDTO | undefined) ?? request;
    const before = { service: current.service, method: current.method };
    const text = current.message;
    edit({ service, method });
    if (effective === null || readOnly || (before.service === service && before.method === method)) return;
    const blank = text.trim() === "" || text.trim() === "{}";
    if (!blank && (before.service === "" || before.method === "")) return;
    const setup = effective;
    void (async () => {
      try {
        const [example, previous] = await Promise.all([
          exampleMessage(messageInput(setup, service, method)),
          blank ? Promise.resolve(null) : exampleMessage(messageInput(setup, before.service, before.method)).catch(() => null),
        ]);
        if (!mounted.current) return;
        if (!blank && (previous === null || !sameJson(text, previous.json))) return;
        const now = (stores.drafts.getState().byId[id]?.request as unknown as RequestDTO | undefined) ?? request;
        if (now.message !== text || now.service !== service || now.method !== method) return;
        edit({ message: example.json });
      } catch {
        // No example (the method has problems): the message stays as it is.
      }
    })();
  };

  const counts: Partial<Record<GrpcSubTab, number>> = {
    proto: (effective?.files ?? []).filter((f) => f.trim() !== "").length,
    metadata: (request.metadata ?? []).filter((r) => r.enabled && r.name !== "").length,
    tests: countAssertions(request.assertions),
  };

  const onTabKeyDown = (e: KeyboardEvent<HTMLButtonElement>) => {
    const at = SUB_TABS.findIndex((tab) => tab.id === subTab);
    let next: number;
    if (e.key === "ArrowRight") next = (at + 1) % SUB_TABS.length;
    else if (e.key === "ArrowLeft") next = (at - 1 + SUB_TABS.length) % SUB_TABS.length;
    else if (e.key === "Home") next = 0;
    else if (e.key === "End") next = SUB_TABS.length - 1;
    else return;
    e.preventDefault();
    const target = SUB_TABS[next];
    if (target === undefined) return;
    selectSubTab(target.id);
    tabRefs.current[target.id]?.focus();
  };

  return (
    <div className={styles.editor} data-request-id={id}>
      {notice !== null && (
        <p className={styles.notice} role="note">
          <IconLock size={13} />
          {notice}
        </p>
      )}

      <div className={styles.bar}>
        <div className={styles.addressGroup}>
          <span className={styles.protocol} aria-hidden="true">
            {web ? "gRPC-Web" : "gRPC"}
          </span>
          <input
            className={styles.address}
            aria-label={t("editorProtocols.grpc.address")}
            placeholder={t("editorProtocols.grpc.addressPlaceholder")}
            spellCheck={false}
            autoComplete="off"
            value={request.address}
            readOnly={readOnly}
            onChange={(e) => edit({ address: e.target.value })}
            onKeyDown={(e) => {
              if (e.key === "Enter" && !e.metaKey && !e.ctrlKey && !e.shiftKey && !e.altKey) invoke();
            }}
          />
          <button
            type="button"
            className={styles.tls}
            aria-pressed={tls}
            disabled={readOnly}
            title={
              tls
                ? t("editorProtocols.grpc.tlsOn")
                : web
                  ? t("editorProtocols.grpc.tlsOffWeb")
                  : t("editorProtocols.grpc.tlsOff")
            }
            onClick={() => edit({ settings: { ...settings, tls: tls ? null : true } })}
          >
            <IconLock size={12} />
            TLS
          </button>
        </div>
        {running ? (
          <>
            <GrpcStreamControls
              call={call}
              state={callState}
              getMessage={currentMessage}
              sendClassName={styles.invoke}
              endClassName={styles.cancel}
            />
            <button type="button" className={styles.cancel} onClick={() => call.cancel()}>
              {t("editorProtocols.cancel")}
            </button>
          </>
        ) : (
          <button
            type="button"
            className={styles.invoke}
            disabled={invokeBlocker !== null}
            title={invokeBlocker ?? (streaming ? t("editorProtocols.grpc.startStreamTitle") : undefined)}
            onClick={invoke}
          >
            <IconPlay size={11} />
            {streaming ? t("editorProtocols.grpc.startStream") : t("editorProtocols.grpc.invoke")}
          </button>
        )}
      </div>
      {callState.sendError !== null && (
        <p className={styles.streamError} role="alert">
          <IconWarning size={13} />
          {callState.sendError.message}
        </p>
      )}

      <MethodPicker
        services={services}
        service={request.service}
        method={request.method}
        described={described}
        readOnly={readOnly}
        onSelect={pickMethod}
        onShowProto={() => selectSubTab("proto")}
      />

      <div className={styles.subTabs} role="tablist" aria-label={t("editorProtocols.tab.requestOptions")}>
        {SUB_TABS.map((tab) => {
          const selected = tab.id === subTab;
          const count = counts[tab.id] ?? 0;
          return (
            <button
              key={tab.id}
              ref={(el) => {
                tabRefs.current[tab.id] = el;
              }}
              type="button"
              role="tab"
              id={`${baseId}-tab-${tab.id}`}
              className={styles.subTab}
              aria-selected={selected}
              aria-controls={selected ? `${baseId}-panel` : undefined}
              tabIndex={selected ? 0 : -1}
              onClick={() => selectSubTab(tab.id)}
              onKeyDown={onTabKeyDown}
            >
              {t(tab.labelKey)}
              {count > 0 && <span className={styles.count}>{count}</span>}
              {tab.id === "proto" && errors > 0 && (
                <span className={styles.errorCount} aria-label={t("editorProtocols.grpc.errorCount", { count: errors })}>
                  {errors}
                </span>
              )}
            </button>
          );
        })}
      </div>

      <div className={styles.section} role="tabpanel" id={`${baseId}-panel`} aria-labelledby={`${baseId}-tab-${subTab}`}>
        {subTab === "proto" && (
          <ProtoPanel
            request={request}
            readOnly={readOnly}
            edit={edit}
            inherited={inherited}
            reloadInherited={reloadInherited}
            described={described}
            reload={reloadDescribe}
          />
        )}
        {subTab === "message" && streaming && (
          <p className={styles.note} data-stream-note="">
            {chosen?.method.kind === "bidi" ? t("editorProtocols.grpc.bidiNote") : t("editorProtocols.grpc.clientStreamNote")}
          </p>
        )}
        {subTab === "message" && (
          <GrpcMessageEditor
            message={request.message}
            setup={effective}
            target={
              chosen === null ? null : { service: chosen.service.fullName, method: chosen.method.name, inputType: chosen.method.inputType }
            }
            readOnly={readOnly}
            onChange={onMessage}
          />
        )}
        {subTab === "metadata" && (
          <>
            <p className={styles.note}>
              <Trans k="editorProtocols.grpc.metadataNote" values={{ suffix: <code>-bin</code> }} />
            </p>
            <KeyValueTable
              label={t("editorProtocols.tab.metadata")}
              bulkEdit
              rows={metadataRows}
              onChange={readOnly ? undefined : setMetadataRows}
              readOnly={readOnly}
              placeholders={{ key: "key" }}
            />
          </>
        )}
        {subTab === "auth" && <AuthEditor id={id} auth={request.auth} readOnly={readOnly} protocol="grpc" />}
        {subTab === "tests" && (
          <AssertionsEditor id={id} assertions={request.assertions} readOnly={readOnly} edit={edit} stream results={streamTests.tests} />
        )}
        {subTab === "settings" && <GrpcSettings request={request} readOnly={readOnly} edit={edit} />}
        {subTab === "notes" && (
          <NotesPanel
            value={request.description ?? ""}
            subject="request"
            readOnly={readOnly}
            onChange={(description) => edit({ description })}
          />
        )}
        {subTab === "code" && <GrpcCode id={id} request={request} setup={effective} />}
      </div>

    </div>
  );
}

interface PickerOption {
  id: string;
  service: ServiceDTO;
  method: MethodDTO;
}

/**
 * The service and method picker (GRPC-10): a button showing the chosen
 * method, opening a filterable list grouped by service with a badge for
 * each method's call type.
 */
function MethodPicker({
  services,
  service,
  method,
  described,
  readOnly,
  onSelect,
  onShowProto,
}: {
  services: readonly ServiceDTO[];
  service: string;
  method: string;
  described: DescribeState;
  readOnly: boolean;
  onSelect: (service: string, method: string) => void;
  onShowProto: () => void;
}) {
  const t = useT();
  const baseId = useId();
  const listId = `${baseId}-list`;
  const [open, setOpen] = useState(false);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const rootRef = useRef<HTMLDivElement>(null);
  const buttonRef = useRef<HTMLButtonElement>(null);
  const filterRef = useRef<HTMLInputElement>(null);

  const groups = useMemo(() => filterServices(services, query), [services, query]);
  const options = useMemo(() => {
    const out: PickerOption[] = [];
    groups.forEach((s, si) =>
      (s.methods ?? []).forEach((m, mi) => out.push({ id: `${baseId}-opt-${si}-${mi}`, service: s, method: m })),
    );
    return out;
  }, [groups, baseId]);

  const selected = service !== "" && method !== "" ? findMethod(services, service, method) : null;

  useEffect(() => {
    if (!open) return;
    filterRef.current?.focus();
    const onDown = (e: MouseEvent) => {
      if (rootRef.current !== null && !rootRef.current.contains(e.target as Node)) setOpen(false);
    };
    document.addEventListener("mousedown", onDown);
    return () => document.removeEventListener("mousedown", onDown);
  }, [open]);

  const openList = () => {
    setQuery("");
    // The index among all methods, since the filter is cleared.
    const all = services.flatMap((s) => (s.methods ?? []).map((m) => [s.fullName, m.name]));
    const at = all.findIndex(([sn, mn]) => sn === service && mn === method);
    setActive(at < 0 ? 0 : at);
    setOpen(true);
  };

  const choose = (o: PickerOption) => {
    onSelect(o.service.fullName, o.method.name);
    setOpen(false);
    buttonRef.current?.focus();
  };

  const onFilterKeyDown = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "ArrowDown") {
      e.preventDefault();
      setActive((a) => Math.min(a + 1, options.length - 1));
    } else if (e.key === "ArrowUp") {
      e.preventDefault();
      setActive((a) => Math.max(a - 1, 0));
    } else if (e.key === "Enter") {
      e.preventDefault();
      const o = options[active];
      if (o !== undefined) choose(o);
    } else if (e.key === "Escape") {
      e.preventDefault();
      setOpen(false);
      buttonRef.current?.focus();
    }
  };

  useEffect(() => {
    if (!open) return;
    const id = options[active]?.id;
    if (id !== undefined) document.getElementById(id)?.scrollIntoView?.({ block: "nearest" });
  }, [open, active, options]);

  const loading = described.status === "loading";
  const missing = service !== "" && method !== "" && selected === null && described.status === "ready";
  let hint: string | null = null;
  if (described.status === "empty") hint = t("editorProtocols.grpc.picker.noFiles");
  else if (described.status === "error") hint = errorText(described.error);
  else if (missing) hint = t("editorProtocols.grpc.picker.missing", { service, method });
  else if (described.status === "ready" && services.length === 0) hint = t("editorProtocols.grpc.picker.noServices");

  let optionIndex = -1;

  return (
    <div className={styles.methodArea} ref={rootRef}>
      <div className={styles.methodRow}>
        <span className={styles.methodLabel} id={`${baseId}-label`}>
          {t("editorProtocols.grpc.picker.label")}
        </span>
        <button
          ref={buttonRef}
          type="button"
          className={styles.methodButton}
          aria-haspopup="listbox"
          aria-expanded={open}
          aria-labelledby={`${baseId}-label ${baseId}-value`}
          disabled={readOnly}
          onClick={() => (open ? setOpen(false) : openList())}
        >
          <span className={styles.methodValue} id={`${baseId}-value`}>
            {service === "" || method === "" ? (
              <span className={styles.placeholder}>{t("editorProtocols.grpc.picker.placeholder")}</span>
            ) : (
              <>
                <span className={styles.methodService}>{service}</span>
                <span className={styles.methodSlash}>/</span>
                <span className={styles.methodName} data-deprecated={selected?.method.deprecated || undefined}>
                  {method}
                </span>
              </>
            )}
          </span>
          {selected !== null && <KindBadge kind={selected.method.kind} />}
          <IconChevron size={12} direction={open ? "up" : "down"} />
        </button>
        {loading && <span className={styles.loading}>{t("editorProtocols.grpc.loadingFiles")}</span>}
      </div>

      {selected !== null && (
        <p className={styles.methodSummary}>
          <span className={styles.typeName} title={selected.method.inputType}>
            {shortName(selected.method.inputType)}
          </span>
          <span aria-hidden="true">→</span>
          <span className={styles.typeName} title={selected.method.outputType}>
            {shortName(selected.method.outputType)}
          </span>
          <span className={styles.srOnly}>
            {t("editorProtocols.grpc.picker.types", { input: selected.method.inputType, output: selected.method.outputType })}
          </span>
          {selected.method.deprecated && <span className={styles.deprecatedTag}>{t("editorProtocols.grpc.deprecated")}</span>}
          {selected.method.comment !== "" && <span className={styles.summaryComment}>{firstLine(selected.method.comment)}</span>}
        </p>
      )}
      {selected === null && hint !== null && (
        <p className={styles.methodHint} data-tone={missing || described.status === "error" ? "bad" : undefined}>
          {missing || described.status === "error" ? <IconWarning size={12} /> : <IconInfo size={12} />}
          <span>{hint}</span>
          {described.status !== "loading" && (
            <button type="button" className={styles.linkButton} onClick={onShowProto}>
              {t("editorProtocols.grpc.picker.protoSetup")}
            </button>
          )}
        </p>
      )}

      {open && (
        <div className={styles.pickerPanel}>
          <div className={styles.filter}>
            <IconSearch size={12} />
            <input
              ref={filterRef}
              className={styles.filterInput}
              role="combobox"
              aria-label={t("editorProtocols.grpc.picker.filterLabel")}
              aria-expanded="true"
              aria-controls={listId}
              aria-activedescendant={options[active]?.id}
              aria-autocomplete="list"
              placeholder={t("editorProtocols.grpc.picker.filterPlaceholder")}
              spellCheck={false}
              autoComplete="off"
              value={query}
              onChange={(e) => {
                setQuery(e.target.value);
                setActive(0);
              }}
              onKeyDown={onFilterKeyDown}
            />
          </div>
          <div className={styles.pickerList} role="listbox" id={listId} aria-label={t("editorProtocols.grpc.picker.methods")}>
            {groups.length === 0 && (
              <p className={styles.pickerEmpty}>
                {services.length === 0
                  ? loading
                    ? t("editorProtocols.grpc.picker.loading")
                    : t("editorProtocols.grpc.picker.noServicesLoaded")
                  : t("editorProtocols.grpc.picker.noMatch")}
              </p>
            )}
            {groups.map((s) => (
              <div key={s.fullName} role="group" aria-label={s.fullName} className={styles.serviceGroup}>
                <div className={styles.serviceHeading} aria-hidden="true">
                  <span className={styles.serviceName} data-deprecated={s.deprecated || undefined}>
                    {s.fullName}
                  </span>
                  <span className={styles.serviceFile}>{s.file}</span>
                  {s.comment !== "" && <span className={styles.serviceComment}>{firstLine(s.comment)}</span>}
                </div>
                {(s.methods ?? []).map((m) => {
                  optionIndex++;
                  const index = optionIndex;
                  const o = options[index];
                  const isSelected = s.fullName === service && m.name === method;
                  return (
                    <div
                      key={m.name}
                      id={o?.id}
                      role="option"
                      aria-selected={isSelected}
                      data-active={index === active || undefined}
                      className={styles.option}
                      onMouseDown={(e) => e.preventDefault()}
                      onMouseEnter={() => setActive(index)}
                      onClick={() => o !== undefined && choose(o)}
                    >
                      <span className={styles.optionName} data-deprecated={m.deprecated || undefined}>
                        {m.name}
                      </span>
                      <KindBadge kind={m.kind} />
                      {m.deprecated && <span className={styles.deprecatedTag}>{t("editorProtocols.grpc.deprecated")}</span>}
                      <span className={styles.optionTypes}>
                        {shortName(m.inputType)} → {shortName(m.outputType)}
                      </span>
                      {m.comment !== "" && <span className={styles.optionComment}>{firstLine(m.comment)}</span>}
                    </div>
                  );
                })}
              </div>
            ))}
          </div>
        </div>
      )}
    </div>
  );
}

function firstLine(text: string): string {
  const at = text.indexOf("\n");
  return at < 0 ? text : `${text.slice(0, at)} …`;
}

/** The call-type badge of a method: unary, client stream, server stream or bidi. */
export function KindBadge({ kind }: { kind: string }) {
  return (
    <span className={styles.kindBadge} data-kind={kind}>
      {methodKindLabel(kind)}
    </span>
  );
}

/** The Proto tab: the request's own setup or its folder's, and the diagnostics. */
function ProtoPanel({
  request,
  readOnly,
  edit,
  inherited,
  reloadInherited,
  described,
  reload,
}: {
  request: RequestDTO;
  readOnly: boolean;
  edit: (patch: RequestData) => void;
  inherited: InheritedState;
  reloadInherited: () => void;
  described: DescribeState;
  reload: () => void;
}) {
  const t = useT();
  const inherit = request.proto.inherit;
  const [editingFolder, setEditingFolder] = useState(false);
  const [folderError, setFolderError] = useState<string | null>(null);
  const [pickError, setPickError] = useState<string | null>(null);
  const setProto = (patch: Partial<RequestDTO["proto"]>) => edit({ proto: { ...request.proto, ...patch } });

  const folder = inherited.status === "ready" ? inherited.value : null;
  const targetId = folder === null ? "" : folder.folderId !== "" ? folder.folderId : folder.parentId;
  const targetName = folder === null ? "" : folder.folderId !== "" ? folder.folderName : folder.parentName;

  const saveFolder = (setup: ProtoSetup) => {
    if (targetId === "") return;
    setFolderError(null);
    updateFolderProto(targetId, setup).then(reloadInherited, (err: unknown) => {
      if (!isCancelled(err)) setFolderError(errorText(toAppError(err)));
    });
  };

  const own = ownSetup(request);
  const folderSetup: ProtoSetup = { files: folder?.files ?? [], importPaths: folder?.importPaths ?? [] };

  return (
    <div className={styles.proto}>
      <label className={styles.inheritRow}>
        <input type="checkbox" checked={inherit} disabled={readOnly} onChange={(e) => setProto({ inherit: e.target.checked })} />
        <span>{t("editorProtocols.grpc.proto.inherit")}</span>
      </label>

      {inherit ? (
        <div className={styles.folderSetup}>
          {inherited.status === "loading" && <p className={styles.note}>{t("editorProtocols.grpc.proto.looking")}</p>}
          {inherited.status === "error" && (
            <p className={styles.problem}>
              <IconWarning size={12} />
              {errorText(inherited.error)}
            </p>
          )}
          {folder !== null && (
            <>
              <p className={styles.note}>
                {folder.folderId !== "" ? (
                  <Trans
                    k="editorProtocols.grpc.proto.usingFolder"
                    values={{ folder: <strong className={styles.folderName}>{folder.folderName}</strong> }}
                  />
                ) : folder.parentId !== "" ? (
                  t("editorProtocols.grpc.proto.noFolderSetup")
                ) : (
                  t("editorProtocols.grpc.proto.notInFolder")
                )}{" "}
                {targetId !== "" && !readOnly && (
                  <button type="button" className={styles.linkButton} onClick={() => setEditingFolder((v) => !v)} aria-expanded={editingFolder}>
                    {editingFolder
                      ? t("editorProtocols.grpc.proto.done")
                      : folder.folderId !== ""
                        ? t("editorProtocols.grpc.proto.editFolder")
                        : t("editorProtocols.grpc.proto.setUp", { folder: targetName })}
                  </button>
                )}
              </p>
              {(folder.folderId !== "" || editingFolder) && (
                <SetupLists
                  folder={targetName}
                  setup={folderSetup}
                  readOnly={readOnly || !editingFolder}
                  onChange={saveFolder}
                  onError={setPickError}
                />
              )}
              {folderError !== null && (
                <p className={styles.problem}>
                  <IconWarning size={12} />
                  {folderError}
                </p>
              )}
            </>
          )}
        </div>
      ) : (
        <SetupLists
          folder={null}
          setup={own}
          readOnly={readOnly}
          onChange={(setup) => setProto({ files: [...setup.files], importPaths: [...setup.importPaths] })}
          onError={setPickError}
        />
      )}
      {pickError !== null && (
        <p className={styles.problem}>
          <IconWarning size={12} />
          {pickError}
        </p>
      )}

      <Diagnostics described={described} reload={reload} />
    </div>
  );
}

/** The .proto files and import paths of a setup, with native pickers to add them. */
function SetupLists({
  folder,
  setup,
  readOnly,
  onChange,
  onError,
}: {
  /** The folder whose setup this is, null for the request's own. */
  folder: string | null;
  setup: ProtoSetup;
  readOnly: boolean;
  onChange: (setup: ProtoSetup) => void;
  onError: (message: string | null) => void;
}) {
  const t = useT();
  const [picking, setPicking] = useState(false);
  const latest = useRef(setup);
  latest.current = setup;
  const access = useFileAccessCheck();

  const pick = (which: "files" | "importPaths") => {
    setPicking(true);
    onError(null);
    const call: Promise<string[]> = which === "files" ? pickProtoFiles() : pickImportPath().then((p) => (p === "" ? [] : [p]));
    call
      .then((paths) => {
        const current = latest.current;
        const added = paths.filter((p) => !current[which].includes(p));
        if (added.length > 0) onChange({ ...current, [which]: [...current[which], ...added] });
        // Files and import paths outside the allowed directories are
        // refused (PRV-10).
        access.check(paths);
      })
      .catch((err: unknown) => {
        if (!isCancelled(err)) onError(errorText(toAppError(err)));
      })
      .finally(() => setPicking(false));
  };

  const remove = (which: "files" | "importPaths", index: number) => {
    const current = latest.current;
    onChange({ ...current, [which]: current[which].filter((_, i) => i !== index) });
  };

  return (
    <>
      <div className={styles.lists}>
        <PathList
          title={t("editorProtocols.grpc.proto.files")}
          empty={t("editorProtocols.grpc.proto.noFiles")}
          icon={<IconFile size={12} />}
          paths={setup.files}
          readOnly={readOnly}
          addLabel={t("editorProtocols.grpc.proto.addFiles")}
          removeLabel={(path) =>
            folder === null
              ? t("editorProtocols.grpc.proto.removeFile", { path })
              : t("editorProtocols.grpc.proto.removeFolderFile", { path, folder })
          }
          busy={picking}
          onAdd={() => pick("files")}
          onRemove={(i) => remove("files", i)}
        />
        <PathList
          title={t("editorProtocols.grpc.proto.importPaths")}
          empty={t("editorProtocols.grpc.proto.noImportPaths")}
          icon={<IconFolder size={12} />}
          paths={setup.importPaths}
          readOnly={readOnly}
          addLabel={t("editorProtocols.grpc.proto.addImportPath")}
          removeLabel={(path) =>
            folder === null
              ? t("editorProtocols.grpc.proto.removeImportPath", { path })
              : t("editorProtocols.grpc.proto.removeFolderImportPath", { path, folder })
          }
          busy={picking}
          onAdd={() => pick("importPaths")}
          onRemove={(i) => remove("importPaths", i)}
        />
      </div>
      <FileAccessNotice access={access} />
    </>
  );
}

function PathList({
  title,
  empty,
  icon,
  paths,
  readOnly,
  addLabel,
  removeLabel,
  busy,
  onAdd,
  onRemove,
}: {
  title: string;
  empty: string;
  icon: ReactNode;
  paths: readonly string[];
  readOnly: boolean;
  addLabel: string;
  removeLabel: (path: string) => string;
  busy: boolean;
  onAdd: () => void;
  onRemove: (index: number) => void;
}) {
  const headingId = useId();
  return (
    <section className={styles.pathSection} aria-labelledby={headingId}>
      <div className={styles.pathHeader}>
        <h3 className={styles.pathTitle} id={headingId}>
          {title}
        </h3>
        {!readOnly && (
          <button type="button" className={styles.smallButton} onClick={onAdd} disabled={busy}>
            <IconPlus size={12} />
            {addLabel}
          </button>
        )}
      </div>
      {paths.length === 0 ? (
        <p className={styles.pathEmpty}>{empty}</p>
      ) : (
        <ul className={styles.pathList} aria-labelledby={headingId}>
          {paths.map((p, i) => (
            <li key={`${i}:${p}`} className={styles.pathRow}>
              {icon}
              <span className={styles.path} title={p}>
                {p}
              </span>
              {!readOnly && (
                <button type="button" className={styles.iconButton} aria-label={removeLabel(p)} onClick={() => onRemove(i)}>
                  <IconClose size={12} />
                </button>
              )}
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function Diagnostics({ described, reload }: { described: DescribeState; reload: () => void }) {
  const t = useT();
  if (described.status === "empty") return null;
  const result = described.status === "ready" ? described.result : described.status === "loading" ? described.previous : null;
  const diagnostics: DiagnosticDTO[] = result?.diagnostics ?? [];
  const warnings = diagnostics.length - (result?.errors ?? 0);
  let summary: string;
  if (described.status === "error") summary = errorText(described.error);
  else if (result === null) summary = t("editorProtocols.grpc.loadingFiles");
  else {
    const parts = [
      t("editorProtocols.grpc.summary.files", { count: result.files }),
      t("editorProtocols.grpc.summary.services", { count: (result.services ?? []).length }),
    ];
    if (result.errors > 0) parts.push(t("editorProtocols.grpc.errorCount", { count: result.errors }));
    if (warnings > 0) parts.push(t("editorProtocols.grpc.summary.warnings", { count: warnings }));
    summary = parts.join(" · ");
  }
  return (
    <div className={styles.diagnostics}>
      <div className={styles.pathHeader}>
        <p className={styles.describeSummary} role="status" data-tone={described.status === "error" || (result?.errors ?? 0) > 0 ? "bad" : undefined}>
          {summary}
        </p>
        <button type="button" className={styles.smallButton} onClick={reload} disabled={described.status === "loading"}>
          {t("editorProtocols.grpc.reload")}
        </button>
      </div>
      {diagnostics.length > 0 && (
        <ul className={styles.diagnosticList} aria-label={t("editorProtocols.grpc.problems")}>
          {diagnostics.map((d, i) => (
            <li key={i} className={styles.diagnostic} data-severity={d.severity}>
              {d.severity === "warning" ? <IconInfo size={12} /> : <IconWarning size={12} />}
              <span className={styles.srOnly}>
                {d.severity === "warning" ? t("editorProtocols.grpc.severity.warning") : t("editorProtocols.grpc.severity.error")}
              </span>
              <span className={styles.location}>{diagnosticLocation(d)}</span>
              <span className={styles.diagnosticMessage}>{d.message}</span>
            </li>
          ))}
        </ul>
      )}
    </div>
  );
}

/**
 * The Code tab (GEN-10): the call Invoke would make, as a grpcurl command.
 * It waits for the inherited .proto setup, which names the files.
 */
function GrpcCode({ id, request, setup }: { id: RequestId; request: RequestDTO; setup: ProtoSetup | null }) {
  const t = useT();
  const key = setup === null ? "" : JSON.stringify(invokeInput(request, setup));
  // A new source only when the call changed: the panel asks again then.
  const source = useMemo(() => (key === "" ? null : grpcSnippetSource(JSON.parse(key) as InvokeInputDTO)), [key]);
  return (
    <SnippetPanel
      id={id}
      request={key}
      source={source ?? undefined}
      unavailable={source === null ? t("editorProtocols.grpc.blocker.loading") : null}
    />
  );
}

const TRANSPORT_HELP: Record<GrpcTransport, MessageKey> = {
  grpc: "editorProtocols.grpc.settings.transport.grpc",
  "grpc-web": "editorProtocols.grpc.settings.transport.grpcWeb",
  "grpc-web-text": "editorProtocols.grpc.settings.transport.grpcWebText",
};

function GrpcSettings({ request, readOnly, edit }: { request: RequestDTO; readOnly: boolean; edit: (patch: RequestData) => void }) {
  const t = useT();
  const settings = request.settings;
  const setSettings = (patch: Partial<RequestDTO["settings"]>) => edit({ settings: { ...settings, ...patch } });
  const baseId = useId();
  const msInput = (key: "connectTimeoutMs" | "deadlineMs", label: string, placeholder: string, help: string) => (
    <label className={styles.settingRow} htmlFor={`${baseId}-${key}`}>
      <span className={styles.settingLabel}>{label}</span>
      <input
        id={`${baseId}-${key}`}
        className={styles.settingInput}
        type="number"
        min={0}
        step={1000}
        placeholder={placeholder}
        value={settings[key] ?? ""}
        readOnly={readOnly}
        onChange={(e) => {
          const v = e.target.value.trim();
          const n = Number(v);
          if (v === "") setSettings({ [key]: null });
          else if (Number.isInteger(n) && n >= 0) setSettings({ [key]: n });
        }}
      />
      <span className={styles.settingHelp}>{help}</span>
    </label>
  );
  const transport = grpcTransport(request);
  const compression = grpcCompression(request);
  return (
    <div className={styles.settings}>
      <label className={styles.settingRow} htmlFor={`${baseId}-transport`}>
        <span className={styles.settingLabel}>{t("editorProtocols.grpc.settings.transport")}</span>
        <select
          id={`${baseId}-transport`}
          className={styles.settingSelect}
          value={transport}
          disabled={readOnly}
          onChange={(e) => setSettings({ transport: e.target.value === "grpc" ? "" : (e.target.value as GrpcTransport) })}
        >
          {/* i18n-ignore-start: transport names */}
          <option value="grpc">gRPC (HTTP/2)</option>
          {/* i18n-ignore-end */}
          <option value="grpc-web">{t("editorProtocols.grpc.settings.grpcWeb")}</option>
          <option value="grpc-web-text">{t("editorProtocols.grpc.settings.grpcWebText")}</option>
        </select>
        <span className={styles.settingHelp}>{t(TRANSPORT_HELP[transport])}</span>
      </label>
      <label className={styles.settingRow} htmlFor={`${baseId}-compression`}>
        <span className={styles.settingLabel}>{t("editorProtocols.grpc.settings.compression")}</span>
        <select
          id={`${baseId}-compression`}
          className={styles.settingSelect}
          value={compression}
          disabled={readOnly}
          onChange={(e) => setSettings({ compression: e.target.value === "gzip" ? "gzip" : "" })}
        >
          <option value="identity">{t("editorProtocols.grpc.settings.compressionNone")}</option>
          {/* i18n-ignore: the name of the encoding */}
          <option value="gzip">gzip</option>
        </select>
        <span className={styles.settingHelp}>
          {compression === "gzip"
            ? t("editorProtocols.grpc.settings.gzipOn")
            : t("editorProtocols.grpc.settings.gzipOff")}
        </span>
      </label>
      <label className={styles.settingRow}>
        <input type="checkbox" checked={settings.tls === true} disabled={readOnly} onChange={(e) => setSettings({ tls: e.target.checked ? true : null })} />
        <span className={styles.settingLabel}>{t("editorProtocols.grpc.settings.tls")}</span>
        <span className={styles.settingHelp}>
          {transport === "grpc" ? t("editorProtocols.grpc.settings.tlsOff") : t("editorProtocols.grpc.settings.tlsOffWeb")}
        </span>
      </label>
      <label className={styles.settingRow}>
        <input
          type="checkbox"
          checked={settings.verifyTls !== false}
          disabled={readOnly || settings.tls !== true}
          onChange={(e) => setSettings({ verifyTls: e.target.checked ? null : false })}
        />
        <span className={styles.settingLabel}>{t("editorProtocols.grpc.settings.verifyTls")}</span>
      </label>
      {msInput(
        "connectTimeoutMs",
        t("editorProtocols.grpc.settings.connectTimeout"),
        t("editorProtocols.grpc.settings.connectTimeoutPlaceholder"),
        t("editorProtocols.grpc.settings.connectTimeoutHelp"),
      )}
      {msInput(
        "deadlineMs",
        t("editorProtocols.grpc.settings.deadline"),
        t("editorProtocols.grpc.settings.deadlinePlaceholder"),
        t("editorProtocols.grpc.settings.deadlineHelp"),
      )}
    </div>
  );
}
