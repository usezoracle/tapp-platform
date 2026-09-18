"use client";

import { useEffect, useMemo, useState } from "react";
import dynamic from "next/dynamic";
import { useQueries, useQuery } from "@tanstack/react-query";
import { cn } from "@/lib/utils";
import { useSession } from "@/lib/auth";
import { holdingsApi, type EquityActivityItem, type HoldingDetail } from "@/lib/api";
import {
  chartFrame,
  formatPointDate,
  isFeatureDisabled,
  isoSeconds,
  nowSeconds,
  sessionSeconds,
} from "@/lib/holdings";
import { Skeleton } from "./Skeleton";
import type { ChartFrame, ChartPoint } from "./LineChart";

/** 160px on the phone, 200 from 1024px, on both the chart and its skeleton. */
export const chartHeightClasses = "h-40 lg:h-[200px]";

const LineChart = dynamic(() => import("./LineChart"), {
  ssr: false,
  loading: () => <Skeleton className={cn("w-full", chartHeightClasses)} />,
});

/** How often the chart asks again. The list and details are otherwise fresh for 60s. */
const LIVE_MS = 30_000;
/** How often the frame's "now" moves on, between fetches. */
const CLOCK_MS = 60_000;
const ACTIVITY_LIMIT = 200;

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
const formatExact = (v: number) => naira.format(v);

