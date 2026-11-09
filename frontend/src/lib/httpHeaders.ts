// Built-in request header data for the header table's suggestions
// (HTTP-07): common names of the IANA HTTP field registry plus widely used
// non-standard ones, each with a short description, and common values.
// Data only: filtering is lib/headerSuggestions.ts. See docs/frontend.md
// (Request editor).
//
// Rules, checked by httpHeaders.test.ts:
// - HTTP_HEADERS is sorted by name (case-insensitively), and names are
//   unique case-insensitively. `name` is the canonical spelling inserted.
// - Values of one header are unique; they keep the order given here (most
//   common first), which is the order suggestions show them in.
// - `client` notes follow what internal/core/httpclient does with the
//   header (docs/http-client.md) and are shown as a hint.
// - Descriptions and notes are message keys (lib.httpHeaders.*, UI-10),
//   looked up by headerSuggestions.ts when it suggests; names and values
//   are protocol text and stay as they are.

import type { MessageKey } from "../i18n";

export interface HeaderValueInfo {
  readonly value: string;
  readonly descriptionKey?: MessageKey;
}

export interface HeaderInfo {
  /** Canonical spelling. */
  readonly name: string;
  readonly descriptionKey: MessageKey;
  /** Common values, most used first. */
  readonly values?: readonly HeaderValueInfo[];
  /** The value is a comma-separated list: values are suggested for the item being typed. */
  readonly list?: boolean;
  /** What PostSonde's HTTP client does with this header itself (docs/http-client.md). */
  readonly clientKey?: MessageKey;
}

const v = (value: string, descriptionKey?: MessageKey): HeaderValueInfo =>
  descriptionKey === undefined ? { value } : { value, descriptionKey };

// i18n-ignore-start: header names and values are protocol text; the text is in message keys
const MEDIA_TYPES: readonly HeaderValueInfo[] = [
  v("application/json"),
  v("application/xml"),
  v("text/plain"),
  v("text/html"),
  v("application/x-www-form-urlencoded"),
  v("multipart/form-data"),
  v("application/octet-stream"),
  v("application/graphql-response+json"),
  v("application/ld+json"),
  v("application/merge-patch+json"),
  v("application/json-patch+json"),
  v("application/problem+json"),
  v("application/vnd.api+json"),
  v("application/x-ndjson"),
  v("application/yaml"),
  v("application/pdf"),
  v("application/zip"),
  v("application/grpc-web+proto"),
  v("text/csv"),
  v("text/css"),
  v("text/javascript"),
  v("text/xml"),
  v("text/event-stream"),
  v("image/png"),
  v("image/jpeg"),
  v("image/svg+xml"),
  v("image/webp"),
  v("application/json; charset=utf-8"),
  v("text/plain; charset=utf-8"),
  v("text/html; charset=utf-8"),
];

const ACCEPT_TYPES: readonly HeaderValueInfo[] = [
  v("*/*", "lib.httpHeaders.value.anyMediaType"),
  v("application/json"),
  v("application/xml"),
  v("text/plain"),
  v("text/html"),
  v("text/event-stream"),
  v("application/graphql-response+json"),
  v("application/problem+json"),
  v("application/octet-stream"),
  v("application/x-ndjson"),
  v("text/csv"),
  v("image/*"),
];

const ENCODINGS: readonly HeaderValueInfo[] = [
  v("gzip"),
  v("br", "lib.httpHeaders.value.brotli"),
  v("deflate"),
  v("zstd"),
  v("identity", "lib.httpHeaders.value.noEncoding"),
];

const CACHE_CONTROL: readonly HeaderValueInfo[] = [
  v("no-cache", "lib.httpHeaders.value.noCache"),
  v("no-store", "lib.httpHeaders.value.noStore"),
  v("max-age=0"),
  v("max-age=3600"),
  v("max-stale"),
  v("min-fresh=60"),
  v("no-transform"),
  v("only-if-cached"),
];

