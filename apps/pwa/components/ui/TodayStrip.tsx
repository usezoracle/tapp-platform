"use client";

import Link from "next/link";
import type { ReactNode } from "react";
import { cn } from "@/lib/utils";
import { hueStyle } from "@/lib/nav";
import { formatMinor, type CardSummary, type HoldingsResponse, type Money } from "@/lib/api";
import { changeClass, formatBps } from "@/lib/holdings";
import { DuotoneIcon, type DuotoneName } from "./DuotoneIcon";

export interface Tile {
  href: string;
  label: string;
  /** The tile's one hue: a 3px rule along its top edge and its glyph. */
  hue: string;
  icon: DuotoneName;
  value: string;
  /** One quiet line under the value. */
  sub: ReactNode;
  /** 0-100: draws the slim bar along the tile's foot. */
  progress?: number;
}

/**
 * The card's daily headroom, as one tile.
 *
 * The limit and the balance are different constraints, and a card is stopped
 * by whichever binds first. The bar tracks the limit; the sub-line names the
 * one that is actually in the way.
 */
export function cardTile(card: CardSummary): Tile {
  const spent = card.spent_today_subunit;
  const daily = card.daily_limit_subunit;
  const headroom = Math.max(0, daily - spent);
  const bindingIsBalance = card.spendable.minor < headroom;
  return {
    href: "/settings/limits",
    label: "Card spend",
    hue: "--nav-card",
    icon: "card",
    value: formatMinor(spent, "NGN"),
    sub: bindingIsBalance ? `${card.spendable.display} spendable` : `of ${formatMinor(daily, "NGN")}`,
    progress: daily > 0 ? Math.min(100, (spent / daily) * 100) : 0,
  };
}

/** The slice of businesses a card has earned, as one tile. */
export function sharesTile(h: HoldingsResponse): Tile {
  const bps =
    h.total_cost.minor > 0
      ? Math.round(((h.total_value.minor - h.total_cost.minor) / h.total_cost.minor) * 10_000)
      : 0;
  return {
    href: "/holdings",
    label: "Stocks",
    hue: "--nav-holdings",
    icon: "chart",
    value: h.total_value.display,
    sub:
      h.holdings.length === 0 ? (
        "None yet"
      ) : (
        <span className={changeClass(bps)}>{formatBps(bps)}</span>
      ),
  };
}

/**
 * Money committed to a handover that has not completed. Shown apart from
 * the spendable figure and never added to it: somebody who has pledged cash
 * to an agent has that amount reserved, and would otherwise find out at a
 * counter.
 */
export function pendingTile(escrow: Money): Tile {
  return {
    href: "/cash",
    label: "Pending",
    hue: "--hue-amber",
    icon: "hourglass",
    value: escrow.display,
    sub: "Handover",
  };
}

/**
 * One row of what today looks like: two or three figures, each a hairline
 * panel that opens the screen it summarises. This is the wallet's whole
 * middle; the tall card panel and the stocks totals it replaces were each a
 * section of their own, and the page read as a list of equal panels.
 */
export function TodayStrip({ tiles, className }: { tiles: Tile[]; className?: string }) {
  if (!tiles.length) return null;
  return (
    <div className={cn("grid auto-cols-fr grid-flow-col gap-2", className)}>
      {tiles.map((t) => (
        <Link
          key={t.href}
          href={t.href}
          style={hueStyle(t.hue)}
          className="focus-ring panel relative grid min-w-0 content-start gap-1.5 p-3 transition-colors hover:bg-hover"
        >
          {/* The colour lives in the glyph's tile, sized like a glyph, not on
              the panel: the panel is the same hairline as every other one. */}
          <span className="flex min-w-0 items-center gap-2 text-xs text-fg-muted">
            <span className="hue-tint hue-text grid size-6 shrink-0 place-items-center rounded-sm">
              <DuotoneIcon name={t.icon} size={14} />
            </span>
            <span className="truncate">{t.label}</span>
          </span>
          <span className="truncate text-sm font-medium tabular-nums text-fg">{t.value}</span>
          {/* Allowed to wrap: three tiles on a 390px phone leave ~90px each,
              and "₦18,300.00 spendable" cut to "₦18,300.00 …" says nothing. */}
          <span className="text-xs leading-4 tabular-nums text-fg-muted [overflow-wrap:anywhere]">{t.sub}</span>
          {/* Under the sub-line, not over it, so the lines of text sit level
              across tiles that have no bar. */}
          {t.progress !== undefined ? (
            <span
              role="progressbar"
              aria-valuenow={Math.round(t.progress)}
              aria-valuemin={0}
              aria-valuemax={100}
              className="mt-1 block h-1 w-full overflow-hidden rounded-full bg-sunken"
            >
              <span className="block h-full rounded-full bg-(--hue)" style={{ width: `${t.progress}%` }} />
            </span>
          ) : null}
        </Link>
      ))}
    </div>
  );
}
