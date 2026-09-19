"use client";

import { useEffect, useMemo, type ReactNode } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { PiWarningBold } from "react-icons/pi";
import { Screen, Section } from "@/components/ui/Screen";
import { PageHeader } from "@/components/ui/PageHeader";
import { Button } from "@/components/ui/Button";
import { BalanceHero, BalanceActions, type StatusLine } from "@/components/ui/Balances";
import { MovementList, mergeFeed, daysAgo } from "@/components/ui/MovementList";
import { InfoBanner } from "@/components/ui/InfoBanner";
import { TodayStrip, cardTile, sharesTile, pendingTile, type Tile } from "@/components/ui/TodayStrip";
import { CardPrompt } from "@/components/ui/CardPrompt";
import { VerifyPrompt } from "@/components/ui/VerifyPrompt";
import { PortfolioChart } from "@/components/ui/PortfolioChart";
import { EmptyState } from "@/components/ui/Surface";
import { CrossFade } from "@/components/ui/CrossFade";
import { Skeleton, SkeletonRows } from "@/components/ui/Skeleton";
import { AnimatedComponent, slideInOut } from "@/components/ui/AnimatedComponents";
import { Web3Avatar } from "@/components/ui/Web3Avatar";
import { sectionLinkClasses, stackClasses } from "@/components/ui/Styles";
import { cn } from "@/lib/utils";
import { useSession } from "@/lib/auth";
import { useBalances, useActivity, useCard } from "@/lib/ledger";
import { useHoldings, useEquityActivity } from "@/lib/holdings";
import { formatMinor, type CardSummary } from "@/lib/api";

/** How far back the home looks. The rest is one tap away, on Activity. */
const RECENT_DAYS = 1;
const RECENT_ROWS = 6;

/**
 * The wallet, and the app's home screen.
 *
 * One implementation behind both routes. They were two copies of the same
 * screen, which is how "/" and "/wallet" came to show different things -- a
 * fix applied to one of them silently did not apply to the other.
 *
 * The order is by weight: what you have (the hero), what it is worth over
 * time (the chart), what today looks like (one strip of figures, the
 * Stocks tile among them), what happened (today and yesterday). Everything
 * past that is a section link away: the stocks themselves are one tap off
 * the tile or the Holdings item. The previous layout gave each of these a
 * panel of equal size, and the page was a scroll of similar boxes with the
 * one number that matters at the top of it, no larger than the rest.
 */
export function WalletScreen() {
  const router = useRouter();
  const { hydrated, session } = useSession();
  const balances = useBalances();
  const activity = useActivity(6);
  const card = useCard();
  const holdings = useHoldings();
  const equity = useEquityActivity(100);

  useEffect(() => {
    if (hydrated && !session) router.replace("/sign-in?next=/wallet");
  }, [hydrated, session, router]);

  const movements = activity.data?.movements ?? [];
  // Money and the stock it bought, in one feed; the six most recent of
  // either kind from today and yesterday.
  const recent = useMemo(
    () =>
      mergeFeed(activity.data?.movements ?? [], equity.data?.activity)
        .filter((f) => daysAgo(f.at) <= RECENT_DAYS)
        .slice(0, RECENT_ROWS),
    [activity.data, equity.data],
  );

  // Money committed to a handover. One currency's worth is shown; two
  // escrows at once is not a state the product produces.
  const escrow = balances.data?.find((b) => b.escrow.minor !== 0)?.escrow;

  // Nothing in, nothing moved: the screen's job is to get the first naira
  // in, not to show three empty sections about what it would do with one.
  const firstUse =
    !!balances.data &&
    balances.data.every((b) => b.available.minor === 0 && b.escrow.minor === 0) &&
    !!activity.data &&
    movements.length === 0;

  const tiles: Tile[] = [];
  if (card.data) tiles.push(cardTile(card.data));
  if (holdings.data) tiles.push(sharesTile(holdings.data));
  if (escrow) tiles.push(pendingTile(escrow));

  if (!hydrated || !session) return <Screen />;

  return (
    <Screen>
      {/* The first-run ask, over the wallet, for somebody not yet verified. */}
      <VerifyPrompt />
      <AnimatedComponent variant={slideInOut} className={cn(stackClasses, "py-4")}>
        {/* Who is signed in. On the phone it is the one place that says so;
            from 768px the rail says it, so the screen takes a title instead. */}
        <header className="flex items-center gap-3 md:hidden">
          <Web3Avatar address={session.email} size={32} />
          <p className="min-w-0 flex-1 truncate text-[13px] text-fg-muted">{session.email}</p>
        </header>
        <div className="hidden md:block">
          <PageHeader title="Wallet" hideBack subtitle="Your balance, card and stocks." />
        </div>

        <CrossFade
          className={stackClasses}
          branchKey={balances.isLoading ? "loading" : balances.isError ? "error" : "ready"}
        >
          {balances.isLoading ? (
            <WalletSkeleton />
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
               left; what has happened on the right. */
            <div className={cn(stackClasses, "lg:grid-cols-2 lg:items-start lg:gap-x-12")}>
              <div className={stackClasses}>
                <section className="grid gap-5">
                  <BalanceHero
                    balances={balances.data ?? []}
                    status={cardStatus(card)}
                  />
                  <BalanceActions />
                </section>

                {firstUse ? (
                  <FirstUse />
                ) : (
                  <>
                    <PortfolioChart />
                    <TodayStrip tiles={tiles} />
                  </>
                )}

                {card.data === null ? <CardPrompt /> : null}

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
              </div>

              {firstUse ? null : (
                <Section title="Recent activity">
                  {activity.isLoading ? (
                    <SkeletonRows rows={3} />
                  ) : recent.length ? (
                    <MovementList items={recent} grouped />
                  ) : movements.length ? (
                    <p className="text-[13px] leading-5 text-fg-muted">
                      Nothing today or yesterday.
                    </p>
                  ) : (
                    <EmptyState title="No activity yet">
                      Add cash through an agent, or receive USDC on Base, and it will show
                      up here.
                    </EmptyState>
                  )}
                  {movements.length ? (
                    /* 44px to press, drawn as one line: the negative margin
                       keeps the section's rhythm where the hit area does not. */
                    <Link
                      href="/history"
                      className={cn(sectionLinkClasses, "-my-3 inline-flex h-11 w-fit items-center")}
                    >
                      View all activity
                    </Link>
                  ) : null}
                </Section>
              )}
            </div>
          )}
        </CrossFade>
      </AnimatedComponent>
    </Screen>
  );
}

