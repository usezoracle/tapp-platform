"use client";

import { use, useEffect, useMemo } from "react";
import { useRouter } from "next/navigation";
import { PiArrowClockwiseBold } from "react-icons/pi";
import { Screen, Section, StatRow } from "@/components/ui/Screen";
import { PageHeader } from "@/components/ui/PageHeader";
import { InfoBanner } from "@/components/ui/InfoBanner";
import { Button } from "@/components/ui/Button";
import { StatusChip } from "@/components/ui/StatusChip";
import { Skeleton, SkeletonRows } from "@/components/ui/Skeleton";
import { listClasses, stackClasses } from "@/components/ui/Styles";
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
      <AnimatedComponent variant={slideInOut} className={cn(stackClasses, "py-4")}>
        <PageHeader
          title={h?.trading_name ?? symbol}
          back="/holdings"
          subtitle={h ? `${h.symbol} · ${h.legal_name}` : undefined}
          trailing={h ? <SymbolTile name={h.trading_name} symbol={h.symbol} size="lg" /> : null}
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
              Stocks are not enabled for this account yet.
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
            {/* From 1024px the value and its figures sit beside the chart;
                the lots and anything still in flight run full width below. */}
            <div className="grid gap-6 md:gap-8 lg:grid-cols-[minmax(0,5fr)_minmax(0,7fr)] lg:gap-x-12">
              <Section title="Value" description="At the last session price.">
                <div className="grid gap-1">
                  <p className="display text-[32px] leading-9">{h.value.display}</p>
                  <p className="flex items-center gap-2 text-[13px] tabular-nums text-fg-muted">
                    <span className={cn("font-medium", changeClass(h.change_bps))}>
                      {formatBps(h.change_bps)}
                    </span>
                    <span>· Cost {h.cost.display}</span>
                  </p>
                </div>
                <StatRow
                  className="border-t border-line pt-3"
                  stats={[
                    { label: "Stocks", value: formatShares(h.holding.shares) },
                    { label: "Sellable", value: formatShares(h.sellable.shares) },
                    {
                      label: "Locked",
                      value: formatShares(h.locked.shares),
                      sub: lockedShares > 0 && h.next_unlock ? `Next ${formatDate(h.next_unlock)}` : undefined,
                    },
                  ]}
                />
              </Section>

              <Section
                title="Price"
                description="Adjusted price over the last 30 days."
                action={
                  h.last_session ? (
                    <span className="text-xs tabular-nums text-fg-muted">
                      {h.last_session.price.display} · {formatDate(h.last_session.date)}
                    </span>
                  ) : null
                }
              >
                <PriceChart prices={h.prices} currency={h.value.currency} lots={h.lots} />
              </Section>
            </div>

            <Section
              title="Lots"
              description={`${h.lots.length} ${h.lots.length === 1 ? "lot" : "lots"} · each locked for 120 days from the tap that earned it, then sellable.`}
            >
              {h.lots.length === 0 ? (
                <div className="panel px-4 py-5 text-[13px] leading-5 text-fg-muted">No lots yet.</div>
              ) : (
                <div className={listClasses}>
                  {h.lots.map((lot) => {
                    const unlocked = new Date(lot.transferable_from).getTime() <= Date.now();
                    return (
                      <div
                        key={`${lot.tap_id ?? "lot"}-${lot.acquired_at}-${lot.shares}`}
                        className="flex min-h-12 items-center gap-3 px-3 py-2 md:grid md:grid-cols-[minmax(0,1fr)_minmax(0,1fr)_minmax(0,1fr)_auto] md:gap-4"
                      >
                        <div className="grid min-w-0 flex-1 gap-0.5">
                          <p className="text-sm font-medium tabular-nums text-fg">
                            {sharesLabel(lot.shares)}
                          </p>
                          <p className="truncate text-xs tabular-nums text-fg-muted md:hidden">
                            Earned {formatDate(lot.acquired_at)} · {lot.cost.display}
                          </p>
                        </div>
                        <p className="hidden text-xs tabular-nums text-fg-muted md:block">
                          Earned {formatDate(lot.acquired_at)}
                        </p>
                        <p className="hidden text-right text-sm tabular-nums text-fg md:block">
                          {lot.cost.display}
                        </p>
                        <div className="flex justify-end md:min-w-28">
                          {unlocked ? (
                            <StatusChip tone="success">Sellable</StatusChip>
                          ) : (
                            <span className="text-xs tabular-nums text-fg-muted">
                              unlocks {formatDate(lot.transferable_from)}
                            </span>
                          )}
                        </div>
                      </div>
                    );
                  })}
                </div>
              )}
            </Section>

            {pendingItems.length > 0 ? (
              <Section
                title="Pending"
                description="Stocks on their way. Queued and pending allocations wait for the exchange to settle; escrowed ones are held until the tap clears."
              >
                <div className={listClasses}>
                  {pendingItems.map((item) => (
                    <PendingRow key={item.tap_id} item={item} />
                  ))}
                </div>
              </Section>
            ) : null}
          </>
        ) : null}
      </AnimatedComponent>
    </Screen>
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
