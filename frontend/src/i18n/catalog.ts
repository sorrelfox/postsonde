// The English catalogue (UI-10): one namespace per area of the frontend,
// each in its own module under en/, so that areas can add messages at the
// same time without touching the same file. A key is
// "<namespace>.<message id>": t("shell.statusBar.ready"). See
// docs/frontend.md#localization.
import app from "./en/app";
import commands from "./en/commands";
import common from "./en/common";
import consoleMessages from "./en/console";
import cookies from "./en/cookies";
import editor from "./en/editor";
import editorProtocols from "./en/editorProtocols";
import environments from "./en/environments";
import history from "./en/history";
import lib from "./en/lib";
import messagelog from "./en/messagelog";
import response from "./en/response";
import runner from "./en/runner";
import servers from "./en/servers";
import settings from "./en/settings";
import shell from "./en/shell";
import stores from "./en/stores";
import styles from "./en/styles";
import tabs from "./en/tabs";
import tree from "./en/tree";
import workspace from "./en/workspace";
import type { Message, Messages, ParamsOf, ParamValue } from "./messages";

/** English, the source language: every message there is. */
export const en = {
  app,
  commands,
  common,
  console: consoleMessages,
  cookies,
  editor,
  editorProtocols,
  environments,
  history,
  lib,
  messagelog,
  response,
  runner,
  servers,
  settings,
  shell,
  stores,
  styles,
  tabs,
  tree,
  workspace,
} as const satisfies Readonly<Record<string, Messages>>;

export type Catalogue = typeof en;
export type Namespace = keyof Catalogue;

/** Every message key: "<namespace>.<id>". */
export type MessageKey = {
  [N in Namespace]: `${N}.${keyof Catalogue[N] & string}`;
}[Namespace];

/** The English message of a key. */
export type MessageOf<K extends MessageKey> = K extends `${infer N extends Namespace}.${infer Id}`
  ? Id extends keyof Catalogue[N]
    ? Catalogue[N][Id]
    : never
  : never;

/** The parameters t() takes for a key: none, or an object with every placeholder. */
export type TArgs<K extends MessageKey> = [keyof ParamsOf<MessageOf<K>>] extends [never]
  ? [params?: undefined]
  : [params: ParamsOf<MessageOf<K>>];

/** The parameters tParts() takes for a key: as t(), but a value may also be a V (a React node). */
export type TPartsArgs<K extends MessageKey, V> = [keyof ParamsOf<MessageOf<K>>] extends [never]
  ? [params?: undefined]
  : [params: { readonly [P in keyof ParamsOf<MessageOf<K>>]: P extends "count" ? number : ParamValue | V }];

/**
 * A translation: some or all of the English namespaces, message by
 * message. A message it lacks is shown in English. A plural message may use
 * the plural categories of its own language.
 */
export type Translation = {
  readonly [N in Namespace]?: { readonly [Id in keyof Catalogue[N]]?: Message };
};

/** Error messages by Go error code (en/errors.ts); not part of the typed keys. */
export type ErrorMessages = Readonly<Record<string, string>>;
