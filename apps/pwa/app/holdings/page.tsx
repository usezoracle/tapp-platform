"use client";

import { useEffect } from "react";
import { useRouter } from "next/navigation";
import { PiArrowClockwiseBold } from "react-icons/pi";
import { Screen, Section } from "@/components/ui/Screen";
import { PageHeader } from "@/components/ui/PageHeader";
import { InfoBanner } from "@/components/ui/InfoBanner";
import { Button } from "@/components/ui/Button";
import { Skeleton, SkeletonRows } from "@/components/ui/Skeleton";
import { listClasses, stackClasses } from "@/components/ui/Styles";
import { AnimatedComponent, slideInOut } from "@/components/ui/AnimatedComponents";
import { HoldingRow, HoldingTableHeader } from "@/components/holdings/HoldingRow";
import { PortfolioTotals } from "@/components/holdings/PortfolioTotals";
import { cn } from "@/lib/utils";
import { useSession } from "@/lib/auth";
import {
  useHoldings,
  isFeatureDisabled,
  isExchangeDown,
  holdingsMessage,
  formatDate,
} from "@/lib/holdings";

export default function HoldingsPage() {
  const router = useRouter();
  const { hydrated, session } = useSession();
  const q = useHoldings();

  useEffect(() => {
    if (hydrated && !session) router.replace("/sign-in?next=/holdings");
  }, [hydrated, session, router]);

  if (!hydrated || !session) return <Screen />;

  return (
    <Screen>
      <AnimatedComponent variant={slideInOut} className={cn(stackClasses, "py-4")}>
        <PageHeader
          title="Your stocks"
          back="/"
          subtitle={q.data ? `As of ${formatDate(q.data.as_of)}` : "Earned one tap at a time"}
        />

        {q.isLoading ? (
          <>
            <div className="grid gap-2">
              <Skeleton className="h-3 w-16" />
              <Skeleton className="h-8 w-40" />
            </div>
            <SkeletonRows rows={3} />
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
        ) : q.data ? (
          <>
            <Section title="Value" description="What your stocks are worth at the last session price.">
              <PortfolioTotals value={q.data.total_value} cost={q.data.total_cost} />
            </Section>

            <Section
              title="Businesses"
              description={`${q.data.holdings.length} ${q.data.holdings.length === 1 ? "business" : "businesses"} · locked for 120 days from the tap that earned them, then sellable.`}
            >
              {q.data.holdings.length === 0 ? (
                <div className="panel px-4 py-5 text-[13px] leading-5 text-fg-muted">
                  Every tap at a participating business earns you a slice of it. None yet.
                </div>
              ) : (
                <div className={listClasses}>
                  <HoldingTableHeader />
                  {q.data.holdings.map((h) => (
                    <HoldingRow key={h.symbol} holding={h} layout="table" />
                  ))}
                </div>
              )}
            </Section>

            <p className="text-xs leading-5 text-fg-subtle">
              Prices come from the last Freedom Exchange session.
            </p>
          </>
        ) : null}
      </AnimatedComponent>
    </Screen>
  );
}
