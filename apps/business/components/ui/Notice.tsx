import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

/** One quiet line of context. `tone="error"` for something that failed. */
export function Notice({ children, tone = "neutral", className }: { children: ReactNode; tone?: "neutral" | "error" | "warning"; className?: string }) {
  return (
    <p
      role={tone === "error" ? "alert" : undefined}
      className={cn(
        "rounded-md border px-3 py-2 text-[13px]",
        tone === "neutral" && "border-line bg-tile text-fg-muted",
        tone === "error" && "border-err-bg bg-err-bg text-err-fg",
        tone === "warning" && "border-warn-bg bg-warn-bg text-warn-fg",
        className,
      )}
    >
      {children}
    </p>
  );
}
