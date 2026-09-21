"use client";

import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

export type ChipTone = "neutral" | "success" | "warning" | "error" | "pending";

interface StatusChipProps {
  /** Optional icon; replaces the dot. */
  icon?: ReactNode;
  children: ReactNode;
  tone?: ChipTone;
  className?: string;
}

const toneClasses: Record<ChipTone, { chip: string; dot: string }> = {
  neutral: { chip: "bg-sunken text-fg-muted",      dot: "bg-fg-subtle" },
  success: { chip: "bg-positive-wash text-positive-fg",        dot: "bg-positive" },
  warning: { chip: "bg-caution-wash text-caution-fg",    dot: "bg-caution" },
  error:   { chip: "bg-negative-wash text-negative-fg",      dot: "bg-negative" },
  pending: { chip: "bg-pending-wash text-pending-fg",    dot: "bg-pending" },
};

/** Small soft chip: pastel ground, dark text, a 6px dot. */
export function StatusChip({ icon, children, tone = "neutral", className }: StatusChipProps) {
  const t = toneClasses[tone];
  return (
    <span
      className={cn(
        "inline-flex h-5 items-center gap-1.5 rounded-sm px-1.5 text-[11px] font-medium leading-none whitespace-nowrap",
        t.chip,
        className,
      )}
    >
      {icon ? (
        <span className="[&>svg]:size-3">{icon}</span>
      ) : (
        <span aria-hidden className={cn("size-1.5 rounded-full", t.dot)} />
      )}
      <span>{children}</span>
    </span>
  );
}
