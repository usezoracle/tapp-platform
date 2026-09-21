"use client";

import { useEffect, useState } from "react";
import Link from "next/link";
import { useRouter } from "next/navigation";
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
import { cardsApi, ApiError } from "@/lib/api";
import { hexToBytes } from "@/lib/cardCrypto";
import {
  packCardPayload,
  readCardPayload,
  webNfcSupported,
  writeCardPayload,
} from "@/lib/webnfc";

export default function ResyncPage() {
  const router = useRouter();
  const { hydrated, session } = useSession();
  const [phase, setPhase] = useState<
    "ready" | "fetching" | "writing" | "done" | "error"
  >("ready");
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (hydrated && !session) router.replace("/sign-in?next=/cards/resync");
  }, [hydrated, session, router]);

  async function go() {
    if (!session) return;
    setError(null);
    setPhase("fetching");
    try {
      if (!webNfcSupported()) {
        throw new Error(
          "iOS Safari can't write to NFC cards. Borrow an Android phone, or contact support for remote recovery.",
        );
      }
      const payload = await cardsApi.resync(session.jwt);
      const tokenBytes = hexToBytes(payload.current_token_ct);
      setPhase("writing");
      const { payload: existing } = await readCardPayload();
      if (existing.length !== 64) {
        throw new Error("Card payload looks wrong — needs re-linking.");
      }
      const K = existing.slice(0, 32);
      const newPayload = packCardPayload(K, tokenBytes);
      await writeCardPayload(newPayload);
      await cardsApi.resyncComplete(payload.resync_nonce, session.jwt);
      setPhase("done");
    } catch (err) {
      const msg =
        err instanceof ApiError
          ? `${err.message}${err.code ? ` (${err.code})` : ""}`
          : err instanceof Error
            ? err.message
            : "Resync failed";
      setError(msg);
      setPhase("error");
    }
  }

  if (phase === "done") {
    return (
      <Screen centered>
        <div className="flex flex-col items-center gap-6 text-center">
          <StatusChip tone="success" icon={<PiCheckCircleFill />}>
            Card back in sync
          </StatusChip>
          <p className="max-w-xs text-sm text-gray-500 dark:text-white/50">
            Try tapping at a merchant again.
          </p>
          <Link href="/settings/card" className="w-full">
            <Button>Done</Button>
          </Link>
        </div>
      </Screen>
    );
  }

  return (
    <Screen centered>
      <AnimatedComponent
        variant={slideInOut}
        className="flex flex-col items-center gap-6 text-center"
      >
        <div className="space-y-3">
          <h1 className="text-xl font-medium text-neutral-900 dark:text-white">
            Resync your card
          </h1>
          <p className="max-w-xs text-sm text-gray-500 dark:text-white/50">
            Tap your card to your phone — we&apos;ll write the canonical token
            back. ~3 seconds.
          </p>
        </div>
        {error ? <InputError message={error} /> : null}
        {error && /no (Tapp|Freedom) payload|payload looks wrong/i.test(error) ? (
          <Link href="/cards/relink" className="w-full">
            <Button>Repair card instead</Button>
          </Link>
        ) : null}
        <Button
          onClick={go}
          loading={phase === "fetching" || phase === "writing"}
        >
          Start resync
        </Button>
        <Link
          href="/settings/card"
          className="text-sm text-gray-500 hover:text-neutral-900 dark:text-white/50 dark:hover:text-white"
        >
          Cancel
        </Link>
      </AnimatedComponent>
    </Screen>
  );
}
