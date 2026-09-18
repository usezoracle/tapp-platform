"use client";

import type { ReactNode } from "react";
import { PiInfoBold } from "react-icons/pi";
import { cn } from "@/lib/utils";

type Tone = "info" | "warning" | "error";

interface InfoBannerProps {
  icon?: ReactNode;
  children: ReactNode;
  tone?: Tone;
  /** Trailing action (a small button). */
  action?: ReactNode;
  className?: string;
}

const toneClasses: Record<Tone, string> = {
  info:    "border-line bg-sunken text-fg-muted [&_svg]:text-fg-muted",
  warning: "border-caution/30 bg-caution-wash text-caution-fg [&_svg]:text-caution",
  error:   "border-negative/30 bg-negative-wash text-negative-fg [&_svg]:text-negative",
};

/** Inline notice. Hairline, 16px icon, 13px text. */
export function InfoBanner({ icon, children, tone = "info", action, className }: InfoBannerProps) {
  return (
    <div
      role={tone === "error" ? "alert" : undefined}
      className={cn(
        "flex items-start gap-2.5 rounded-lg border px-3 py-2.5 text-[13px] leading-5",
        toneClasses[tone],
        className,
      )}
    >
      <span className="mt-0.5 shrink-0 [&>svg]:size-4">{icon ?? <PiInfoBold />}</span>
      <div className="min-w-0 flex-1">{children}</div>
      {action ? <div className="shrink-0">{action}</div> : null}
    </div>
  );
}
