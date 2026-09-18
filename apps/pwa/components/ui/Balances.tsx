"use client";

import Link from "next/link";
import {
  PiArrowDownLeftBold,
  PiLockSimpleBold,
  PiMoneyWavyBold,
  PiQrCodeBold,
} from "react-icons/pi";
import { cn } from "@/lib/utils";
import { Amount, CurrencyTag } from "./Amount";
import { Button } from "./Button";
import { HOME_CURRENCY, worthShowing, useBalanceTotal } from "@/lib/ledger";
import type { CurrencyBalance } from "@/lib/api";

/**
 * What you have: one headline, then the currencies it is made of.
 *
 * The headline is everything, converted to naira. Leading with the naira
 * balance alone was worse in a specific way: the card said "Balance" and
 * showed ₦0.00 to somebody holding ten cents, which reads as having nothing.
 * A label that says "everything" must not show one currency's slice.
 *
 * The breakdown underneath is what keeps the earlier objection answered. A
 * converted headline moves when the rate moves and nobody has spent anything,
 * so the per-currency figures stay on screen, unconverted and exact. One
 * number ALONE would leave no way to tell a price change from a payment;
 * one number ABOVE its parts does not.
 *
 * The headline carries no "approximate" caveat: the breakdown is the
 * disclosure. `total.converted` still comes back from the server, so the
 * caveat can be reinstated in one line if it is ever wanted.
 *
 * With no rate available the server sends no total, and the headline falls
 * back to the home currency rather than inventing one.
 *
 * No box around any of it: the page is the container. The one big number
 * on the home screen is set in the display face with tabular numerals.
 */
export function Balances({
  balances,
  className,
}: {
  balances: CurrencyBalance[];
  className?: string;
}) {
  const total = useBalanceTotal();
  const home = balances.find((b) => b.currency === HOME_CURRENCY);

  // With a total, every currency belongs in the breakdown -- the headline is
  // no longer any one of them. Without one, the headline IS naira, so naira
  // would otherwise be shown twice.
  const headline = total.data?.amount ?? home?.available;
  const parts = balances.filter((b) =>
    total.data ? worthShowing(b) : b.currency !== HOME_CURRENCY && worthShowing(b),
  );

  return (
    <div className={cn("grid gap-3", className)}>
      <div className="grid gap-1">
        <p className="eyebrow">Balance</p>
        <Amount value={headline} size="hero" />
        {!total.data && home && home.escrow.minor !== 0 ? (
          <Escrowed amount={home.escrow} />
        ) : null}
      </div>

      {parts.length ? (
        <div className="grid gap-2 border-t border-line pt-3">
          {parts.map((b) => (
            <div key={b.currency} className="grid gap-0.5">
              <div className="flex items-center justify-between gap-3">
                <CurrencyTag code={b.currency} />
                <Amount value={b.available} size="md" />
              </div>
              {b.escrow.minor !== 0 ? <Escrowed amount={b.escrow} /> : null}
            </div>
          ))}
        </div>
      ) : null}
    </div>
  );
}

/**
 * Money that is committed but not yet gone.
 *
 * Shown apart from the spendable figure and never added to it. Somebody who
 * has pledged cash to an agent has that amount reserved against the handover;
 * folding it into "balance" would show them money they cannot spend, and they
 * would find out at a checkout counter.
 */
function Escrowed({ amount }: { amount: CurrencyBalance["escrow"] }) {
  return (
    <p className="flex items-center gap-1.5 text-xs text-fg-muted [&>svg]:size-3.5">
      <PiLockSimpleBold className="shrink-0" />
      <Amount value={amount} size="sm" className="font-normal" /> held for a
      handover
    </p>
  );
}

/**
 * The row of things you can do with a balance.
 *
 * Three, deliberately: the two ways money comes in and the one way it goes
 * out at a counter. A fourth would push these to a scroll on a small phone,
 * which is where an action goes to be never used.
 *
 * Cash in is the loud one. This is a naira product before it is a crypto
 * one: the largest group of people it is for hold physical notes and want
 * them in a balance.
 */
export function BalanceActions() {
  // Receive goes to the chooser, not straight to a chain. Somebody adding
  // money has not yet decided whether they are handing over naira or sending
  // crypto, and sending them to one of the two answers is picking for them.
  return (
    <div className="grid grid-cols-3 gap-2">
      <Link href="/cash" className="block">
        <Button variant="primary" leadingIcon={<PiMoneyWavyBold />} className="px-2 [&_svg]:size-4">
          Cash in
        </Button>
      </Link>
      <Link href="/deposit" className="block">
        <Button variant="secondary" leadingIcon={<PiArrowDownLeftBold />} className="px-2 [&_svg]:size-4">
          Receive
        </Button>
      </Link>
      <Link href="/pay" className="block">
        <Button variant="secondary" leadingIcon={<PiQrCodeBold />} className="px-2 [&_svg]:size-4">
          Pay
        </Button>
      </Link>
    </div>
  );
}
