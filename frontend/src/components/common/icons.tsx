// The app's icon set: small inline SVG components, 24×24 viewBox,
// stroked with currentColor so they take the colour of their parent. No icon
// font and nothing loaded remotely. Paths follow the mockup.
//
// Icons are decorative (aria-hidden) unless given an aria-label.

import type { ReactNode, SVGProps } from "react";

export interface IconProps extends Omit<SVGProps<SVGSVGElement>, "ref" | "children"> {
  /** Width and height in px. Default 16. */
  size?: number | string;
}

function Svg({ size = 16, strokeWidth = 1.9, children, ...rest }: IconProps & { children: ReactNode }) {
  const labelled = rest["aria-label"] !== undefined || rest["aria-labelledby"] !== undefined;
  return (
    <svg
      xmlns="http://www.w3.org/2000/svg"
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth={strokeWidth}
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden={labelled ? undefined : true}
      role={labelled ? "img" : undefined}
      focusable="false"
      {...rest}
    >
      {children}
    </svg>
  );
}

const FOLDER = "M3 7a2 2 0 0 1 2-2h4l2 2h8a2 2 0 0 1 2 2v9a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2z";

/** The PostSonde signal mark: a dot with two wave arcs each side (assets/icons/postsonde.svg). */
export function IconSignal({ strokeWidth = 2, ...props }: IconProps) {
  return (
    <Svg strokeWidth={strokeWidth} {...props}>
      <circle cx="12" cy="12" r="2.5" fill="currentColor" stroke="none" />
      <path d="M6.3 17.7a8 8 0 0 1 0-11.4" />
      <path d="M17.7 6.3a8 8 0 0 1 0 11.4" />
      <path d="M3.5 20.5a12 12 0 0 1 0-17" />
      <path d="M20.5 3.5a12 12 0 0 1 0 17" />
    </Svg>
  );
}

export function IconCollections(props: IconProps) {
  return (
    <Svg {...props}>
      <path d={FOLDER} />
      <path d="M3 11h18" />
    </Svg>
  );
}

export function IconHistory(props: IconProps) {
  return (
    <Svg {...props}>
      <path d="M3 12a9 9 0 1 0 3-6.7" />
      <path d="M3 4v5h5" />
      <path d="M12 8v4l3 2" />
    </Svg>
  );
}

export function IconEnvironments(props: IconProps) {
  return (
    <Svg {...props}>
      <path d="m12 3 9 5-9 5-9-5z" />
      <path d="m3 13 9 5 9-5" />
    </Svg>
  );
}

export function IconSettings(props: IconProps) {
  return (
    <Svg {...props}>
      <circle cx="12" cy="12" r="3" />
      <path d="M19.4 15a1.7 1.7 0 0 0 .3 1.8l.1.1a2 2 0 1 1-2.8 2.8l-.1-.1a1.7 1.7 0 0 0-1.8-.3 1.7 1.7 0 0 0-1 1.5V21a2 2 0 1 1-4 0v-.1a1.7 1.7 0 0 0-1.1-1.5 1.7 1.7 0 0 0-1.8.3l-.1.1a2 2 0 1 1-2.8-2.8l.1-.1a1.7 1.7 0 0 0 .3-1.8 1.7 1.7 0 0 0-1.5-1H3a2 2 0 1 1 0-4h.1a1.7 1.7 0 0 0 1.5-1.1 1.7 1.7 0 0 0-.3-1.8l-.1-.1a2 2 0 1 1 2.8-2.8l.1.1a1.7 1.7 0 0 0 1.8.3H9a1.7 1.7 0 0 0 1-1.5V3a2 2 0 1 1 4 0v.1a1.7 1.7 0 0 0 1 1.5 1.7 1.7 0 0 0 1.8-.3l.1-.1a2 2 0 1 1 2.8 2.8l-.1.1a1.7 1.7 0 0 0-.3 1.8V9a1.7 1.7 0 0 0 1.5 1H21a2 2 0 1 1 0 4h-.1a1.7 1.7 0 0 0-1.5 1z" />
    </Svg>
  );
}