/**
 * The line under the balance: the most useful true thing about the card
 * right now. The limit and the balance are different constraints, and a
 * card is stopped by whichever binds first, so the line names that one.
 */
function cardStatus(card: ReturnType<typeof useCard>): StatusLine {
  if (card.isLoading) {
    return { icon: "card", hue: "--nav-settings", text: <Skeleton className="h-3 w-44" /> };
  }
  if (card.isError) return { icon: "warning", hue: "--hue-amber", text: "Card status unavailable" };
  const c: CardSummary | null | undefined = card.data;
  if (!c) return { icon: "link", hue: "--nav-settings", text: "No card linked" };
  if (c.needs_resync) return { icon: "warning", hue: "--hue-amber", text: "Card needs a resync" };
  const headroom = Math.max(0, c.daily_limit_subunit - c.spent_today_subunit);
  if (c.spendable.minor < headroom) {
    return { icon: "card", hue: "--nav-card", text: `Your card can spend ${c.spendable.display} today` };
  }
  return { icon: "card", hue: "--nav-card", text: `${formatMinor(headroom, "NGN")} left on your card today` };
}

/**
 * Before the first naira. One sentence and one step. The step is the
 * agent map rather than the deposit chooser, because the chooser is what
 * Receive above already opens; this is the concrete thing to do next for
 * the person this product is for -- somebody holding notes.
 */
function FirstUse() {
  return (
    <div className="panel grid gap-3 p-4">
      <div className="grid gap-1">
        <p className="text-sm font-medium text-fg">Add money to start</p>
        <p className="text-[13px] leading-5 text-fg-muted">
          Hand cash to an agent near you, or receive USDC on Base. Your card and stocks
          follow from there.
        </p>
      </div>
      <Link href="/agents" className="block w-fit">
        <Button variant="secondary" fullWidth={false}>
          Find an agent
        </Button>
      </Link>
    </div>
  );
}

/** The final layout, drawn in grey, so nothing jumps when the numbers land. */
function WalletSkeleton() {
  return (
    <div className={cn(stackClasses, "lg:grid-cols-2 lg:items-start lg:gap-x-12")}>
      <div className={stackClasses}>
        <div className="grid gap-5">
          <div className="grid gap-2">
            <Skeleton className="h-4 w-14" />
            <Skeleton className="h-10 w-48" />
            <div className="flex gap-1.5">
              <Skeleton className="h-6 w-28" />
              <Skeleton className="h-6 w-24" />
            </div>
            <Skeleton className="h-4 w-52" />
          </div>
          <div className="grid grid-cols-3 gap-2">
            <Skeleton className="h-11 w-full" />
            <Skeleton className="h-11 w-full" />
            <Skeleton className="h-11 w-full" />
          </div>
        </div>
        <div className="grid gap-3">
          <Skeleton className="h-3 w-24" />
          <Skeleton className="h-7 w-40" />
          <Skeleton className="h-40 w-full lg:h-[200px]" />
        </div>
        <div className="grid grid-cols-2 gap-2">
          {[0, 1].map((i) => (
            <div key={i} className="panel grid gap-1.5 p-3">
              <Skeleton className="h-3 w-16" />
              <Skeleton className="h-4 w-24" />
              <Skeleton className="h-3 w-20" />
            </div>
          ))}
        </div>
      </div>
      <div className="grid gap-3">
        <Skeleton className="h-4 w-28" />
        <SkeletonRows rows={3} />
      </div>
    </div>
  );
}
