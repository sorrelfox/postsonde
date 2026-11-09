// Pure helpers of the new-folder and new-request dialogs (COL-04, creating
// items): the request kinds offered, name validation, the folders an item
// can go into, the default target and a preview of the file path Go will
// write. See docs/frontend.md (New folder and new request).

import { t, type MessageKey } from "../../i18n";
import type { NewRequestKind } from "../../lib/collection";
import type { CollectionState, NodeId, TreeNode } from "../../stores";

export interface RequestKindInfo {
  readonly kind: NewRequestKind;
  /** The protocol's name: not translated. */
  readonly label: string;
  /** The message of its one-line description: t(descriptionKey) where it is shown. */
  readonly descriptionKey: MessageKey;
}

function requestKind(kind: NewRequestKind, label: string, descriptionKey: MessageKey): RequestKindInfo {
  return { kind, label, descriptionKey };
}

/** Every request kind the app can create, in the design's order. */
export const REQUEST_KINDS: readonly RequestKindInfo[] = [
  // i18n-ignore-start: protocol names
  requestKind("http", "HTTP", "tree.kind.http"),
  requestKind("graphql", "GraphQL", "tree.kind.graphql"),
  requestKind("websocket", "WebSocket", "tree.kind.websocket"),
  requestKind("tcp", "TCP", "tree.kind.tcp"),
  requestKind("udp", "UDP", "tree.kind.udp"),
  requestKind("grpc", "gRPC", "tree.kind.grpc"),
  requestKind("sse", "SSE", "tree.kind.sse"),
  requestKind("socketio", "Socket.IO", "tree.kind.socketio"),
  // i18n-ignore-end
];

/** Longest name accepted, in characters. */
export const MAX_NAME_LENGTH = 200;

/** The top level of the project, as a target folder. */
export const TOP_LEVEL = "";

/**
 * Why `name` cannot be used for a new item in the folder `parentId`
 * (TOP_LEVEL for the top level), or null when it can. An empty name is
 * invalid but has no message: the confirm button is just disabled.
 * Siblings with the same name (ignoring case and surrounding spaces) are
 * refused, so two items in a folder never look the same. `exceptId` is the
 * item being renamed: its own name does not count as taken.
 */
export function validateName(state: CollectionState, parentId: NodeId, name: string, exceptId?: NodeId): string | null {
  const trimmed = name.trim();
  if (trimmed === "") return "";
  if ([...trimmed].length > MAX_NAME_LENGTH) return t("tree.nameCheck.tooLong", { max: MAX_NAME_LENGTH });
  if ([...trimmed].some(isControl)) return t("tree.nameCheck.control");
  const folded = trimmed.toLocaleLowerCase();
  const taken = siblingIds(state, parentId).some(
    (id) => id !== exceptId && state.nodes[id]?.name.trim().toLocaleLowerCase() === folded,
  );
  return taken ? t("tree.nameCheck.taken") : null;
}

/** C0 control characters and DEL (line breaks, tabs, NUL). */
function isControl(ch: string): boolean {
  const code = ch.codePointAt(0) ?? 0;
  return code < 0x20 || code === 0x7f;
}

function siblingIds(state: CollectionState, parentId: NodeId): readonly NodeId[] {
  if (parentId === TOP_LEVEL) return state.rootIds;
  const parent: TreeNode | undefined = state.nodes[parentId];
  return parent?.kind === "folder" ? parent.childIds : [];
}

export interface FolderOption {
  /** The folder's id, or TOP_LEVEL. */
  readonly id: NodeId;
  readonly name: string;
  /** 0 for the top level, 1 for top-level folders, … */
  readonly depth: number;
  /** A read-only folder (its _folder.json cannot be written) takes no new items. */
  readonly disabled: boolean;
}

/**
 * The folders a new item can go into: the top level first, then every
 * folder of the tree depth first in display order, collapsed ones too.
 */
export function folderOptions(state: CollectionState, topLevelName: string): FolderOption[] {
  const out: FolderOption[] = [{ id: TOP_LEVEL, name: topLevelName, depth: 0, disabled: false }];
  const visit = (ids: readonly NodeId[], depth: number) => {
    for (const id of ids) {
      const node: TreeNode | undefined = state.nodes[id];
      if (node?.kind !== "folder" || depth > 1000) continue;
      out.push({ id, name: node.name, depth, disabled: node.readOnly !== "" });
      visit(node.childIds, depth + 1);
    }
  };
  visit(state.rootIds, 1);
  return out;
}