/** Three sliders: the settings screen's General page. */
export function IconSliders(props: IconProps) {
  return (
    <Svg {...props}>
      <path d="M4 21v-7M4 10V3M12 21v-9M12 8V3M20 21v-5M20 12V3" />
      <path d="M1 14h6M9 8h6M17 16h6" />
    </Svg>
  );
}

/** A globe: the settings screen's Network page. */
export function IconGlobe(props: IconProps) {
  return (
    <Svg {...props}>
      <circle cx="12" cy="12" r="9" />
      <path d="M3 12h18M12 3a14 14 0 0 1 0 18M12 3a14 14 0 0 0 0 18" />
    </Svg>
  );
}

/** Angle brackets around a slash: the settings screen's Editor page. */
export function IconCode(props: IconProps) {
  return (
    <Svg {...props}>
      <path d="m8 8-4 4 4 4M16 8l4 4-4 4M13 5l-2 14" />
    </Svg>
  );
}

/** A shield with a tick: the settings screen's Privacy page. */
export function IconShield(props: IconProps) {
  return (
    <Svg {...props}>
      <path d="M12 3 4 6v6c0 5 3.5 8 8 9 4.5-1 8-4 8-9V6z" />
      <path d="m9 12 2 2 4-4" />
    </Svg>
  );
}

/** A tick. */
export function IconCheck({ strokeWidth = 2.2, ...props }: IconProps) {
  return (
    <Svg strokeWidth={strokeWidth} {...props}>
      <path d="m5 12 5 5 9-10" />
    </Svg>
  );
}

/** A circular arrow: refresh (the GraphQL schema strip, GQL-10). */
export function IconRefresh({ strokeWidth = 2.2, ...props }: IconProps) {
  return (
    <Svg strokeWidth={strokeWidth} {...props}>
      <path d="M21 12a9 9 0 1 1-2.6-6.4" />
      <path d="M21 3v6h-6" />
    </Svg>
  );
}

/** Two stacked server units with status lights: the local servers view (SRV-01). */
export function IconServers(props: IconProps) {
  return (
    <Svg {...props}>
      <rect x="3" y="4" width="18" height="7" rx="2" />
      <rect x="3" y="13" width="18" height="7" rx="2" />
      <path d="M7 7.5h.01M7 16.5h.01M11 7.5h6M11 16.5h6" />
    </Svg>
  );
}

/** Two overlapping pages: copy to the clipboard. */
export function IconCopy(props: IconProps) {
  return (
    <Svg {...props}>
      <rect x="9" y="9" width="12" height="12" rx="2" />
      <path d="M5 15H4a2 2 0 0 1-2-2V4a2 2 0 0 1 2-2h9a2 2 0 0 1 2 2v1" />
    </Svg>
  );
}

/** A filled square: stop. */
export function IconStop(props: IconProps) {
  return (
    <Svg fill="currentColor" stroke="none" {...props}>
      <rect x="6" y="6" width="12" height="12" rx="1.5" />
    </Svg>
  );
}

/** A window with a left panel: shows or hides the sidebar. */
export function IconSidebar(props: IconProps) {
  return (
    <Svg {...props}>
      <rect x="3" y="4" width="18" height="16" rx="2" />
      <path d="M9 4v16" />
    </Svg>
  );
}

export function IconSearch(props: IconProps) {
  return (
    <Svg strokeWidth={2} {...props}>
      <circle cx="11" cy="11" r="7" />
      <path d="m20 20-3.5-3.5" />
    </Svg>
  );
}

/** An arrow down into a tray: save to a file. */
export function IconDownload(props: IconProps) {
  return (
    <Svg strokeWidth={2} {...props}>
      <path d="M12 4v11" />
      <path d="m7 10 5 5 5-5" />
      <path d="M5 20h14" />
    </Svg>
  );
}

