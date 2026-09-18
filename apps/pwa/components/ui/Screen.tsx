import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

interface ScreenProps {
  children?: ReactNode;
  className?: string;
  /** Vertically center the content (true) vs top-aligned (false). */
  centered?: boolean;
}

/**
 * Per-page content shell. The container, its width and its padding live
 * in AppShell, so this is just the column inside. Section rhythm is 24px
 * on the phone and 32px from 768px; use `gap-3`/`gap-4` inside sections.
 */
export function Screen({ children, className, centered = false }: ScreenProps) {
  return (
    <div
      className={cn(
        "flex w-full flex-col gap-6 text-sm text-fg md:gap-8",
        centered && "min-h-[70vh] justify-center",
        className,
      )}
    >
      {children}
    </div>
  );
}

/** Eyebrow + optional trailing action; sits above a list or panel. */
export function SectionHeader({
  title,
  action,
  className,
}: {
  title: ReactNode;
  action?: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("flex h-5 items-center justify-between", className)}>
      <h2 className="eyebrow">{title}</h2>
      {action}
    </div>
  );
}

/**
 * A titled section: eyebrow, an optional one-line description, then the
 * body. Sections are separated by the page's vertical rhythm, never by a
 * box around them; the body is where a panel goes, if one is needed.
 */
export function Section({
  title,
  description,
  action,
  children,
  className,
  id,
}: {
  title: ReactNode;
  description?: ReactNode;
  action?: ReactNode;
  children?: ReactNode;
  className?: string;
  id?: string;
}) {
  return (
    <section id={id} className={cn("grid content-start gap-3", className)}>
      <div className="flex items-start justify-between gap-4">
        <div className="grid min-w-0 gap-0.5">
          <h2 className="eyebrow">{title}</h2>
          {description ? (
            <p className="text-[13px] leading-5 text-fg-muted">{description}</p>
          ) : null}
        </div>
        {action ? <div className="shrink-0 pt-px">{action}</div> : null}
      </div>
      {children}
    </section>
  );
}

export interface Stat {
  label: ReactNode;
  value: ReactNode;
  /** One quiet line under the value. */
  sub?: ReactNode;
  className?: string;
}

/**
 * A row of figures: label above, tabular value, an optional sub-line.
 * Hairlines between the figures, no box around them.
 */
export function StatRow({ stats, className }: { stats: Stat[]; className?: string }) {
  return (
    <dl
      className={cn(
        "grid grid-flow-col auto-cols-fr divide-x divide-line",
        className,
      )}
    >
      {stats.map((s, i) => (
        <div key={i} className={cn("grid content-start gap-0.5 px-4 first:pl-0 last:pr-0", s.className)}>
          <dt className="text-xs text-fg-muted">{s.label}</dt>
          <dd className="text-sm font-medium tabular-nums text-fg">{s.value}</dd>
          {s.sub ? <dd className="text-xs tabular-nums text-fg-muted">{s.sub}</dd> : null}
        </div>
      ))}
    </dl>
  );
}
