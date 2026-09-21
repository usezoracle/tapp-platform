/**
 * Holdings: the slice of a business a cardholder earns with every tap.
 *
 * These are real securities on Freedom Exchange, read through the API. No
 * mock: a read that fails reaches the UI as an error. Two failures are
 * ordinary enough to have names (see lib/holdings.ts):
 *
 *   404  the feature is not enabled for this deployment
 *   503  the exchange is unreachable right now
 */

import { request } from "./http";
import type { Money } from "./money";

export interface Quantity {
  units: number;
  /** Decimal string, e.g. "0.125000". Format with `formatShares`. */
  shares: string;
}

export interface LastSession {
  date: string;
  price: Money;
  source: string;
}

export interface Lot {
  units: number;
  /** Decimal string. */
  shares: string;
  cost: Money;
  acquired_at: string;
  /** ISO date the 120-day lock on tap-earned stock ends. */
  transferable_from: string;
  tap_id: string | null;
}

/** Raw exchange passthrough: prices in kobo, volume in 1e-8 units. */
export interface PricePoint {
  date: string;
  source: string;
  price_kobo: number;
  adjusted_price_kobo: number;
  volume_units: number;
  trades: number;
}

export interface Holding {
  symbol: string;
  legal_name: string;
  trading_name: string;
  holding: Quantity;
  sellable: Quantity;
  locked: Quantity;
  next_unlock: string | null;
  cost: Money;
  reference_price: Money | null;
  value: Money;
  change_bps: number;
  lot_count: number;
  last_session: LastSession | null;
}

export interface HoldingsResponse {
  as_of: string;
  total_value: Money;
  total_cost: Money;
  holdings: Holding[];
}

export interface HoldingDetail extends Holding {
  lots: Lot[];
  /** Newest first, at most 90 sessions. */
  prices: PricePoint[];
}

export type EquityState =
  | "held"
  | "cancelled"
  | "queued"
  | "failed"
  | "escrowed"
  | "pending"
  | "allocated"
  | "reversed";

export interface EquityActivityItem {
  tap_id: string;
  symbol: string | null;
  funding: Money | null;
  state: EquityState;
  bought: Quantity | null;
  price: Money | null;
  at: string;
  /** The business tapped at, and its listing. */
  merchant?: { ref: string; name: string; symbol: string } | null;
  /** The whole tap the funding came out of. */
  tap_amount?: Money;
}

export interface EquityActivityResponse {
  activity: EquityActivityItem[];
}

export const holdingsApi = {
  list: (jwt: string) =>
    request<HoldingsResponse>("GET", "/v1/me/holdings", { token: jwt }),
  detail: (jwt: string, symbol: string) =>
    request<HoldingDetail>(
      "GET", `/v1/me/holdings/${encodeURIComponent(symbol)}`, { token: jwt }),
  equityActivity: (jwt: string, limit: number) =>
    request<EquityActivityResponse>(
      "GET", `/v1/me/equity-activity?limit=${limit}`, { token: jwt }),
};
