import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

/**
 * The three surfaces, and the rule for choosing between them.
 *
 *   raised   something sitting on the page: a card, a sheet, a row group
 *   sunken   something set into it: an input, a well, a read-only panel
 *   outline  a boundary with no fill: grouping without weight
 *
 * If a container is not one of those three it probably does not need to be a
 * container. The reference app this borrows its patterns from has no such rule
 * and forty flat colour names, so every new screen picks a background by
 * looking at whichever screen its author had open last.
 *
 * None of these write `dark:`. The tokens are defined once per theme in
 * globals.css, so a surface cannot be right in one theme and wrong in the
 * other -- which is what happens the moment a component hard-codes both.
 */
type Kind = "raised" | "sunken" | "outline";

const KINDS: Record<Kind, string> = {
  raised: "bg-raised border border-line",
  sunken: "bg-sunken border border-transparent",
  outline: "bg-transparent border border-line",
};

// Written out rather than interpolated. Tailwind generates CSS by scanning
// source text for complete class names, so `rounded-${radius}` produces no
// rule at all -- the component renders with square corners and nothing warns.
//
// The three steps all resolve to 12px (see the radius scale in globals.css):
// the prop survives for the call sites that pass it, the corners no longer
// vary from one card to the next.
const RADII = {
  xl: "rounded-xl",
  "2xl": "rounded-2xl",
  "3xl": "rounded-3xl",
} as const;

const PADDING = {
  none: "",
  sm: "p-3",
  md: "p-4",
  lg: "p-5",
} as const;

interface SurfaceProps {
  kind?: Kind;
  padding?: keyof typeof PADDING;
  /** Matches the app's radius ladder: full → xl → 2xl → 3xl. */
  radius?: keyof typeof RADII;
  className?: string;
  children?: ReactNode;
}

export function Surface({
  kind = "raised",
  padding = "lg",
  radius = "2xl",
  className,
  children,
}: SurfaceProps) {
  return (
    <div
      className={cn(
        RADII[radius],
        KINDS[kind],
        PADDING[padding],
        className,
      )}
    >
      {children}
    </div>
  );
}

/**
 * A section heading. One style, used everywhere, so the eye can find the
 * structure of a screen without reading it.
 */
export function SectionLabel({
  children,
  action,
  className,
}: {
  children: ReactNode;
  action?: ReactNode;
  className?: string;
}) {
  return (
    <div className={cn("flex h-5 items-center justify-between", className)}>
      <h2 className="eyebrow">{children}</h2>
      {action}
    </div>
  );
}

/**
 * What to show when there is nothing to show.
 *
 * Takes a line of explanation and, ideally, the action that would make the
 * emptiness end. An empty state that only says "nothing here" leaves somebody
 * exactly where they were.
 */
export function EmptyState({
  icon,
  title,
  children,
  action,
}: {
  icon?: ReactNode;
  title: string;
  children?: ReactNode;
  action?: ReactNode;
}) {
  return (
    <div className="panel grid gap-1 px-4 py-5">
      {icon ? <span className="mb-1 text-fg-subtle [&>svg]:size-5">{icon}</span> : null}
      <p className="text-sm font-medium text-fg">{title}</p>
      {children ? (
        <p className="text-[13px] leading-5 text-fg-muted">{children}</p>
      ) : null}
      {action ? <div className="mt-2">{action}</div> : null}
    </div>
  );
}