/**
 * The value of everything the card has earned, over the last 30 days.
 *
 * Computed here, exactly, from the events that moved it: each allocation
 * (a tap's shares landing, at its own moment) and each session's price.
 * At any time t the value is, over the symbols, the units allocated at or
 * before t times the price in force at t: the latest session price dated
 * at or before t, or, before the first print, the allocation's own price.
 * The line has a point at the start of the frame (0 if nothing was held),
 * two at every allocation (the second before and the moment itself: a
 * step up, at 20:17 if that is when it landed), one per session since,
 * and one at now, so it moves whenever the value did. Integers throughout (a unit is 1e-8 of a share; a price is kobo per
 * share) and only the final kobo becomes a float for plotting.
 *
 * Live: this component alone asks for the list, the details and the
 * activity every 30s, on the same query keys the rest of the app uses, so
 * nothing is fetched twice.
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

  const activity = useQuery({
    queryKey: ["holdings", "equity-activity", email, ACTIVITY_LIMIT],
    enabled,
    queryFn: () => holdingsApi.equityActivity(session!.jwt, ACTIVITY_LIMIT),
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

  const now = useClock();
  const frame = useMemo(() => chartFrame(now), [now]);
  const points = useMemo(
    () => portfolioSeries(activity.data?.activity ?? [], details.loaded, frame),
    [activity.data, details.loaded, frame],
  );

  if (list.isError && isFeatureDisabled(list.error)) return null;
  // The shares module below already says what went wrong; a second banner
  // about the same request would say it twice.
  if (list.isError) return null;

  const loading = list.isLoading || activity.isLoading || (symbols.length > 0 && details.pending);

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
  const change = changeSince(points);

  return (
    <section className={cn("grid gap-3", className)}>
      <div className="grid gap-0.5">
        <p className="eyebrow">Shares value</p>
        <p className="display text-2xl leading-7">{naira.format(last.value)}</p>
        {change ? (
          <p
            className={cn(
              "text-[13px] leading-5 tabular-nums",
              change.delta > 0 ? "text-positive" : change.delta < 0 ? "text-negative" : "text-fg-muted",
            )}
          >
            {change.delta > 0 ? "+" : change.delta < 0 ? "−" : ""}
            {naira.format(Math.abs(change.delta))}
            {` (${signedPct(change.ratio)})`}
            <span className="text-fg-muted"> · since {formatPointDate(change.since)}</span>
          </p>
        ) : (
          <p className="text-[13px] leading-5 text-fg-muted">First session</p>
        )}
      </div>
      <LineChart
        points={points}
        frame={frame}
        format={formatAxis}
        formatExact={formatExact}
        live
        ariaLabel="Shares value over the last 30 days"
        className={chartHeightClasses}
      />
    </section>
  );
}

/** The current time, moved on every minute so the frame's end keeps up. */
function useClock(): number {
  const [now, setNow] = useState(nowSeconds);
  useEffect(() => {
    const id = setInterval(() => setNow(nowSeconds()), CLOCK_MS);
    return () => clearInterval(id);
  }, []);
  return now;
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

/**
 * The header's change: from the first moment something was held in the
 * frame to now. Nothing to say until the line has moved or a day has
 * passed since that moment, so a single fresh allocation reads as the
 * first session rather than as "+₦0.00".
 */
export function changeSince(points: ChartPoint[]): { delta: number; ratio: number; since: number } | null {
  const first = points.find((p) => p.value > 0);
  const last = points[points.length - 1];
  if (!first || !last || first === last) return null;
  const sameDay = Math.floor(first.time / DAY) === Math.floor(last.time / DAY);
  if (first.value === last.value && sameDay) return null;
  const delta = last.value - first.value;
  return { delta, ratio: delta / first.value, since: first.time };
}

// BigInt through the constructor, not literals: the tsconfig targets ES2017.
const ZERO = BigInt(0);
const UNITS_PER_SHARE = BigInt(100_000_000);
const HALF_UNIT = BigInt(50_000_000);
const DAY = 86_400;

/** Shares landing: when, how many units, and at what price (kobo a share). */
interface Allocation {
  at: number;
  units: bigint;
  kobo: bigint;
}

/** A session's adjusted price, from its UTC midnight. */
interface Session {
  at: number;
  kobo: bigint;
}

/**
 * The line, from the activity's allocations and the details' sessions.
 * Exported for tests. A lot the activity does not carry (older than its
 * limit, or not from a tap) is taken from the holding itself, at its
 * average cost, so the value never misses shares the card holds.
 */
export function portfolioSeries(
  activity: EquityActivityItem[],
  details: HoldingDetail[],
  frame: ChartFrame,
): ChartPoint[] {
  const bySymbol = new Map<string, { allocations: Allocation[]; sessions: Session[] }>();
  const of = (symbol: string) => {
    let s = bySymbol.get(symbol);
    if (!s) bySymbol.set(symbol, (s = { allocations: [], sessions: [] }));
    return s;
  };

  const known = new Set<string>();
  for (const item of activity) {
    if (item.state !== "allocated" || !item.symbol || !item.price || item.bought.units <= 0) continue;
    const at = isoSeconds(item.at);
    if (Number.isNaN(at)) continue;
    known.add(item.tap_id);
    of(item.symbol).allocations.push({
      at,
      units: BigInt(Math.round(item.bought.units)),
      kobo: BigInt(Math.round(item.price.minor)),
    });
  }
  for (const d of details) {
    const s = of(d.symbol);
    for (const l of d.lots) {
      if (l.tap_id && known.has(l.tap_id)) continue;
      const at = isoSeconds(l.acquired_at);
      if (Number.isNaN(at) || l.units <= 0) continue;
      s.allocations.push({
        at,
        units: BigInt(Math.round(l.units)),
        kobo: BigInt(Math.round((l.cost.minor * 1e8) / l.units)),
      });
    }
    for (const p of d.prices) {
      s.sessions.push({ at: sessionSeconds(p.date.slice(0, 10)), kobo: BigInt(Math.round(p.adjusted_price_kobo ?? p.price_kobo)) });
    }
    s.sessions.sort((a, b) => a.at - b.at);
  }
  for (const s of bySymbol.values()) s.allocations.sort((a, b) => a.at - b.at);

  const firstHeld = Math.min(...[...bySymbol.values()].flatMap((s) => s.allocations.map((a) => a.at)));
  if (!Number.isFinite(firstHeld)) return [];

  // When the value can change: the frame's start, each allocation, each
  // session once something is held, and now. An allocation is a step, so
  // it gets the second before it too, at the value it stepped up from.
  const times = new Set<number>([frame.from, frame.to]);
  for (const s of bySymbol.values()) {
    for (const a of s.allocations) {
      if (a.at > frame.from + 1 && a.at < frame.to) times.add(a.at - 1);
      if (a.at > frame.from && a.at < frame.to) times.add(a.at);
    }
    for (const p of s.sessions) if (p.at > frame.from && p.at < frame.to && p.at >= firstHeld) times.add(p.at);
  }

  const valueAt = (t: number): number => {
    let sum = ZERO; // units × kobo, i.e. kobo × 1e8
    for (const s of bySymbol.values()) {
      let units = ZERO;
      let own: bigint | null = null;
      for (const a of s.allocations) {
        if (a.at > t) break;
        units += a.units;
        own = a.kobo;
      }
      if (units === ZERO) continue;
      let price: bigint | null = null;
      for (const p of s.sessions) {
        if (p.at > t) break;
        price = p.kobo;
      }
      sum += units * (price ?? own!);
    }
    return Number((sum + HALF_UNIT) / UNITS_PER_SHARE) / 100;
  };

  return [...times].sort((a, b) => a - b).map((time) => ({ time, value: valueAt(time) }));
}
