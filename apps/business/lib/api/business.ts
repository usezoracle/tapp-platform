/**
 * The merchant's business on the equity market, as apps/api/docs/equity.md
 * describes it. Quantities travel as `{units, shares}` (units = 1e-8 share),
 * money as `{minor, currency, display}`.
 */

import { request } from "./http";

export interface Money {
  minor: number;
  currency: "NGN" | "USD";
  display: string;
}

export interface Quantity {
  units: number;
  shares: string;
}

export interface Finding {
  criterion: string;
  met: boolean;
  detail: string;
}

export type BusinessState = "submitted" | "listed" | "rejected";

export interface LastSession {
  date: string;
  state: string;
  price: Money | null;
  volume: Quantity;
}

export interface CapTable {
  /** What one share is valued at today. */
  price: Money;
  /** Shares in issue at today's price: what the company is presently worth. */
  market_cap: Money;
  shares_authorised: Quantity;
  /** The company's declared shares in issue. */
  in_issue: Quantity;
  /** The part of the shares in issue held on the exchange's register. */
  on_register: Quantity;
  treasury_remaining: Quantity;
  released_today: Quantity;
  daily_release: Quantity;
  holders: number;
  top_holders: { cardholder_ref: string; units: number; shares: string }[];
  pending_funding: Money;
  escrowed_funding: Money;
  reference_price: Money | null;
  last_session: LastSession | null;
  halted: boolean;
  halt_reason?: string;
}

export interface Business {
  sender_id: string;
  legal_name: string;
  trading_name: string;
  rc_number: string;
  mcc: string;
  symbol: string;
  state: BusinessState;
  findings: Finding[];
  instrument_id: string | null;
  reference_price: Money;
  submitted_at: string;
  decided_at: string | null;
  live: CapTable | null;
  live_error?: string;
}

export interface Evidence {
  trading_months: number;
  audited_accounts: boolean;
  auditor_on_list: boolean;
  shares_in_issue: number;
  public_shares: number;
  holders: number;
  treasury_units: number;
  board_resolution: boolean;
  directors_clear: boolean;
}

export interface HolderAllocation {
  cardholder_ref: string;
  units: number;
  label: string;
}

export interface BusinessRequest {
  legal_name: string;
  trading_name: string;
  rc_number: string;
  mcc: string;
  symbol: string;
  evidence: Evidence;
  reference_price: { minor: number; currency: "NGN" };
  shares_authorised_units: number;
  daily_release_units: number;
  cofund_bps: number;
  holders: HolderAllocation[];
}

export interface HolderRow {
  cardholder_ref: string;
  holding: Quantity;
  locked: Quantity;
  cost: Money;
  first_acquired: string;
}

export interface HoldersPage {
  holders: HolderRow[];
  next_cursor: string | null;
}

export interface Me {
  id: string;
  email: string;
  firstName: string;
  lastName: string;
  scopes: string[];
  hasSenderProfile: boolean;
}

export const getMe = (token: string) => request<Me>("GET", "/v1/me", { token });

export const getBusiness = (token: string) =>
  request<Business>("GET", "/v1/sender/me/business", { token });

export const createBusiness = (token: string, body: BusinessRequest) =>
  request<Business>("POST", "/v1/sender/me/business", { token, body });

export const getHolders = (token: string, cursor?: string | null, limit = 50) => {
  const q = new URLSearchParams({ limit: String(limit) });
  if (cursor) q.set("cursor", cursor);
  return request<HoldersPage>("GET", `/v1/sender/me/business/holders?${q}`, { token });
};
