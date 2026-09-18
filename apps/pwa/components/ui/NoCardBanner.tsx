"use client";

import Link from "next/link";
import { PiCreditCardBold } from "react-icons/pi";
import { Button } from "./Button";
import { Section } from "./Screen";
import { tileClasses } from "./Styles";

/**
 * Wallet-level notice for people who have not linked a physical Tapp Card
 * yet. The wallet works without one; this names the thing they are missing
 * and the two ways to get it.
 */
export function NoCardBanner() {
  return (
    <Section title="Card" description="Tap to pay at any merchant from this balance.">
      <div className="panel grid gap-3 p-4">
        <div className="flex items-center gap-3">
          <span className={tileClasses}>
            <PiCreditCardBold />
          </span>
          <h3 className="heading text-sm">No card linked</h3>
        </div>
        <p className="text-[13px] leading-5 text-fg-muted">
          Your card spends from this balance, within your limits, with no chargebacks.
        </p>
        <div className="grid grid-cols-2 gap-2">
          <Link href="/link" className="block">
            <Button variant="primary" size="sm">
              I have a card
            </Button>
          </Link>
          <Link href="mailto:labs@zoracle.xyz?subject=Order%20a%20Tapp%20Card" className="block">
            <Button variant="secondary" size="sm">
              Order a card
            </Button>
          </Link>
        </div>
      </div>
    </Section>
  );
}
