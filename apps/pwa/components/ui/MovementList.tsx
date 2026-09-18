import {
  PiCreditCardBold,
  PiMoneyWavyBold,
  PiArrowsLeftRightBold,
  PiArrowDownLeftBold,
  PiArrowUpRightBold,
  PiLockSimpleBold,
  PiReceiptBold,
} from "react-icons/pi";
import Link from "next/link";
import type { ReactNode } from "react";
import { cn } from "@/lib/utils";
import { hueStyle } from "@/lib/nav";
import { Amount } from "./Amount";
import { CurrencyIcon } from "./CurrencyIcon";
import { DuotoneIcon } from "./DuotoneIcon";
import { StatusChip } from "./StatusChip";
import { EmptyState } from "./Surface";
import { listClasses, rowClasses, tileBaseClasses } from "./Styles";
import type { Movement } from "@/lib/ledger";
import { equityLine, formatShares, type EquityActivityItem } from "@/lib/holdings";

/**
 * How a ledger reason reads to the person it happened to.
 *
 * The ledger's vocabulary is precise and not conversational: "tap.debit",
 * "handover.credited_trader", "fx.spread". Mapping it here rather than
 * softening it at the source keeps the ledger's own words exact — they are
 * what an auditor reconciles against — while the app says something a person
 * recognises.
 *
 * The lookup is by prefix, so a new movement type in an existing domain gets a
 * sensible icon and label without anyone remembering to come back here. An
 * unmapped reason falls through to its own text rather than to "Transaction",
 * because a label nobody can act on is worse than a slightly technical one.
 */
const DOMAINS: Record<string, { icon: ReactNode; label: string }> = {
  tap: { icon: <PiCreditCardBold />, label: "Card payment" },
  handover: { icon: <PiMoneyWavyBold />, label: "Cash handover" },
  deposit: { icon: <PiArrowDownLeftBold />, label: "Deposit" },
  withdrawal: { icon: <PiArrowUpRightBold />, label: "Withdrawal" },
  fx: { icon: <PiArrowsLeftRightBold />, label: "Conversion" },
  settlement: { icon: <PiReceiptBold />, label: "Transfer" },
  merchant_payout: { icon: <PiReceiptBold />, label: "Payout" },
  agent: { icon: <PiMoneyWavyBold />, label: "Agent float" },
  treasury: { icon: <PiReceiptBold />, label: "Treasury" },
};

const EXACT: Record<string, string> = {
  "tap.debit": "Card payment",
  "tap.fee": "Card fee",
  "handover.escrowed": "Cash held for handover",
  "handover.credited_trader": "Cash handed over",
  "handover.released": "Handover cancelled",
  "fx.sold": "Converted out",
  "fx.bought": "Converted in",
  "fx.spread": "Conversion spread",
  "deposit.credited": "USDC received",
  "withdrawal.debited": "Withdrawal sent",
  "settlement.refunded_sender": "Refunded",
};

function describe(reason: string): { icon: ReactNode; label: string } {
  // A reason can carry a suffix after a colon -- "handover.released:expired".
  const [base] = reason.split(":");
  const domain = DOMAINS[base.split(".")[0]] ?? {
    icon: <PiReceiptBold />,
    label: base,
  };
  return { icon: domain.icon, label: EXACT[base] ?? domain.label };
}

/**
 * One thing in the feed: a movement of money, or shares bought with one.
 *
 * The buyback is its own row rather than a line under the tap, because to
 * the person it is its own event -- it lands minutes after the tap, at a
 * price the tap did not know -- and a row is what an event gets. The tap
 * row keeps its one-line note about the shares it earned; the note says
 * what the tap did, the row says what happened next, and neither carries
 * an amount the other also counts.
 */
export type FeedItem =
  | { kind: "movement"; at: string; movement: Movement }
  | { kind: "equity"; at: string; item: EquityActivityItem };

