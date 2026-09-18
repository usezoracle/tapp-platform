"use client";

import { useMemo } from "react";
import dynamic from "next/dynamic";
import { useQueries, useQuery } from "@tanstack/react-query";
import { cn } from "@/lib/utils";
import { useSession } from "@/lib/auth";
import { holdingsApi, type HoldingDetail } from "@/lib/api";
import { isFeatureDisabled } from "@/lib/holdings";
import { Skeleton } from "./Skeleton";
import type { ChartPoint } from "./LineChart";

/** 160px on the phone, 200 from 1024px, on both the chart and its skeleton. */
export const chartHeightClasses = "h-40 lg:h-[200px]";

const LineChart = dynamic(() => import("./LineChart"), {
  ssr: false,
  loading: () => <Skeleton className={cn("w-full", chartHeightClasses)} />,
});

/** How often the chart asks again. The list and details are otherwise fresh for 60s. */
const LIVE_MS = 30_000;
const SESSIONS = 90;

const retry = (count: number, err: unknown) => !isFeatureDisabled(err) && count < 1;

const naira = new Intl.NumberFormat("en-NG", {
  style: "currency",
  currency: "NGN",
  minimumFractionDigits: 2,
  maximumFractionDigits: 2,
});
const nairaAxis = new Intl.NumberFormat("en-NG", {
  style: "currency",
  currency: "NGN",
  maximumFractionDigits: 0,
});
const formatAxis = (v: number) => nairaAxis.format(v);

/**
 * The value of everything the card has earned, session by session.
 *
 * Computed here, exactly: for each session date, the sum over holdings of
 * the units held on that date times that day's adjusted price, in
 * integers (a unit is 1e-8 of a share; the price is kobo per share), and
 * only the final kobo figure becomes a float for plotting. Dates are the
 * union of every holding's price dates, the last 90, from the first date
 * on which anything was held: the sessions before the first tap would be
 * a flat line at zero, which is not a history of anything. A holding
 * whose exchange did not print a price on one of those dates carries its
 * last price forward, the way a portfolio statement does.
 *
 * Live: this component alone asks for the list and the details every 30s,
 * on the same query keys the rest of the app uses, so nothing is fetched
 * twice; a new session lands on the line through `update()`.
 */
export function PortfolioChart({ className }: { className?: string }) {
  const { session, hydrated } = useSession();
  const enabled = hydrated && !!session;
  const email = session?.email ?? "";

  const list = useQuery({
    queryKey: ["holdings", "list", email],
    enabled,
    queryFn: () => holdingsApi.list(session!.jwt),
    staleTime: 60_000,
    refetchInterval: LIVE_MS,
    retry,
  });

  const symbols = useMemo(
    () => (list.data?.holdings ?? []).map((h) => h.symbol),
    [list.data],
  );

  const details = useQueries({
    queries: symbols.map((symbol) => ({
      queryKey: ["holdings", "detail", email, symbol],
      enabled,
      queryFn: () => holdingsApi.detail(session!.jwt, symbol),
      staleTime: 60_000,
      refetchInterval: LIVE_MS,
      retry,
    })),
    combine,
  });

  const points = useMemo(() => portfolioSeries(details.loaded), [details.loaded]);

  if (list.isError && isFeatureDisabled(list.error)) return null;
  // The shares module below already says what went wrong; a second banner
  // about the same request would say it twice.
  if (list.isError) return null;

  const loading = list.isLoading || (symbols.length > 0 && details.pending);

  if (loading) {
    return (
      <section className={cn("grid gap-3", className)} aria-busy>
        <div className="grid gap-1.5">
          <Skeleton className="h-3 w-24" />
          <Skeleton className="h-7 w-40" />
          <Skeleton className="h-3 w-32" />
        </div>
        <Skeleton className={cn("w-full", chartHeightClasses)} />
      </section>
    );
  }

  if (symbols.length === 0 || points.length === 0) {
    return (
      <section className={cn("grid gap-3", className)}>
        <p className="eyebrow">Shares value</p>
        <div className="panel px-4 py-5 text-[13px] leading-5 text-fg-muted">
          Your shares value will chart here after your first tap at a listed business.
        </div>
      </section>
    );
  }

  const last = points[points.length - 1];
  const first = points[0];
  const delta = points.length > 1 ? last.value - first.value : null;
  const ratio = delta !== null && first.value !== 0 ? delta / first.value : null;

  return (
    <section className={cn("grid gap-3", className)}>
      <div className="grid gap-0.5">
        <p className="eyebrow">Shares value</p>
        <p className="display text-2xl leading-7">{naira.format(last.value)}</p>
        {delta !== null ? (
          <p
            className={cn(
              "text-[13px] leading-5 tabular-nums",
              delta > 0 ? "text-positive" : delta < 0 ? "text-negative" : "text-fg-muted",
            )}
          >
            {delta > 0 ? "+" : delta < 0 ? "−" : ""}
            {naira.format(Math.abs(delta))}
            {ratio !== null ? ` (${signedPct(ratio)})` : ""}
            <span className="text-fg-muted"> · {points.length} sessions</span>
          </p>
        ) : (
          <p className="text-[13px] leading-5 text-fg-muted">First session</p>
        )}
      </div>
      <LineChart
        points={points}
        format={formatAxis}
        formatExact={(v) => naira.format(v)}
        live
        ariaLabel={`Shares value over the last ${points.length} sessions`}
        className={chartHeightClasses}
      />
    </section>
  );
}

