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

/* ------------------------------------------------------- exact valuation */

/**
 * "100000" or "100000.5" (naira, up to two places) -> kobo, exactly.
 * Null when the text is not a price.
 */
export function koboFromText(naira: string): bigint | null {
  const m = /^(\d+)(?:\.(\d{0,2}))?$/.exec(naira.trim().replace(/,/g, ""));
  if (!m) return null;
  const frac = (m[2] ?? "").padEnd(2, "0");
  return BigInt(m[1]) * BigInt(100) + BigInt(frac);
}

/** "10000000" (whole shares) -> BigInt. Null when the text is not a whole number. */
export function wholeFromText(shares: string): bigint | null {
  const t = shares.trim().replace(/,/g, "");
  return /^\d+$/.test(t) ? BigInt(t) : null;
}

/** Digits with thousands separators, no locale rounding: "1000000" -> "1,000,000". */
export function groupDigits(digits: string): string {
  return digits.replace(/\B(?=(\d{3})+(?!\d))/g, ",");
}

/** Kobo -> "₦1,000,000" or "₦40.50" (decimals only when there are any). */
export function formatKobo(kobo: bigint): string {
  const hundred = BigInt(100);
  const whole = groupDigits((kobo / hundred).toString());
  const frac = Number(kobo % hundred);
  return frac === 0 ? `₦${whole}` : `₦${whole}.${String(frac).padStart(2, "0")}`;
}

/** The largest kobo figure that survives the trip through a JSON number exactly. */
export const MAX_KOBO = BigInt(Number.MAX_SAFE_INTEGER);

/** Kobo -> "100000.50" or "100000": the text a naira field holds for a wire amount. */
export function nairaTextFromMinor(minor: number): string {
  if (!Number.isFinite(minor) || minor < 0) return "";
  const kobo = BigInt(Math.trunc(minor));
  const hundred = BigInt(100);
  const whole = (kobo / hundred).toString();
  const frac = Number(kobo % hundred);
  return frac === 0 ? whole : `${whole}.${String(frac).padStart(2, "0")}`;
}

/**
 * How the exchange prices a listing, computed exactly on kobo and whole
 * shares so the form shows the same figures the exchange will set:
 *
 *   fair value    = net assets + revenue (trailing 12 months)
 *   listing price = fair value ÷ shares in issue, rounded down to the kobo
 *   company value = listing price × shares in issue (fair value less the rounding)
 */
export interface Pricing {
  fairValue: bigint;
  listingPrice: bigint;
  companyValue: bigint;
  shares: bigint;
}

/** Net assets + revenue in kobo. Null until both are typed and more than zero. */
export function fairValueFromText(netAssets: string, revenue: string): bigint | null {
  const a = koboFromText(netAssets);
  const r = koboFromText(revenue);
  if (a === null || r === null || a <= BigInt(0) || r <= BigInt(0)) return null;
  return a + r;
}

/** The exchange's pricing of the typed figures. Null until financials and shares in issue are valid. */
export function pricingFromText(netAssets: string, revenue: string, sharesInIssue: string): Pricing | null {
  const fairValue = fairValueFromText(netAssets, revenue);
  const shares = wholeFromText(sharesInIssue);
  if (fairValue === null || shares === null || shares === BigInt(0)) return null;
  const listingPrice = fairValue / shares;
  return { fairValue, listingPrice, companyValue: listingPrice * shares, shares };
}
