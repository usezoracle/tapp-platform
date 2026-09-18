"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import {
  PiCaretRightBold,
  PiCreditCardBold,
  PiSlidersHorizontalBold,
  PiLockKeyBold,
  PiIdentificationCardBold,
  PiQuestionBold,
  PiSignOutBold,
  PiCopyBold,
  PiCheckBold,
  PiChartLineUpBold,
  PiEnvelopeSimpleBold,
  PiWalletBold,
  PiGlobeSimpleBold,
} from "react-icons/pi";
import { Screen, Section } from "@/components/ui/Screen";
import { PageHeader } from "@/components/ui/PageHeader";
import { StatusChip } from "@/components/ui/StatusChip";
import { listClasses, rowClasses, stackClasses, tileClasses } from "@/components/ui/Styles";
import { AnimatedComponent, slideInOut } from "@/components/ui/AnimatedComponents";
import { KycTierChip } from "@/components/ui/KycTierChip";
import { Web3Avatar } from "@/components/ui/Web3Avatar";
import { cn } from "@/lib/utils";
import { useSession } from "@/lib/auth";
import { useCard, useDepositAddress, useKycStatus } from "@/lib/ledger";

/**
 * Settings, as six titled sections: Account, Card, Security, Limits, Your
 * stocks, About. From 768px they fall into two columns; on the phone they
 * stack in the same order.
 */
