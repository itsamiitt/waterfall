// Inline SVG icon set (ADR-0016: no icon pack). Stroke inherits currentColor so status color
// always travels with the icon + label pair (doc 08 §6.1: never color-only).
import type { JSX } from "react";
import type { IconName } from "../../lib/status";

const PATHS: Record<IconName, JSX.Element> = {
  check: <path d="M3 8.5 6.5 12 13 4" />,
  x: (
    <>
      <path d="M4 4l8 8" />
      <path d="M12 4l-8 8" />
    </>
  ),
  slash: (
    <>
      <circle cx="8" cy="8" r="6" />
      <path d="M3.8 3.8l8.4 8.4" />
    </>
  ),
  pause: (
    <>
      <path d="M6 4v8" />
      <path d="M10 4v8" />
    </>
  ),
  clock: (
    <>
      <circle cx="8" cy="8" r="6" />
      <path d="M8 5v3.5l2.4 1.4" />
    </>
  ),
  wrench: <path d="M9.5 2.5a3.5 3.5 0 0 0-4.6 4.4L2 9.8V14h4.2l2.9-2.9a3.5 3.5 0 0 0 4.4-4.6L11 9 7 5z" />,
  flag: (
    <>
      <path d="M4 14V2.5" />
      <path d="M4 3h8l-2 2.75L12 8.5H4" />
    </>
  ),
  triangle: (
    <>
      <path d="M8 2.5 14.5 13.5H1.5z" />
      <path d="M8 6.5v3.5" />
      <path d="M8 12.2v.1" />
    </>
  ),
  question: (
    <>
      <circle cx="8" cy="8" r="6" />
      <path d="M6.2 6.2A1.9 1.9 0 0 1 8 5c1 0 1.9.7 1.9 1.7 0 1.2-1.9 1.4-1.9 2.6" />
      <path d="M8 11.6v.1" />
    </>
  ),
  gauge: (
    <>
      <path d="M2.5 12a6 6 0 1 1 11 0" />
      <path d="M8 9.5 11 6" />
    </>
  ),
  shield: <path d="M8 1.8 13.5 4v4.2c0 3.3-2.4 5.3-5.5 6.3-3.1-1-5.5-3-5.5-6.3V4z" />,
  refresh: (
    <>
      <path d="M13 8a5 5 0 1 1-1.5-3.5" />
      <path d="M13 2.5V5h-2.5" />
    </>
  ),
  archive: (
    <>
      <path d="M2 3h12v3H2z" />
      <path d="M3.5 6v7h9V6" />
      <path d="M6.5 9h3" />
    </>
  ),
  dot: <circle cx="8" cy="8" r="3" fill="currentColor" stroke="none" />,
};

export type { IconName };

export function Icon({ name, className }: { name: IconName; className?: string }) {
  return (
    <svg
      className={className ?? "p-icon"}
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      {PATHS[name]}
    </svg>
  );
}
