/**
 * The listing form: what a merchant types, what the API is sent, and the
 * map between the two. The form holds strings (whole shares, naira); the
 * request holds units and kobo. `toRequest` is the only place that converts.
 */

import { z } from "zod";
import type { BusinessRequest } from "./api";
import { MAX_KOBO, MAX_SHARES, koboFromText, sharesToUnits } from "./units";

export const STEPS = ["Legal identity", "Shares", "Evidence", "Review"] as const;

const RC_RE = /^(RC|BN)\d+$/;
export const SYMBOL_RE = /^[A-Z][A-Z0-9]{2,11}$/;
const UUID_RE = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

/** Typed text, with grouping commas and whitespace dropped before checking. */
const cleaned = z.string().transform((s) => s.trim().replace(/,/g, ""));

/** A whole number of shares, typed as text. */
const wholeShares = (what: string) =>
  cleaned.pipe(
    z
      .string()
      .min(1, `Enter ${what}`)
      .regex(/^\d+$/, "Whole shares only, no decimals")
      .refine((s) => Number(s) <= MAX_SHARES, `At most ${MAX_SHARES.toLocaleString("en-NG")} shares`),
  );

const wholeCount = (what: string) =>
  cleaned.pipe(z.string().min(1, `Enter ${what}`).regex(/^\d+$/, "A whole number"));

/** A naira figure typed as text: whole naira, up to two places of kobo, more than zero. */
const nairaAmount = (what: string) =>
  cleaned.pipe(
    z
      .string()
      .min(1, `Enter ${what}`)
      .regex(/^\d+(\.\d{1,2})?$/, "Naira, up to two decimal places")
      .refine((s) => (koboFromText(s) ?? BigInt(0)) > BigInt(0), "Must be more than zero")
      .refine((s) => (koboFromText(s) ?? BigInt(0)) <= MAX_KOBO, "Too large to submit"),
  );

export const listingSchema = z
  .object({
    legal_name: z.string().trim().min(1, "The registered name of the company"),
    trading_name: z.string().trim().min(1, "The name over the door"),
    rc_number: z
      .string()
      .trim()
      .toUpperCase()
      .regex(RC_RE, "A CAC number like RC1483920 or BN2345678"),
    mcc: z.string().min(1, "Pick a category"),
    mcc_other: z.string().trim(),
    symbol: z
      .string()
      .trim()
      .toUpperCase()
      .regex(SYMBOL_RE, "3 to 12 characters, A-Z and 0-9, starting with a letter"),

    shares_in_issue: wholeShares("the shares in issue"),
    shares_authorised: wholeShares("the authorised shares"),
    treasury_shares: wholeShares("the treasury pool"),
    public_shares: wholeShares("the public shares"),
    holders_count: wholeCount("how many holders there are"),
    daily_release: wholeShares("the daily release cap"),
    cofund_bps: cleaned.pipe(
      z
        .string()
        .min(1, "0 if you are not co-funding")
        .regex(/^\d+$/, "A whole number of basis points")
        .refine((s) => Number(s) <= 300, "At most 300 (3%)"),
    ),
    founders: z.array(
      z.object({
        label: z.string().trim().min(1, "Who this allocation is for"),
        shares: wholeShares("the shares"),
        cardholder_ref: z
          .string()
          .trim()
          .regex(UUID_RE, "The Tapp user id of this person, like 8f3c1a2e-…"),
        /** True for the row prefilled with the signed-in merchant's own id. */
        owner: z.boolean(),
      }),
    ),

    trading_months: wholeCount("how many months you have traded"),
    net_assets: nairaAmount("your net assets"),
    revenue: nairaAmount("your revenue for the last 12 months"),
    audited_accounts: z.boolean(),
    auditor_on_list: z.boolean(),
    board_resolution: z.boolean(),
    directors_clear: z.boolean(),
  })
  .superRefine((v, ctx) => {
    if (v.mcc === "other" && !/^\d{4}$/.test(v.mcc_other)) {
      ctx.addIssue({ code: "custom", path: ["mcc_other"], message: "A four-digit merchant category code" });
    }
    const n = (s: string) => Number(s);
    if (n(v.shares_authorised) < n(v.shares_in_issue)) {
      ctx.addIssue({ code: "custom", path: ["shares_authorised"], message: "Authorised shares cannot be fewer than shares in issue" });
    }
    if (n(v.public_shares) > n(v.shares_in_issue)) {
      ctx.addIssue({ code: "custom", path: ["public_shares"], message: "Public shares cannot exceed shares in issue" });
    }
    if (n(v.daily_release) > n(v.treasury_shares)) {
      ctx.addIssue({ code: "custom", path: ["daily_release"], message: "The daily cap cannot exceed the treasury pool" });
    }
    const founderTotal = v.founders.reduce((sum, f) => sum + n(f.shares), 0);
    if (founderTotal > n(v.treasury_shares)) {
      ctx.addIssue({ code: "custom", path: ["founders"], message: "Founders' allocations come from treasury and cannot exceed it" });
    }
  });

export type ListingInput = z.input<typeof listingSchema>;
export type ListingOutput = z.output<typeof listingSchema>;

export const emptyListing: ListingInput = {
  legal_name: "",
  trading_name: "",
  rc_number: "",
  mcc: "",
  mcc_other: "",
  symbol: "",
  shares_in_issue: "",
  shares_authorised: "",
  treasury_shares: "",
  public_shares: "",
  holders_count: "",
  daily_release: "",
  cofund_bps: "0",
  founders: [],
  trading_months: "",
  net_assets: "",
  revenue: "",
  audited_accounts: false,
  auditor_on_list: false,
  board_resolution: false,
  directors_clear: false,
};