export default function SettingsPage() {
  const router = useRouter();
  const { hydrated, session, clear } = useSession();
  const deposit = useDepositAddress();
  const card = useCard();
  const kyc = useKycStatus();
  const [copied, setCopied] = useState(false);

  // The deposit address, which is the only address a holder has. There is no
  // per-user wallet any more -- the treasury is pooled and this is a derived
  // address that credits their ledger balance when USDC lands on it.
  const displayAddress = deposit.data?.address ?? "";

  const copyToClipboard = async () => {
    if (!displayAddress) return;
    try {
      await navigator.clipboard.writeText(displayAddress);
      setCopied(true);
      setTimeout(() => setCopied(false), 2000);
    } catch (err) {
      console.error("Failed to copy address:", err);
    }
  };

  useEffect(() => {
    if (hydrated && !session) router.replace("/sign-in?next=/settings");
  }, [hydrated, session, router]);

  if (!hydrated || !session) return <Screen />;

  return (
    <Screen>
      <AnimatedComponent variant={slideInOut} className={cn(stackClasses, "py-4")}>
        <PageHeader
          title="Settings"
          back="/"
          subtitle={session.email}
          trailing={<Web3Avatar address={session.email} size={32} />}
        />

        <div className={cn(stackClasses, "md:grid-cols-2 md:gap-x-12")}>
          <Section title="Account" description="Who you are signed in as, and how verified you are.">
            <div className={listClasses}>
              <SettingsRow
                icon={<PiEnvelopeSimpleBold />}
                title={session.email}
                subtitle="Signed in"
              />
              <SettingsRow
                href="/settings/kyc"
                icon={<PiIdentificationCardBold />}
                title="Identity verification"
                subtitle={
                  kyc.data?.next
                    ? `Next: ${kyc.data.next.tier_name}`
                    : kyc.data
                      ? "Fully verified"
                      : kyc.isError
                        ? "Not available on this deployment"
                        : "BVN and photo, raises your limits"
                }
                trailing={kyc.data ? <KycTierChip status={kyc.data} /> : <StatusChip>—</StatusChip>}
              />
              <SettingsRow
                icon={<PiWalletBold />}
                title="Deposit address"
                subtitle={
                  deposit.data
                    ? `USDC on ${deposit.data.network}`
                    : deposit.isError
                      ? "Could not load your address"
                      : "Loading"
                }
                trailing={
                  deposit.data ? (
                    <button
                      type="button"
                      onClick={copyToClipboard}
                      className={cn(
                        "focus-ring inline-flex h-7 items-center gap-1 rounded-sm px-1.5 text-xs font-medium transition-colors [&>svg]:size-3.5",
                        copied ? "text-positive" : "text-accent hover:bg-hover",
                      )}
                    >
                      {copied ? <PiCheckBold /> : <PiCopyBold />}
                      {copied ? "Copied" : "Copy"}
                    </button>
                  ) : undefined
                }
              />
              {deposit.data ? (
                <button
                  type="button"
                  onClick={copyToClipboard}
                  title="Copy address"
                  className="focus-ring block w-full break-all px-3 py-2.5 text-left font-mono text-xs leading-5 text-fg transition-colors hover:bg-hover"
                >
                  {displayAddress}
                </button>
              ) : null}
            </div>
          </Section>

          <Section title="Card" description="The physical card that spends from this balance.">
            <div className={listClasses}>
              <SettingsRow
                href="/settings/card"
                icon={<PiCreditCardBold />}
                title="Tapp Card"
                subtitle={card.data ? "Manage your physical card" : "Link a card to tap and pay"}
                trailing={
                  card.data ? (
                    <StatusChip tone="success">Linked</StatusChip>
                  ) : (
                    <StatusChip>None</StatusChip>
                  )
                }
              />
            </div>
          </Section>

          <Section title="Security" description="Your PIN, and the way out.">
            <div className={listClasses}>
              <SettingsRow
                href="/settings/security"
                icon={<PiLockKeyBold />}
                title="PIN"
                subtitle="Change the PIN your card asks for"
              />
              <button
                type="button"
                onClick={clear}
                className={cn(rowClasses, "focus-ring w-full text-left")}
              >
                <span className={cn(tileClasses, "text-negative")}>
                  <PiSignOutBold />
                </span>
                <span className="grid flex-1 gap-0.5">
                  <span className="text-sm font-medium text-negative-fg">Sign out</span>
                  <span className="text-xs text-fg-muted">Sign back in to restore access.</span>
                </span>
              </button>
            </div>
          </Section>

          <Section title="Limits" description="Caps that stop the card before your balance does.">
            <div className={listClasses}>
              {card.data ? (
                <SettingsRow
                  href="/settings/limits"
                  icon={<PiSlidersHorizontalBold />}
                  title="Spend limits"
                  subtitle="Daily, per tap, step-up threshold"
                />
              ) : (
                <SettingsRow
                  icon={<PiSlidersHorizontalBold />}
                  title="Spend limits"
                  subtitle="Link a card to set them"
                />
              )}
            </div>
          </Section>

          <Section title="Your stocks" description="Businesses you own a slice of, earned one tap at a time.">
            <div className={listClasses}>
              <SettingsRow
                href="/holdings"
                icon={<PiChartLineUpBold />}
                title="Holdings"
                subtitle="Value, lots and unlock dates"
              />
            </div>
          </Section>

          <Section title="About" description="Help, and the network this wallet settles on.">
            <div className={listClasses}>
              <SettingsRow
                href="mailto:labs@zoracle.xyz"
                icon={<PiQuestionBold />}
                title="Help and support"
                subtitle="labs@zoracle.xyz"
                external
              />
              <SettingsRow
                icon={<PiGlobeSimpleBold />}
                title="Network"
                subtitle={deposit.data ? `USDC on ${deposit.data.network}` : "USDC on Base"}
              />
            </div>
          </Section>
        </div>
      </AnimatedComponent>
    </Screen>
  );
}

function SettingsRow({
  href,
  icon,
  title,
  subtitle,
  trailing,
  external,
}: {
  /** Without one the row is a plain fact, not a link. */
  href?: string;
  icon: React.ReactNode;
  title: React.ReactNode;
  subtitle?: React.ReactNode;
  trailing?: React.ReactNode;
  external?: boolean;
}) {
  const inner = (
    <div className={cn(rowClasses, !href && "hover:bg-transparent")}>
      <span className={tileClasses}>{icon}</span>
      <div className="grid min-w-0 flex-1 gap-0.5">
        <p className="truncate text-sm font-medium text-fg">{title}</p>
        {subtitle ? <p className="truncate text-xs text-fg-muted">{subtitle}</p> : null}
      </div>
      {trailing ?? (href ? <PiCaretRightBold className="size-3.5 text-fg-subtle" /> : null)}
    </div>
  );
  if (!href) return inner;
  if (external) {
    return (
      <a href={href} target="_blank" rel="noopener noreferrer" className="focus-ring block">
        {inner}
      </a>
    );
  }
  return (
    <Link href={href} className="focus-ring block">
      {inner}
    </Link>
  );
}
