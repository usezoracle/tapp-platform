import {
  PiCreditCardBold,
  PiMoneyWavyBold,
  PiArrowsLeftRightBold,
  PiArrowDownLeftBold,
  PiArrowUpRightBold,
  PiLockSimpleBold,
  PiReceiptBold,
} from "react-icons/pi";
import type { ReactNode } from "react";
import { cn } from "@/lib/utils";
import { Amount } from "./Amount";
import { EmptyState } from "./Surface";
import { listClasses, rowClasses, tileClasses } from "./Styles";
import type { Movement } from "@/lib/ledger";
import { equityLine, type EquityActivityItem } from "@/lib/holdings";

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

export function MovementList({
  movements,
  emptyState,
  equityByRef,
  grouped = false,
  className,
}: {
  movements: Movement[];
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
  if (!movements.length) {
    return (
      emptyState ?? (
        <EmptyState title="Nothing yet">
          Every movement of your money shows up here, in and out.
        </EmptyState>
      )
    );
  }

  if (!grouped) {
    return (
      <div className={cn(listClasses, className)}>
        {movements.map((m) => (
          <MovementRow key={m.id} movement={m} equity={equityFor(m, equityByRef)} />
        ))}
      </div>
    );
  }

  return (
    <div className={cn("grid gap-4", className)}>
      {groupByDay(movements).map((group) => (
        <section key={group.key} className="grid gap-2">
          <h3 className="sticky-label eyebrow -mx-1 px-1 py-1">{group.label}</h3>
          <div className={listClasses}>
            {group.movements.map((m) => (
              <MovementRow
                key={m.id}
                movement={m}
                equity={equityFor(m, equityByRef)}
                when={timeOfDay(m.at)}
              />
            ))}
          </div>
        </section>
      ))}
    </div>
  );
}

interface DayGroup {
  key: string;
  label: string;
  movements: Movement[];
}

/**
 * Consecutive movements on the same local day. The list arrives newest
 * first, so a day's rows are already adjacent; this only draws the lines
 * between days.
 */
function groupByDay(movements: Movement[]): DayGroup[] {
  const out: DayGroup[] = [];
  for (const m of movements) {
    const key = dayKey(m.at);
    const last = out[out.length - 1];
    if (last && last.key === key) last.movements.push(m);
    else out.push({ key, label: dayLabel(m.at), movements: [m] });
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

  return (
    <div className={rowClasses}>
      <span className={cn(tileClasses, incoming && !held && "text-positive")}>
        {held ? <PiLockSimpleBold /> : icon}
      </span>

      <span className="grid min-w-0 flex-1 gap-0.5">
        <span className="truncate text-sm font-medium text-fg">{label}</span>
        <span className="truncate text-xs text-fg-muted">
          {held ? "Held · " : ""}
          {whenText ?? when(movement.at)}
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
