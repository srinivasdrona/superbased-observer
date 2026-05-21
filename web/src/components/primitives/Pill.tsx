import clsx from "clsx";
import type { ReactNode } from "react";

type Variant = "neutral" | "success" | "warn" | "danger" | "info" | "accent";

const VARIANT_CLASS: Record<Variant, string> = {
  neutral: "border-line-2 bg-bg-2 text-fg-2",
  success: "border-success/30 bg-success-soft text-success",
  warn: "border-warn/30 bg-warn-soft text-warn",
  danger: "border-danger/30 bg-danger-soft text-danger",
  info: "border-info/30 bg-info-soft text-info",
  accent: "border-accent/30 bg-accent-soft text-accent",
};

export function Pill({
  children,
  variant = "neutral",
  className,
  title,
}: {
  children: ReactNode;
  variant?: Variant;
  className?: string;
  title?: string;
}) {
  return (
    <span
      title={title}
      className={clsx(
        // Design's `.pill` spec: font 10px weight 600, padding 1px 7px,
        // text-transform lowercase, letter-spacing 0.02em.
        "inline-flex items-center gap-1 rounded-pill border px-[7px] py-[1px] text-[10px] font-semibold lowercase leading-[1.4] tracking-[0.02em]",
        VARIANT_CLASS[variant],
        className,
      )}
    >
      {children}
    </span>
  );
}
