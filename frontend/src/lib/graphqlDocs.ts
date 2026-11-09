// Helpers of the GraphQL docs explorer (GQL-14): grouping the type list,
// splitting a type reference around its named type, the highlighted
// characters of a search hit and the navigation stack. Go does the search
// (GraphQLService.DocsSearch); fuzzyMatch only marks what matched. See
// docs/graphql.md (Docs explorer).
import { t, type MessageKey } from "../i18n";
import { fuzzyMatch, normalizeQuery } from "./fuzzy";
import type { DocsTargetDTO, DocsTypeNameDTO } from "./graphqlSchema";

/** The kinds of named types in the order the explorer lists them, with their headings. */
export const TYPE_KIND_GROUPS: readonly { kind: string; labelKey: MessageKey }[] = [
  { kind: "OBJECT", labelKey: "lib.graphqlDocs.group.objects" },
  { kind: "INTERFACE", labelKey: "lib.graphqlDocs.group.interfaces" },
  { kind: "UNION", labelKey: "lib.graphqlDocs.group.unions" },
  { kind: "ENUM", labelKey: "lib.graphqlDocs.group.enums" },
  { kind: "INPUT_OBJECT", labelKey: "lib.graphqlDocs.group.inputObjects" },
  { kind: "SCALAR", labelKey: "lib.graphqlDocs.group.scalars" },
];

/** Short words for a type kind, as badges show them. */
export const KIND_LABELS: Readonly<Record<string, string>> = {
  OBJECT: "type",
  INTERFACE: "interface",
  UNION: "union",
  ENUM: "enum",
  INPUT_OBJECT: "input",
  SCALAR: "scalar",
};

export interface TypeGroup {
  readonly kind: string;
  /** The heading, in the language in use. */
  readonly label: string;
  readonly types: readonly DocsTypeNameDTO[];
}

/** The types by kind, in TYPE_KIND_GROUPS order, keeping their order; empty groups left out, unknown kinds last. */
export function groupTypes(types: readonly DocsTypeNameDTO[]): TypeGroup[] {
  const byKind = new Map<string, DocsTypeNameDTO[]>();
  for (const type of types) {
    let list = byKind.get(type.kind);
    if (list === undefined) {
      list = [];
      byKind.set(type.kind, list);
    }
    list.push(type);
  }
  const groups: TypeGroup[] = [];
  for (const { kind, labelKey } of TYPE_KIND_GROUPS) {
    const list = byKind.get(kind);
    if (list !== undefined) groups.push({ kind, label: t(labelKey), types: list });
    byKind.delete(kind);
  }
  for (const [kind, list] of byKind) groups.push({ kind, label: kind, types: list });
  return groups;
}

/**
 * Splits a type reference such as "[User!]!" around its named type `name`:
 * ["[", "User", "!]!"]. A reference without the name is [ref, "", ""].
 */
export function splitTypeRef(ref: string, name: string): [string, string, string] {
  // A reference is the name wrapped in "[", "]" and "!": its only word.
  const at = name === "" ? -1 : ref.indexOf(name);
  if (at < 0) return [ref, "", ""];
  return [ref.slice(0, at), name, ref.slice(at + name.length)];
}

/**
 * The characters of `name` to highlight for the search `query` (the member
 * part of "Type.member"), or [] when fuzzyMatch finds none (Go matched
 * differently).
 */
export function hitPositions(query: string, name: string, member: boolean): readonly number[] {
  let q = normalizeQuery(query);
  const dot = q.lastIndexOf(".");
  if (dot >= 0) {
    if (!member) return [];
    q = q.slice(dot + 1);
  }
  if (q === "") return [];
  return fuzzyMatch(q, name)?.positions ?? [];
}

/**
 * A page of the explorer: the start page (with a directive to show, as
 * "@name"), or a type with a member to show.
 */
export type DocsPage =
  | { readonly kind: "home"; readonly member?: string }
  | { readonly kind: "type"; readonly name: string; readonly member?: string };

export const HOME: DocsPage = { kind: "home" };

/** The label of a page in the breadcrumbs. */
export function pageLabel(page: DocsPage): string {
  return page.kind === "home" ? t("lib.graphqlDocs.home") : page.name;
}

/**
 * Opens `next` on top of `stack` (which always starts with the home page).
 * Opening the page already on top only changes its member.
 */
export function pushPage(stack: readonly DocsPage[], next: DocsPage): DocsPage[] {
  const top = stack[stack.length - 1];
  if (top !== undefined && top.kind === "type" && next.kind === "type" && top.name === next.name) {
    return [...stack.slice(0, -1), next];
  }
  if (next.kind === "home") return [next];
  return [...stack, next];
}

/** Where the docs show what a name in the query refers to (GQL-15): a type page and member, or a directive on the start page. */
export function docsPageFor(target: DocsTargetDTO): DocsPage | null {
  if (!target.found) return null;
  if (target.type === "") return target.directive === "" ? null : { kind: "home", member: `@${target.directive}` };
  return { kind: "type", name: target.type, member: target.member || undefined };
}