/** Which fields each step owns, for per-step validation and error routing. */
export const STEP_FIELDS: (keyof ListingInput)[][] = [
  ["legal_name", "trading_name", "rc_number", "mcc", "mcc_other", "symbol"],
  ["shares_in_issue", "shares_authorised", "treasury_shares", "public_shares", "holders_count", "daily_release", "cofund_bps", "founders"],
  ["trading_months", "net_assets", "revenue", "audited_accounts", "auditor_on_list", "board_resolution", "directors_clear"],
  [],
];

/** The API's `data: {field: problem}` names, mapped onto form fields. */
export function serverFieldToForm(field: string): string | null {
  const flat: Record<string, string> = {
    legal_name: "legal_name",
    trading_name: "trading_name",
    rc_number: "rc_number",
    mcc: "mcc",
    symbol: "symbol",
    "evidence.trading_months": "trading_months",
    "evidence.shares_in_issue": "shares_in_issue",
    "evidence.public_shares": "public_shares",
    "evidence.holders": "holders_count",
    "evidence.treasury_units": "treasury_shares",
    "evidence.net_assets": "net_assets",
    "evidence.revenue": "revenue",
    shares_authorised_units: "shares_authorised",
    daily_release_units: "daily_release",
    cofund_bps: "cofund_bps",
  };
  if (flat[field]) return flat[field];
  const m = /^holders\[(\d+)\]\.(cardholder_ref|units)$/.exec(field);
  if (m) return `founders.${m[1]}.${m[2] === "units" ? "shares" : "cardholder_ref"}`;
  return null;
}

export function stepOfField(formField: string): number {
  const root = formField.split(".")[0] as keyof ListingInput;
  const i = STEP_FIELDS.findIndex((fs) => fs.includes(root));
  return i === -1 ? 0 : i;
}

/** "100000.50" -> 10000050. The schema has already bounded it to a safe integer. */
const minorFromText = (naira: string) => Number(koboFromText(naira) ?? BigInt(0));

/** Form -> wire. The exchange sets the price, so no price is sent. */
export function toRequest(v: ListingOutput): BusinessRequest {
  return {
    legal_name: v.legal_name,
    trading_name: v.trading_name,
    rc_number: v.rc_number,
    mcc: v.mcc === "other" ? v.mcc_other : v.mcc,
    symbol: v.symbol,
    evidence: {
      trading_months: Number(v.trading_months),
      audited_accounts: v.audited_accounts,
      auditor_on_list: v.auditor_on_list,
      shares_in_issue: sharesToUnits(Number(v.shares_in_issue)),
      public_shares: sharesToUnits(Number(v.public_shares)),
      holders: Number(v.holders_count),
      treasury_units: sharesToUnits(Number(v.treasury_shares)),
      board_resolution: v.board_resolution,
      directors_clear: v.directors_clear,
      net_assets: { minor: minorFromText(v.net_assets), currency: "NGN" },
      revenue: { minor: minorFromText(v.revenue), currency: "NGN" },
    },
    shares_authorised_units: sharesToUnits(Number(v.shares_authorised)),
    daily_release_units: sharesToUnits(Number(v.daily_release)),
    cofund_bps: Number(v.cofund_bps),
    holders: v.founders.map((f) => ({
      cardholder_ref: f.cardholder_ref,
      units: sharesToUnits(Number(f.shares)),
      label: f.label,
    })),
  };
}

/** "Mama Put Kitchens" -> "MAMAPUTK". Letters and digits only, led by a letter, 8 at most. */
export function suggestSymbol(tradingName: string): string {
  const s = tradingName.toUpperCase().replace(/[^A-Z0-9]/g, "").replace(/^[0-9]+/, "");
  return s.slice(0, 8);
}

/* ------------------------------------------------------------------ draft */

/**
 * v1 drafts carried a `reference_price` the merchant typed. The exchange now
 * sets the price, so v2 drops that field and adds the financials. A v1 draft
 * is read once, migrated (known fields kept, the rest discarded) and
 * rewritten under the v2 key.
 */
const DRAFT_KEY = "tapp.business.draft.v2";
const LEGACY_DRAFT_KEYS = ["tapp.business.draft.v1"];

export interface Draft {
  values: ListingInput;
  step: number;
}

/** Only the fields the form knows, each starting from its empty value. */
function migrateValues(stored: Partial<Record<string, unknown>>): ListingInput {
  const values = { ...emptyListing } as Record<string, unknown>;
  for (const key of Object.keys(emptyListing)) {
    if (key in stored && stored[key] !== undefined) values[key] = stored[key];
  }
  return values as unknown as ListingInput;
}

export function readDraft(): Draft | null {
  if (typeof window === "undefined") return null;
  try {
    let raw: string | null = null;
    for (const key of [DRAFT_KEY, ...LEGACY_DRAFT_KEYS]) {
      raw = window.localStorage.getItem(key);
      if (raw) break;
    }
    if (!raw) return null;
    const d = JSON.parse(raw) as { values?: Partial<Record<string, unknown>>; step?: number };
    if (!d.values) return null;
    return { values: migrateValues(d.values), step: Math.min(Math.max(d.step ?? 0, 0), STEPS.length - 1) };
  } catch {
    return null;
  }
}

export function writeDraft(d: Draft): void {
  if (typeof window === "undefined") return;
  try {
    window.localStorage.setItem(DRAFT_KEY, JSON.stringify(d));
    for (const key of LEGACY_DRAFT_KEYS) window.localStorage.removeItem(key);
  } catch {
    // Storage full or blocked: the form still works, it just will not survive a reload.
  }
}

export function clearDraft(): void {
  if (typeof window === "undefined") return;
  for (const key of [DRAFT_KEY, ...LEGACY_DRAFT_KEYS]) window.localStorage.removeItem(key);
}