export function IconFilter(props: IconProps) {
  return (
    <Svg strokeWidth={2} {...props}>
      <path d="M3 5h18M6 12h12M10 19h4" />
    </Svg>
  );
}

export function IconSun(props: IconProps) {
  return (
    <Svg strokeWidth={2} {...props}>
      <circle cx="12" cy="12" r="4" />
      <path d="M12 2v2M12 20v2M4.9 4.9l1.4 1.4M17.7 17.7l1.4 1.4M2 12h2M20 12h2M4.9 19.1l1.4-1.4M17.7 6.3l1.4-1.4" />
    </Svg>
  );
}

export function IconMoon(props: IconProps) {
  return (
    <Svg strokeWidth={2} {...props}>
      <path d="M21 12.8A9 9 0 1 1 11.2 3a7 7 0 0 0 9.8 9.8z" />
    </Svg>
  );
}

/** "Follow the system" (theme preference). */
export function IconMonitor(props: IconProps) {
  return (
    <Svg strokeWidth={2} {...props}>
      <rect x="3" y="4" width="18" height="12" rx="2" />
      <path d="M8 20h8M12 16v4" />
    </Svg>
  );
}

const CHEVRONS = {
  down: "m6 9 6 6 6-6",
  up: "m6 15 6-6 6 6",
  right: "m9 6 6 6-6 6",
  left: "m15 6-6 6 6 6",
} as const;

export function IconChevron({ direction = "down", ...props }: IconProps & { direction?: keyof typeof CHEVRONS }) {
  return (
    <Svg strokeWidth={2.2} {...props}>
      <path d={CHEVRONS[direction]} />
    </Svg>
  );
}

export function IconFolder(props: IconProps) {
  return (
    <Svg strokeWidth={2} {...props}>
      <path d={FOLDER} />
    </Svg>
  );
}

export function IconFolderPlus(props: IconProps) {
  return (
    <Svg {...props}>
      <path d={FOLDER} />
      <path d="M12 11v6M9 14h6" />
    </Svg>
  );
}

export function IconPlus(props: IconProps) {
  return (
    <Svg strokeWidth={2} {...props}>
      <path d="M12 5v14M5 12h14" />
    </Svg>
  );
}

export function IconClose(props: IconProps) {
  return (
    <Svg strokeWidth={2} {...props}>
      <path d="M6 6l12 12M18 6 6 18" />
    </Svg>
  );
}

/** A bin: delete (dialogs and the undo toast). */
export function IconTrash(props: IconProps) {
  return (
    <Svg {...props}>
      <path d="M4 7h16M10 11v6M14 11v6M6 7l1 13h10l1-13M9 7V4h6v3" />
    </Svg>
  );
}

/** A push pin: a pinned tab (NAV-01). */
export function IconPin(props: IconProps) {
  return (
    <Svg strokeWidth={2} {...props}>
      <path d="M9 4h6M10 4v5l-3 4h10l-3-4V4M12 13v7" />
    </Svg>
  );
}

/** A cookie with a bite taken out: the cookie manager (CK-02). */
export function IconCookie(props: IconProps) {
  return (
    <Svg {...props}>
      <path d="M21 12.5A9 9 0 1 1 11.5 3a3 3 0 0 0 3.5 3.5 3 3 0 0 0 3.5 3.5 3 3 0 0 0 2.5 2.5z" />
      <path d="M8.5 9.5h.01M8 15h.01M12.5 12.5h.01M15.5 16h.01" />
    </Svg>
  );
}

export function IconLock(props: IconProps) {
  return (
    <Svg strokeWidth={2} {...props}>
      <rect x="5" y="11" width="14" height="10" rx="2" />
      <path d="M8 11V7a4 4 0 0 1 8 0v4" />
    </Svg>
  );
}