/** The states that are an event. Queued has not started; failed says nothing (the tap still went through). */
const ROW_STATES = new Set(["allocated", "pending", "escrowed", "reversed"]);

/** Movements and buybacks in one list, newest first. */
export function mergeFeed(
  movements: Movement[],
  equity: EquityActivityItem[] | undefined,
): FeedItem[] {
  const out: FeedItem[] = movements.map((movement) => ({ kind: "movement", at: movement.at, movement }));
  for (const item of equity ?? []) {
    if (ROW_STATES.has(item.state)) out.push({ kind: "equity", at: item.at, item });
  }
  return out.sort((a, b) => Date.parse(b.at) - Date.parse(a.at));
}

export function MovementList({
  items,
  emptyState,
  equityByRef,
  grouped = false,
  className,
}: {
  items: FeedItem[];
  emptyState?: ReactNode;
  /**
   * tap id → equity activity, from `indexEquityByTapId`. A tap row that
   * earned shares says so on its second line. Optional: without it the list
   * is exactly the ledger.
   */
  equityByRef?: Map<string, EquityActivityItem>;
  /**
   * One panel per day, under a day label that stays put while its rows
   * scroll past. Rows then carry the time of day, since the day is already
   * said once above them.
   */
  grouped?: boolean;
  className?: string;
}) {
  if (!items.length) {
    return (
      emptyState ?? (
        <EmptyState title="Nothing yet">
          Every movement of your money shows up here, in and out.
        </EmptyState>
      )
    );
  }

  const row = (f: FeedItem, whenText?: string) =>
    f.kind === "movement" ? (
      <MovementRow
        key={`m-${f.movement.id}`}
        movement={f.movement}
        equity={equityFor(f.movement, equityByRef)}
        when={whenText}
      />
    ) : (
      <EquityRow key={`e-${f.item.tap_id}`} item={f.item} when={whenText} />
    );

  if (!grouped) {
    return <div className={cn(listClasses, className)}>{items.map((f) => row(f))}</div>;
  }

  return (
    <div className={cn("grid gap-4", className)}>
      {groupByDay(items).map((group) => (
        <section key={group.key} className="grid gap-2">
          <h3 className="sticky-label eyebrow -mx-1 px-1 py-1">{group.label}</h3>
          <div className={listClasses}>{group.items.map((f) => row(f, timeOfDay(f.at)))}</div>
        </section>
      ))}
    </div>
  );
}

interface DayGroup {
  key: string;
  label: string;
  items: FeedItem[];
}

/**
 * Consecutive items on the same local day. The feed is newest first, so a
 * day's rows are already adjacent; this only draws the lines between days.
 */
function groupByDay(items: FeedItem[]): DayGroup[] {
  const out: DayGroup[] = [];
  for (const f of items) {
    const key = dayKey(f.at);
    const last = out[out.length - 1];
    if (last && last.key === key) last.items.push(f);
    else out.push({ key, label: dayLabel(f.at), items: [f] });
  }
  return out;
}

function dayKey(iso: string): string {
  const d = new Date(iso);
  if (!Number.isFinite(d.getTime())) return "unknown";
  return `${d.getFullYear()}-${d.getMonth()}-${d.getDate()}`;
}

/**
 * Whole local days between then and now: 0 today, 1 yesterday. NaN for a
 * date that does not parse, so a caller filtering on it drops the row.
 */
export function daysAgo(iso: string, now = new Date()): number {
  const d = new Date(iso);
  if (!Number.isFinite(d.getTime())) return NaN;
  const startOf = (x: Date) => new Date(x.getFullYear(), x.getMonth(), x.getDate()).getTime();
  return Math.round((startOf(now) - startOf(d)) / 86_400_000);
}

