// The auto Content-Type (HTTP-16): the Content-Type header a body
// implies and when an explicit header overrides it. A mirror of
// internal/core/request/contenttype.go, which decides what is sent; both
// run the cases of internal/core/request/testdata/content_type.json, so
// they cannot drift. See docs/http-client.md (Auto Content-Type).

/** The fields of a body the rule reads. */
export interface ContentTypeBody {
  mode: string;
  type: string;
  /** A file body's path. */
  path?: string;
  /** A file body's Content-Type override. */
  contentType?: string;
}

/**
 * The media types of file bodies and multipart file parts by lower-case
 * extension (HTTP-14, HTTP-15). A fixed table, the same as Go's
 * request.extensionMediaTypes (both are checked against the fixture's
 * "extensions"), so the editor shows exactly what is sent.
 */
export const EXTENSION_MEDIA_TYPES: Readonly<Record<string, string>> = {
  ".avif": "image/avif",
  ".bmp": "image/bmp",
  ".css": "text/css",
  ".csv": "text/csv",
  ".gif": "image/gif",
  ".gz": "application/gzip",
  ".htm": "text/html",
  ".html": "text/html",
  ".ico": "image/vnd.microsoft.icon",
  ".jpeg": "image/jpeg",
  ".jpg": "image/jpeg",
  ".js": "text/javascript",
  ".json": "application/json",
  ".md": "text/markdown",
  ".mjs": "text/javascript",
  ".mp3": "audio/mpeg",
  ".mp4": "video/mp4",
  ".ogg": "audio/ogg",
  ".pdf": "application/pdf",
  ".png": "image/png",
  ".svg": "image/svg+xml",
  ".tar": "application/x-tar",
  ".txt": "text/plain",
  ".wasm": "application/wasm",
  ".wav": "audio/wav",
  ".webm": "video/webm",
  ".webp": "image/webp",
  ".xml": "application/xml",
  ".yaml": "application/yaml",
  ".yml": "application/yaml",
  ".zip": "application/zip",
};

/** The type of a file whose extension is not in the table. */
export const OCTET_STREAM = "application/octet-stream";

/** The base name of a path with / or \ separators. */
export function baseName(path: string): string {
  return path.slice(Math.max(path.lastIndexOf("/"), path.lastIndexOf("\\")) + 1);
}

/**
 * The media type of a file named `name` (a path is fine) by its extension,
 * in any letter case; "" for an extension the table does not have. The
 * extension starts at the last dot of the base name, as Go's path.Ext.
 */
export function mediaTypeByExtension(name: string): string {
  const base = baseName(name);
  const dot = base.lastIndexOf(".");
  if (dot < 0) return "";
  const ext = base.slice(dot).toLowerCase();
  return Object.prototype.hasOwnProperty.call(EXTENSION_MEDIA_TYPES, ext) ? EXTENSION_MEDIA_TYPES[ext] : "";
}

/** The Content-Type a file is sent with: `override`, else by extension, else application/octet-stream. */
export function fileMediaType(name: string, override: string): string {
  if (override !== "") return override;
  return mediaTypeByExtension(name) || OCTET_STREAM;
}

/** The fields of a header row the rule reads. */
export interface ContentTypeHeaderRow {
  name: string;
  enabled: boolean;
}

/** The media type a raw body of `type` implies; "" for a type this version does not know. */
export function rawMediaType(type: string): string {
  switch (type) {
    case "json":
      return "application/json";
    case "xml":
      return "application/xml";
    case "html":
      return "text/html";
    case "text":
    case "":
      return "text/plain";
    case "yaml":
      return "application/yaml";
    default:
      return "";
  }
}

/**
 * The Content-Type `body` implies, or "" for none: nothing for no body and
 * unknown modes, rawMediaType for raw, `application/x-www-form-urlencoded`,
 * `multipart/form-data` (the boundary is added when sending) and
 * fileMediaType(path, contentType) for a file.
 */
export function bodyContentType(body: ContentTypeBody): string {
  switch (body.mode) {
    case "raw":
      return rawMediaType(body.type);
    case "urlencoded":
      return "application/x-www-form-urlencoded";
    case "multipart":
      return "multipart/form-data";
    case "file":
      return fileMediaType(body.path ?? "", body.contentType ?? "");
    default:
      return "";
  }
}

/**
 * True when `rows` has an enabled Content-Type row, in any letter case.
 * Names are compared as typed: a name made of {{variables}} is only known
 * to be Content-Type once Go renders it when sending.
 */
export function hasContentTypeHeader(rows: readonly ContentTypeHeaderRow[] | null): boolean {
  return (rows ?? []).some((r) => r.enabled && r.name.toLowerCase() === "content-type");
}

/** The implicit Content-Type of a request, and whether a header row overrides it. */
export interface ImplicitContentType {
  /** What the body implies; "" when it implies nothing (then there is no implicit header). */
  value: string;
  /** An enabled Content-Type row is sent instead. */
  overridden: boolean;
}

export function implicitContentType(
  body: ContentTypeBody,
  headers: readonly ContentTypeHeaderRow[] | null,
): ImplicitContentType {
  return { value: bodyContentType(body), overridden: hasContentTypeHeader(headers) };
}
