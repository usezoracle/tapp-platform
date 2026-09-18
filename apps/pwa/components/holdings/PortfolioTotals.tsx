"use client";

import { cn } from "@/lib/utils";
import type { Money } from "@/lib/api";
import { formatBps, changeClass } from "@/lib/holdings";

/**
 * Total value, cost, and value-vs-cost change: the hero under the Value
 * eyebrow on /holdings. It carries no label of its own; the section does.
 */
export function PortfolioTotals({ value, cost }: { value: Money; cost: Money }) {
  const bps = cost.minor > 0 ? Math.round(((value.minor - cost.minor) / cost.minor) * 10_000) : 0;
  return (
    <div className="grid gap-1">
      <p className="display text-[32px] leading-9">{value.display}</p>
      <p className="flex items-center gap-2 text-[13px] tabular-nums text-fg-muted">
        <span className={cn("font-medium", changeClass(bps))}>{formatBps(bps)}</span>
        <span>· Cost {cost.display}</span>
      </p>
    </div>
  );
}
