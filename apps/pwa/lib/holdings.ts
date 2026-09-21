"use client";

/**
 * Holdings — the slice of a business a cardholder earns with every tap.
 *
 * Read through the shared API client (lib/api/holdings.ts), so the envelope,
 * the base URL and the 401 refresh are the same as every other resource.
 * Nothing here falls back: a read that fails surfaces as an error for the UI
 * to show. Two errors are ordinary enough to be handled rather than shown
 * raw:
 *
 *   404 → the feature is off for this deployment. Callers render nothing
 *         or an honest empty state, and never retry (`isFeatureDisabled`).
 *   503 → the exchange is unreachable. Inline message and a retry button
 *         (`isExchangeDown`).
 */

import { useQuery } from "@tanstack/react-query";
import { useSession } from "./auth";
import { ApiError, holdingsApi, type EquityState } from "./api";
export type {
  Holding,
  HoldingDetail,
  HoldingsResponse,
  EquityActivityItem,
  EquityActivityResponse,
  EquityState,
  Lot,
  PricePoint,
  Quantity,
  LastSession,
} from "./api";

// -----------------------------------------------------------------------------
// Failure modes
// -----------------------------------------------------------------------------

export const isFeatureDisabled = (err: unknown): boolean =>
  err instanceof ApiError && err.status === 404;

export const isExchangeDown = (err: unknown): boolean =>
  err instanceof ApiError && err.status === 503;

/**
 * What to tell the person. The server's message where it has one that a
 * person can act on; a sentence of our own for the two named failures, whose
 * server messages are written for an operator.
 */
export function holdingsMessage(err: unknown): string {
  if (isFeatureDisabled(err)) return "Stocks are not enabled for this account.";
  if (isExchangeDown(err)) return "The equity market is unreachable right now. Try again shortly.";
  return err instanceof Error ? err.message : "Could not load your stocks.";
}

/** Never retry a 404 (feature off); retry anything else once. */
const retry = (count: number, err: unknown) => !isFeatureDisabled(err) && count < 1;

/** States that are on their way to becoming stocks. */
export const isInFlight = (state: EquityState) =>
  state === "held" || state === "queued" || state === "pending" || state === "escrowed";

// -----------------------------------------------------------------------------
// Hooks
// -----------------------------------------------------------------------------

export function useHoldings() {
  const { session, hydrated } = useSession();
  return useQuery({
    queryKey: ["holdings", "list", session?.email ?? ""],
    enabled: hydrated && !!session,
    queryFn: () => holdingsApi.list(session!.jwt),
    staleTime: 60_000,
    retry,
  });
}

export function useHolding(symbol: string | null) {
  const { session, hydrated } = useSession();
  return useQuery({
    queryKey: ["holdings", "detail", session?.email ?? "", symbol],
    enabled: hydrated && !!session && !!symbol,
    queryFn: () => holdingsApi.detail(session!.jwt, symbol!),
    staleTime: 60_000,
    retry,
  });
}

export function useEquityActivity(limit = 100) {
  const { session, hydrated } = useSession();
  return useQuery({
    queryKey: ["holdings", "equity-activity", session?.email ?? "", limit],
    enabled: hydrated && !!session,
    queryFn: () => holdingsApi.equityActivity(session!.jwt, limit),
    staleTime: 60_000,
    retry,
  });
}

// -----------------------------------------------------------------------------
// Formatting
// -----------------------------------------------------------------------------

/**
 * A quantity of stock as text: "0.125000" → "0.125", "3.000" → "3".
 *
 * Full precision by default, which the holding page keeps -- it is the one
 * place the exact figure is the point. Rows and the feed pass `decimals`
 * (4) so "0.58311111" reads as "0.5831"; trailing zeros are trimmed either
 * way. The API's field is still called `shares`; the word the app uses is
 * "stocks".
 */
export function formatShares(shares: string, decimals?: number): string {
  let s = shares;
  if (decimals !== undefined) {
    const n = Number(shares);
    if (Number.isFinite(n)) s = n.toFixed(decimals);
  }
  if (!s.includes(".")) return s;
  const trimmed = s.replace(/0+$/, "").replace(/\.$/, "");
  return trimmed === "" || trimmed === "-" ? "0" : trimmed;
}

/** Rows and the feed: four decimals, zeros trimmed. */
export const BRIEF_DECIMALS = 4;

/** "1" → "1 stock", "0.125" → "0.125 stocks". */
export function sharesLabel(shares: string, decimals?: number): string {
  const n = formatShares(shares, decimals);
  return `${n} ${n === "1" ? "stock" : "stocks"}`;
}

/** 125 → "+1.25%", -40 → "−0.40%", 0 → "0.00%". */
export function formatBps(bps: number): string {
  const pct = Math.abs(bps) / 100;
  const s = pct.toFixed(2) + "%";
  if (bps > 0) return "+" + s;
  if (bps < 0) return "−" + s;
  return s;
}

export const changeTone = (bps: number): "up" | "down" | "flat" =>
  bps > 0 ? "up" : bps < 0 ? "down" : "flat";

/** The text colour for a change, by direction. Flat is muted, not coloured. */
export const changeClass = (bps: number): string => {
  const tone = changeTone(bps);
  return tone === "up" ? "text-positive" : tone === "down" ? "text-negative" : "text-fg-muted";
};

/** Two-letter tile initials from a trading name: "Mama Put" → "MP". */
export function holdingInitials(name: string): string {
  const words = name.trim().split(/\s+/).filter(Boolean);
  if (words.length === 0) return "?";
  if (words.length === 1) return words[0].slice(0, 2).toUpperCase();
  return (words[0][0] + words[1][0]).toUpperCase();
}

/** "2026-11-04T00:00:00Z" → "4 Nov 2026". */
export function formatDate(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleDateString("en-GB", { day: "numeric", month: "short", year: "numeric" });
}

// -----------------------------------------------------------------------------
// Chart time
// -----------------------------------------------------------------------------

const DAY = 86_400;

/** How far back the charts look. */
export const CHART_DAYS = 30;

/** The chart's x-axis: the last 30 days, ending now, in UTC seconds. */
export function chartFrame(now = nowSeconds()): { from: number; to: number } {
  return { from: now - CHART_DAYS * DAY, to: now };
}

export const nowSeconds = () => Math.floor(Date.now() / 1000);

/** A session date "2026-11-04" as the UTC seconds of its midnight. */
export function sessionSeconds(date: string): number {
  return Date.UTC(+date.slice(0, 4), +date.slice(5, 7) - 1, +date.slice(8, 10)) / 1000;
}

/** An ISO datetime as UTC seconds; NaN when unparseable. */
export const isoSeconds = (iso: string) => Date.parse(iso) / 1000;

/**
 * A chart point's date. A time on a UTC midnight is a session date, shown
 * as that date; a time of day is shown in the reader's own zone.
 */
export function formatPointDate(sec: number): string {
  const d = new Date(sec * 1000);
  const utc = sec % DAY === 0;
  return d.toLocaleDateString("en-GB", { day: "numeric", month: "short", year: "numeric", ...(utc ? { timeZone: "UTC" } : {}) });
}

/** "2026-11-04" → "4 Nov" — for chart axes and compact rows. */
export function formatDayMonth(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleDateString("en-GB", { day: "numeric", month: "short" });
}
