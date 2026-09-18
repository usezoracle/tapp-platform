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
} from "react-icons/pi";
import { Screen, SectionHeader } from "@/components/ui/Screen";
import { PageHeader } from "@/components/ui/PageHeader";
import { StatusChip } from "@/components/ui/StatusChip";
import { listClasses, rowClasses, tileClasses } from "@/components/ui/Styles";
import { AnimatedComponent, slideInOut } from "@/components/ui/AnimatedComponents";
import { KycTierChip } from "@/components/ui/KycTierChip";
import { Web3Avatar } from "@/components/ui/Web3Avatar";
import { cn } from "@/lib/utils";
import { useSession } from "@/lib/auth";
import { useCard, useDepositAddress, useKycStatus } from "@/lib/ledger";

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
      <AnimatedComponent variant={slideInOut} className="grid gap-6 py-4">
        <PageHeader
          title="Settings"
          back="/"
          subtitle={session.email}
          trailing={<Web3Avatar address={session.email} size={32} />}
        />

        <section className="grid gap-3">
          <SectionHeader title="Card" />
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
            {card.data && (
              <SettingsRow
                href="/settings/limits"
                icon={<PiSlidersHorizontalBold />}
                title="Spend limits"
                subtitle="Daily, per tap, step-up threshold"
              />
            )}
          </div>
        </section>

        <section className="grid gap-3">
          <SectionHeader title="Account" />
          <div className={listClasses}>
            <SettingsRow
              href="/holdings"
              icon={<PiChartLineUpBold />}
              title="Your shares"
              subtitle="Businesses you own a slice of"
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
              href="/settings/security"
              icon={<PiLockKeyBold />}
              title="Security"
              subtitle="PIN, sign-out"
            />
          </div>
        </section>

        <section className="grid gap-3">
          <SectionHeader
            title="Deposit address"
            action={
              deposit.data ? (
                <button
                  type="button"
                  onClick={copyToClipboard}
                  className={cn(
                    "focus-ring inline-flex items-center gap-1 rounded-sm text-xs font-medium transition-colors [&>svg]:size-3.5",
                    copied ? "text-positive" : "text-accent hover:underline",
                  )}
                >
                  {copied ? <PiCheckBold /> : <PiCopyBold />}
                  {copied ? "Copied" : "Copy"}
                </button>
              ) : null
            }
          />
          <div className="panel grid gap-1.5 p-3">
            <button
              type="button"
              onClick={copyToClipboard}
              title="Copy address"
              className="focus-ring break-all rounded-sm text-left font-mono text-xs leading-5 text-fg"
            >
              {displayAddress || "—"}
            </button>
            <p className="text-xs text-fg-muted">
              {deposit.data
                ? `USDC on ${deposit.data.network}`
                : deposit.isError
                  ? "Could not load your address"
                  : "Loading"}
            </p>
          </div>
        </section>

        <section className="grid gap-3">
          <SectionHeader title="Support" />
          <div className={listClasses}>
            <SettingsRow
              href="mailto:labs@zoracle.xyz"
              icon={<PiQuestionBold />}
              title="Help and support"
              subtitle="labs@zoracle.xyz"
              external
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
        </section>
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
  href: string;
  icon: React.ReactNode;
  title: React.ReactNode;
  subtitle?: React.ReactNode;
  trailing?: React.ReactNode;
  external?: boolean;
}) {
  const inner = (
    <div className={rowClasses}>
      <span className={tileClasses}>{icon}</span>
      <div className="grid min-w-0 flex-1 gap-0.5">
        <p className="truncate text-sm font-medium text-fg">{title}</p>
        {subtitle ? <p className="truncate text-xs text-fg-muted">{subtitle}</p> : null}
      </div>
      {trailing ?? <PiCaretRightBold className="size-3.5 text-fg-subtle" />}
    </div>
  );
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
