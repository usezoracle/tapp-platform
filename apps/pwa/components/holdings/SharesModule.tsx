"use client";

import Link from "next/link";
import { PiArrowClockwiseBold } from "react-icons/pi";
import { Section } from "@/components/ui/Screen";
import { InfoBanner } from "@/components/ui/InfoBanner";
import { Button } from "@/components/ui/Button";
import { SkeletonRows } from "@/components/ui/Skeleton";
import { listClasses, sectionLinkClasses } from "@/components/ui/Styles";
import { cn } from "@/lib/utils";
import type { Money } from "@/lib/api";
import {
  useHoldings,
  isFeatureDisabled,
  isExchangeDown,
  holdingsMessage,
  formatBps,
  changeClass,
} from "@/lib/holdings";
import { HoldingRow } from "./HoldingRow";

/**
 * "Your shares" on the home screen. Hidden entirely when the API says
 * the feature is off (404). Shows up to three holdings.
 */
export function SharesModule() {
  const q = useHoldings();

  if (q.isError && isFeatureDisabled(q.error)) return null;

  return (
    <Section
      title="Your shares"
      description="A slice of every business you tap at."
      action={
        q.data && q.data.holdings.length > 0 ? (
          <Link href="/holdings" className={sectionLinkClasses}>
            View all
          </Link>
        ) : null
      }
    >

      {q.isLoading ? (
        <SkeletonRows rows={2} />
      ) : q.isError ? (
        <InfoBanner
          tone={isExchangeDown(q.error) ? "warning" : "error"}
          action={
            <Button
              size="sm"
              variant="secondary"
              fullWidth={false}
              leadingIcon={<PiArrowClockwiseBold />}
              onClick={() => q.refetch()}
              loading={q.isFetching}
            >
              Retry
            </Button>
          }
        >
          {holdingsMessage(q.error)}
        </InfoBanner>
      ) : q.data && q.data.holdings.length === 0 ? (
        <div className="panel px-4 py-5 text-[13px] leading-5 text-fg-muted">
          Every tap at a participating business earns you a slice of it. None yet.
        </div>
      ) : q.data ? (
        <div className={listClasses}>
          <PortfolioTotals value={q.data.total_value} cost={q.data.total_cost} />
          {q.data.holdings.slice(0, 3).map((h) => (
            <HoldingRow key={h.symbol} holding={h} />
          ))}
        </div>
      ) : null}
    </Section>
  );
}

/**
 * Total value, cost, and value-vs-cost change. Shared by home and /holdings.
 * `large` is the hero form under a section eyebrow, so it carries no label
 * of its own.
 */
export function PortfolioTotals({ value, cost, large }: { value: Money; cost: Money; large?: boolean }) {
  const bps = cost.minor > 0 ? Math.round(((value.minor - cost.minor) / cost.minor) * 10_000) : 0;
  if (large) {
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
  return (
    <div className="flex items-end justify-between gap-4 px-3 py-3">
      <div className="grid gap-0.5">
        <p className="text-xs text-fg-muted">Total value</p>
        <p className="display text-2xl leading-7">{value.display}</p>
      </div>
      <div className="grid gap-0.5 text-right">
        <p className="text-xs tabular-nums text-fg-muted">Cost {cost.display}</p>
        <p className={cn("text-sm font-medium tabular-nums", changeClass(bps))}>{formatBps(bps)}</p>
      </div>
    </div>
  );
}
