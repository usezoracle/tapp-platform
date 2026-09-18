import { cn } from "@/lib/utils";

/** Single-tone skeleton block. Size it with width/height classes. */
export function Skeleton({ className }: { className?: string }) {
  return <div aria-hidden className={cn("skeleton h-4 w-24", className)} />;
}

/** N dense rows in a hairline list — the loading state for any list. */
export function SkeletonRows({ rows = 3 }: { rows?: number }) {
  return (
    <div className="panel divide-y divide-line overflow-hidden">
      {Array.from({ length: rows }).map((_, i) => (
        <div key={i} className="flex h-12 items-center gap-3 px-3">
          <div className="skeleton size-7" />
          <div className="grid flex-1 gap-1.5">
            <div className="skeleton h-3 w-32" />
            <div className="skeleton h-2.5 w-20" />
          </div>
          <div className="skeleton h-3 w-16" />
        </div>
      ))}
    </div>
  );
}
