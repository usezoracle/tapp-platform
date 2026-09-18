"use client";

import { useMemo } from "react";
import dynamic from "next/dynamic";
import { Skeleton } from "@/components/ui/Skeleton";
import { chartHeightClasses } from "@/components/ui/PortfolioChart";
import type { ChartMarker, ChartPoint } from "@/components/ui/LineChart";
import type { Lot, PricePoint } from "@/lib/holdings";
import { cn } from "@/lib/utils";

const LineChart = dynamic(() => import("@/components/ui/LineChart"), {
  ssr: false,
  loading: () => <Skeleton className={cn("w-full", chartHeightClasses)} />,
});

interface PriceChartProps {
  /** Newest first, as the API returns it. */
  prices: PricePoint[];
  currency: string;
  /** The cardholder's lots: each one's acquisition date is marked on the line. */
  lots?: Lot[];
}

/**
 * One symbol's adjusted price over its last 90 sessions, on the same
 * chart the wallet draws its shares value with, so the two read as one
 * system. The marks under the line are the sessions on which a tap
 * earned a lot of this business.
 */
export function PriceChart({ prices, currency, lots }: PriceChartProps) {
  const points = useMemo<ChartPoint[]>(
    () =>
      [...prices]
        .map((p) => ({ time: p.date.slice(0, 10), value: (p.adjusted_price_kobo ?? p.price_kobo) / 100 }))
        .sort((a, b) => (a.time < b.time ? -1 : a.time > b.time ? 1 : 0)),
    [prices],
  );

  // A tap on a weekend lands on the next session's mark; one mark per
  // session however many lots it carries, since a second marker on the
  // same time would sit on the first.
  const markers = useMemo<ChartMarker[]>(() => {
    const days = new Set<string>();
    for (const l of lots ?? []) {
      const d = l.acquired_at.slice(0, 10);
      const session = points.find((p) => p.time >= d);
      if (session) days.add(session.time);
    }
    return [...days].sort().map((time) => ({ time }));
  }, [lots, points]);

  const format = useMemo(() => {
    const f = new Intl.NumberFormat("en-NG", {
      style: "currency",
      currency: currency || "NGN",
      maximumFractionDigits: 2,
    });
    return (v: number) => f.format(v);
  }, [currency]);

  if (points.length === 0) {
    return (
      <div className="panel px-4 py-8 text-center text-[13px] text-fg-muted">
        No priced sessions yet.
      </div>
    );
  }

  return (
    <LineChart
      points={points}
      format={format}
      markers={markers}
      live={points.length === 1}
      ariaLabel="Adjusted price over the last 90 sessions"
      className={chartHeightClasses}
    />
  );
}
