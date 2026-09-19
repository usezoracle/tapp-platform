"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { resetCards } from "@/lib/cardReset";
import {
  PiArrowLeftBold,
  PiCheckCircleFill,
  PiWarningOctagonFill,
} from "react-icons/pi";
import { Screen } from "@/components/ui/Screen";
import { Button } from "@/components/ui/Button";
import { ReceiptCard } from "@/components/ui/ReceiptCard";
import { StatusChip } from "@/components/ui/StatusChip";
import { InfoBanner } from "@/components/ui/InfoBanner";
import {
  AnimatedComponent,
  slideInOut,
} from "@/components/ui/AnimatedComponents";
import { useSession } from "@/lib/auth";
import { cardsApi, type CardSummary } from "@/lib/api";
import { formatNgn } from "@/lib/utils";

export default function SettingsCardPage() {
  const router = useRouter();
  const { hydrated, session } = useSession();

  useEffect(() => {
    if (hydrated && !session) router.replace("/sign-in?next=/settings/card");
  }, [hydrated, session, router]);

  const card = useQuery({
    queryKey: ["cards", "me"],
    queryFn:  () => cardsApi.me(session!.jwt),
    enabled:  !!session,
    retry:    false,
  });

  if (!hydrated || !session) return <Screen />;

  return (
    <Screen>
      <AnimatedComponent
        variant={slideInOut}
        className="grid gap-6 py-10 text-sm text-neutral-900 dark:text-white"
      >
        <Link
          href="/settings"
          className="inline-flex w-fit items-center gap-1 text-xs font-medium text-gray-500 transition-colors hover:text-neutral-900 dark:text-white/50 dark:hover:text-white"
        >
          <PiArrowLeftBold /> Back to settings
        </Link>

        <div className="space-y-2">
          <h1 className="text-xl font-medium">Linked Freedom Card</h1>
          <p className="text-sm text-gray-500 dark:text-white/50">
            Manage your physical contactless card.
          </p>
        </div>

        {card.isLoading ? (
          <div className="flex justify-center py-10">
            <div className="loader" />
          </div>
        ) : card.isError || !card.data ? (
          <NoCardState />
        ) : (
          <CardDetail card={card.data} />
        )}
      </AnimatedComponent>
    </Screen>
  );
}

function NoCardState() {
  return (
    <>
      <InfoBanner>
        <p className="font-medium text-neutral-900 dark:text-white">
          No card linked
        </p>
        <p className="mt-1 text-xs">
          Tap your physical Freedom Card to the back of your phone, or open
          the activation link printed on the card.
        </p>
      </InfoBanner>
      <div className="grid gap-2 w-full">
        <Link href="/link" className="w-full">
          <Button>Link a Freedom Card</Button>
        </Link>
      </div>
    </>
  );
}

function CardDetail({ card }: { card: CardSummary }) {
  return (
    <>
      <div className="flex items-center justify-between">
        <StatusBadge status={card.status} />
        <StatusChip className="font-mono">
          {card.id.slice(0, 8)}…
        </StatusChip>
      </div>

      <ReceiptCard
        rows={[
          { label: "Daily limit",       value: <span className="tabular-nums">{formatNgn(card.daily_limit_subunit / 100)}</span> },
          { label: "Per-tap limit",     value: <span className="tabular-nums">{formatNgn(card.per_tap_limit_subunit / 100)}</span> },
          { label: "Step-up above",     value: <span className="tabular-nums">{formatNgn(card.step_up_threshold_subunit / 100)}</span> },
          { label: "Spent today",       value: <span className="tabular-nums">{formatNgn(card.spent_today_subunit / 100)}</span> },
          { label: "Spendable now",     value: <span className="tabular-nums">{card.spendable.display}</span> },
          { label: "PIN attempts left", value: card.pin_attempts_remaining },
        ]}
      />

      {card.needs_resync && (
        <InfoBanner tone="warning" icon={<PiWarningOctagonFill className="text-amber-500" />}>
          <p className="font-medium text-neutral-900 dark:text-white">
            Card out of sync
          </p>
          <p className="mt-1 text-xs">
            The last write was interrupted. Run a quick resync to keep
            your card operational.
          </p>
        </InfoBanner>
      )}

      <div className="grid gap-3">
        <Link href="/settings/limits" className="block w-full">
          <Button variant="primary">Edit limits</Button>
        </Link>
        <Link href="/cards/resync" className="block w-full">
          <Button variant="secondary">Resync</Button>
        </Link>
        <Link href="/cards/revoke" className="block w-full">
          <Button variant="danger">Revoke card</Button>
        </Link>
        <ResetCardButton />
      </div>
    </>
  );
}

/**
 * Deletes the holder's card rows so they can link a card again from scratch.
 *
 * Their balance is not involved. It lives in the ledger against the person,
 * not against the card, so there is nothing here to strand.
 */
function ResetCardButton() {
  const { session } = useSession();
  const router = useRouter();
  const qc = useQueryClient();
  const [confirming, setConfirming] = useState(false);
  const [busy, setBusy] = useState(false);
  const [msg, setMsg] = useState<string | null>(null);
  const [error, setError] = useState<string | null>(null);

  async function run() {
    if (!session) return;
    setBusy(true);
    setError(null);
    try {
      await resetCards(session.jwt, setMsg);
      await qc.invalidateQueries({ queryKey: ["cards", "me"] });
      router.replace("/");
    } catch (e) {
      setError(e instanceof Error ? e.message : "Reset failed — try again.");
      setBusy(false);
    }
  }

  if (!confirming) {
    return (
      <Button variant="secondary" onClick={() => setConfirming(true)}>
        Reset card
      </Button>
    );
  }

  return (
    <div className="grid gap-2 rounded-2xl border border-dashed border-gray-200 p-3 dark:border-white/10">
      <p className="text-xs text-gray-500 dark:text-white/50">
        This returns your card balance to your wallet and removes the card so
        you can start over. You&apos;ll sign one transaction per card.
      </p>
      {error ? <p className="text-xs text-red-600 dark:text-red-400">{error}</p> : null}
      <div className="grid grid-cols-2 gap-2">
        <Button variant="secondary" onClick={() => setConfirming(false)} disabled={busy}>
          Cancel
        </Button>
        <Button variant="danger" onClick={run} loading={busy}>
          {busy ? (msg ?? "Working…") : "Confirm reset"}
        </Button>
      </div>
    </div>
  );
}

function StatusBadge({ status }: { status: CardSummary["status"] }) {
  if (status === "live") {
    return (
      <StatusChip tone="success" icon={<PiCheckCircleFill />}>
        Live
      </StatusChip>
    );
  }
  if (status === "revoked") {
    return (
      <StatusChip tone="error" icon={<PiWarningOctagonFill />}>
        Revoked
      </StatusChip>
    );
  }
  if (status === "locked") {
    return (
      <StatusChip tone="warning" icon={<PiWarningOctagonFill />}>
        Locked
      </StatusChip>
    );
  }
  return <StatusChip>{status.charAt(0).toUpperCase() + status.slice(1)}</StatusChip>;
}
