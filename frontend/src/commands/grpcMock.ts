// gRPC mock server commands (SSRV-07): change a server's .proto files and
// canned responses through the Go ServersService. The method list, message
// examples and checks come from the gRPC service (lib/grpc.ts: Describe,
// ExampleMessage and ValidateMessage with output set). See docs/servers.md.
//
// Failed calls reject with an AppError (lib/errors.ts).

import { toAppError } from "../lib/errors";
import * as api from "../lib/servers";
import type { GrpcMockConfig, GrpcMockMethod, LocalServer, Stores } from "../stores";
import { toLocalServer } from "./servers";

async function run<T>(call: () => Promise<T>): Promise<T> {
  try {
    return await call();
  } catch (err: unknown) {
    throw toAppError(err);
  }
}

/** The DTO of a configuration, for UpdateConfig. */
export function toGrpcMockConfigDTO(config: GrpcMockConfig): api.GRPCMockConfigDTO {
  return {
    files: [...config.files],
    importPaths: [...config.importPaths],
    reflection: config.reflection,
    methods: config.methods.map((m) => ({
      service: m.service,
      method: m.method,
      messages: [...m.messages],
      status: m.status,
      statusMessage: m.statusMessage,
      headers: m.headers.map((h) => ({ ...h })),
      trailers: m.trailers.map((h) => ({ ...h })),
      delayMs: m.delayMs,
      intervalMs: m.intervalMs,
      bidi: m.bidi,
    })),
  };
}

/**
 * Replaces a gRPC mock server's configuration; a running server links the
 * files again and answers with the new responses at once. A saved server
 * writes it to its folder too.
 */
export async function updateGrpcMockConfig(stores: Stores, id: string, config: GrpcMockConfig): Promise<LocalServer> {
  const dto = await run(() => api.updateServerConfig(id, { mockHttp: null, webSocket: null, webhook: null, grpcMock: toGrpcMockConfigDTO(config) }));
  const server = toLocalServer(dto);
  stores.servers.dispatch({ type: "servers/put", server });
  return server;
}

/** A new canned response for a method: OK with `message` (an example of its output type), or none. */
export function newGrpcMockMethod(service: string, method: string, message: string | null): GrpcMockMethod {
  return {
    service,
    method,
    messages: message === null ? [] : [message],
    status: 0,
    statusMessage: "",
    headers: [],
    trailers: [],
    delayMs: 0,
    intervalMs: 0,
    bidi: "each",
  };
}

/** The address a gRPC client dials ("localhost:50051"), or null when not running. */
export function grpcMockAddress(server: LocalServer): string | null {
  return server.state === "running" && server.boundPort > 0 ? `localhost:${server.boundPort}` : null;
}

/** A grpcurl command that lists the services by reflection. */
export function grpcurlListCommand(server: LocalServer, address: string): string {
  // i18n-ignore-start: shell commands
  if (server.tls === null) return `grpcurl -plaintext ${address} list`;
  return server.tls.certificate === "generated" ? `grpcurl -cacert ca.pem ${address} list` : `grpcurl ${address} list`;
  // i18n-ignore-end
}

/** The gRPC status codes, 0 to 16, with their names. */
export const GRPC_STATUS_CODES: readonly { code: number; name: string }[] = [
  "OK",
  "CANCELLED",
  "UNKNOWN",
  "INVALID_ARGUMENT",
  "DEADLINE_EXCEEDED",
  "NOT_FOUND",
  "ALREADY_EXISTS",
  "PERMISSION_DENIED",
  "RESOURCE_EXHAUSTED",
  "FAILED_PRECONDITION",
  "ABORTED",
  "OUT_OF_RANGE",
  "UNIMPLEMENTED",
  "INTERNAL",
  "UNAVAILABLE",
  "DATA_LOSS",
  "UNAUTHENTICATED",
].map((name, code) => ({ code, name }));
