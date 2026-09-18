"use client";

import { useEffect, useMemo } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { PiWarningBold } from "react-icons/pi";
import { Screen, Section } from "@/components/ui/Screen";
import { PageHeader } from "@/components/ui/PageHeader";
import { Button } from "@/components/ui/Button";
import { Balances, BalanceActions } from "@/components/ui/Balances";
import { MovementList } from "@/components/ui/MovementList";
import { InfoBanner } from "@/components/ui/InfoBanner";
import { CardAllowanceWidget } from "@/components/ui/CardAllowanceWidget";
import { NoCardBanner } from "@/components/ui/NoCardBanner";
import { EmptyState } from "@/components/ui/Surface";
import { CrossFade } from "@/components/ui/CrossFade";
import { Skeleton, SkeletonRows } from "@/components/ui/Skeleton";
import { AnimatedComponent, slideInOut } from "@/components/ui/AnimatedComponents";
import { Web3Avatar } from "@/components/ui/Web3Avatar";
import { SharesModule } from "@/components/holdings/SharesModule";
import { sectionLinkClasses, stackClasses } from "@/components/ui/Styles";
import { cn } from "@/lib/utils";
import { useSession } from "@/lib/auth";
import { useBalances, useActivity, useCard } from "@/lib/ledger";
import { useEquityActivity, indexEquityByTapId } from "@/lib/holdings";

/**
 * The wallet, and the app's home screen.
 *
 * One implementation behind both routes. They were two copies of the same
 * screen, which is how "/" and "/wallet" came to show different things -- a
 * fix applied to one of them silently did not apply to the other.
 */
export function WalletScreen() {
  const router = useRouter();
  const { hydrated, session } = useSession();
  const balances = useBalances();
  const activity = useActivity(6);
  const card = useCard();
  const equity = useEquityActivity(100);
  const equityByRef = useMemo(() => indexEquityByTapId(equity.data?.activity), [equity.data]);

  useEffect(() => {
    if (hydrated && !session) router.replace("/sign-in?next=/wallet");
  }, [hydrated, session, router]);

  if (!hydrated || !session) return <Screen />;

  return (
    <Screen>
      <AnimatedComponent variant={slideInOut} className={cn(stackClasses, "py-4")}>
        {/* Who is signed in. On the phone it is the one place that says so;
            from 768px the rail says it, so the screen takes a title instead. */}
        <header className="flex items-center gap-3 md:hidden">
          <Web3Avatar address={session.email} size={32} />
          <p className="min-w-0 flex-1 truncate text-[13px] text-fg-muted">{session.email}</p>
        </header>
        <div className="hidden md:block">
          <PageHeader title="Wallet" hideBack subtitle="Your balance, card and shares." />
        </div>

        <CrossFade
          className={stackClasses}
          branchKey={balances.isLoading ? "loading" : balances.isError ? "error" : "ready"}
        >
          {balances.isLoading ? (
            <div className={stackClasses}>
              <div className="grid gap-2">
                <Skeleton className="h-3 w-14" />
                <Skeleton className="h-10 w-48" />
              </div>
              <div className="grid grid-cols-3 gap-2">
                <Skeleton className="h-10 w-full" />
                <Skeleton className="h-10 w-full" />
                <Skeleton className="h-10 w-full" />
              </div>
              <SkeletonRows rows={3} />
            </div>
          ) : balances.isError ? (
            /* An unreadable balance is an error, never a zero. Its predecessor
               answered zero whenever the RPC provider was unreachable, so an
               outage and an empty account looked identical. */
            <InfoBanner tone="error" icon={<PiWarningBold />}>
              <p className="font-medium">Could not load your balance</p>
              <p className="mt-0.5 text-xs">
                {balances.error instanceof Error
                  ? balances.error.message
                  : "Try again in a moment."}
              </p>
            </InfoBanner>
          ) : (
            /* From 1024px: what you have and what you can do with it on the
               left; what has happened on the right, as the full list. */
            <div className={cn(stackClasses, "lg:grid-cols-2 lg:items-start lg:gap-x-12")}>
              <div className={stackClasses}>
                <section className="grid gap-4">
                  <Balances balances={balances.data ?? []} />
                  <BalanceActions />
                </section>

                {card.data === null ? <NoCardBanner /> : null}
                {card.data ? <CardAllowanceWidget card={card.data} /> : null}

                {card.data?.needs_resync ? (
                  <InfoBanner
                    tone="warning"
                    icon={<PiWarningBold />}
                    action={
                      <Link href="/cards/resync" className="block">
                        <Button variant="secondary" size="sm" fullWidth={false}>
                          Resync
                        </Button>
                      </Link>
                    }
                  >
                    <p className="font-medium">Card out of sync</p>
                    <p className="mt-0.5 text-xs">A quick resync keeps it working at the counter.</p>
                  </InfoBanner>
                ) : null}

                <SharesModule />
              </div>

              <Section
                title="Recent activity"
                description="Every movement of your money, in and out."
                action={
                  activity.data?.movements.length ? (
                    <Link href="/history" className={sectionLinkClasses}>
                      View all
                    </Link>
                  ) : undefined
                }
              >
                {activity.isLoading ? (
                  <SkeletonRows rows={3} />
                ) : (
                  <MovementList
                    movements={activity.data?.movements ?? []}
                    equityByRef={equityByRef}
                    grouped
                    emptyState={
                      <EmptyState title="No activity yet">
                        Add cash through an agent, or receive USDC on Base, and it will show
                        up here.
                      </EmptyState>
                    }
                  />
                )}
              </Section>
            </div>
          )}
        </CrossFade>
      </AnimatedComponent>
    </Screen>
  );
}