/**
 * The folder a new item goes into by default, from the node it was asked
 * for (a context menu's row, or the tree's selection): that folder, or the
 * folder of an item, else the top level. A read-only folder gives the top level.
 */
export function defaultTarget(state: CollectionState, from: NodeId | null): NodeId {
  if (from === null) return TOP_LEVEL;
  const node: TreeNode | undefined = state.nodes[from];
  if (node === undefined) return TOP_LEVEL;
  const folderId = node.kind === "folder" ? node.id : node.parentId;
  if (folderId === null) return TOP_LEVEL;
  const folder: TreeNode | undefined = state.nodes[folderId];
  return folder?.kind === "folder" && folder.readOnly === "" ? folder.id : TOP_LEVEL;
}

// File names: a port of Slug in internal/core/collection/slug.go, used only
// to preview where the item will be written. Go picks the real name.

const MAX_SLUG_LENGTH = 64;
const FALLBACK_SLUG = "untitled";

const TRANSLIT: ReadonlyMap<string, string> = (() => {
  const groups: [string, string][] = [
    ["a", "àáâãäåāăąǎ"],
    ["c", "çćĉċč"],
    ["d", "ďđð"],
    ["e", "èéêëēĕėęěẽ"],
    ["g", "ĝğġģ"],
    ["h", "ĥħ"],
    ["i", "ìíîïĩīĭįıǐ"],
    ["j", "ĵ"],
    ["k", "ķ"],
    ["l", "ĺļľŀł"],
    ["n", "ñńņňŉ"],
    ["o", "òóôõöøōŏőǒ"],
    ["r", "ŕŗř"],
    ["s", "śŝşšș"],
    ["t", "ţťŧț"],
    ["u", "ùúûüũūŭůűųǔ"],
    ["w", "ŵ"],
    ["y", "ýÿŷ"],
    ["z", "źżž"],
    ["ae", "æ"],
    ["oe", "œ"],
    ["ss", "ß"],
    ["th", "þ"],
    ["ij", "ĳ"],
    ["a", "а"],
    ["b", "б"],
    ["v", "в"],
    ["g", "гґ"],
    ["d", "д"],
    ["e", "еёэ"],
    ["ye", "є"],
    ["zh", "ж"],
    ["z", "з"],
    ["i", "иійї"],
    ["k", "к"],
    ["l", "л"],
    ["m", "м"],
    ["n", "н"],
    ["o", "о"],
    ["p", "п"],
    ["r", "р"],
    ["s", "с"],
    ["t", "т"],
    ["u", "уў"],
    ["f", "ф"],
    ["kh", "х"],
    ["ts", "ц"],
    ["ch", "ч"],
    ["sh", "ш"],
    ["shch", "щ"],
    ["y", "ы"],
    ["yu", "ю"],
    ["ya", "я"],
  ];
  const map = new Map<string, string>();
  for (const [to, from] of groups) for (const ch of from) map.set(ch, to);
  return map;
})();

const DROPPED = new Set(["'", String.fromCodePoint(0x2019), "ъ", "ь"]);

/** The base of the file or directory name Go gives an item called `name` (collection.Slug). */
export function slug(name: string): string {
  let out = "";
  let pendingHyphen = false;
  for (const ch of name.toLowerCase()) {
    if (DROPPED.has(ch) || /^\p{M}$/u.test(ch)) continue;
    const keep = /^[a-z0-9]$/.test(ch) ? ch : (TRANSLIT.get(ch) ?? "");
    if (keep === "") {
      pendingHyphen = out.length > 0;
      continue;
    }
    if (pendingHyphen) {
      out += "-";
      pendingHyphen = false;
    }
    out += keep;
  }
  if (out.length > MAX_SLUG_LENGTH) {
    let cut = out.slice(0, MAX_SLUG_LENGTH);
    const i = cut.lastIndexOf("-");
    if (out[MAX_SLUG_LENGTH] !== "-" && i > 0) cut = cut.slice(0, i);
    out = cut.replace(/-+$/, "");
  }
  return out === "" ? FALLBACK_SLUG : out;
}

