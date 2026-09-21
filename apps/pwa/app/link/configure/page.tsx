"use client";

import { Suspense, useEffect, useState } from "react";
import { useRouter, useSearchParams } from "next/navigation";
import { Screen } from "@/components/ui/Screen";
import { Button } from "@/components/ui/Button";
import { InputError } from "@/components/ui/InputError";
import { PinInput } from "@/components/ui/PinInput";
import {
  AnimatedComponent,
  slideInOut,
} from "@/components/ui/AnimatedComponents";
import { useSession } from "@/lib/auth";
import { cardsApi, kycApi } from "@/lib/api";
import { useLinkStore } from "@/lib/cardLinkStore";
import { formatNgn } from "@/lib/utils";

export default function LinkConfigurePage() {
  return (
    <Suspense fallback={<Screen centered />}>
      <Body />
    </Suspense>
  );
}

// Opening positions only. Every one of them is clamped to what the holder's
// verification actually allows once the tier loads -- the daily default used
// to be 40,000 against an unverified ceiling of 20,000, so an untouched form
// was rejected by the server, and the rejection arrived as "Something went
// wrong setting up your card".
const DEFAULTS = {
  dailyNGN:    40_000,
  perTapNGN:   2_000,
  stepUpNGN:   15_000,
};

// Shown until the real ceiling arrives. Deliberately the lowest tier's daily
// limit rather than the highest: if the tier never loads, offering more than
// somebody can have produces a rejection, while offering less produces a
// working card with a modest limit they can raise.
const FALLBACK_DAILY_MAX_NGN = 20_000;

