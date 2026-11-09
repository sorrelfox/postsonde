import { useEffect, useMemo, useState } from "react";
import { previewResponseBody, type ResponseDTO } from "../../lib/http";
import { formatError } from "../../lib/errors";
import { base64Bytes, previewKind, previewSrcdoc, PREVIEW_SANDBOX } from "../../lib/preview";
import { useT } from "../../i18n/react";
import { formatSize } from "../messagelog/format";
import styles from "./ResponseView.module.css";

export interface ResponsePreviewProps {
  response: ResponseDTO;
}

/**
 * The Preview view of a response body (HTTP-35, PRV-04): an image body as
 * an <img> from a blob: URL of its bytes, an HTML body in a sandboxed
 * srcdoc frame that runs no script and loads nothing. SVG is only ever an
 * <img> source, never put into the page, so its scripts cannot run.
 * Rendered only for a response previewKind says has a preview.
 */
export function ResponsePreview({ response }: ResponsePreviewProps) {
  return previewKind(response) === "html" ? <HtmlPreview response={response} /> : <ImagePreview response={response} />;
}

/** What the image preview has: the object URL of the bytes, or why not. */
type ImageState =
  | { status: "loading" }
  | { status: "ready"; url: string; size: number }
  | { status: "failed"; message: string }
  | { status: "notKept" };

function ImagePreview({ response }: ResponsePreviewProps) {
  const t = useT();
  const type = response.previewType;
  // Without kept bytes, an SVG body's text is the image; nothing else is.
  const svgText = type === "image/svg+xml" && !response.bodyHex && !response.bodyClipped ? response.body : null;
  const [state, setState] = useState<{ for: ResponseDTO; state: ImageState }>({ for: response, state: { status: "loading" } });
  const [broken, setBroken] = useState<ResponseDTO | null>(null);
  const current = state.for === response ? state.state : ({ status: "loading" } as const);

  useEffect(() => {
    let url = "";
    let live = true;
    const show = (bytes: BlobPart, size: number) => {
      url = URL.createObjectURL(new Blob([bytes], { type }));
      setState({ for: response, state: { status: "ready", url, size } });
    };
    if (response.bodyId !== "") {
      const call = previewResponseBody(response.bodyId);
      call.then(
        (p) => {
          if (live) show(base64Bytes(p.data), p.size);
        },
        (err: unknown) => {
          if (live) setState({ for: response, state: { status: "failed", message: formatError(err) } });
        },
      );
      return () => {
        live = false;
        call.cancel?.();
        if (url !== "") URL.revokeObjectURL(url);
      };
    }
    if (svgText !== null) show(svgText, response.bodyShown);
    else setState({ for: response, state: { status: "notKept" } });
    return () => {
      if (url !== "") URL.revokeObjectURL(url);
    };
  }, [response, type, svgText]);

  if (current.status === "loading") return <p className={styles.empty}>{t("response.preview.loading")}</p>;
  if (current.status === "notKept") return <p className={styles.empty} role="note">{t("response.preview.notKept")}</p>;
  if (current.status === "failed") return <p className={styles.empty} role="note">{current.message}</p>;
  return (
    <div className={styles.bodyPane}>
      <div className={styles.bodyNotes}>
        <p className={styles.bodyNote}>{t("response.preview.imageNote", { type, size: formatSize(current.size) })}</p>
        {response.bodyTruncated && <p className={styles.bodyNote}>{t("response.preview.imageTruncated")}</p>}
        {broken === response && (
          <p className={styles.bodyNote} role="note">
            {t("response.preview.imageFailed", { type })}
          </p>
        )}
      </div>
      <div className={styles.previewImageBox} data-preview="image">
        <img
          className={styles.previewImage}
          src={current.url}
          alt={t("response.preview.imageLabel")}
          referrerPolicy="no-referrer"
          onError={() => setBroken(response)}
        />
      </div>
    </div>
  );
}

/** Go's MaxBodyText: the most of a body the response carries as text. */
const MAX_HTML_BYTES = 1024 * 1024;

function HtmlPreview({ response }: ResponsePreviewProps) {
  const t = useT();
  const srcdoc = useMemo(
    () => (response.bodyHex || response.bodyClipped ? null : previewSrcdoc(response.body)),
    [response],
  );
  if (response.bodyHex) return <p className={styles.empty} role="note">{t("response.preview.htmlUndecoded")}</p>;
  if (srcdoc === null) return <p className={styles.empty} role="note">{t("response.preview.htmlTooLarge", { size: formatSize(MAX_HTML_BYTES) })}</p>;
  return (
    <div className={styles.bodyPane}>
      <div className={styles.bodyNotes}>
        <p className={styles.bodyNote}>{t("response.preview.sandboxNote")}</p>
      </div>
      <iframe
        className={styles.previewFrame}
        data-preview="html"
        title={t("response.preview.htmlLabel")}
        sandbox={PREVIEW_SANDBOX}
        referrerPolicy="no-referrer"
        srcDoc={srcdoc}
      />
    </div>
  );
}
