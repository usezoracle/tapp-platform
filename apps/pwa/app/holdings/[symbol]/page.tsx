"use client";

import { use, useEffect, useMemo } from "react";
import { useRouter } from "next/navigation";
import { PiArrowClockwiseBold } from "react-icons/pi";
import { Screen, SectionHeader } from "@/components/ui/Screen";
import { PageHeader } from "@/components/ui/PageHeader";
import { InfoBanner } from "@/components/ui/InfoBanner";
import { Button } from "@/components/ui/Button";
import { StatusChip } from "@/components/ui/StatusChip";
import { Skeleton, SkeletonRows } from "@/components/ui/Skeleton";
import { listClasses } from "@/components/ui/Styles";
import { AnimatedComponent, slideInOut } from "@/components/ui/AnimatedComponents";
import { PriceChart } from "@/components/holdings/PriceChart";
import { SymbolTile } from "@/components/holdings/SymbolTile";
import { cn } from "@/lib/utils";
import { useSession } from "@/lib/auth";
import {
  useHolding,
  useEquityActivity,
  isFeatureDisabled,
  isExchangeDown,
  holdingsMessage,
  formatShares,
  sharesLabel,
  formatBps,
  changeClass,
  formatDate,
  isInFlight,
  type EquityActivityItem,
} from "@/lib/holdings";