/** Today, Yesterday, then the date -- with the year once it is not this one. */
function dayLabel(iso: string): string {
  const d = new Date(iso);
  if (!Number.isFinite(d.getTime())) return "Unknown day";
  const now = new Date();
  const days = daysAgo(iso, now);
  if (days === 0) return "Today";
  if (days === 1) return "Yesterday";
  return d.toLocaleDateString("en-NG", {
    weekday: "short",
    day: "numeric",
    month: "short",
    year: d.getFullYear() === now.getFullYear() ? undefined : "numeric",
  });
}

function timeOfDay(iso: string): string {
  const d = new Date(iso);
  if (!Number.isFinite(d.getTime())) return "";
  return d.toLocaleTimeString("en-NG", { hour: "2-digit", minute: "2-digit" });
}

/**
 * The shares a tap earned, if this row is that tap.
 *
 * Joined by `refId`, which for a tap movement is the tap id the equity feed
 * is keyed on. The fee row of the same tap carries the same id and is skipped:
 * the shares were bought with the payment, not the fee, and naming them twice
 * would read as twice the shares.
 */
function equityFor(
  m: Movement,
  byRef: Map<string, EquityActivityItem> | undefined,
): EquityActivityItem | undefined {
  if (!byRef || m.refType !== "tap" || !m.refId) return undefined;
  const [base] = m.reason.split(":");
  if (base === "tap.fee") return undefined;
  return byRef.get(m.refId);
}

/**
 * What a tap row says. With the merchant known, the title is where the
 * card was used -- that is what somebody scanning the list is looking for
 * -- and "Card payment" moves to the line under it. A refund of a tap
 * (money back from a merchant) says so first, then where from.
 */
function tapTitle(m: Movement, label: string): { title: string; kind: string | null } {
  const merchant = m.merchant;
  const [base] = m.reason.split(":");
  if (!merchant || base === "tap.fee") return { title: label, kind: null };
  if (m.amount.minor > 0) return { title: `Refund · ${merchant.name}`, kind: "Card refund" };
  return { title: merchant.name, kind: label };
}

/** 48px row: quiet icon tile, label + when, right-aligned tabular amount. */
function MovementRow({
  movement,
  equity,
  when: whenText,
}: {
  movement: Movement;
  equity?: EquityActivityItem;
  /** Overrides the relative "2h ago" -- a grouped list passes the time. */
  when?: string;
}) {
  const { icon, label } = describe(movement.reason);
  const incoming = movement.amount.minor > 0;

  // A movement into escrow is not income, even though its sign is positive
  // from the escrow account's point of view. Marking it keeps somebody from
  // reading "money arrived" when what happened is "money was set aside".
  const held = movement.account === "escrow";
  const shares = equityLine(equity);
  const { title, kind } = tapTitle(movement, label);
  const symbol = movement.merchant?.symbol ?? null;

  // The tile's one colour says what kind of movement this is before the
  // label does: green for money arriving, ink for money leaving, the
  // holdings violet for a tap that also bought shares. Held money is
  // neither in nor out and stays muted.
  const tone = shares
    ? "hue-text"
    : held
      ? "text-fg-muted"
      : incoming
        ? "text-positive"
        : "text-fg";

  return (
    <div className={rowClasses}>
      {/* The currency sits on the tile's corner the way a status dot would,
          so the movement's own glyph stays the primary mark. Absolute, so
          the row keeps its 48px whatever the badge does. */}
      <span className="relative shrink-0">
        <span className={cn(tileBaseClasses, tone)} style={shares ? hueStyle("--nav-card") : undefined}>
          {held ? <PiLockSimpleBold /> : icon}
        </span>
        <CurrencyIcon
          currency={movement.amount.currency}
          size={16}
          className="absolute -right-1 -bottom-1 ring-[1.5px] ring-raised"
        />
      </span>

      <span className="grid min-w-0 flex-1 gap-0.5">
        <span className="truncate text-sm font-medium text-fg">{title}</span>
        <span className="truncate text-xs text-fg-muted">
          {held ? "Held · " : ""}
          {kind ? `${kind} · ` : ""}
          {whenText ?? when(movement.at)}
          {kind && symbol ? ` · ${symbol}` : ""}
        </span>
        {/* Its own line, not appended to the date: beside a right-aligned
            amount there is not room for both, and "+0.125 MAMAPUT sha…" tells
            nobody anything. */}
        {shares ? <span className="truncate text-xs text-fg">{shares}</span> : null}
      </span>

      <Amount value={movement.amount} size="sm" signed showPlus className="shrink-0 text-right" />
    </div>
  );
}

