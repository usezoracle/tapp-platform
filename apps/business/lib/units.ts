/**
 * Shares and money, between the form and the wire.
 *
 * The form takes whole shares and naira; the API takes units (1e-8 of a
 * share) and kobo. The conversion lives here and nowhere else, so a number
 * on screen is never a hundred million times what was meant.
 */

export const UNITS_PER_SHARE = 100_000_000;
export const KOBO_PER_NAIRA = 100;

/** The largest whole-share figure whose units still fit a safe integer. */
export const MAX_SHARES = Math.floor(Number.MAX_SAFE_INTEGER / UNITS_PER_SHARE); // 90,071,992

export const sharesToUnits = (shares: number): number => Math.round(shares * UNITS_PER_SHARE);

export const nairaToKobo = (naira: number): number => Math.round(naira * KOBO_PER_NAIRA);

/** "0.203125" or "8000000" from the wire -> "0.203125" / "8,000,000". */
export function formatShares(q: { shares: string; units: number } | null | undefined): string {
  if (!q) return "—";
  const n = Number(q.shares);
  if (!Number.isFinite(n)) return q.shares;
  return n.toLocaleString("en-NG", { maximumFractionDigits: 8 });
}

export const formatInt = (n: number): string => n.toLocaleString("en-NG");

/** A share count typed as whole shares, rendered with grouping. */
export const formatWholeShares = (n: number): string =>
  Number.isFinite(n) ? n.toLocaleString("en-NG", { maximumFractionDigits: 0 }) : "—";

export const formatNaira = (naira: number): string =>
  `₦${naira.toLocaleString("en-NG", { minimumFractionDigits: 2, maximumFractionDigits: 2 })}`;
