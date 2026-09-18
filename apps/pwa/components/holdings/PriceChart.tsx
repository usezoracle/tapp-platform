"use client";

import { useMemo } from "react";
import dynamic from "next/dynamic";
import { Skeleton } from "@/components/ui/Skeleton";
import { chartHeightClasses } from "@/components/ui/PortfolioChart";
import type { ChartFrame, ChartMarker, ChartPoint } from "@/components/ui/LineChart";
import { chartFrame, isoSeconds, sessionSeconds, type Lot, type PricePoint } from "@/lib/holdings";
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
 * One symbol's adjusted price over the last 30 days, on the same chart the
 * wallet draws its shares value with, so the two read as one system: one
 * point per session, and at the frame's start the price then in force, so
 * a symbol that last printed before the frame still has a line. The marks
 * under the line are the sessions on which a tap earned a lot of this
 * business.
 */
export function PriceChart({ prices, currency, lots }: PriceChartProps) {
  // The frame is fixed at mount; the page is not long-lived enough to
  // need a clock.
  const frame = useMemo(() => chartFrame(), []);
  const points = useMemo(() => priceSeries(prices, frame), [prices, frame]);

  // A tap on a weekend lands on the next session's mark; one mark per
  // session however many lots it carries, since a second marker on the
  // same time would sit on the first. Lots before the frame are not shown.
  const markers = useMemo<ChartMarker[]>(() => {
    const times = new Set<number>();
    for (const l of lots ?? []) {
      const at = isoSeconds(l.acquired_at);
      if (Number.isNaN(at) || at < frame.from) continue;
      const session = points.find((p) => p.time >= at);
      if (session) times.add(session.time);
    }
    return [...times].sort((a, b) => a - b).map((time) => ({ time }));
  }, [lots, points, frame]);

  const { format, formatExact } = useMemo(() => {
    const whole = new Intl.NumberFormat("en-NG", { style: "currency", currency: currency || "NGN", maximumFractionDigits: 0 });
    const exact = new Intl.NumberFormat("en-NG", { style: "currency", currency: currency || "NGN", minimumFractionDigits: 2, maximumFractionDigits: 2 });
    return { format: (v: number) => whole.format(v), formatExact: (v: number) => exact.format(v) };
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
      frame={frame}
      format={format}
      formatExact={formatExact}
      markers={markers}
      live={points.length === 1}
      ariaLabel="Adjusted price over the last 30 days"
      className={chartHeightClasses}
    />
  );
}

/** Exported for tests: the sessions inside the frame, after the price in force at its start. */
export function priceSeries(prices: PricePoint[], frame: ChartFrame): ChartPoint[] {
  const sessions = prices
    .map((p) => ({ time: sessionSeconds(p.date.slice(0, 10)), value: (p.adjusted_price_kobo ?? p.price_kobo) / 100 }))
    .sort((a, b) => a.time - b.time);
  const before = sessions.filter((s) => s.time <= frame.from).pop();
  const inside = sessions.filter((s) => s.time > frame.from && s.time <= frame.to);
  return before ? [{ time: frame.from, value: before.value }, ...inside] : inside;
}
