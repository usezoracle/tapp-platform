"use client";

import { Suspense, useEffect, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { PiCheckCircleFill } from "react-icons/pi";
import { Screen } from "@/components/ui/Screen";
import { Button } from "@/components/ui/Button";
import { InputError } from "@/components/ui/InputError";
import { StatusChip } from "@/components/ui/StatusChip";
import {
  AnimatedComponent,
  slideInOut,
} from "@/components/ui/AnimatedComponents";
import { useSession } from "@/lib/auth";
import { useLinkStore } from "@/lib/cardLinkStore";
import { bytesToHex } from "@/lib/cardCrypto";
import { linkApi, ApiError } from "@/lib/api";

export default function LinkSignPage() {
  return (
    <Suspense fallback={<Screen centered />}>
      <Body />
    </Suspense>
  );
}

function Body() {
  const router = useRouter();
  const params = useSearchParams();
  const cardId = params.get("card");
  const sessionId = params.get("session");
  const { hydrated, session } = useSession();
  const link = useLinkStore();

  const [phase, setPhase] = useState<
    "ready" | "signing" | "submitting" | "done" | "error"
  >("ready");
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (!cardId) router.replace("/");
    if (hydrated && !session) {
      router.replace(`/sign-in?next=/link/sign?card=${cardId ?? ""}`);
    }
    if (
      !link.K ||
      !link.linkingProof ||
      !link.pinVerifier ||
      !link.cardPassword ||
      !link.rotationToken ||
      !link.cardUidHash
    ) {
      router.replace(`/link/configure?card=${cardId ?? ""}`);
    }
  }, [cardId, hydrated, session, link, router]);

  async function go() {
    if (!session) return;
    setError(null);
    setPhase("signing");

    try {
      // There is no on-chain step any more. Funds are ledger-side, so this
      // screen no longer fabricates a cap object id and a transaction digest
      // for a transaction that never happened -- it confirms to the server
      // what was actually read back off the chip.
      //
      // Activation is idempotent: a session already activated returns the same
      // answer rather than repeating anything, so a retry here is free.
      if (!sessionId) throw new Error("This setup session has been lost.");
      if (!link.cardUidHash || !link.rotationToken) {
        throw new Error("Write the card before finishing setup.");
      }

      setPhase("submitting");
      await linkApi.activate(
        sessionId,
        {
          card_uid_hash: bytesToHex(link.cardUidHash),
          read_back: bytesToHex(link.rotationToken),
        },
        session.jwt,
      );

      link.reset();
      setPhase("done");
      router.replace("/settings/card");
    } catch (err) {
      const msg =
        err instanceof ApiError
          ? `${err.message}${err.code ? ` (${err.code})` : ""}`
          : err instanceof Error
            ? err.message
            : "Linking failed";
      setError(msg);
      setPhase("error");
    }
  }

  return (
    <Screen centered>
      <AnimatedComponent
        variant={slideInOut}
        className="flex flex-col items-center gap-8 text-center"
      >
        {phase === "ready" ? (
          <>
            <div className="space-y-3">
              <h1 className="text-xl font-medium text-neutral-900 dark:text-white">
                Confirm &amp; activate your card
              </h1>
              <p className="max-w-xs text-sm text-gray-500 dark:text-white/50">
                Step 4 of 4 — Link your card to your wallet and activate tap payments.
              </p>
            </div>

            <Button onClick={go} disabled={!session}>Confirm &amp; finish</Button>
          </>
        ) : phase === "signing" ? (
          <>
            <div className="loader" />
            <p className="text-sm text-gray-500 dark:text-white/50">
              Activating your card…
            </p>
          </>
        ) : phase === "submitting" ? (
          <>
            <div className="loader" />
            <p className="text-sm text-gray-500 dark:text-white/50">
              Finalizing with Freedom…
            </p>
          </>
        ) : phase === "done" ? (
          <StatusChip tone="success" icon={<PiCheckCircleFill />}>
            Done — taking you to your card
          </StatusChip>
        ) : (
          <>
            <InputError message={error ?? "Linking failed"} />
            <Button onClick={go} variant="secondary">
              Try again
            </Button>
          </>
        )}
      </AnimatedComponent>
    </Screen>
  );
}
