"use client";

import Link from "next/link";
import { PiCreditCardBold } from "react-icons/pi";
import { Button } from "./Button";
import { tileClasses } from "./Styles";

/**
 * The one line on the wallet for somebody with no Tapp Card yet: what it
 * is, and the one thing to do about it. Ordering a card is a text link
 * under the sentence, not a second button -- two equal buttons for one
 * decision make a person choose before they know which applies.
 */
export function CardPrompt() {
  return (
    <div className="panel flex items-center gap-3 p-3">
      <span className={tileClasses}>
        <PiCreditCardBold />
      </span>
      <span className="grid min-w-0 flex-1 gap-0.5">
        <span className="text-sm font-medium text-fg">Link your Tapp Card</span>
        <span className="text-xs leading-4 text-fg-muted">Tap to pay from this balance.</span>
        <a
          href="mailto:labs@zoracle.xyz?subject=Order%20a%20Tapp%20Card"
          className="focus-ring w-fit rounded-sm text-xs leading-4 text-accent hover:underline"
        >
          Order a card
        </a>
      </span>
      <Link href="/link" className="block shrink-0">
        <Button variant="primary" size="sm" fullWidth={false}>
          Link card
        </Button>
      </Link>
    </div>
  );
}