const WINDOWS_DEVICE = /^(con|prn|aux|nul|com[0-9]|lpt[0-9])$/;

/**
 * The slash path, relative to the project folder, that a new item would
 * get: `orders/refund-order.json` for a request, `orders/payments/_folder.json`
 * for a folder. Collisions with the folder's items get "-2", "-3", … as in
 * Go; files that are not in the tree (fixtures, scripts) are not known
 * here, so Go may still pick another name.
 */
export function previewPath(state: CollectionState, parentId: NodeId, name: string, isFolder: boolean): string {
  const parent: TreeNode | undefined = parentId === TOP_LEVEL ? undefined : state.nodes[parentId];
  const dir = parent?.kind === "folder" ? parent.path : "";
  const taken = new Set(
    siblingIds(state, parentId).map((id) => {
      const path = state.nodes[id]?.path ?? "";
      return path.slice(path.lastIndexOf("/") + 1).toLowerCase();
    }),
  );
  if (dir === "") taken.add("postsonde.json").add("environments");
  const ext = isFolder ? "" : ".json";
  const entry = uniqueEntry(taken, slug(name), ext);
  const prefix = dir === "" ? "" : `${dir}/`;
  return isFolder ? `${prefix}${entry}/_folder.json` : `${prefix}${entry}`;
}

/** `base` + `ext`, or with "-2", "-3", … while that is taken (lower-case entries) or a Windows device name. */
function uniqueEntry(taken: ReadonlySet<string>, base: string, ext: string): string {
  let entry = base + ext;
  for (let n = 2; taken.has(entry) || WINDOWS_DEVICE.test(entry.replace(/\..*$/, "")); n++) entry = `${base}-${n}${ext}`;
  return entry;
}

// Renaming (COL-04).

/**
 * Why item `id` cannot be renamed to `name`, or null when it can; "" (no
 * message) for an empty name and for its current name, where there is
 * nothing to do. Same rules as validateName in its folder, not counting
 * the item itself.
 */
export function validateRename(state: CollectionState, id: NodeId, name: string): string | null {
  const node: TreeNode | undefined = state.nodes[id];
  if (node === undefined) return "";
  if (name.trim() === node.name) return "";
  return validateName(state, node.parentId ?? TOP_LEVEL, name, id);
}

/**
 * Where renaming item `id` to `name` moves its file or directory, as slash
 * paths relative to the project folder (a folder's with a trailing slash),
 * or null when it stays: a port of renamedEntry in
 * internal/core/collection/ops.go. Like previewPath, only the folder's
 * items are known here, so Go may still pick another name.
 */
export function renamePreview(state: CollectionState, id: NodeId, name: string): { from: string; to: string } | null {
  const node: TreeNode | undefined = state.nodes[id];
  if (node === undefined || (node.kind !== "folder" && node.kind !== "request") || name.trim() === "") return null;
  const isFolder = node.kind === "folder";
  const ext = isFolder ? "" : ".json";
  const cut = node.path.lastIndexOf("/");
  const prefix = node.path.slice(0, cut + 1);
  const entry = node.path.slice(cut + 1);
  const base = entry.endsWith(ext) ? entry.slice(0, entry.length - ext.length) : entry;
  const target = slug(name.trim());
  if (base === target) return null;

  const taken = new Set(
    siblingIds(state, node.parentId ?? TOP_LEVEL)
      .filter((other) => other !== id)
      .map((other) => {
        const path = state.nodes[other]?.path ?? "";
        return path.slice(path.lastIndexOf("/") + 1).toLowerCase();
      }),
  );
  if (prefix === "") taken.add("postsonde.json").add("environments");
  // "orders-2" renamed to "Orders" keeps its name while "orders" is taken.
  const rest = base.startsWith(`${target}-`) ? base.slice(target.length + 1) : "";
  const next = /^[0-9]+$/.test(rest) && taken.has(target + ext) ? base + ext : uniqueEntry(taken, target, ext);
  if (next === entry) return null;
  const slash = isFolder ? "/" : "";
  return { from: `${node.path}${slash}`, to: `${prefix}${next}${slash}` };
}
