import { Fragment, useCallback, useEffect, useId, useLayoutEffect, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { useT } from "../../i18n/react";
import type { CancellablePromise } from "../../lib/calls";
import { isCancelled, toAppError } from "../../lib/errors";
import {
  groupTypes,
  hitPositions,
  HOME,
  KIND_LABELS,
  pageLabel,
  pushPage,
  splitTypeRef,
  type DocsPage,
} from "../../lib/graphqlDocs";
import {
  GRAPHQL_DEPTHS,
  graphqlDocsOverview,
  graphqlDocsSearch,
  graphqlDocsType,
  type DocsDirectiveDTO,
  type DocsFieldDTO,
  type DocsHitDTO,
  type DocsInputValueDTO,
  type DocsOverviewDTO,
  type DocsSearchDTO,
  type DocsTypeDTO,
  type GenerateRequest,
} from "../../lib/graphqlSchema";
import { IconChevron, IconClose, IconCode, IconPlus, IconSearch, IconWarning } from "../common/icons";
import styles from "./GraphqlDocs.module.css";

// The docs explorer of the GraphQL editor (GQL-14): the cached schema's
// root types, all types by kind and the directives; a type's fields with
// their arguments, input fields or enum values, descriptions and
// deprecations; clickable types with breadcrumbs and Back; and a search
// over every type and member, run in Go (GraphQLService.DocsSearch) so a
// schema of thousands of types is never loaded whole. Everything comes
// from the schema cache, so it works offline. From a field or type, a
// whole operation or fragment can be written into the query (GQL-16), and
// a Ctrl/Cmd-click in the query opens its page here (GQL-15, `jump`).
// See docs/graphql.md.

/** How long the search waits after typing, in ms. */
export const DOCS_SEARCH_DELAY_MS = 120;
/** The most search hits shown. */
export const DOCS_SEARCH_LIMIT = 100;
/** Rows of a long list rendered at first and per "Show more". */
export const DOCS_PAGE_SIZE = 200;

export interface GraphqlDocsProps {
  /** SchemaInfoDTO.key: the cached schema, "" when none. */
  readonly schemaKey: string;
  /** Changes when the schema is fetched again (SchemaInfoDTO.fetchedAt), so pages are read anew. */
  readonly version: string;
  /** Fetches the schema (the strip's button), for the empty state. */
  readonly onFetch?: () => void;
  readonly fetching?: boolean;
  /** Inserts text at the query editor's cursor; absent when there is no query editor to insert into. */
  readonly onInsert?: (text: string) => void;
  /** Writes an operation or fragment into the query (GQL-16); absent when there is no query editor to write into. */
  readonly onGenerate?: (request: GenerateRequest) => void;
  /** The selection depth of onGenerate, and its setter. */
  readonly depth?: number;
  readonly onDepth?: (depth: number) => void;
  /** A page to open, from a Ctrl/Cmd-click in the query (GQL-15); each new `seq` opens it again. */
  readonly jump?: { readonly page: DocsPage; readonly seq: number } | null;
  readonly onClose: () => void;
}

type Load<T> = { status: "loading" } | { status: "ready"; value: T } | { status: "error"; message: string };

/** Calls `load` whenever `deps` change, dropping the answers of older calls. */
function useLoad<T>(load: (() => CancellablePromise<T>) | null, deps: readonly unknown[]): Load<T> | null {
  const [state, setState] = useState<Load<T> | null>(null);
  useEffect(() => {
    if (load === null) {
      setState(null);
      return;
    }
    let live = true;
    setState({ status: "loading" });
    const call = load();
    call.then(
      (value) => {
        if (live) setState({ status: "ready", value });
      },
      (err: unknown) => {
        if (live && !isCancelled(err)) setState({ status: "error", message: toAppError(err).message });
      },
    );
    return () => {
      live = false;
      void call.cancel();
    };
    // The deps are the caller's: `load` is a new closure on every render.
  }, deps);
  return state;
}

export function GraphqlDocs({
  schemaKey,
  version,
  onFetch,
  fetching = false,
  onInsert,
  onGenerate,
  depth,
  onDepth,
  jump,
  onClose,
}: GraphqlDocsProps) {
  const t = useT();
  const [stack, setStack] = useState<DocsPage[]>([HOME]);
  const [query, setQuery] = useState("");
  const [active, setActive] = useState(0);
  const bodyRef = useRef<HTMLDivElement>(null);
  const searchId = useId();

  // A new schema starts over at the start page.
  useEffect(() => {
    setStack([HOME]);
  }, [schemaKey]);

  const page = stack[stack.length - 1] ?? HOME;
  const open = useCallback((next: DocsPage) => {
    setStack((s) => pushPage(s, next));
    setQuery("");
  }, []);
  const back = () => setStack((s) => (s.length > 1 ? s.slice(0, -1) : s));

  // A Ctrl/Cmd-click in the query: its page on top of the stack.
  const jumpSeq = jump?.seq ?? 0;
  const jumpPage = jump?.page ?? null;
  useEffect(() => {
    if (jumpPage !== null) open(jumpPage);
    // Only a new jump opens the page again, not a new closure.
  }, [jumpSeq]);

  const overview = useLoad<DocsOverviewDTO>(schemaKey === "" ? null : () => graphqlDocsOverview(schemaKey), [schemaKey, version]);
  const typeName = page.kind === "type" ? page.name : "";
  const type = useLoad<DocsTypeDTO>(
    schemaKey === "" || typeName === "" ? null : () => graphqlDocsType(schemaKey, typeName),
    [schemaKey, version, typeName],
  );

  // The search runs in Go a moment after typing pauses.
  const [search, setSearch] = useState<{ query: string; result: DocsSearchDTO } | null>(null);
  const [searchError, setSearchError] = useState<string | null>(null);
  const trimmed = query.trim();
  useEffect(() => {
    setActive(0);
    if (schemaKey === "" || trimmed === "") {
      setSearch(null);
      setSearchError(null);
      return;
    }
    let live = true;
    let call: ReturnType<typeof graphqlDocsSearch> | null = null;
    const timer = setTimeout(() => {
      call = graphqlDocsSearch(schemaKey, trimmed, DOCS_SEARCH_LIMIT);
      call.then(
        (result) => {
          if (!live) return;
          setSearch({ query: trimmed, result });
          setSearchError(null);
        },
        (err: unknown) => {
          if (live && !isCancelled(err)) setSearchError(toAppError(err).message);
        },
      );
    }, DOCS_SEARCH_DELAY_MS);
    return () => {
      live = false;
      clearTimeout(timer);
      if (call !== null) void call.cancel();
    };
  }, [schemaKey, version, trimmed]);

  const hits = trimmed !== "" && search !== null ? (search.result.items ?? []) : null;
  const openHit = (hit: DocsHitDTO) => open({ kind: "type", name: hit.type, member: hit.member || undefined });

  const onSearchKey = (e: KeyboardEvent<HTMLInputElement>) => {
    if (e.key === "Escape" && query !== "") {
      e.preventDefault();
      e.stopPropagation();
      setQuery("");
      return;
    }
    if (hits === null || hits.length === 0) return;
    if (e.key === "ArrowDown" || e.key === "ArrowUp") {
      e.preventDefault();
      const delta = e.key === "ArrowDown" ? 1 : -1;
      setActive((a) => (a + delta + hits.length) % hits.length);
    } else if (e.key === "Enter") {
      e.preventDefault();
      const hit = hits[Math.min(active, hits.length - 1)];
      if (hit !== undefined) openHit(hit);
    }
  };

  // Scroll the page to its member (or its top) once it is shown.
  const member = page.member ?? "";
  const ready = page.kind === "type" ? type?.status === "ready" : overview?.status === "ready";
  useLayoutEffect(() => {
    const body = bodyRef.current;
    if (body === null || hits !== null) return;
    if (member === "") {
      body.scrollTop = 0;
      return;
    }
    const el = Array.from(body.querySelectorAll<HTMLElement>("[data-member]")).find((e) => e.dataset.member === member);
    el?.scrollIntoView?.({ block: "center" });
  }, [page, ready, member, hits]);

  let content: ReactNode;
  if (schemaKey === "") {
    content = (
      <div className={styles.empty}>
        <p>{t("editorProtocols.docs.noSchema")}</p>
        {onFetch !== undefined && (
          <button type="button" className={styles.action} disabled={fetching} onClick={onFetch}>
            {fetching ? t("editorProtocols.schema.fetching") : t("editorProtocols.schema.fetch")}
          </button>
        )}
      </div>
    );
  } else if (trimmed !== "") {
    content = (
      <SearchResults
        query={search?.query ?? trimmed}
        result={search?.result ?? null}
        error={searchError}
        active={active}
        listId={`${searchId}-results`}
        onOpen={openHit}
      />
    );
  } else if (page.kind === "home") {
    content = <HomePage state={overview} member={member} onOpen={open} />;
  } else {
    content = (
      <TypePage state={type} name={page.name} member={member} onOpen={open} onInsert={onInsert} onGenerate={onGenerate} />
    );
  }

  return (
    <aside className={styles.docs} aria-label={t("editorProtocols.docs.label")}>
      <header className={styles.header}>
        <button
          type="button"
          className={styles.iconButton}
          aria-label={t("editorProtocols.docs.back")}
          title={t("editorProtocols.docs.back")}
          disabled={stack.length <= 1 || trimmed !== ""}
          onClick={back}
        >
          <IconChevron direction="left" size={14} />
        </button>
        <h2 className={styles.title}>{t("editorProtocols.docs.title")}</h2>
        {onGenerate !== undefined && depth !== undefined && onDepth !== undefined && (
          <label className={styles.depth} title={t("editorProtocols.docs.depthTitle")}>
            {t("editorProtocols.docs.depth")}
            <select className={styles.depthSelect} value={depth} onChange={(e) => onDepth(Number(e.target.value))}>
              {GRAPHQL_DEPTHS.map((d) => (
                <option key={d} value={d}>
                  {d}
                </option>
              ))}
            </select>
          </label>
        )}
        <button
          type="button"
          className={styles.iconButton}
          aria-label={t("editorProtocols.docs.close")}
          title={t("editorProtocols.docs.close")}
          onClick={onClose}
        >
          <IconClose size={13} />
        </button>
      </header>
      <div className={styles.search}>
        <IconSearch size={13} />
        <input
          type="search"
          className={styles.searchInput}
          placeholder={t("editorProtocols.docs.searchPlaceholder")}
          aria-label={t("editorProtocols.docs.searchLabel")}
          aria-controls={hits !== null ? `${searchId}-results` : undefined}
          aria-activedescendant={hits !== null && hits.length > 0 ? `${searchId}-results-${active}` : undefined}
          value={query}
          disabled={schemaKey === ""}
          spellCheck={false}
          onChange={(e) => setQuery(e.target.value)}
          onKeyDown={onSearchKey}
        />
      </div>
      {trimmed === "" && schemaKey !== "" && stack.length > 1 && (
        <nav className={styles.crumbs} aria-label={t("editorProtocols.docs.breadcrumbs")}>
          {stack.map((p, i) => {
            const last = i === stack.length - 1;
            return (
              <Fragment key={`${i}-${pageLabel(p)}`}>
                {i > 0 && <span className={styles.crumbSep}>›</span>}
                {last ? (
                  <span className={styles.crumbCurrent} aria-current="page">
                    {pageLabel(p)}
                  </span>
                ) : (
                  <button type="button" className={styles.crumb} onClick={() => setStack((s) => s.slice(0, i + 1))}>
                    {pageLabel(p)}
                  </button>
                )}
              </Fragment>
            );
          })}
        </nav>
      )}
      <div className={styles.body} ref={bodyRef}>
        {content}
      </div>
    </aside>
  );
}

function Status({ state }: { state: Load<unknown> | null }) {
  const t = useT();
  if (state === null || state.status === "loading") return <p className={styles.muted}>{t("editorProtocols.docs.loading")}</p>;
  if (state.status === "error") {
    return (
      <p className={styles.error} role="alert">
        <IconWarning size={12} />
        {state.message}
      </p>
    );
  }
  return null;
}

/** A description as the schema wrote it (Markdown is shown as plain text). */
function Description({ text, className }: { text: string; className?: string }) {
  if (text.trim() === "") return null;
  return <p className={`${styles.description} ${className ?? ""}`}>{text}</p>;
}

function Deprecated({ reason }: { reason: string }) {
  const t = useT();
  return (
    <span
      className={styles.deprecated}
      title={reason === "" ? t("editorProtocols.docs.deprecatedTitle") : t("editorProtocols.docs.deprecatedReason", { reason })}
    >
      {t("editorProtocols.docs.deprecated")}
    </span>
  );
}

/** A link that opens a type. */
function TypeLink({ name, onOpen }: { name: string; onOpen: (page: DocsPage) => void }) {
  return (
    <button type="button" className={styles.typeLink} onClick={() => onOpen({ kind: "type", name })}>
      {name}
    </button>
  );
}

/** A type reference ("[User!]!") whose named type opens its page. */
function TypeRef({ type, name, onOpen }: { type: string; name: string; onOpen: (page: DocsPage) => void }) {
  const [before, named, after] = splitTypeRef(type, name);
  return (
    <span className={styles.typeRef}>
      {before}
      {named !== "" && <TypeLink name={named} onOpen={onOpen} />}
      {after}
    </span>
  );
}

/** Up to `limit` rows, then a button that shows DOCS_PAGE_SIZE more. */
function useLimit(count: number, atLeast = 0): [number, ReactNode] {
  const t = useT();
  const [limit, setLimit] = useState(DOCS_PAGE_SIZE);
  const shown = Math.min(count, Math.max(limit, atLeast));
  const more =
    shown < count ? (
      <button type="button" className={styles.more} onClick={() => setLimit(shown + DOCS_PAGE_SIZE)}>
        {t("editorProtocols.docs.showMore", { count: Math.min(DOCS_PAGE_SIZE, count - shown), rest: count - shown })}
      </button>
    ) : null;
  return [shown, more];
}

function HomePage({ state, member, onOpen }: { state: Load<DocsOverviewDTO> | null; member: string; onOpen: (page: DocsPage) => void }) {
  const t = useT();
  if (state?.status !== "ready") return <Status state={state} />;
  const overview = state.value;
  if (!overview.schema) return <p className={styles.muted}>{t("editorProtocols.docs.notCached")}</p>;
  // Go never sends null lists; the bindings allow them.
  const types = overview.types ?? [];
  const roots = overview.roots ?? [];
  const directives = overview.directives ?? [];
  const groups = groupTypes(types);
  const small = types.length <= DOCS_PAGE_SIZE;
  return (
    <div className={styles.page}>
      <Description text={overview.description} />
      <section aria-label={t("editorProtocols.docs.rootTypes")}>
        <h3 className={styles.caption}>{t("editorProtocols.docs.rootTypes")}</h3>
        <ul className={styles.list}>
          {roots.map((r) => (
            <li key={r.operation} className={styles.row}>
              <span className={styles.name}>{r.operation}</span>
              <span className={styles.punct}>: </span>
              <TypeLink name={r.type} onOpen={onOpen} />
            </li>
          ))}
        </ul>
      </section>
      <section aria-label={t("editorProtocols.docs.allTypes")}>
        <h3 className={styles.caption}>{t("editorProtocols.docs.allTypesCount", { count: types.length })}</h3>
        {groups.map((g) => (
          <TypeGroupList key={g.kind} label={g.label} names={g.types.map((ty) => ty.name)} open={small} onOpen={onOpen} />
        ))}
      </section>
      {directives.length > 0 && (
        <section aria-label={t("editorProtocols.docs.directives")}>
          <h3 className={styles.caption}>{t("editorProtocols.docs.directives")}</h3>
          <ul className={styles.list}>
            {directives.map((d) => (
              <DirectiveRow key={d.name} directive={d} target={member === `@${d.name}`} onOpen={onOpen} />
            ))}
          </ul>
        </section>
      )}
    </div>
  );
}

function TypeGroupList({ label, names, open, onOpen }: { label: string; names: readonly string[]; open: boolean; onOpen: (page: DocsPage) => void }) {
  const [expanded, setExpanded] = useState(open);
  const [shown, more] = useLimit(names.length);
  return (
    <div className={styles.group}>
      <button type="button" className={styles.groupHead} aria-expanded={expanded} onClick={() => setExpanded((e) => !e)}>
        <IconChevron direction={expanded ? "down" : "right"} size={11} />
        {label}
        <span className={styles.count}>{names.length}</span>
      </button>
      {expanded && (
        <>
          <ul className={styles.typeList}>
            {names.slice(0, shown).map((n) => (
              <li key={n}>
                <TypeLink name={n} onOpen={onOpen} />
              </li>
            ))}
          </ul>
          {more}
        </>
      )}
    </div>
  );
}

function DirectiveRow({ directive, target, onOpen }: { directive: DocsDirectiveDTO; target: boolean; onOpen: (page: DocsPage) => void }) {
  return (
    <li className={styles.member} data-member={`@${directive.name}`} data-target={target || undefined}>
      <div className={styles.signature}>
        <span className={styles.name}>@{directive.name}</span>
        {/* i18n-ignore: the GraphQL SDL keyword of a repeatable directive */}
        {directive.repeatable && <span className={styles.badge}>repeatable</span>}
      </div>
      <Description text={directive.description} />
      <Args args={directive.args} onOpen={onOpen} />
      {/* i18n-ignore: "on" and the locations as in the SDL of a directive definition */}
      <p className={styles.locations}>on {(directive.locations ?? []).join(" | ")}</p>
    </li>
  );
}

function Args({ args, onOpen }: { args: readonly DocsInputValueDTO[] | null; onOpen: (page: DocsPage) => void }) {
  const t = useT();
  if (args === null || args.length === 0) return null;
  return (
    <ul className={styles.args} aria-label={t("editorProtocols.docs.arguments")}>
      {args.map((a) => (
        <InputValueRow key={a.name} value={a} onOpen={onOpen} />
      ))}
    </ul>
  );
}

function InputValueRow({ value, onOpen, member = false }: { value: DocsInputValueDTO; onOpen: (page: DocsPage) => void; member?: boolean }) {
  return (
    <li className={member ? styles.member : styles.arg} data-member={member ? value.name : undefined}>
      <div className={styles.signature}>
        <span className={styles.name} data-deprecated={value.deprecated || undefined}>
          {value.name}
        </span>
        <span className={styles.punct}>: </span>
        <TypeRef type={value.type} name={value.typeName} onOpen={onOpen} />
        {value.hasDefault && <span className={styles.default}> = {value.defaultValue}</span>}
        {value.deprecated && <Deprecated reason={value.deprecationReason} />}
      </div>
      <Description text={value.description} />
      {value.deprecated && value.deprecationReason !== "" && <p className={styles.reason}>{value.deprecationReason}</p>}
    </li>
  );
}

function TypePage({
  state,
  name,
  member,
  onOpen,
  onInsert,
  onGenerate,
}: {
  state: Load<DocsTypeDTO> | null;
  name: string;
  member: string;
  onOpen: (page: DocsPage) => void;
  onInsert?: (text: string) => void;
  onGenerate?: (request: GenerateRequest) => void;
}) {
  const t = useT();
  if (state?.status !== "ready") return <Status state={state} />;
  const ty = state.value;
  if (!ty.found) return <p className={styles.muted}>{t("editorProtocols.docs.noType", { name })}</p>;
  const fields = ty.fields ?? [];
  const inputFields = ty.inputFields ?? [];
  const enumValues = ty.enumValues ?? [];
  return (
    <div className={styles.page}>
      <div className={styles.typeHead}>
        <h3 className={styles.typeName}>{ty.name}</h3>
        <span className={styles.badge}>{KIND_LABELS[ty.kind] ?? ty.kind}</span>
        {ty.root !== "" && <span className={styles.badge}>{t("editorProtocols.docs.rootBadge", { operation: ty.root })}</span>}
        {ty.isOneOf && <span className={styles.badge}>@oneOf</span>}
        {onGenerate !== undefined && COMPOSITE.has(ty.kind) && ty.root === "" && (
          <button
            type="button"
            className={styles.generateType}
            title={t("editorProtocols.docs.fragmentTitle", { type: ty.name })}
            onClick={() => onGenerate({ kind: "fragment", type: ty.name, field: "" })}
          >
            <IconCode size={12} />
            {t("editorProtocols.docs.fragment")}
          </button>
        )}
      </div>
      <Description text={ty.description} />
      {ty.specifiedByUrl !== "" && (
        <p className={styles.muted}>{t("editorProtocols.docs.specifiedBy", { url: ty.specifiedByUrl })}</p>
      )}
      <TypeNames label={t("editorProtocols.docs.implements")} names={ty.interfaces ?? []} onOpen={onOpen} />
      <TypeNames
        label={ty.kind === "UNION" ? t("editorProtocols.docs.members") : t("editorProtocols.docs.possibleTypes")}
        names={ty.possibleTypes ?? []}
        onOpen={onOpen}
      />
      <TypeNames label={t("editorProtocols.docs.implementedBy")} names={ty.implementations ?? []} onOpen={onOpen} />
      {fields.length > 0 && (
        <Fields
          key={ty.name}
          typeName={ty.name}
          root={ty.root}
          fields={fields}
          member={member}
          onOpen={onOpen}
          onInsert={onInsert}
          onGenerate={onGenerate}
        />
      )}
      {inputFields.length > 0 && (
        <section aria-label={t("editorProtocols.docs.inputFields")}>
          <h3 className={styles.caption}>{t("editorProtocols.docs.inputFieldsCount", { count: inputFields.length })}</h3>
          <ul className={styles.list}>
            {inputFields.map((v) => (
              <InputValueRow key={v.name} value={v} onOpen={onOpen} member />
            ))}
          </ul>
        </section>
      )}
      {enumValues.length > 0 && (
        <section aria-label={t("editorProtocols.docs.values")}>
          <h3 className={styles.caption}>{t("editorProtocols.docs.valuesCount", { count: enumValues.length })}</h3>
          <ul className={styles.list}>
            {enumValues.map((v) => (
              <li key={v.name} className={styles.member} data-member={v.name}>
                <div className={styles.signature}>
                  <span className={styles.name} data-deprecated={v.deprecated || undefined}>
                    {v.name}
                  </span>
                  {v.deprecated && <Deprecated reason={v.deprecationReason} />}
                </div>
                <Description text={v.description} />
                {v.deprecated && v.deprecationReason !== "" && <p className={styles.reason}>{v.deprecationReason}</p>}
              </li>
            ))}
          </ul>
        </section>
      )}
    </div>
  );
}

function TypeNames({ label, names, onOpen }: { label: string; names: readonly string[]; onOpen: (page: DocsPage) => void }) {
  if (names.length === 0) return null;
  return (
    <p className={styles.related}>
      <span className={styles.relatedLabel}>{label}</span>
      {names.map((n, i) => (
        <Fragment key={n}>
          {i > 0 && ", "}
          <TypeLink name={n} onOpen={onOpen} />
        </Fragment>
      ))}
    </p>
  );
}

/** The kinds of types with a selection set. */
const COMPOSITE = new Set(["OBJECT", "INTERFACE", "UNION"]);

/** Whether an argument must be given: non-null without a default. */
function isRequired(a: DocsInputValueDTO): boolean {
  return a.type.endsWith("!") && !a.hasDefault;
}

/**
 * What the code button of a field writes (GQL-16): an operation for a
 * field of a root type, else a fragment on the type with that field; null
 * for a fragment the field's required arguments rule out.
 */
export function generateRequestFor(typeName: string, root: string, field: DocsFieldDTO): GenerateRequest | null {
  if (root !== "") return { kind: "operation", type: typeName, field: field.name };
  if ((field.args ?? []).some(isRequired)) return null;
  return { kind: "fragment", type: typeName, field: field.name };
}

function Fields({
  typeName,
  root,
  fields,
  member,
  onOpen,
  onInsert,
  onGenerate,
}: {
  typeName: string;
  /** The operation the type is the root of, or "". */
  root: string;
  fields: readonly DocsFieldDTO[];
  member: string;
  onOpen: (page: DocsPage) => void;
  onInsert?: (text: string) => void;
  onGenerate?: (request: GenerateRequest) => void;
}) {
  const t = useT();
  const target = member === "" ? -1 : fields.findIndex((f) => f.name === member);
  const [shown, more] = useLimit(fields.length, target + 1);
  return (
    <section aria-label={t("editorProtocols.docs.fields")}>
      <h3 className={styles.caption}>{t("editorProtocols.docs.fieldsCount", { count: fields.length })}</h3>
      <ul className={styles.list}>
        {fields.slice(0, shown).map((f) => (
          <li key={f.name} className={styles.member} data-member={f.name} data-target={f.name === member || undefined}>
            <div className={styles.signature}>
              <span className={styles.name} data-deprecated={f.deprecated || undefined}>
                {f.name}
              </span>
              {(f.args ?? []).length > 0 && <span className={styles.punct}>(…)</span>}
              <span className={styles.punct}>: </span>
              <TypeRef type={f.type} name={f.typeName} onOpen={onOpen} />
              {f.deprecated && <Deprecated reason={f.deprecationReason} />}
              {onInsert !== undefined && (
                <button
                  type="button"
                  className={styles.insert}
                  aria-label={t("editorProtocols.docs.insertLabel", { name: f.name })}
                  title={t("editorProtocols.docs.insertTitle")}
                  onClick={() => onInsert(f.name)}
                >
                  <IconPlus size={11} />
                </button>
              )}
              {onGenerate !== undefined && <GenerateButton request={generateRequestFor(typeName, root, f)} root={root} onGenerate={onGenerate} />}
            </div>
            <Description text={f.description} />
            {f.deprecated && f.deprecationReason !== "" && <p className={styles.reason}>{f.deprecationReason}</p>}
            <Args args={f.args} onOpen={onOpen} />
          </li>
        ))}
      </ul>
      {more}
    </section>
  );
}

/** The button that writes a field's operation or fragment into the query. */
function GenerateButton({
  request,
  root,
  onGenerate,
}: {
  request: GenerateRequest | null;
  root: string;
  onGenerate: (request: GenerateRequest) => void;
}) {
  const t = useT();
  if (request === null) return null;
  const label =
    request.kind === "operation"
      ? t("editorProtocols.docs.writeOperation", { operation: root, field: request.field })
      : t("editorProtocols.docs.writeFragment", { field: request.field });
  return (
    <button
      type="button"
      className={styles.generate}
      aria-label={label}
      title={label}
      onClick={() => onGenerate(request)}
    >
      <IconCode size={11} />
    </button>
  );
}

const HIT_KINDS: Readonly<Record<string, string>> = {
  type: "T",
  field: "F",
  "input-field": "I",
  "enum-value": "E",
  argument: "A",
};

function Highlighted({ text, positions }: { text: string; positions: readonly number[] }) {
  if (positions.length === 0) return <>{text}</>;
  const marked = new Set(positions);
  const parts: ReactNode[] = [];
  let run = "";
  let inMark = false;
  const flush = (key: number) => {
    if (run === "") return;
    parts.push(inMark ? <mark key={key}>{run}</mark> : run);
    run = "";
  };
  for (let i = 0; i < text.length; i++) {
    const m = marked.has(i);
    if (m !== inMark) {
      flush(i);
      inMark = m;
    }
    run += text[i];
  }
  flush(text.length);
  return <>{parts}</>;
}

function SearchResults({
  query,
  result,
  error,
  active,
  listId,
  onOpen,
}: {
  query: string;
  result: DocsSearchDTO | null;
  error: string | null;
  active: number;
  listId: string;
  onOpen: (hit: DocsHitDTO) => void;
}) {
  const t = useT();
  if (error !== null) return <Status state={{ status: "error", message: error }} />;
  if (result === null) return <p className={styles.muted}>{t("editorProtocols.docs.searching")}</p>;
  const items = result.items ?? [];
  if (items.length === 0) return <p className={styles.muted}>{t("editorProtocols.docs.noMatches", { query })}</p>;
  return (
    <>
      <p className={styles.muted} role="status">
        {result.count > items.length
          ? t("editorProtocols.docs.matchesBest", { count: result.count, shown: items.length })
          : t("editorProtocols.docs.matches", { count: result.count })}
      </p>
      <ul className={styles.hits} id={listId} role="listbox" aria-label={t("editorProtocols.docs.results")}>
        {items.map((h, i) => (
          <li
            key={`${h.kind}-${h.parent}-${h.name}`}
            id={`${listId}-${i}`}
            role="option"
            aria-selected={i === active}
            className={styles.hit}
            onClick={() => onOpen(h)}
          >
            <span className={styles.hitKind} data-kind={h.kind} aria-hidden="true">
              {HIT_KINDS[h.kind] ?? "·"}
            </span>
            <span className={styles.hitMain}>
              <span className={styles.hitName}>
                {h.parent !== "" && <span className={styles.hitParent}>{h.parent}.</span>}
                <span data-deprecated={h.deprecated || undefined}>
                  <Highlighted text={h.name} positions={hitPositions(query, h.name, h.parent !== "")} />
                </span>
                {h.detail !== "" && <span className={styles.hitDetail}>{h.kind === "type" ? KIND_LABELS[h.detail] ?? h.detail : h.detail}</span>}
                {h.deprecated && <Deprecated reason="" />}
              </span>
              {h.description !== "" && <span className={styles.hitDescription}>{h.description}</span>}
            </span>
          </li>
        ))}
      </ul>
    </>
  );
}
