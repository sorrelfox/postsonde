// The image and HTML preview of a response body (HTTP-35, PRV-04): what
// can be previewed, and the sandboxed document an HTML body is shown in.
// Why each attribute and directive: docs/privacy.md#response-preview.
import type { ResponseDTO } from "./http";

/** The image types Go reports in ResponseDTO.previewType (preview.go). */
export const IMAGE_PREVIEW_TYPES: ReadonlySet<string> = new Set([
  "image/png",
  "image/jpeg",
  "image/gif",
  "image/webp",
  "image/svg+xml",
  "image/bmp",
  "image/x-icon",
]);

export const HTML_PREVIEW_TYPE = "text/html";

export type PreviewKind = "image" | "html";

/** The preview a response has, from Go's previewType; null for none. */
export function previewKind(response: ResponseDTO): PreviewKind | null {
  if (response.body === "") return null;
  const type = response.previewType ?? "";
  if (IMAGE_PREVIEW_TYPES.has(type)) return "image";
  if (type === HTML_PREVIEW_TYPE) return "html";
  return null;
}

/**
 * The policy of the preview document, on top of the app's own (which a
 * srcdoc document inherits): nothing is fetched, not even from the app.
 * Only images already in the document (data:) or in memory (blob:) and
 * inline styles. Scripts are off twice: here, and by the frame's sandbox.
 */
export const PREVIEW_CSP = [
  // i18n-ignore-start: CSP directives, not UI text
  "default-src 'none'",
  "img-src data: blob:",
  "style-src 'unsafe-inline'",
  "base-uri 'none'",
  "form-action 'none'",
  // i18n-ignore-end
].join("; ");

/**
 * The `sandbox` attribute of the preview frame: empty, so every
 * restriction applies. No allow-scripts (no script runs), no
 * allow-same-origin (the document has an opaque origin: no access to the
 * app, its storage or its IPC), no allow-forms, allow-popups,
 * allow-top-navigation or allow-modals.
 */
export const PREVIEW_SANDBOX = "";

/** Escapes text for a double-quoted HTML attribute. */
function attr(text: string): string {
  return text.replace(/&/g, "&amp;").replace(/"/g, "&quot;").replace(/</g, "&lt;").replace(/>/g, "&gt;");
}

/**
 * The srcdoc of the preview frame: the CSP <meta> (with DNS prefetching
 * and the referrer off, and a light page by default) first, before any of the body's markup, so it governs every element
 * of the body. A srcdoc document is never in quirks mode, so the body's
 * own doctype after them changes nothing; a <meta> CSP of the body can
 * only restrict further.
 */
export function previewSrcdoc(html: string): string {
  return (
    `<meta http-equiv="Content-Security-Policy" content="${attr(PREVIEW_CSP)}">` +
    `<meta http-equiv="x-dns-prefetch-control" content="off">` +
    `<meta name="referrer" content="no-referrer">` +
    // A page's own colours, as in a browser, whatever the app's theme.
    `<style>:root{color-scheme:light;background:Canvas;color:CanvasText}</style>` +
    html
  );
}

/** Decodes standard Base64 (BodyPreviewDTO.data) to bytes. */
export function base64Bytes(data: string) {
  const bin = atob(data);
  const out = new Uint8Array(bin.length);
  for (let i = 0; i < bin.length; i++) out[i] = bin.charCodeAt(i);
  return out;
}
