"use client";

import { useEffect, useState } from "react";

/**
 * The public market feed, read through our own route handler (the upstream
 * does not send CORS headers). One request per page load, shared by every
 * component that asks, so the strip and the table never disagree.
 */

export interface Instrument {
  symbol: string;
  legal_name: string;
  trading_name: string;
  status: string;
  halted: boolean;
  price_kobo: number;
  change_bps: number | null;
  reference_price_kobo: number;
  market_cap_kobo: number;
  holders: number;
  volume_units_today: number;
  listed_at: string;
}

export interface MarketFeed {
  business_date: string;
  is_trading: boolean;
  phase: string;
  as_of: string;
  instruments: Instrument[];
}

let inflight: Promise<MarketFeed | null> | null = null;

function load(): Promise<MarketFeed | null> {
  if (!inflight) {
    inflight = fetch("/api/market", { cache: "no-store" })
      .then((r) => (r.ok ? (r.json() as Promise<MarketFeed>) : null))
      .then((feed) => (feed && Array.isArray(feed.instruments) ? feed : null))
      .catch(() => null);
  }
  return inflight;
}

export type MarketState = { status: "loading" } | { status: "down" } | { status: "ok"; feed: MarketFeed };

export function useMarket(): MarketState {
  const [state, setState] = useState<MarketState>({ status: "loading" });
  useEffect(() => {
    let alive = true;
    load().then((feed) => {
      if (!alive) return;
      setState(feed ? { status: "ok", feed } : { status: "down" });
    });
    return () => {
      alive = false;
    };
  }, []);
  return state;
}

/* ---- formatting ---- */

const naira = new Intl.NumberFormat("en-NG", {
  style: "currency",
  currency: "NGN",
  minimumFractionDigits: 2,
  maximumFractionDigits: 2,
});

export function formatPrice(kobo: number): string {
  return naira.format(kobo / 100);
}

/** ₦545m, ₦1.2b, ₦830k -- one significant decimal, dropped when it is zero. */
export function formatCompactNaira(kobo: number): string {
  const n = kobo / 100;
  const units: [number, string][] = [
    [1e9, "b"],
    [1e6, "m"],
    [1e3, "k"],
  ];
  for (const [size, suffix] of units) {
    if (n >= size) {
      const v = n / size;
      const s = v >= 100 ? v.toFixed(0) : v.toFixed(1).replace(/\.0$/, "");
      return `₦${s}${suffix}`;
    }
  }
  return naira.format(n);
}

export function formatChange(bps: number | null): { text: string; tone: "up" | "down" | "flat" } {
  if (bps === null || bps === 0) return { text: "—", tone: "flat" };
  const pct = (bps / 100).toFixed(2);
  return bps > 0 ? { text: `+${pct}%`, tone: "up" } : { text: `${pct}%`, tone: "down" };
}

export function sessionLabel(feed: MarketFeed): string {
  if (feed.is_trading) return "Open";
  switch (feed.phase) {
    case "pre_open":
    case "preopen":
      return "Pre-open";
    case "auction":
      return "Pricing";
    default:
      return "Closed";
  }
}
