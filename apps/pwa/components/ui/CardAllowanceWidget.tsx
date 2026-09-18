"use client";

import Link from "next/link";
import { PiSlidersHorizontalBold } from "react-icons/pi";
import { Amount } from "./Amount";
import { formatMinor, type CardSummary } from "@/lib/api";

interface Props {
  card: CardSummary;
}

/**
 * Glance-level allowance card on /wallet. Shows today's tap-card
 * spend against the daily cap, plus per-tap and step-up thresholds.
 * Tapping the gear → /settings/limits to edit.
 */
export function CardAllowanceWidget({ card }: Props) {
  const spent = card.spent_today_subunit;
  const daily = card.daily_limit_subunit;
  const pct = daily > 0 ? Math.min(100, (spent / daily) * 100) : 0;

  // The limit and the balance are different constraints, and a card is stopped
  // by whichever binds first. Showing only the limit lets somebody plan a
  // purchase they cannot afford; showing only the balance lets them plan one
  // their own daily cap will refuse.
  const headroomMinor = Math.max(0, daily - spent);
  const bindingIsBalance = card.spendable.minor < headroomMinor;

  return (
    <div className="panel grid gap-3 p-4">
      <div className="flex items-center justify-between">
        <h3 className="eyebrow">Card spend today</h3>
        <Link
          href="/settings/limits"
          aria-label="Edit limits"
          className="focus-ring -m-1 grid size-7 place-items-center rounded-sm text-fg-muted transition-colors hover:bg-sunken hover:text-fg [&>svg]:size-4"
        >
          <PiSlidersHorizontalBold />
        </Link>
      </div>

      <div className="grid gap-2">
        <p className="flex items-baseline gap-1.5">
          <span className="display text-2xl leading-7">{formatMinor(spent, "NGN")}</span>
          <span className="text-[13px] tabular-nums text-fg-muted">
            / {formatMinor(daily, "NGN")}
          </span>
        </p>
        <div
          role="progressbar"
          aria-valuenow={Math.round(pct)}
          aria-valuemin={0}
          aria-valuemax={100}
          className="h-1 w-full overflow-hidden rounded-full bg-sunken"
        >
          <div className="h-full rounded-full bg-accent transition-[width]" style={{ width: `${pct}%` }} />
        </div>
        <p className="text-xs text-fg-muted">
          {bindingIsBalance ? (
            <>
              You can spend <Amount value={card.spendable} size="sm" className="font-normal" />{" "}
              — that is your balance, not your limit.
            </>
          ) : (
            <>
              {formatMinor(headroomMinor, "NGN")}{" "}left before today&apos;s limit.
            </>
          )}
        </p>
      </div>

      <dl className="grid grid-cols-2 divide-x divide-line border-t border-line pt-3">
        <div className="grid gap-0.5 pr-3">
          <dt className="text-xs text-fg-muted">Per tap</dt>
          <dd className="text-sm font-medium tabular-nums text-fg">
            {formatMinor(card.per_tap_limit_subunit, "NGN")}
          </dd>
        </div>
        <div className="grid gap-0.5 pl-3">
          <dt className="text-xs text-fg-muted">Step-up above</dt>
          <dd className="text-sm font-medium tabular-nums text-fg">
            {formatMinor(card.step_up_threshold_subunit, "NGN")}
          </dd>
        </div>
      </dl>
    </div>
  );
}
