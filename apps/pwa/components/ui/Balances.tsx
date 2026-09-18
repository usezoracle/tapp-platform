"use client";

import Link from "next/link";
import type { ReactNode } from "react";
import { PiArrowDownLeftBold, PiMoneyWavyBold, PiQrCodeBold } from "react-icons/pi";
import { cn } from "@/lib/utils";
import { Amount } from "./Amount";
import { Button } from "./Button";
import { HOME_CURRENCY, useBalanceTotal } from "@/lib/ledger";
import type { CurrencyBalance } from "@/lib/api";

/**
 * What you have: one headline, the currencies it is made of, and one line
 * about what that means today.
 *
 * The headline is everything, converted to naira. Leading with the naira
 * balance alone was worse in a specific way: the card said "Balance" and
 * showed ₦0.00 to somebody holding ten cents, which reads as having nothing.
 * A label that says "everything" must not show one currency's slice.
 *
 * The legs sit under it as two quiet chips, not rows. A converted headline
 * moves when the rate moves and nobody has spent anything, so the
 * per-currency figures stay on screen, unconverted and exact -- but they
 * are the disclosure, not the point, and a chip says that where a row of
 * equal weight did not.
 *
 * With no rate available the server sends no total, and the headline falls
 * back to the home currency rather than inventing one; the naira chip is
 * then left out, since it would repeat the headline.
 *
 * No box around any of it: the page is the container.
 */
export function BalanceHero({
  balances,
  status,
  className,
}: {
  balances: CurrencyBalance[];
  /** One line under the legs: the most useful true thing right now. */
  status?: ReactNode;
  className?: string;
}) {
  const total = useBalanceTotal();
  const home = balances.find((b) => b.currency === HOME_CURRENCY);

  const headline = total.data?.amount ?? home?.available;

  // A breakdown of one part is not a breakdown. With a total, the legs are
  // shown only when there are at least two of them worth showing -- a lone
  // "NGN ₦0.00" under a ₦0.00 headline says the same thing twice. Without
  // a total the headline is naira itself, so any other leg is new
  // information and is shown on its own.
  const nonZero = (b: CurrencyBalance) => b.available.minor !== 0 || b.escrow.minor !== 0;
  const legs = total.data ? balances.filter(nonZero) : balances.filter((b) => b.currency !== HOME_CURRENCY && nonZero(b));
  const parts = total.data && legs.length < 2 ? [] : legs;

  return (
    <div className={cn("grid gap-2", className)}>
      <p className="eyebrow">Balance</p>
      <Amount value={headline} size="headline" />
      {parts.length ? (
        <dl className="flex flex-wrap items-center gap-1.5">
          {parts.map((b) => (
            <div
              key={b.currency}
              className="inline-flex h-6 items-center gap-1.5 rounded-sm bg-sunken px-2"
            >
              <dt className="text-[11px] font-medium uppercase tracking-wide text-fg-muted">
                {b.currency}
              </dt>
              <dd className="text-xs font-medium tabular-nums text-fg">{b.available.display}</dd>
            </div>
          ))}
        </dl>
      ) : null}
      {status ? <p className="text-[13px] leading-5 text-fg-muted">{status}</p> : null}
    </div>
  );
}

/**
 * The row of things you can do with a balance.
 *
 * Three, deliberately: the two ways money comes in and the one way it goes
 * out at a counter. A fourth would push these to a scroll on a small phone,
 * which is where an action goes to be never used. 44px tall: these are
 * pressed with a thumb, standing up.
 *
 * Cash in is the loud one. This is a naira product before it is a crypto
 * one: the largest group of people it is for hold physical notes and want
 * them in a balance.
 */
export function BalanceActions() {
  // Receive goes to the chooser, not straight to a chain. Somebody adding
  // money has not yet decided whether they are handing over naira or sending
  // crypto, and sending them to one of the two answers is picking for them.
  const icon = "whitespace-nowrap [&_svg]:size-4";
  return (
    <div className="grid grid-cols-3 gap-2">
      <Link href="/cash" className="block">
        <Button variant="primary" size="lg" leadingIcon={<PiMoneyWavyBold />} className={icon}>
          Cash in
        </Button>
      </Link>
      <Link href="/deposit" className="block">
        <Button variant="secondary" size="lg" leadingIcon={<PiArrowDownLeftBold />} className={icon}>
          Receive
        </Button>
      </Link>
      <Link href="/pay" className="block">
        <Button variant="secondary" size="lg" leadingIcon={<PiQrCodeBold />} className={icon}>
          Pay
        </Button>
      </Link>
    </div>
  );
}
