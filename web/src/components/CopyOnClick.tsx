import { useCallback, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import clsx from "clsx";

// CopyOnClick — wraps any content with a click-to-copy affordance.
// Hover reveals a small copy glyph; click writes `value` to the
// system clipboard and surfaces a brief "copied" toast.
//
// The toast is rendered via a portal anchored to the document body
// rather than inline, so it floats above adjacent row text and the
// page-level slide-over without being clipped by table cells or
// `overflow-hidden` ancestors. Position tracks the trigger via
// getBoundingClientRect at flash time.
export function CopyOnClick({
  value,
  children,
  className,
  title,
}: {
  value: string;
  children: React.ReactNode;
  className?: string;
  title?: string;
}) {
  const btnRef = useRef<HTMLButtonElement>(null);
  const [toast, setToast] = useState<{ top: number; left: number } | null>(
    null,
  );

  useEffect(() => {
    if (!toast) return;
    const t = window.setTimeout(() => setToast(null), 1100);
    return () => window.clearTimeout(t);
  }, [toast]);

  const onCopy = useCallback(
    async (e: React.MouseEvent) => {
      e.stopPropagation();
      try {
        await navigator.clipboard.writeText(value);
        const rect = btnRef.current?.getBoundingClientRect();
        if (rect) {
          // Anchor the toast a hair above the trigger, centered on
          // the visible content. The portal coordinate space is
          // document, so use rect (viewport) + window.scroll*.
          setToast({
            top: rect.top + window.scrollY - 6,
            left: rect.left + window.scrollX + rect.width / 2,
          });
        }
      } catch {
        // Clipboard API unavailable; leave the affordance silent.
      }
    },
    [value],
  );

  return (
    <>
      <button
        ref={btnRef}
        type="button"
        onClick={onCopy}
        title={title ?? `Click to copy · ${value}`}
        className={clsx(
          "group/copy relative inline-flex items-center gap-1 text-left transition-colors hover:text-accent",
          className,
        )}
      >
        {children}
        <CopyIcon className="opacity-0 transition-opacity group-hover/copy:opacity-100" />
      </button>
      {toast &&
        createPortal(
          <span
            role="status"
            aria-live="polite"
            style={{ top: toast.top, left: toast.left }}
            className="pointer-events-none fixed z-[100] -translate-x-1/2 -translate-y-full whitespace-nowrap rounded-1 border border-success/50 bg-bg-1 px-2 py-1 text-[10px] font-medium text-success shadow-drawer"
          >
            copied
          </span>,
          document.body,
        )}
    </>
  );
}

function CopyIcon({ className }: { className?: string }) {
  return (
    <svg
      width={10}
      height={10}
      viewBox="0 0 16 16"
      fill="none"
      className={className}
      aria-hidden
    >
      <rect
        x="4"
        y="4"
        width="9"
        height="9"
        rx="1.2"
        stroke="currentColor"
        strokeWidth="1.4"
      />
      <path
        d="M11 4V3a1 1 0 0 0-1-1H3a1 1 0 0 0-1 1v7a1 1 0 0 0 1 1h1"
        stroke="currentColor"
        strokeWidth="1.4"
      />
    </svg>
  );
}
