"use client";

import { Suspense, useEffect, useRef, useState } from "react";
import Link from "next/link";
import { useRouter, useSearchParams } from "next/navigation";
import {
  PiShieldCheckFill,
  PiCreditCardBold,
  PiBroadcastBold,
  PiArrowRightBold,
  PiArrowLeftBold,
  PiDeviceMobileBold,
} from "react-icons/pi";
import { Screen } from "@/components/ui/Screen";
import { Button } from "@/components/ui/Button";
import { Icon } from "@/components/ui/Icon";
import { IconSuccessBadge } from "@/lib/icons";
import { useSession } from "@/lib/auth";
import { linkApi, ApiError } from "@/lib/api";
import {
  AnimatedComponent,
  fadeInOut,
  slideInOut,
} from "@/components/ui/AnimatedComponents";

export default function LinkPage() {
  return (
    <Suspense fallback={<Screen centered><div /></Screen>}>
      <LinkPageBody />
    </Suspense>
  );
}

function LinkPageBody() {
  const router = useRouter();
  const params = useSearchParams();
  const token = params.get("token");
  const { hydrated, session } = useSession();

  if (!token) return <NoTokenState />;
  if (!hydrated) return <LoadingState />;
  if (!session) return <SignInState token={token} />;
  return (
    <ClaimingState
      token={token}
      jwt={session.jwt}
      onDone={(sessionId, cardId) =>
        router.replace(`/link/configure?session=${sessionId}&card=${cardId}`)
      }
    />
  );
}

