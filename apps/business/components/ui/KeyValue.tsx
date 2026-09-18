import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

/** Dense key/value rows inside a panel. */
export function KeyValueList({ rows, className }: { rows: { k: ReactNode; v: ReactNode }[]; className?: string }) {
  return (
    <dl className={cn("divide-y divide-line", className)}>
      {rows.map((r, i) => (
        <div key={i} className="grid grid-cols-[minmax(0,1fr)_minmax(0,1.4fr)] gap-3 px-4 py-2.5 text-[13px] sm:grid-cols-[220px_minmax(0,1fr)]">
          <dt className="text-fg-muted">{r.k}</dt>
          <dd className="tabular-nums break-words text-fg">{r.v}</dd>
        </div>
      ))}
    </dl>
  );
}

/** A number that matters, with its eyebrow. */
export function Stat({ label, value, sub }: { label: string; value: ReactNode; sub?: ReactNode }) {
  return (
    <div className="px-4 py-3">
      <p className="eyebrow">{label}</p>
      <p className="display mt-1 text-[20px] leading-6">{value}</p>
      {sub ? <p className="mt-0.5 text-xs text-fg-subtle">{sub}</p> : null}
    </div>
  );
}
