import clsx from "clsx";
import type { ReactNode } from "react";

// IdLink — design's `.id-link` primitive (`design/app.css:543-551`).
// Renders a mono accent-colored ID with a dotted underline (3px
// offset) that brightens to accent-strong on hover. Used wherever a
// session-id / message-id / hash is rendered as a clickable inline
// label. When `onClick` is provided, mounts as a button so it picks
// up tabbable + accessible behavior; otherwise mounts as a span.
export function IdLink({
  children,
  onClick,
  title,
  className,
}: {
  children: ReactNode;
  onClick?: () => void;
  title?: string;
  className?: string;
}) {
  const base = clsx(
    "inline cursor-pointer font-mono text-[11px] text-accent transition-colors hover:text-accent-strong",
    // Dotted underline matches design — 3px offset puts the dots just
    // below the descender line.
    "underline decoration-dotted decoration-accent/50 underline-offset-[3px] hover:decoration-accent-strong",
    className,
  );
  if (onClick) {
    return (
      <button
        type="button"
        onClick={(e) => {
          e.stopPropagation();
          onClick();
        }}
        title={title}
        className={base}
      >
        {children}
      </button>
    );
  }
  return (
    <span title={title} className={base}>
      {children}
    </span>
  );
}