function combine(results: { data: HoldingDetail | undefined; isPending: boolean }[]) {
  return {
    loaded: results.map((r) => r.data).filter((d): d is HoldingDetail => !!d),
    pending: results.some((r) => r.isPending),
  };
}

function signedPct(ratio: number): string {
  const s = (Math.abs(ratio) * 100).toFixed(2) + "%";
  return ratio > 0 ? "+" + s : ratio < 0 ? "−" + s : s;
}

// BigInt through the constructor, not literals: the tsconfig targets ES2017.
const ZERO = BigInt(0);
const UNITS_PER_SHARE = BigInt(100_000_000);
const HALF_UNIT = BigInt(50_000_000);

/** Exported for tests and for the holdings screens, should they want the same line. */
export function portfolioSeries(details: HoldingDetail[]): ChartPoint[] {
  const dateSet = new Set<string>();
  const per = details.map((d) => {
    const prices = d.prices
      .map((p) => ({ date: day(p.date), kobo: BigInt(Math.round(p.adjusted_price_kobo ?? p.price_kobo)) }))
      .sort((a, b) => (a.date < b.date ? -1 : a.date > b.date ? 1 : 0));
    for (const p of prices) dateSet.add(p.date);
    const lots = d.lots.map((l) => ({ date: day(l.acquired_at), units: BigInt(Math.round(l.units)) }));
    return { prices, lots };
  });

  const firstHeld = per
    .flatMap((h) => h.lots.map((l) => l.date))
    .sort()[0];
  if (!firstHeld) return [];
  const dates = [...dateSet]
    .filter((d) => d >= firstHeld)
    .sort()
    .slice(-SESSIONS);

  return dates.map((date) => {
    let sum = ZERO; // units × kobo, i.e. kobo × 1e8
    for (const h of per) {
      let units = ZERO;
      for (const l of h.lots) if (l.date <= date) units += l.units;
      if (units === ZERO) continue;
      let price: bigint | null = null;
      for (const p of h.prices) {
        if (p.date <= date) price = p.kobo;
        else break;
      }
      if (price === null) continue;
      sum += units * price;
    }
    const kobo = (sum + HALF_UNIT) / UNITS_PER_SHARE;
    return { time: date, value: Number(kobo) / 100 };
  });
}

/** The calendar date of an ISO date or datetime. */
const day = (iso: string) => iso.slice(0, 10);
