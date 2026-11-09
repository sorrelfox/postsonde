// GraphQL mock server commands (SSRV-08): change a server's schema,
// generated-data settings and overrides through the Go ServersService,
// and describe a configuration's schema for the editor. See
// docs/servers.md.
//
// Failed calls reject with an AppError (lib/errors.ts).

import { toAppError } from "../lib/errors";
import * as api from "../lib/servers";
import type { GraphQLMockConfig, GraphQLMockOverride, LocalServer, Stores } from "../stores";
import { toLocalServer } from "./servers";

async function run<T>(call: () => Promise<T>): Promise<T> {
  try {
    return await call();
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/** The DTO of a configuration, for UpdateConfig and DescribeGraphQLMock. */
export function toGraphQLMockConfigDTO(config: GraphQLMockConfig): api.GraphQLMockConfigDTO {
  return {
    path: config.path,
    schemaFile: config.schemaFile,
    schemaText: config.schemaText,
    introspection: config.introspection,
    listLength: config.listLength,
    nullPercent: config.nullPercent,
    seed: config.seed,
    allowOrigin: config.allowOrigin,
    overrides: config.overrides.map((o) => ({ ...o })),
  };
}

/**
 * Replaces a GraphQL mock server's configuration; a running server loads
 * the schema again and answers with the new overrides at once. A saved
 * server writes it to its folder too.
 */
export async function updateGraphQLMockConfig(stores: Stores, id: string, config: GraphQLMockConfig): Promise<LocalServer> {
  const dto = await run(() =>
    api.updateServerConfig(id, { mockHttp: null, webSocket: null, webhook: null, graphqlMock: toGraphQLMockConfigDTO(config) }),
  );
  const server = toLocalServer(dto);
  stores.servers.dispatch({ type: "servers/put", server });
  return server;
}

/** A new override of `target` (a field or a scalar or enum type), answering null until a value is written. */
export function newGraphQLMockOverride(target: string): GraphQLMockOverride {
  return { enabled: true, target, args: "", mode: "value", value: "", error: "", code: "" };
}

/** The endpoint URL a client posts to ("http://localhost:4000/graphql"), or null when not running. */
export function graphQLMockUrl(server: LocalServer): string | null {
  if (server.state !== "running" || server.boundPort <= 0 || server.graphqlMock === null) return null;
  const scheme = server.tls === null ? "http" : "https";
  return `${scheme}://localhost:${server.boundPort}${server.graphqlMock.path}`;
}

/** Whether text is valid JSON ("" counts, as null). */
export function isJsonText(text: string): boolean {
  if (text.trim() === "") return true;
  try {
    JSON.parse(text);
    return true;
  } catch {
    return false;
  }
}
