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
import { ApiError, holdingsApi, type EquityActivityItem, type EquityState } from "./api";
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
  if (isFeatureDisabled(err)) return "Shares are not enabled for this account.";
  if (isExchangeDown(err)) return "The equity market is unreachable right now. Try again shortly.";
  return err instanceof Error ? err.message : "Could not load your shares.";
}

/** Never retry a 404 (feature off); retry anything else once. */
const retry = (count: number, err: unknown) => !isFeatureDisabled(err) && count < 1;

/** States that are on their way to becoming shares. */
export const isInFlight = (state: EquityState) =>
  state === "queued" || state === "pending" || state === "escrowed";

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

/** "0.125000" → "0.125", "3.000" → "3", "12" → "12". */
export function formatShares(shares: string): string {
  if (!shares.includes(".")) return shares;
  const trimmed = shares.replace(/0+$/, "").replace(/\.$/, "");
  return trimmed === "" || trimmed === "-" ? "0" : trimmed;
}

/** "1" → "1 share", "0.125" → "0.125 shares". */
export function sharesLabel(shares: string): string {
  const n = formatShares(shares);
  return `${n} ${n === "1" ? "share" : "shares"}`;
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

/** "2026-11-04" → "4 Nov" — for chart axes and compact rows. */
export function formatDayMonth(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleDateString("en-GB", { day: "numeric", month: "short" });
}

/**
 * Index equity activity by tap id so a ledger movement can show
 * "+0.125 MAMAPUT shares" without the activity payload carrying an equity
 * field. A movement joins on its `refId` when its `refType` is "tap".
 */
export function indexEquityByTapId(
  items: EquityActivityItem[] | undefined,
): Map<string, EquityActivityItem> {
  const map = new Map<string, EquityActivityItem>();
  for (const item of items ?? []) map.set(item.tap_id, item);
  return map;
}

/**
 * The equity line under a tap row, or null when there is nothing to say.
 *
 * "+0.125 MAMAPUT shares" once allocated; in-flight and reversed states are
 * named; a failed allocation says nothing, because the tap itself still went
 * through and a red line under it would read as the payment failing.
 */
export function equityLine(item: EquityActivityItem | undefined): string | null {
  if (!item || !item.symbol || item.state === "failed") return null;
  const n = formatShares(item.bought.shares);
  const noun = n === "1" ? "share" : "shares";
  if (item.state === "allocated") return `+${n} ${item.symbol} ${noun}`;
  if (isInFlight(item.state)) return `${n} ${item.symbol} ${noun} · pending`;
  return `${n} ${item.symbol} ${noun} · reversed`;
}
