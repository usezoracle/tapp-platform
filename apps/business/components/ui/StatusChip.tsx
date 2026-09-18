import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

export type ChipTone = "neutral" | "success" | "warning" | "error" | "pending";

const toneClasses: Record<ChipTone, { chip: string; dot: string }> = {
  neutral: { chip: "bg-tile text-fg-muted", dot: "bg-fg-subtle" },
  success: { chip: "bg-ok-bg text-ok-fg", dot: "bg-ok-dot" },
  warning: { chip: "bg-warn-bg text-warn-fg", dot: "bg-warn-dot" },
  error: { chip: "bg-err-bg text-err-fg", dot: "bg-err-dot" },
  pending: { chip: "bg-pend-bg text-pend-fg", dot: "bg-pend-dot" },
};

/** Small soft chip: pastel ground, dark text, a 6px dot. */
export function StatusChip({
  children,
  tone = "neutral",
  className,
}: {
  children: ReactNode;
  tone?: ChipTone;
  className?: string;
}) {
  const t = toneClasses[tone];
  return (
    <span
      className={cn(
        "inline-flex h-5 items-center gap-1.5 rounded-sm px-1.5 text-[11px] font-medium leading-none whitespace-nowrap",
        t.chip,
        className,
      )}
    >
      <span aria-hidden className={cn("size-1.5 rounded-full", t.dot)} />
      <span>{children}</span>
    </span>
  );
}