export function IconWarning(props: IconProps) {
  return (
    <Svg strokeWidth={2} {...props}>
      <path d="M10.3 3.9 1.8 18a2 2 0 0 0 1.7 3h17a2 2 0 0 0 1.7-3L13.7 3.9a2 2 0 0 0-3.4 0z" />
      <path d="M12 9v4M12 17h.01" />
    </Svg>
  );
}

/** An "i" in a circle: a hint. */
export function IconInfo(props: IconProps) {
  return (
    <Svg strokeWidth={2} {...props}>
      <circle cx="12" cy="12" r="9" />
      <path d="M12 11v5M12 8h.01" />
    </Svg>
  );
}

export function IconEye(props: IconProps) {
  return (
    <Svg strokeWidth={2} {...props}>
      <path d="M2 12s3.5-7 10-7 10 7 10 7-3.5 7-10 7S2 12 2 12z" />
      <circle cx="12" cy="12" r="3" />
    </Svg>
  );
}

export function IconEyeOff(props: IconProps) {
  return (
    <Svg strokeWidth={2} {...props}>
      <path d="M10.6 5.1A10.4 10.4 0 0 1 12 5c6.5 0 10 7 10 7a17.6 17.6 0 0 1-2.9 3.9M6.6 6.6C3.8 8.3 2 12 2 12s3.5 7 10 7a9.7 9.7 0 0 0 5.4-1.6" />
      <path d="M9.9 9.9a3 3 0 0 0 4.2 4.2M3 3l18 18" />
    </Svg>
  );
}

/** A document with a folded corner: a file on disk (body and multipart files). */
export function IconFile(props: IconProps) {
  return (
    <Svg strokeWidth={2} {...props}>
      <path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z" />
      <path d="M14 3v5h5" />
    </Svg>
  );
}

/** A document with an arrow down into it: import a file (IMP-10). */
export function IconFileImport(props: IconProps) {
  return (
    <Svg strokeWidth={2} {...props}>
      <path d="M14 3H7a2 2 0 0 0-2 2v14a2 2 0 0 0 2 2h10a2 2 0 0 0 2-2V8z" />
      <path d="M14 3v5h5" />
      <path d="M12 10v7" />
      <path d="m9 14 3 3 3-3" />
    </Svg>
  );
}

/** An open book: the GraphQL docs explorer (GQL-14). */
export function IconBook(props: IconProps) {
  return (
    <Svg strokeWidth={2} {...props}>
      <path d="M3 5.5A1.5 1.5 0 0 1 4.5 4H9a3 3 0 0 1 3 3v13a2.5 2.5 0 0 0-2.5-2.5h-5A1.5 1.5 0 0 1 3 16z" />
      <path d="M21 5.5A1.5 1.5 0 0 0 19.5 4H15a3 3 0 0 0-3 3v13a2.5 2.5 0 0 1 2.5-2.5h5A1.5 1.5 0 0 0 21 16z" />
    </Svg>
  );
}

/** The Send button's play triangle, filled. */
export function IconPlay(props: IconProps) {
  return (
    <Svg fill="currentColor" stroke="none" {...props}>
      <path d="M6 4.5v15l13-7.5z" />
    </Svg>
  );
}

/** A keyboard: the settings screen's Shortcuts page (UI-07). */
export function IconKeyboard(props: IconProps) {
  return (
    <Svg {...props}>
      <rect x="2.5" y="6" width="19" height="12" rx="2" />
      <path d="M6.5 10h.01M10 10h.01M14 10h.01M17.5 10h.01M8 14h8" />
    </Svg>
  );
}

/** Two overlapping windows: open in a new window (APP-06). */
export function IconNewWindow(props: IconProps) {
  return (
    <Svg {...props}>
      <rect x="8" y="3" width="13" height="11" rx="2" />
      <path d="M16 17v2a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2v-7a2 2 0 0 1 2-2h1M8 7h13" />
    </Svg>
  );
}
