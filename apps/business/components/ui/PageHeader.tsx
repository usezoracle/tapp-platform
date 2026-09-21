import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

/** Page title in the display face, one muted line beneath, a trailing slot. */
export function PageHeader({
  eyebrow,
  title,
  subtitle,
  trailing,
  className,
}: {
  eyebrow?: ReactNode;
  title: ReactNode;
  subtitle?: ReactNode;
  trailing?: ReactNode;
  className?: string;
}) {
  return (
    <header className={cn("flex items-start justify-between gap-4", className)}>
      <div className="min-w-0">
        {eyebrow ? <p className="eyebrow mb-1">{eyebrow}</p> : null}
        <h1 className="display text-[22px] leading-7">{title}</h1>
        {subtitle ? <p className="mt-1 text-[13px] text-fg-muted">{subtitle}</p> : null}
      </div>
      {trailing ? <div className="shrink-0 pt-0.5">{trailing}</div> : null}
    </header>
  );
}
