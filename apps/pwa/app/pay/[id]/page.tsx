"use client";

import { use, useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import Link from "next/link";
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import {
  PiCheckCircleFill,
  PiStorefrontBold,
  PiWarningOctagonFill,
} from "react-icons/pi";
import { Screen } from "@/components/ui/Screen";
import { Button } from "@/components/ui/Button";
import { InfoBanner } from "@/components/ui/InfoBanner";
import { Surface } from "@/components/ui/Surface";
import { Amount } from "@/components/ui/Amount";
import {
  AnimatedComponent,
  slideInOut,
} from "@/components/ui/AnimatedComponents";
import { useSession } from "@/lib/auth";
import { useBalances, balanceIn } from "@/lib/ledger";
import { checkoutApi, ApiError, type Checkout } from "@/lib/api";

export default function PayCheckoutPage({
  params,
}: {
  params: Promise<{ id: string }>;
}) {
  const { id } = use(params);
  const router = useRouter();
  const { hydrated, session } = useSession();
  const qc = useQueryClient();
  const [paid, setPaid] = useState<Checkout | null>(null);

  // Readable without signing in, on purpose. Somebody handed a phone across a
  // counter needs to see what they are being asked for before deciding whether
  // this is a thing they want an account for.
  const checkout = useQuery({
    queryKey: ["checkout", id],
    queryFn: () => checkoutApi.get(id),
    // An open request expires on a clock, so it is worth re-reading; a settled
    // one never changes again.
    refetchInterval: (query) =>
      query.state.data?.state === "open" ? 5_000 : false,
    retry: false,
  });

  const balances = useBalances();
  const funds = balanceIn(balances.data, checkout.data?.currency ?? "NGN");

  const pay = useMutation({
    mutationFn: () => checkoutApi.pay(id, session!.jwt),
    onSuccess: (done) => {
      setPaid(done);
      void qc.invalidateQueries({ queryKey: ["ledger"] });
    },
  });

  if (checkout.isLoading) {
    return (
      <Screen centered>
        <div className="loader mx-auto" />
      </Screen>
    );
  }

  if (checkout.error || !checkout.data) {
    return (
      <Screen centered>
        <AnimatedComponent variant={slideInOut} className="grid gap-6 text-center">
          <PiWarningOctagonFill className="mx-auto text-4xl text-[var(--caution)]" />
          <div className="grid gap-1">
            <p className="text-lg font-medium text-[var(--fg)]">
              This request isn&apos;t here
            </p>
            <p className="text-sm leading-relaxed text-[var(--fg-muted)]">
              It may have expired, or the link may be wrong. Ask them to show
              it again.
            </p>
          </div>
          <Link href="/wallet">
            <Button variant="secondary">Go to my wallet</Button>
          </Link>
        </AnimatedComponent>
      </Screen>
    );
  }

  const c = paid ?? checkout.data;
  const merchant = c.merchant_name || "this merchant";

  if (c.state === "paid") {
    return (
      <Screen centered>
        <AnimatedComponent variant={slideInOut} className="grid justify-items-center gap-6 text-center">
          <PiCheckCircleFill className="text-5xl text-[var(--positive)]" />
          <div className="grid gap-1">
            <Amount value={c.amount} size="hero" />
            <p className="text-sm text-[var(--fg-muted)]">paid to {merchant}</p>
          </div>
          <Link href="/wallet" className="w-full">
            <Button>Done</Button>
          </Link>
        </AnimatedComponent>
      </Screen>
    );
  }

  if (c.state !== "open") {
    return (
      <Screen centered>
        <AnimatedComponent variant={slideInOut} className="grid gap-6 text-center">
          <PiWarningOctagonFill className="mx-auto text-4xl text-[var(--caution)]" />
          <div className="grid gap-1">
            <p className="text-lg font-medium text-[var(--fg)]">
              {c.state === "expired" ? "This request expired" : "This request was withdrawn"}
            </p>
            <p className="text-sm leading-relaxed text-[var(--fg-muted)]">
              Nothing was taken. Ask {merchant} to send a new one.
            </p>
          </div>
          <Link href="/wallet">
            <Button variant="secondary">Go to my wallet</Button>
          </Link>
        </AnimatedComponent>
      </Screen>
    );
  }

  const short = !!funds && funds.available.minor < c.amount.minor;

  return (
    <Screen centered>
      <AnimatedComponent variant={slideInOut} className="grid gap-7">
        <div className="grid justify-items-center gap-3 text-center">
          <span className="grid h-12 w-12 place-items-center rounded-full bg-[var(--sunken)] text-xl text-[var(--fg-muted)]">
            <PiStorefrontBold />
          </span>
          <p className="text-sm text-[var(--fg-muted)]">{merchant} is asking for</p>
          <Amount value={c.amount} size="hero" />
          {c.narration ? (
            <p className="max-w-[26ch] text-sm text-[var(--fg-muted)]">{c.narration}</p>
          ) : null}
        </div>

        {!hydrated ? (
          <div className="loader mx-auto" />
        ) : !session ? (
          <div className="grid gap-2">
            <Button onClick={() => router.push(`/sign-in?next=/pay/${id}`)}>
              Sign in to pay
            </Button>
            <p className="text-center text-xs leading-relaxed text-[var(--fg-muted)]">
              You&apos;re paying from your Freedom balance. Nothing leaves it until
              you confirm.
            </p>
          </div>
        ) : (
          <div className="grid gap-3">
            <Surface kind="sunken" padding="md" radius="2xl" className="flex items-baseline justify-between gap-3">
              <span className="text-sm text-[var(--fg-muted)]">Your balance</span>
              <Amount value={funds?.available} size="md" />
            </Surface>

            {short ? (
              <InfoBanner tone="warning">
                <p className="font-medium text-[var(--fg)]">Not enough to cover this</p>
                <p className="mt-1 text-xs leading-relaxed">
                  Add cash through an agent, or convert from another currency,
                  then come back to this link.
                </p>
                <div className="mt-3 flex gap-2">
                  <Link href="/cash">
                    <Button variant="secondary" size="sm" fullWidth={false}>
                      Add cash
                    </Button>
                  </Link>
                  <Link href="/convert">
                    <Button variant="secondary" size="sm" fullWidth={false}>
                      Convert
                    </Button>
                  </Link>
                </div>
              </InfoBanner>
            ) : null}

            {pay.error ? <PayError error={pay.error} /> : null}

            <Button
              onClick={() => pay.mutate()}
              loading={pay.isPending}
              disabled={short}
            >
              Pay {c.amount_display}
            </Button>

            <Link href="/wallet">
              <Button variant="ghost">Not now</Button>
            </Link>
          </div>
        )}
      </AnimatedComponent>
    </Screen>
  );
}

function PayError({ error }: { error: unknown }) {
  const code = error instanceof ApiError ? error.code : undefined;
  const guidance: Record<string, string> = {
    not_open: "It was paid or withdrawn while this page was open.",
    own_request: "This is your own request — somebody else needs to pay it.",
    insufficient_funds: "There is not enough in your balance any more.",
  };
  return (
    <InfoBanner tone="warning" icon={<PiWarningOctagonFill className="text-amber-500" />}>
      <p className="font-medium text-[var(--fg)]">
        {error instanceof Error ? error.message : "That didn't go through"}
      </p>
      {code && guidance[code] ? (
        <p className="mt-1 text-xs leading-relaxed">{guidance[code]}</p>
      ) : null}
      <p className="mt-1 text-xs">Nothing has been taken from your balance.</p>
    </InfoBanner>
  );
}
