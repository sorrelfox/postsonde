// Calls into the Go GRPCService (internal/app/services/grpc) and the gRPC
// parts of CollectionService: describing a request's .proto setup for the
// service and method picker (GRPC-01, GRPC-02, GRPC-10), the native pickers
// for .proto files and import paths, folder setups that requests can
// inherit, calls of every kind (GRPC-11 to GRPC-14, GRPC-18, GRPC-21),
// which run as grpc operations, the request stream of client-streaming and
// bidi calls (GRPC-22), and the request message's
// example, validation and autocomplete (GRPC-15, GRPC-16, GRPC-17). See
// docs/grpc.md.
import {
  CollectionService,
  type GRPCProtoDTO,
  type InheritedGRPCProtoDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/collection";
import {
  GRPCService,
  type CallEventDTO,
  type DescribeResultDTO,
  type DiagnosticDTO,
  type ExampleDTO,
  type CallRefDTO,
  type InvokeInputDTO,
  type MessageCheckDTO,
  type MessageCompleteInputDTO,
  type MessageCompletionDTO,
  type MessageCompletionItemDTO,
  type MessageErrorDTO,
  type MessageInputDTO,
  type MetadataDTO,
  type MethodDTO,
  type RichStatusDTO,
  type SendMessageInputDTO,
  type SentMessageDTO,
  type ServiceDTO,
  type StatusDetailDTO,
  type StatusDTO,
} from "../../bindings/github.com/sorrelfox/postsonde/internal/app/services/grpc";
import { t, type MessageKey } from "../i18n";
import type { CancellablePromise } from "./calls";
import type { RequestDTO } from "./collection";
import type { SnippetDTO, SnippetLanguageDTO } from "./http";
import type { StartedDTO } from "./operations";

export type {
  CallEventDTO,
  CallRefDTO,
  DescribeResultDTO,
  DiagnosticDTO,
  ExampleDTO,
  GRPCProtoDTO,
  InheritedGRPCProtoDTO,
  InvokeInputDTO,
  MessageCheckDTO,
  MessageCompleteInputDTO,
  MessageCompletionDTO,
  MessageCompletionItemDTO,
  MessageErrorDTO,
  MessageInputDTO,
  MetadataDTO,
  MethodDTO,
  RichStatusDTO,
  SendMessageInputDTO,
  SentMessageDTO,
  ServiceDTO,
  StatusDetailDTO,
  StatusDTO,
};

/** The domain of call operations: their events are `grpc:op:data|progress|done`. */
export const GRPC_DOMAIN = "grpc";

/**
 * Starts a call. Resolves with the operation ID at once; the request, the
 * headers, each response message and the end arrive as `grpc:op:data`
 * items (follow them with startOperation). Input Go cannot send rejects.
 */
export function invokeGrpc(input: InvokeInputDTO): CancellablePromise<StartedDTO> {
  return GRPCService.Invoke(input);
}

/**
 * Sends one message on the request stream of a running client-streaming
 * or bidi call (GRPC-13, GRPC-14). Resolves once the transport has taken
 * it; its `sent` item follows as call data. Rejects for a call that is not
 * running (grpc.call_not_running), after closeGrpcSend (grpc.send_closed)
 * and for a message that does not fit the input type.
 */
export function sendGrpcMessage(input: SendMessageInputDTO): CancellablePromise<SentMessageDTO> {
  return GRPCService.SendMessage(input);
}

/** Ends the request stream of a running client-streaming or bidi call (half-close). */
export function closeGrpcSend(input: CallRefDTO): CancellablePromise<void> {
  return GRPCService.CloseSend(input);
}

/** How a gRPC call goes on the wire (GRPC-25): HTTP/2, or gRPC-Web with binary or base64 framing. */
export type GrpcTransport = "grpc" | "grpc-web" | "grpc-web-text";

/** The request's transport; missing or unknown values are plain gRPC. */
export function grpcTransport(request: RequestDTO): GrpcTransport {
  const t = request.settings.transport;
  return t === "grpc-web" || t === "grpc-web-text" ? t : "grpc";
}

/** The request's compression (GRPC-24): "gzip", or "identity" for none. */
export function grpcCompression(request: RequestDTO): "identity" | "gzip" {
  return request.settings.compression === "gzip" ? "gzip" : "identity";
}

/** Whether calls of this kind can go over the transport: gRPC-Web cannot stream from the client. */
export function transportAllows(transport: GrpcTransport, kind: string): boolean {
  return transport === "grpc" || !streamsFromClient(kind);
}

/** The Invoke input of request `request` with its effective proto setup (its own or its folder's). */
export function invokeInput(request: RequestDTO, setup: ProtoSetup): InvokeInputDTO {
  const settings = request.settings;
  return {
    requestId: request.id,
    files: [...setup.files],
    importPaths: [...setup.importPaths],
    address: request.address.trim(),
    transport: grpcTransport(request),
    compression: grpcCompression(request),
    tls: settings.tls === true,
    verifyTls: settings.verifyTls,
    connectTimeoutMs: settings.connectTimeoutMs,
    deadlineMs: settings.deadlineMs,
    service: request.service,
    method: request.method,
    message: request.message,
    metadata: (request.metadata ?? []).map((r) => ({ name: r.name, value: r.value, enabled: r.enabled })),
  };
}

/**
 * The request message of `service`/`method` in `setup`, with `message` to
 * validate; with `output`, its response message (the gRPC mock server's
 * canned responses, SSRV-07).
 */
export function messageInput(setup: ProtoSetup, service: string, method: string, message = "", output = false): MessageInputDTO {
  return { files: [...setup.files], importPaths: [...setup.importPaths], service, method, message, output };
}

/**
 * A template of the method's request message: every field at its default,
 * one element per list and map, recursive types expanded once (GRPC-16).
 * Rejects when the method is not in the setup.
 */
export function exampleMessage(input: MessageInputDTO): CancellablePromise<ExampleDTO> {
  return GRPCService.ExampleMessage(input);
}

/**
 * Checks the message JSON against the method's input type as Invoke parses
 * it (GRPC-15). Invalid JSON is a result with a position in UTF-16 units;
 * rejects when the method is not in the setup.
 */
export function validateMessage(input: MessageInputDTO): CancellablePromise<MessageCheckDTO> {
  return GRPCService.ValidateMessage(input);
}

/**
 * Field-name and enum suggestions at `offset` (UTF-16 units) of the
 * message JSON (GRPC-17), from the setup's cached descriptors. Rejects
 * when the method is not in the setup.
 */
export function completeMessage(input: MessageCompleteInputDTO): CancellablePromise<MessageCompletionDTO> {
  return GRPCService.CompleteMessage(input);
}

/** Whether message `text` counts as untouched next to `example`: the same JSON, whitespace outside strings aside. */
export function sameJson(text: string, example: string): boolean {
  try {
    return JSON.stringify(JSON.parse(text)) === JSON.stringify(JSON.parse(example));
  } catch {
    return false;
  }
}

/** Whether a method of this kind can be called: every known kind. */
export function canInvoke(kind: string): boolean {
  return kind === "unary" || kind === "server_stream" || kind === "client_stream" || kind === "bidi";
}

/** Whether calls of this kind stream from the client: messages are sent one at a time after Invoke opens the stream. */
export function streamsFromClient(kind: string): boolean {
  return kind === "client_stream" || kind === "bidi";
}

/** "NOT_FOUND: no such order", or just the name. */
export function statusText(status: StatusDTO): string {
  return status.message !== "" ? `${status.name}: ${status.message}` : status.name;
}

/** A method's call type, as MethodDTO.kind. */
export type GrpcMethodKind = "unary" | "client_stream" | "server_stream" | "bidi";

/** The .proto files and import paths to load, as stored paths. */
export interface ProtoSetup {
  files: readonly string[];
  importPaths: readonly string[];
}

/**
 * Loads and links a setup and lists its services and diagnostics. Problems
 * with the files are diagnostics, not rejections. `reload` skips Go's cache.
 */
export function describeProto(setup: ProtoSetup, reload = false): CancellablePromise<DescribeResultDTO> {
  return GRPCService.Describe({ files: [...setup.files], importPaths: [...setup.importPaths], reload });
}

/** Native dialog for .proto files; resolves to their stored paths, [] when cancelled. */
export async function pickProtoFiles(): Promise<string[]> {
  return (await GRPCService.PickProtoFiles()) ?? [];
}

/** Native folder dialog for an import path; resolves to its stored path, "" when cancelled. */
export function pickImportPath(): CancellablePromise<string> {
  return GRPCService.PickImportPath();
}

/** The setup request `id` would inherit from its folders, and the folder it is in. */
export function getInheritedProto(id: string): CancellablePromise<InheritedGRPCProtoDTO> {
  return CollectionService.InheritedGRPCProto(id);
}

/** Writes folder `folderId`'s shared setup at once; empty lists remove it. */
export function updateFolderProto(folderId: string, setup: ProtoSetup): CancellablePromise<void> {
  return CollectionService.UpdateFolderGRPCProto({ folderId, files: [...setup.files], importPaths: [...setup.importPaths] });
}

const KIND_LABELS: Readonly<Record<GrpcMethodKind, MessageKey>> = {
  unary: "lib.grpc.kind.unary",
  client_stream: "lib.grpc.kind.clientStream",
  server_stream: "lib.grpc.kind.serverStream",
  bidi: "lib.grpc.kind.bidi",
};

/** The badge text of a method kind; an unknown kind is shown as it is. */
export function methodKindLabel(kind: string): string {
  const key = KIND_LABELS[kind as GrpcMethodKind] as MessageKey | undefined;
  return key === undefined ? kind : t(key);
}

/** The services whose name, or any of whose methods' names, contain `query` (case-insensitive), with only the matching methods unless the service name matches. */
export function filterServices(services: readonly ServiceDTO[], query: string): ServiceDTO[] {
  const q = query.trim().toLowerCase();
  if (q === "") return [...services];
  const out: ServiceDTO[] = [];
  for (const s of services) {
    if (s.fullName.toLowerCase().includes(q)) {
      out.push(s);
      continue;
    }
    const methods = (s.methods ?? []).filter((m) => m.name.toLowerCase().includes(q));
    if (methods.length > 0) out.push({ ...s, methods });
  }
  return out;
}

/** The method `service`/`method` in a describe result, or null. */
export function findMethod(
  services: readonly ServiceDTO[],
  service: string,
  method: string,
): { service: ServiceDTO; method: MethodDTO } | null {
  const s = services.find((x) => x.fullName === service);
  const m = s?.methods?.find((x) => x.name === method);
  return s !== undefined && m !== undefined ? { service: s, method: m } : null;
}

/** `file:line:column` of a diagnostic (just the file for whole-file problems). */
export function diagnosticLocation(d: DiagnosticDTO): string {
  const file = d.file !== "" ? d.file : d.path;
  return d.line > 0 ? `${file}:${d.line}:${d.column}` : file;
}

/** A setup's stable key, to tell when it changed. */
export function setupKey(setup: ProtoSetup): string {
  return JSON.stringify([setup.files, setup.importPaths]);
}

/**
 * `call` as code in `language` (a `SnippetLanguageDTO.id` of
 * grpcSnippetLanguages), for the Code tab (GEN-10): what Invoke would
 * send, the credentials of authorization metadata masked unless
 * `revealSecrets`. Nothing is dialed. See docs/snippets.md#grpcurl.
 */
export function grpcSnippet(call: InvokeInputDTO, language: string, revealSecrets: boolean): CancellablePromise<SnippetDTO> {
  return GRPCService.Snippet({ call, language, revealSecrets });
}

/** The languages of the gRPC Code tab, in the order it lists them. */
export async function grpcSnippetLanguages(): Promise<SnippetLanguageDTO[]> {
  return (await GRPCService.SnippetLanguages()) ?? [];
}
