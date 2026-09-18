"use client";

import Link from "next/link";
import { Amount } from "./Amount";
import { Section, StatRow } from "./Screen";
import { sectionLinkClasses } from "./Styles";
import { formatMinor, type CardSummary } from "@/lib/api";

interface Props {
  card: CardSummary;
}

/**
 * Glance-level allowance on the wallet: today's tap-card spend against
 * the daily cap, plus the per-tap and step-up thresholds. "Manage" goes
 * to /settings/limits to edit them.
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
    <Section
      title="Card spend today"
      action={
        <Link href="/settings/limits" className={sectionLinkClasses}>
          Manage
        </Link>
      }
    >
      <div className="panel grid gap-3 p-4">
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
                You can spend <Amount value={card.spendable} size="sm" className="font-normal" />.
                That is your balance, not your limit.
              </>
            ) : (
              <>{formatMinor(headroomMinor, "NGN")} left before today&apos;s limit.</>
            )}
          </p>
        </div>

        <StatRow
          className="border-t border-line pt-3"
          stats={[
            { label: "Per tap", value: formatMinor(card.per_tap_limit_subunit, "NGN") },
            { label: "Step-up above", value: formatMinor(card.step_up_threshold_subunit, "NGN") },
          ]}
        />
      </div>
    </Section>
  );
}
