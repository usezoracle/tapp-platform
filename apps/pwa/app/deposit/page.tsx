"use client";

import { useEffect } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import {
  PiBankBold,
  PiMoneyWavyBold,
  PiCoinsBold,
  PiCaretRightBold,
} from "react-icons/pi";
import { Screen } from "@/components/ui/Screen";
import { Surface } from "@/components/ui/Surface";
import { Amount } from "@/components/ui/Amount";
import {
  AnimatedComponent,
  slideInOut,
} from "@/components/ui/AnimatedComponents";
import { useSession } from "@/lib/auth";
import { useBalances, balanceIn } from "@/lib/ledger";

/**
 * The ways money comes in.
 *
 * A chooser rather than a single screen, because they are genuinely different
 * acts with different risks. A bank transfer means getting an account number
 * right; cash means walking to somebody; USDC means getting an address right.
 * Putting them behind one "Deposit" button meant the routes people were most
 * likely to use were the ones they could not see.
 *
 * Naira leads with the bank transfer, not with cash. It is the route somebody
 * can use from where they are standing, at any hour, with the banking app they
 * already have -- and unlike a cash pledge it needs nobody else to turn up.
 * Cash is still here, one row down, for the people it exists for.
 */
export default function DepositPage() {
  const router = useRouter();
  const { hydrated, session } = useSession();
  const balances = useBalances();

  useEffect(() => {
    if (hydrated && !session) router.replace("/sign-in?next=/deposit");
  }, [hydrated, session, router]);

  if (!hydrated || !session) return <Screen />;

  const ngn = balanceIn(balances.data, "NGN");
  const usd = balanceIn(balances.data, "USD");

  return (
    <Screen>
      <AnimatedComponent variant={slideInOut} className="grid gap-6 py-8">
        <header className="grid gap-1">
          <h1 className="text-xl font-medium text-[var(--fg)]">Add money</h1>
          <p className="text-sm leading-relaxed text-[var(--fg-muted)]">
            Two ways in. Both land in the same balance.
          </p>
        </header>

        <Route
          href="/deposit/naira"
          icon={<PiBankBold />}
          title="Transfer naira"
          body="Transfer to your own account number from any Nigerian bank. Naira in your balance when it lands."
          balanceLabel="Your naira"
          balance={ngn?.available}
        />

        <Route
          href="/cash"
          icon={<PiMoneyWavyBold />}
          title="Deposit cash"
          body="Photograph the notes and hand them to an agent near you. Naira in your balance when they confirm."
          balanceLabel="Your naira"
          balance={ngn?.available}
        />

        <Route
          href="/deposit/base"
          icon={<PiCoinsBold />}
          title="Deposit crypto"
          body="Send USDC on Base to your own address. Dollars in your balance once the network confirms it."
          balanceLabel="Your dollars"
          balance={usd?.available}
        />

        <p className="px-1 text-center text-xs leading-relaxed text-[var(--fg-muted)]">
          Holding both?{" "}
          <Link href="/convert" className="font-medium text-[var(--accent)]">
            Convert between them
          </Link>
          .
        </p>
      </AnimatedComponent>
    </Screen>
  );
}

function Route({
  href,
  icon,
  title,
  body,
  balanceLabel,
  balance,
}: {
  href: string;
  icon: React.ReactNode;
  title: string;
  body: string;
  balanceLabel: string;
  balance: Parameters<typeof Amount>[0]["value"];
}) {
  return (
    <Link href={href}>
      <Surface radius="3xl" className="grid gap-3 transition-colors hover:bg-[var(--sunken)]">
        <div className="flex items-start gap-3">
          <span className="grid h-10 w-10 shrink-0 place-items-center rounded-full bg-[var(--sunken)] text-lg text-[var(--fg-muted)]">
            {icon}
          </span>
          <div className="min-w-0 flex-1">
            <p className="text-sm font-medium text-[var(--fg)]">{title}</p>
            <p className="mt-1 text-xs leading-relaxed text-[var(--fg-muted)]">{body}</p>
          </div>
          <PiCaretRightBold className="mt-1 shrink-0 text-[var(--fg-subtle)]" />
        </div>
        <div className="flex items-baseline justify-between border-t border-[var(--line)] pt-3">
          <span className="text-xs text-[var(--fg-muted)]">{balanceLabel}</span>
          <Amount value={balance} size="md" />
        </div>
      </Surface>
    </Link>
  );
}