const LANGUAGES: readonly HeaderValueInfo[] = [
  v("en-US"),
  v("en"),
  v("en-GB"),
  v("de-DE"),
  v("fr-FR"),
  v("es-ES"),
  v("ja-JP"),
  v("zh-CN"),
  v("ru-RU"),
  v("*", "lib.httpHeaders.value.anyLanguage"),
  v("en-US,en;q=0.9", "lib.httpHeaders.value.usEnglish"),
];

const DROPPED_ON_H2: MessageKey = "lib.httpHeaders.client.droppedOnH2";

export const HTTP_HEADERS: readonly HeaderInfo[] = [
  { name: "A-IM", descriptionKey: "lib.httpHeaders.aIm" },
  { name: "Accept", descriptionKey: "lib.httpHeaders.accept", values: ACCEPT_TYPES, list: true },
  {
    name: "Accept-Charset",
    descriptionKey: "lib.httpHeaders.acceptCharset",
    values: [v("utf-8"), v("iso-8859-1")],
    list: true,
  },
  {
    name: "Accept-Encoding",
    descriptionKey: "lib.httpHeaders.acceptEncoding",
    values: ENCODINGS,
    list: true,
    clientKey: "lib.httpHeaders.client.acceptEncoding",
  },
  { name: "Accept-Language", descriptionKey: "lib.httpHeaders.acceptLanguage", values: LANGUAGES, list: true },
  { name: "Accept-Patch", descriptionKey: "lib.httpHeaders.acceptPatch", values: [v("application/json-patch+json"), v("application/merge-patch+json")], list: true },
  { name: "Access-Control-Request-Headers", descriptionKey: "lib.httpHeaders.accessControlRequestHeaders", list: true, values: [v("content-type"), v("authorization")] },
  {
    name: "Access-Control-Request-Method",
    descriptionKey: "lib.httpHeaders.accessControlRequestMethod",
    values: [v("GET"), v("POST"), v("PUT"), v("PATCH"), v("DELETE")],
  },
  {
    name: "Authorization",
    descriptionKey: "lib.httpHeaders.authorization",
    values: [v("Bearer ", "lib.httpHeaders.value.bearer"), v("Basic ", "lib.httpHeaders.value.basic"), v("Digest "), v("Negotiate "), v("AWS4-HMAC-SHA256 ")],
    clientKey: "lib.httpHeaders.client.authorization",
  },
  { name: "Cache-Control", descriptionKey: "lib.httpHeaders.cacheControl", values: CACHE_CONTROL, list: true },
  {
    name: "Connection",
    descriptionKey: "lib.httpHeaders.connection",
    values: [v("keep-alive"), v("close", "lib.httpHeaders.value.close"), v("Upgrade")],
    list: true,
    clientKey: "lib.httpHeaders.client.connection",
  },
  {
    name: "Content-Disposition",
    descriptionKey: "lib.httpHeaders.contentDisposition",
    values: [v('attachment; filename="file.txt"'), v("inline"), v('form-data; name="field"')],
  },
  {
    name: "Content-Encoding",
    descriptionKey: "lib.httpHeaders.contentEncoding",
    values: [v("gzip"), v("br", "lib.httpHeaders.value.brotli"), v("deflate"), v("zstd")],
    list: true,
  },
  { name: "Content-Language", descriptionKey: "lib.httpHeaders.contentLanguage", values: LANGUAGES, list: true },
  {
    name: "Content-Length",
    descriptionKey: "lib.httpHeaders.contentLength",
    clientKey: "lib.httpHeaders.client.contentLength",
  },
  { name: "Content-Location", descriptionKey: "lib.httpHeaders.contentLocation" },
  { name: "Content-MD5", descriptionKey: "lib.httpHeaders.contentMd5" },
  { name: "Content-Range", descriptionKey: "lib.httpHeaders.contentRange", values: [v("bytes 0-499/1234")] },
  {
    name: "Content-Type",
    descriptionKey: "lib.httpHeaders.contentType",
    values: MEDIA_TYPES,
    clientKey: "lib.httpHeaders.client.contentType",
  },
  {
    name: "Cookie",
    descriptionKey: "lib.httpHeaders.cookie",
    values: [v("name=value"), v("session=")],
    clientKey: "lib.httpHeaders.client.cookie",
  },
  { name: "Date", descriptionKey: "lib.httpHeaders.date", values: [v("Tue, 15 Sep 2026 08:00:00 GMT")] },
  { name: "Digest", descriptionKey: "lib.httpHeaders.digest" },
  { name: "DNT", descriptionKey: "lib.httpHeaders.dnt", values: [v("1"), v("0")] },
  { name: "Early-Data", descriptionKey: "lib.httpHeaders.earlyData", values: [v("1")] },
  { name: "Expect", descriptionKey: "lib.httpHeaders.expect", values: [v("100-continue", "lib.httpHeaders.value.continue")] },
  { name: "Forwarded", descriptionKey: "lib.httpHeaders.forwarded", values: [v("for=192.0.2.60;proto=https;by=203.0.113.43")] },
  { name: "From", descriptionKey: "lib.httpHeaders.from", values: [v("user@example.com")] },
  {
    name: "Host",
    descriptionKey: "lib.httpHeaders.host",
    clientKey: "lib.httpHeaders.client.host",
  },
  { name: "Idempotency-Key", descriptionKey: "lib.httpHeaders.idempotencyKey", values: [v("{{$uuid}}")] },
  { name: "If-Match", descriptionKey: "lib.httpHeaders.ifMatch", values: [v('"etag"'), v("*")], list: true },
  { name: "If-Modified-Since", descriptionKey: "lib.httpHeaders.ifModifiedSince", values: [v("Tue, 15 Sep 2026 08:00:00 GMT")] },
  { name: "If-None-Match", descriptionKey: "lib.httpHeaders.ifNoneMatch", values: [v('"etag"'), v("*")], list: true },
  { name: "If-Range", descriptionKey: "lib.httpHeaders.ifRange", values: [v('"etag"')] },
  { name: "If-Unmodified-Since", descriptionKey: "lib.httpHeaders.ifUnmodifiedSince", values: [v("Tue, 15 Sep 2026 08:00:00 GMT")] },
  { name: "Keep-Alive", descriptionKey: "lib.httpHeaders.keepAlive", values: [v("timeout=5, max=1000")], clientKey: DROPPED_ON_H2 },
  { name: "Max-Forwards", descriptionKey: "lib.httpHeaders.maxForwards", values: [v("10")] },
  { name: "Origin", descriptionKey: "lib.httpHeaders.origin", values: [v("{{baseUrl}}", "lib.httpHeaders.value.baseUrl"), v("null", "lib.httpHeaders.value.opaqueOrigin")] },
  { name: "Pragma", descriptionKey: "lib.httpHeaders.pragma", values: [v("no-cache")] },
  { name: "Prefer", descriptionKey: "lib.httpHeaders.prefer", values: [v("return=minimal"), v("return=representation"), v("respond-async"), v("wait=10"), v("handling=strict"), v("handling=lenient")], list: true },
  { name: "Priority", descriptionKey: "lib.httpHeaders.priority", values: [v("u=0"), v("u=3"), v("u=7"), v("i")], list: true },
  {
    name: "Proxy-Authorization",
    descriptionKey: "lib.httpHeaders.proxyAuthorization",
    values: [v("Basic "), v("Bearer ")],
    clientKey: "lib.httpHeaders.client.proxyAuthorization",
  },
  { name: "Proxy-Connection", descriptionKey: "lib.httpHeaders.proxyConnection", values: [v("keep-alive")], clientKey: DROPPED_ON_H2 },
  { name: "Range", descriptionKey: "lib.httpHeaders.range", values: [v("bytes=0-499"), v("bytes=500-"), v("bytes=-500")] },
  {
    name: "Referer",
    descriptionKey: "lib.httpHeaders.referer",
    values: [v("{{baseUrl}}/")],
    clientKey: "lib.httpHeaders.client.referer",
  },
  { name: "Repr-Digest", descriptionKey: "lib.httpHeaders.reprDigest", values: [v("sha-256=::")] },
  { name: "Sec-Fetch-Dest", descriptionKey: "lib.httpHeaders.secFetchDest", values: [v("empty"), v("document"), v("image"), v("script")] },
  { name: "Sec-Fetch-Mode", descriptionKey: "lib.httpHeaders.secFetchMode", values: [v("cors"), v("navigate"), v("no-cors"), v("same-origin")] },
  { name: "Sec-Fetch-Site", descriptionKey: "lib.httpHeaders.secFetchSite", values: [v("same-origin"), v("same-site"), v("cross-site"), v("none")] },
  { name: "Sec-WebSocket-Protocol", descriptionKey: "lib.httpHeaders.secWebsocketProtocol", list: true },
  { name: "SOAPAction", descriptionKey: "lib.httpHeaders.soapaction", values: [v('""')] },
  { name: "TE", descriptionKey: "lib.httpHeaders.te", values: [v("trailers")], list: true },
  { name: "traceparent", descriptionKey: "lib.httpHeaders.traceparent", values: [v("00-4bf92f3577b34da6a3ce929d0e0e4736-00f067aa0ba902b7-01")] },
  { name: "tracestate", descriptionKey: "lib.httpHeaders.tracestate", list: true },
  {
    name: "Transfer-Encoding",
    descriptionKey: "lib.httpHeaders.transferEncoding",
    values: [v("chunked")],
    clientKey: "lib.httpHeaders.client.transferEncoding",
  },
  { name: "Upgrade", descriptionKey: "lib.httpHeaders.upgrade", values: [v("websocket"), v("h2c")], list: true, clientKey: DROPPED_ON_H2 },
  { name: "Upgrade-Insecure-Requests", descriptionKey: "lib.httpHeaders.upgradeInsecureRequests", values: [v("1")] },
  {
    name: "User-Agent",
    descriptionKey: "lib.httpHeaders.userAgent",
    values: [v("PostSonde"), v("curl/8.7.1"), v("Mozilla/5.0")],
    clientKey: "lib.httpHeaders.client.userAgent",
  },
  { name: "Via", descriptionKey: "lib.httpHeaders.via", list: true },
  { name: "Want-Repr-Digest", descriptionKey: "lib.httpHeaders.wantReprDigest", values: [v("sha-256=1")], list: true },
  { name: "Warning", descriptionKey: "lib.httpHeaders.warning" },
  { name: "X-API-Key", descriptionKey: "lib.httpHeaders.xApiKey" },
  { name: "X-Correlation-ID", descriptionKey: "lib.httpHeaders.xCorrelationId", values: [v("{{$uuid}}")] },
  { name: "X-CSRF-Token", descriptionKey: "lib.httpHeaders.xCsrfToken" },
  { name: "X-Forwarded-For", descriptionKey: "lib.httpHeaders.xForwardedFor", values: [v("203.0.113.195")], list: true },
  { name: "X-Forwarded-Host", descriptionKey: "lib.httpHeaders.xForwardedHost", values: [v("example.com")] },
  { name: "X-Forwarded-Proto", descriptionKey: "lib.httpHeaders.xForwardedProto", values: [v("https"), v("http")] },
  { name: "X-HTTP-Method-Override", descriptionKey: "lib.httpHeaders.xHttpMethodOverride", values: [v("PATCH"), v("PUT"), v("DELETE")] },
  { name: "X-Real-IP", descriptionKey: "lib.httpHeaders.xRealIp", values: [v("203.0.113.195")] },
  { name: "X-Request-ID", descriptionKey: "lib.httpHeaders.xRequestId", values: [v("{{$uuid}}")] },
  { name: "X-Requested-With", descriptionKey: "lib.httpHeaders.xRequestedWith", values: [v("XMLHttpRequest")] },
];
// i18n-ignore-end

const byLowerName = new Map(HTTP_HEADERS.map((h) => [h.name.toLowerCase(), h]));

/** The built-in header named `name`, matched case-insensitively and ignoring surrounding spaces. */
export function findHeader(name: string): HeaderInfo | undefined {
  return byLowerName.get(name.trim().toLowerCase());
}