function NoTokenState() {
  const router = useRouter();
  const [manualInput, setManualInput] = useState("");
  const [inputError, setInputError] = useState<string | null>(null);
  const [nfcSupported, setNfcSupported] = useState(false);
  const [nfcScanning, setNfcScanning] = useState(false);
  const [nfcError, setNfcError] = useState<string | null>(null);

  useEffect(() => {
    if (typeof window !== "undefined" && "NDEFReader" in window) {
      setNfcSupported(true);
      void startNfcScan();
    }
  }, []);

  function parseActivationToken(raw: string): string | null {
    const trimmed = raw.trim();
    if (!trimmed) return null;
    try {
      if (trimmed.includes("token=")) {
        const url = new URL(trimmed.startsWith("http") ? trimmed : `https://${trimmed}`);
        const t = url.searchParams.get("token");
        if (t) return t.trim();
      }
      if (trimmed.includes("/c/")) {
        const parts = trimmed.split("/c/");
        if (parts[1]) return parts[1].split(/[?#/]/)[0].trim();
      }
    } catch {
      // not a standard URL, treat as raw token
    }
    if (/^[a-zA-Z0-9_-]{4,64}$/.test(trimmed)) {
      return trimmed;
    }
    return null;
  }

  async function startNfcScan() {
    try {
      setNfcScanning(true);
      setNfcError(null);
      const ndef = new (window as any).NDEFReader();
      await ndef.scan();
      ndef.onreading = (event: any) => {
        for (const record of event.message.records) {
          if (record.recordType === "url" || record.recordType === "text") {
            const textDecoder = new TextDecoder();
            const text = textDecoder.decode(record.data);
            const token = parseActivationToken(text);
            if (token) {
              router.push(`/link?token=${encodeURIComponent(token)}`);
              return;
            }
          }
        }
      };
      ndef.onreadingerror = () => {
        setNfcError("Could not read card data. Please hold card steady against the back.");
      };
    } catch (err: any) {
      setNfcScanning(false);
      if (err.name === "NotAllowedError") {
        setNfcError("NFC permission required. Please grant permission or enter code below.");
      }
    }
  }

  function handleManualSubmit(e: React.FormEvent) {
    e.preventDefault();
    setInputError(null);
    const token = parseActivationToken(manualInput);
    if (!token) {
      setInputError("Please enter a valid activation code or paste your full card link.");
      return;
    }
    router.push(`/link?token=${encodeURIComponent(token)}`);
  }

  return (
    <Screen centered>
      <AnimatedComponent
        variant={slideInOut}
        className="flex w-full max-w-sm flex-col items-center gap-6 text-center"
      >
        <div className="w-full flex items-center justify-start">
          <Link
            href="/"
            className="inline-flex items-center gap-1 text-xs font-medium text-gray-500 hover:text-neutral-900 dark:text-white/50 dark:hover:text-white"
          >
            <PiArrowLeftBold /> Back to wallet
          </Link>
        </div>

        {/* Animated NFC Wave Graphic */}
        <div className="relative flex items-center justify-center">
          <div className="absolute size-24 rounded-full bg-blue-600/10 animate-ping" />
          <div className="absolute size-20 rounded-full bg-blue-500/20 animate-pulse" />
          <div className="relative grid size-16 place-items-center rounded-2xl bg-blue-600 text-white shadow-lg shadow-blue-500/25 dark:bg-blue-500">
            <PiBroadcastBold className="size-8" />
          </div>
        </div>

        <div className="space-y-2">
          <h1 className="text-xl font-semibold text-neutral-900 dark:text-white">
            Activate your Tapp Card
          </h1>
          <p className="text-xs leading-relaxed text-gray-500 dark:text-white/60">
            Hold your physical Tapp Card against the back of your phone to pair it with your wallet.
          </p>
        </div>

        {/* Live NFC Status for supported browsers */}
        {nfcSupported && (
          <div className="w-full rounded-2xl border border-blue-100 bg-blue-50/70 p-3 text-xs text-blue-800 dark:border-blue-900/30 dark:bg-blue-950/30 dark:text-blue-300">
            <div className="flex items-center justify-center gap-2">
              <span className="size-2 rounded-full bg-blue-600 animate-pulse" />
              <span className="font-medium">
                {nfcScanning ? "NFC Scanner ready — Tap card now" : "Tap button below to start NFC reader"}
              </span>
            </div>
            {!nfcScanning && (
              <Button
                variant="primary"
                onClick={startNfcScan}
                size="sm"
                className="mt-2.5 w-full"
              >
                Scan with NFC
              </Button>
            )}
            {nfcError && <p className="mt-2 text-red-500 text-[11px]">{nfcError}</p>}
          </div>
        )}

        {/* iOS / General NFC tip */}
        {!nfcSupported && (
          <div className="w-full rounded-2xl border border-gray-200 bg-gray-50/70 p-3.5 text-left text-xs text-gray-600 dark:border-white/10 dark:bg-white/5 dark:text-white/70">
            <div className="flex items-start gap-2.5">
              <PiDeviceMobileBold className="size-5 shrink-0 text-blue-600 dark:text-blue-400 mt-0.5" />
              <p className="leading-snug">
                On iPhone, tap your card near the top edge. A notification banner will appear asking to open your activation link.
              </p>
            </div>
          </div>
        )}

        {/* Manual Code / Link Input */}
        <form onSubmit={handleManualSubmit} className="w-full space-y-3 pt-2">
          <div className="flex items-center gap-2 text-xs text-gray-400 dark:text-white/40">
            <div className="h-px flex-1 bg-gray-200 dark:bg-white/10" />
            <span>OR ENTER CODE</span>
            <div className="h-px flex-1 bg-gray-200 dark:bg-white/10" />
          </div>

          <div className="space-y-1.5 text-left">
            <input
              type="text"
              value={manualInput}
              onChange={(e) => {
                setManualInput(e.target.value);
                setInputError(null);
              }}
              placeholder="Paste activation link or code"
              className="w-full rounded-xl border border-gray-300 bg-white px-3.5 py-2.5 text-xs text-neutral-900 placeholder:text-gray-400 focus:border-blue-500 focus:outline-none dark:border-white/15 dark:bg-neutral-900 dark:text-white dark:placeholder:text-white/30"
            />
            {inputError && (
              <p className="text-[11px] text-red-500">{inputError}</p>
            )}
          </div>

          <Button type="submit" variant="secondary" className="w-full py-2.5 text-xs font-semibold">
            Continue to activation
          </Button>
        </form>
      </AnimatedComponent>
    </Screen>
  );
}

function LoadingState() {
  return (
    <Screen centered>
      <div className="loader" />
    </Screen>
  );
}

function SignInState({ token }: { token: string }) {
  const nextHref = `/link?token=${encodeURIComponent(token)}`;
  return (
    <Screen centered>
      <div className="flex flex-col items-center gap-8 text-center">
        <div className="space-y-3">
          <h1 className="text-xl font-medium text-neutral-900 dark:text-white">
            You tapped a new card
          </h1>
          <p className="text-sm text-gray-500 dark:text-white/50">
            Sign in to claim it as yours.
          </p>
        </div>
        <a href={`/sign-in?next=${encodeURIComponent(nextHref)}`} className="w-full max-w-xs">
          <Button>Sign in to claim card</Button>
        </a>
        <p className="inline-flex items-center gap-1 text-xs text-gray-500 dark:text-white/50">
          <PiShieldCheckFill className="text-blue-500" />
          This link works once. No one else can claim it after you.
        </p>
      </div>
    </Screen>
  );
}

function ClaimingState({
  token,
  jwt,
  onDone,
}: {
  token: string;
  jwt: string;
  onDone: (sessionId: string, cardId: string) => void;
}) {
  const [status, setStatus] = useState<"claiming" | "error" | "already-yours">(
    "claiming",
  );
  const [error, setError] = useState<string | null>(null);

  // onDone is read through a ref so it can stay OUT of the effect's
  // dependencies.
  //
  // The parent passes it as an inline arrow, so it has a new identity on every
  // render -- and the session context hands out a new value whenever a token
  // refresh calls notifySessionChange(), because readSession() JSON.parses a
  // fresh object each time. With onDone in the dependency list, any of that
  // tore this effect down mid-flight: the cleanup set cancelled, the claim
  // that was already succeeding on the server had its response thrown away,
  // and a replacement request went out. The card ended up claimed while this
  // screen span on "Claiming your card…" forever.
  const onDoneRef = useRef(onDone);
  useEffect(() => {
    onDoneRef.current = onDone;
  });

  useEffect(() => {
    let cancelled = false;
    async function go() {
      try {
        const session = await linkApi.start(token, jwt);
        if (cancelled) return;
        onDoneRef.current(session.id, session.cardId);
      } catch (err) {
        if (cancelled) return;
        if (
          err instanceof ApiError &&
          (err.code === "card_already_claimed_by_you" ||
            err.code === "card_already_live")
        ) {
          // Same card re-tapped, or a new card while they already have a live
          // one — either way, send them to the card they have.
          setStatus("already-yours");
          return;
        }
        setStatus("error");
        setError(err instanceof Error ? err.message : "Could not claim this card");
      }
    }
    void go();
    return () => {
      cancelled = true;
    };
    // Deliberately keyed on the card and the identity claiming it, nothing
    // else. Claiming is a one-shot side effect; re-running it on an unrelated
    // re-render is what broke it.
  }, [token, jwt]);

  if (status === "claiming") {
    return (
      <Screen centered>
        <div className="flex flex-col items-center gap-6 text-center">
          <div className="loader" />
          <p className="text-sm text-gray-500 dark:text-white/50">
            Claiming your card…
          </p>
        </div>
      </Screen>
    );
  }

  if (status === "already-yours") {
    return (
      <Screen centered>
        <div className="flex flex-col items-center gap-6 text-center">
          <Icon xml={IconSuccessBadge} size={84} />
          <p className="text-sm text-neutral-900 dark:text-white">
            This card is already linked to your account.
          </p>
          <a href="/" className="w-full">
            <Button variant="secondary">Go to wallet</Button>
          </a>
        </div>
      </Screen>
    );
  }

  return (
    <Screen centered>
      <div className="flex flex-col items-center gap-6 text-center">
        <h1 className="text-xl font-medium text-neutral-900 dark:text-white">
          Couldn&apos;t claim this card
        </h1>
        <p className="text-sm text-gray-500 dark:text-white/50">{error}</p>
        <a href="/" className="w-full">
          <Button variant="secondary">Go to wallet</Button>
        </a>
      </div>
    </Screen>
  );
}
