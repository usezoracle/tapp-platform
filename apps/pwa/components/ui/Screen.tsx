import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

interface ScreenProps {
  children?: ReactNode;
  className?: string;
  /** Vertically center the content (true) vs top-aligned (false). */
  centered?: boolean;
}

/**
 * Per-page content shell. The mobile container + padding live on the
 * root layout (see app/layout.tsx), so this is just the column inside.
 * Section rhythm is 24px; use `gap-3`/`gap-4` inside sections.
 */
export function Screen({ children, className, centered = false }: ScreenProps) {
  return (
    <div
      className={cn(
        "flex w-full flex-col gap-6 text-sm text-fg",
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