/**
 * Shares bought with a tap, as a row of their own. The amount on the right
 * is the funding -- the slice of the tap that became shares -- in the
 * holdings violet and unsigned, since it is neither money in nor out of
 * the balance: the tap row above already carries that.
 */
function EquityRow({ item, when: whenText }: { item: EquityActivityItem; when?: string }) {
  const symbol = item.symbol ?? item.merchant?.symbol ?? null;
  const n = formatShares(item.bought.shares);
  const pending = item.state === "pending" || item.state === "escrowed";

  const title =
    item.state === "allocated"
      ? `Bought ${n} ${symbol ?? ""} ${n === "1" ? "share" : "shares"}`.replace(/\s+/g, " ")
      : item.state === "reversed"
        ? `${symbol ?? "Your"} shares returned`
        : `Buying ${symbol ?? ""} shares`.replace(/\s+/g, " ");

  const from = item.tap_amount
    ? `${item.funding.display} from your ${item.tap_amount.display} tap`
    : `${item.funding.display} of your tap`;
  const where = item.merchant ? ` at ${item.merchant.name}` : "";

  const body = (
    <div className={rowClasses} style={hueStyle("--nav-card")}>
      <span className="relative shrink-0">
        <span className={cn(tileBaseClasses, "hue-text")}>
          <DuotoneIcon name="chart" />
        </span>
        <CurrencyIcon
          currency={item.funding.currency}
          size={16}
          className="absolute -right-1 -bottom-1 ring-[1.5px] ring-raised"
        />
      </span>

      <span className="grid min-w-0 flex-1 gap-0.5">
        <span className="flex min-w-0 items-center gap-2">
          <span className="truncate text-sm font-medium text-fg">{title}</span>
          {pending ? <StatusChip tone="pending">pending</StatusChip> : null}
        </span>
        {/* A whole sentence; on a phone it takes two lines rather than
            losing the merchant to an ellipsis. */}
        <span className="text-xs leading-4 text-fg-muted [overflow-wrap:anywhere]">
          {from}
          {where} · {whenText ?? when(item.at)}
        </span>
      </span>

      <span className="grid shrink-0 gap-0.5 text-right">
        <span className="hue-text text-sm font-medium tabular-nums">{item.funding.display}</span>
        {item.price ? (
          <span className="text-xs tabular-nums text-fg-muted">@ {item.price.display}</span>
        ) : null}
      </span>
    </div>
  );

  return symbol ? (
    <Link href={`/holdings/${encodeURIComponent(symbol)}`} className="focus-ring block">
      {body}
    </Link>
  ) : (
    body
  );
}

/**
 * Relative for anything recent, absolute once it stops being "recent".
 *
 * "3 days ago" is worse than a date: past a couple of days people want to know
 * which day it was, not how to count backwards to it.
 */
function when(iso: string): string {
  const then = new Date(iso);
  const seconds = (Date.now() - then.getTime()) / 1000;
  if (!Number.isFinite(seconds)) return "";
  if (seconds < 60) return "just now";
  if (seconds < 3_600) return `${Math.floor(seconds / 60)}m ago`;
  if (seconds < 86_400) return `${Math.floor(seconds / 3_600)}h ago`;
  if (seconds < 172_800) return "yesterday";
  return then.toLocaleDateString("en-NG", { day: "numeric", month: "short" });
}