function Body() {
  const router = useRouter();
  const params = useSearchParams();
  const cardId = params.get("card");
  const sessionId = params.get("session");
  const { hydrated, session } = useSession();
  const setLinkSession = useLinkStore((s) => s.setSession);
  const setLimits = useLinkStore((s) => s.setLimits);

  // Clamped at the first render, not just once the tier arrives. A range
  // input whose value exceeds its max pins the thumb visually but leaves the
  // state untouched, so an unclamped default would sit at 40,000 behind a
  // slider that appears to read 20,000 -- and submit the number nobody saw.
  const [daily, setDaily] = useState(
    Math.min(DEFAULTS.dailyNGN, FALLBACK_DAILY_MAX_NGN),
  );
  const [perTap, setPerTap] = useState(
    Math.min(DEFAULTS.perTapNGN, FALLBACK_DAILY_MAX_NGN),
  );
  const [stepUp, setStepUp] = useState(
    Math.min(DEFAULTS.stepUpNGN, FALLBACK_DAILY_MAX_NGN),
  );
  const [pin, setPin] = useState("");
  const [pinConfirm, setPinConfirm] = useState("");
  const [error, setError] = useState<string | null>(null);
  const [dailyMaxNGN, setDailyMaxNGN] = useState(FALLBACK_DAILY_MAX_NGN);
  const [tierName, setTierName] = useState<string | null>(null);
  const [canVerifyFurther, setCanVerifyFurther] = useState(false);

  useEffect(() => {
    if (!cardId || !sessionId) router.replace("/");
    if (hydrated && !session)
      router.replace(
        `/sign-in?next=/link/configure?session=${sessionId ?? ""}&card=${cardId ?? ""}`,
      );
    if (cardId && sessionId) setLinkSession(sessionId, cardId);
  }, [cardId, sessionId, hydrated, session, router, setLinkSession]);

  // One card per user: if the holder already has a live card, the link flow
  // is a dead end — bounce them to their card instead of re-showing "set
  // limits".
  //
  // The condition used to also require cap_object_id, which was the on-chain
  // spending cap. There is no on-chain cap any more, so that field is always
  // absent and the guard would never have fired: somebody with a working card
  // would have been walked through setup a second time.
  useEffect(() => {
    if (!session) return;
    let cancelled = false;
    cardsApi
      .me(session.jwt)
      .then((c) => {
        if (!cancelled && c.status === "live") {
          router.replace("/settings/card");
        }
      })
      .catch(() => {});
    return () => {
      cancelled = true;
    };
  }, [session, router]);

  // The ceiling comes from the server, not from a table in here.
  //
  // The limits a tier allows are a policy decision that changes without this
  // bundle being rebuilt, and a client that keeps its own copy will one day
  // offer a number the server refuses -- which is exactly the failure this
  // replaces. GET /v1/kyc already returns the daily limit for the holder's
  // tier, so ask.
  useEffect(() => {
    if (!session) return;
    let cancelled = false;
    kycApi
      .status(session.jwt)
      .then((k) => {
        if (cancelled) return;
        const maxNGN = Math.floor((k.limits?.daily?.minor ?? 0) / 100);
        if (maxNGN > 0) {
          setDailyMaxNGN(maxNGN);
          // Pull the chosen values under the ceiling, preserving the ordering
          // the server also enforces: per-tap <= step-up <= daily.
          setDaily((d) => Math.min(d, maxNGN));
          setStepUp((s) => Math.min(s, maxNGN));
          setPerTap((p) => Math.min(p, maxNGN));
        }
        setTierName(k.tier_name ?? null);
        setCanVerifyFurther(Boolean(k.next));
      })
      .catch(() => {
        // Leave the conservative fallback in place. A card set up with a low
        // limit is recoverable; one the server rejects is a dead end.
      });
    return () => {
      cancelled = true;
    };
  }, [session]);

  function submit() {
    if (pin.length !== 4 || !/^\d{4}$/.test(pin)) {
      setError("PIN must be 4 digits.");
      return;
    }
    if (pin !== pinConfirm) {
      setError("PINs don't match.");
      return;
    }
    if (perTap > stepUp || stepUp > daily) {
      setError("Limits must satisfy: per-tap ≤ step-up ≤ daily.");
      return;
    }
    if (daily > dailyMaxNGN) {
      setError(
        `Your daily limit can be at most ${formatNgn(dailyMaxNGN)} at your current verification level.`,
      );
      return;
    }
    setError(null);
    setLimits({
      daily:   daily * 100,
      perTap:  perTap * 100,
      stepUp:  stepUp * 100,
      pin,
    });
    router.push(`/link/write?session=${sessionId}&card=${cardId}`);
  }

  return (
    <Screen>
      <AnimatedComponent variant={slideInOut} className="grid gap-6 py-10 text-sm text-neutral-900 dark:text-white">
        <div className="space-y-2">
          <h1 className="text-xl font-medium">Set your limits</h1>
          <p className="text-sm text-gray-500 dark:text-white/50">
            Step 2 of 4 — choose how much your card can spend.
          </p>
        </div>

        <div className="grid divide-y divide-dashed divide-gray-200 rounded-3xl border border-gray-200 px-4 transition-all dark:divide-white/10 dark:border-white/10">
          <RangeField
            label="Daily limit"
            help={
              tierName
                ? `Max total spend per UTC day — up to ${formatNgn(dailyMaxNGN)} on ${tierName}`
                : "Max total spend per UTC day"
            }
            value={daily}
            onChange={setDaily}
            min={Math.min(5_000, dailyMaxNGN)}
            max={dailyMaxNGN}
            step={5_000}
            display={formatNgn(daily)}
          />
          {/* Each slider is capped by the one above it, so the ordering the
              server enforces -- per-tap <= step-up <= daily -- cannot be
              violated by dragging. The step-up ceiling used to be a fixed
              50,000, which on an unverified account is more than twice the
              whole daily allowance. */}
          <RangeField
            label="Per-tap limit"
            help="Taps below this need no PIN"
            value={perTap}
            onChange={setPerTap}
            min={500}
            max={Math.max(500, Math.min(5_000, stepUp))}
            step={500}
            display={formatNgn(perTap)}
          />
          <RangeField
            label="Step-up threshold"
            help="Above this needs a biometric on your phone"
            value={stepUp}
            onChange={setStepUp}
            min={Math.min(5_000, daily)}
            max={Math.min(50_000, daily)}
            step={1_000}
            display={formatNgn(stepUp)}
          />
        </div>

        {canVerifyFurther && (
          <p className="text-xs text-gray-500 dark:text-white/40">
            Verify your identity in Settings to raise these limits.
          </p>
        )}

        <div className="grid gap-4 rounded-3xl border border-gray-200 p-4 dark:border-white/10">
          <PinInput label="Choose a 4-digit PIN" value={pin} onChange={setPin} />
          <PinInput label="Confirm PIN" value={pinConfirm} onChange={setPinConfirm} />
        </div>

        {error ? <InputError message={error} /> : null}

        <Button onClick={submit}>
          Continue — tap your card
        </Button>
      </AnimatedComponent>
    </Screen>
  );
}

function RangeField({
  label,
  help,
  value,
  onChange,
  min,
  max,
  step,
  display,
}: {
  label: string;
  help: string;
  value: number;
  onChange: (n: number) => void;
  min: number;
  max: number;
  step: number;
  display: string;
}) {
  return (
    <div className="grid gap-2 py-4">
      <div className="flex items-baseline justify-between">
        <label className="text-sm font-medium text-neutral-900 dark:text-white">
          {label}
        </label>
        <span className="rounded-full bg-gray-50 px-2 py-1 text-xs font-medium tabular-nums text-neutral-900 dark:bg-white/5 dark:text-white/80">
          {display}
        </span>
      </div>
      <input
        type="range"
        min={min}
        max={max}
        step={step}
        value={value}
        onChange={(e) => onChange(Number(e.target.value))}
        className="w-full accent-blue-600"
      />
      <p className="text-xs text-gray-400 dark:text-white/40">{help}</p>
    </div>
  );
}
