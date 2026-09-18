import type { CapTable as CapTableShape } from "@/lib/api";
import { Stat } from "@/components/ui/KeyValue";
import { Notice } from "@/components/ui/Notice";
import { formatInt, formatShares } from "@/lib/units";
import { formatDate } from "@/lib/utils";

/** The market's cap table right now. */
export function CapTable({ live }: { live: CapTableShape }) {
  const released = Number(live.released_today.shares);
  const cap = Number(live.daily_release.shares);
  const pct = cap > 0 ? Math.min(100, Math.round((released / cap) * 100)) : 0;
  const waiting = live.pending_funding.minor + live.escrowed_funding.minor;

  return (
    <div className="grid gap-4">
      {live.halted ? (
        <Notice tone="warning">
          Trading is halted{live.halt_reason ? ` (${live.halt_reason})` : ""}. Taps still queue; shares are released when it resumes.
        </Notice>
      ) : null}

      <section className="panel">
        <div className="grid grid-cols-2 divide-x divide-line sm:grid-cols-4">
          <Stat label="Authorised" value={formatShares(live.shares_authorised)} />
          <Stat label="In issue" value={formatShares(live.in_issue)} />
          <Stat label="Treasury remaining" value={formatShares(live.treasury_remaining)} />
          <Stat label="Holders" value={formatInt(live.holders)} />
        </div>
        <div className="border-t border-line px-4 py-3">
          <div className="flex items-baseline justify-between gap-3 text-[13px]">
            <span className="text-fg-muted">Released today</span>
            <span className="tabular-nums text-fg">
              {formatShares(live.released_today)} <span className="text-fg-subtle">of {formatShares(live.daily_release)}</span>
            </span>
          </div>
          <div className="mt-2 h-1 w-full overflow-hidden rounded-full bg-tile" role="progressbar" aria-valuenow={pct} aria-valuemin={0} aria-valuemax={100} aria-label="Released today of daily cap">
            <div className="h-full rounded-full bg-accent transition-[width]" style={{ width: `${pct}%` }} />
          </div>
        </div>
      </section>

      <section className="panel">
        <div className="px-4 pt-3">
          <p className="eyebrow">Equity your customers are waiting to own</p>
        </div>
        <div className="grid grid-cols-2 divide-x divide-line">
          <Stat label="Pending" value={live.pending_funding.display} sub="Queued at the market, not yet priced" />
          <Stat label="Escrowed" value={live.escrowed_funding.display} sub="Priced, waiting for release" />
        </div>
        {waiting === 0 ? (
          <p className="border-t border-line px-4 py-2.5 text-xs text-fg-subtle">Nothing waiting. Every tap so far has been allocated.</p>
        ) : null}
      </section>

      <section className="panel">
        <div className="grid grid-cols-2 divide-x divide-line sm:grid-cols-3">
          <Stat label="Reference price" value={live.reference_price?.display ?? "—"} />
          {live.last_session ? (
            <>
              <Stat label="Last session" value={live.last_session.price?.display ?? "—"} sub={`${formatDate(live.last_session.date)} · ${live.last_session.state}`} />
              <Stat label="Session volume" value={formatShares(live.last_session.volume)} sub="shares" />
            </>
          ) : (
            <div className="px-4 py-3 sm:col-span-2">
              <p className="eyebrow">Last session</p>
              <p className="mt-1 text-[13px] text-fg-muted">No session has been held yet.</p>
            </div>
          )}
        </div>
      </section>
    </div>
  );
}