export default function HoldingPage({ params }: { params: Promise<{ symbol: string }> }) {
  const { symbol: raw } = use(params);
  const symbol = decodeURIComponent(raw);
  const router = useRouter();
  const { hydrated, session } = useSession();
  const q = useHolding(symbol);
  const activity = useEquityActivity(100);

  useEffect(() => {
    if (hydrated && !session) {
      router.replace(`/sign-in?next=/holdings/${encodeURIComponent(symbol)}`);
    }
  }, [hydrated, session, router, symbol]);

  const pendingItems = useMemo(
    () => (activity.data?.activity ?? []).filter((i) => i.symbol === symbol && isInFlight(i.state)),
    [activity.data, symbol],
  );

  if (!hydrated || !session) return <Screen />;

  const h = q.data;
  const lockedShares = h ? Number(h.locked.shares) : 0;

  return (
    <Screen>
      <AnimatedComponent variant={slideInOut} className="grid gap-6 py-4">
        <PageHeader
          title={h?.trading_name ?? symbol}
          back="/holdings"
          subtitle={h ? `${h.symbol} · ${h.legal_name}` : undefined}
          trailing={h ? <SymbolTile name={h.trading_name} size="lg" /> : null}
        />

        {q.isLoading ? (
          <>
            <div className="grid gap-2">
              <Skeleton className="h-3 w-16" />
              <Skeleton className="h-8 w-40" />
            </div>
            <Skeleton className="h-44 w-full" />
            <SkeletonRows rows={2} />
          </>
        ) : q.isError ? (
          isFeatureDisabled(q.error) ? (
            <div className="panel px-4 py-5 text-[13px] leading-5 text-fg-muted">
              Shares are not enabled for this account yet.
            </div>
          ) : (
            <InfoBanner
              tone={isExchangeDown(q.error) ? "warning" : "error"}
              action={
                <Button
                  size="sm"
                  variant="secondary"
                  fullWidth={false}
                  leadingIcon={<PiArrowClockwiseBold />}
                  onClick={() => q.refetch()}
                  loading={q.isFetching}
                >
                  Retry
                </Button>
              }
            >
              {holdingsMessage(q.error)}
            </InfoBanner>
          )
        ) : h ? (
          <>
            {/* Value / cost / change */}
            <div className="grid gap-1">
              <p className="eyebrow">Value</p>
              <p className="display text-[32px] leading-9">{h.value.display}</p>
              <p className="flex items-center gap-2 text-[13px] tabular-nums text-fg-muted">
                <span className={cn("font-medium", changeClass(h.change_bps))}>
                  {formatBps(h.change_bps)}
                </span>
                <span>· Cost {h.cost.display}</span>
              </p>
            </div>

            <dl className="grid grid-cols-3 gap-px overflow-hidden rounded-lg border border-line bg-line">
              <Stat label="Shares" value={formatShares(h.holding.shares)} />
              <Stat label="Sellable" value={formatShares(h.sellable.shares)} />
              <Stat label="Locked" value={formatShares(h.locked.shares)} />
            </dl>

            {/* Chart */}
            <section className="grid gap-3">
              <SectionHeader
                title="Adjusted price · 90 sessions"
                action={
                  h.last_session ? (
                    <span className="text-xs tabular-nums text-fg-muted">
                      {h.last_session.price.display} · {formatDate(h.last_session.date)}
                    </span>
                  ) : null
                }
              />
              <PriceChart prices={h.prices} currency={h.value.currency} />
            </section>

            {/* Pending / escrowed */}
            {pendingItems.length > 0 ? (
              <section className="grid gap-3">
                <SectionHeader title="Not yet allocated" />
                <div className={listClasses}>
                  {pendingItems.map((item) => (
                    <PendingRow key={item.tap_id} item={item} />
                  ))}
                </div>
                <p className="text-xs leading-5 text-fg-subtle">
                  Queued and pending allocations are waiting for the exchange to settle.
                  Escrowed ones are held until the tap clears.
                </p>
              </section>
            ) : null}

            {/* Lots */}
            <section className="grid gap-3">
              <SectionHeader
                title={`${h.lots.length} ${h.lots.length === 1 ? "lot" : "lots"}`}
                action={
                  lockedShares > 0 && h.next_unlock ? (
                    <span className="text-xs text-fg-muted">Next unlock {formatDate(h.next_unlock)}</span>
                  ) : null
                }
              />
              {h.lots.length === 0 ? (
                <div className="panel px-4 py-5 text-[13px] leading-5 text-fg-muted">No lots yet.</div>
              ) : (
                <div className={listClasses}>
                  {h.lots.map((lot) => {
                    const unlocked = new Date(lot.transferable_from).getTime() <= Date.now();
                    return (
                      <div
                        key={`${lot.tap_id ?? "lot"}-${lot.acquired_at}-${lot.shares}`}
                        className="flex min-h-12 items-center gap-3 px-3 py-2"
                      >
                        <div className="grid min-w-0 flex-1 gap-0.5">
                          <p className="text-sm font-medium tabular-nums text-fg">
                            {sharesLabel(lot.shares)}
                          </p>
                          <p className="truncate text-xs text-fg-muted">
                            Earned {formatDate(lot.acquired_at)} · {lot.cost.display}
                          </p>
                        </div>
                        {unlocked ? (
                          <StatusChip tone="success">Sellable</StatusChip>
                        ) : (
                          <span className="text-xs tabular-nums text-fg-muted">
                            unlocks {formatDate(lot.transferable_from)}
                          </span>
                        )}
                      </div>
                    );
                  })}
                </div>
              )}
              <p className="text-xs leading-5 text-fg-subtle">
                Shares earned from a tap are locked for 120 days from that tap, then you can
                sell them.
              </p>
            </section>
          </>
        ) : null}
      </AnimatedComponent>
    </Screen>
  );
}

function Stat({ label, value }: { label: string; value: string }) {
  return (
    <div className="grid gap-0.5 bg-raised px-3 py-2.5">
      <dt className="text-xs text-fg-muted">{label}</dt>
      <dd className="text-sm font-medium tabular-nums text-fg">{value}</dd>
    </div>
  );
}

function PendingRow({ item }: { item: EquityActivityItem }) {
  return (
    <div className="flex min-h-12 items-center gap-3 px-3 py-2">
      <div className="grid min-w-0 flex-1 gap-0.5">
        <p className="text-sm font-medium tabular-nums text-fg">{sharesLabel(item.bought.shares)}</p>
        <p className="truncate text-xs text-fg-muted">
          {formatDate(item.at)} · {item.funding.display}
          {item.price ? ` at ${item.price.display}` : ""}
        </p>
      </div>
      <StatusChip tone={item.state === "escrowed" ? "warning" : "pending"}>
        {item.state === "escrowed" ? "Escrowed" : item.state === "queued" ? "Queued" : "Pending"}
      </StatusChip>
    </div>
  );
}
