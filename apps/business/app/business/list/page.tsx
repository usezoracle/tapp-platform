"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import { useEffect, useRef, useState, type MouseEvent } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useFieldArray, useForm, type FieldPath } from "react-hook-form";
import { zodResolver } from "@hookform/resolvers/zod";
import { RequireSession } from "@/components/RequireSession";
import { PageHeader } from "@/components/ui/PageHeader";
import { Stepper, Check } from "@/components/ui/Stepper";
import { Button } from "@/components/ui/Button";
import { TextField, SelectField } from "@/components/ui/Field";
import { Declaration } from "@/components/ui/Declaration";
import { KeyValueList, Stat } from "@/components/ui/KeyValue";
import { Notice } from "@/components/ui/Notice";
import { StatusChip, type ChipTone } from "@/components/ui/StatusChip";
import { labelClasses, hintClasses, errorClasses, linkClasses, secondaryBtnClasses } from "@/components/ui/Styles";
import { Findings } from "@/components/business/Findings";
import { ApiError, createBusiness, type Business, type Me } from "@/lib/api";
import { businessKey, useBusiness, useMe } from "@/lib/queries";
import { MCC_OPTIONS, mccLabel } from "@/lib/mcc";
import { formatNaira, formatWholeShares, valuationFromText } from "@/lib/units";
import { maskRef } from "@/lib/utils";
import {
  STEPS,
  STEP_FIELDS,
  SYMBOL_RE,
  clearDraft,
  emptyListing,
  listingSchema,
  readDraft,
  serverFieldToForm,
  stepOfField,
  suggestSymbol,
  toRequest,
  writeDraft,
  type ListingInput,
  type ListingOutput,
} from "@/lib/listing-form";

export default function ListRoute() {
  return <RequireSession>{(session) => <ListGate token={session.jwt} />}</RequireSession>;
}

/** The form needs the merchant's own user id for the founders' row, so wait for /v1/me. */
function ListGate({ token }: { token: string }) {
  const me = useMe(token);
  const business = useBusiness(token);
  if (me.isPending || business.isPending) {
    return (
      <div className="grid gap-4" aria-busy>
        <div className="skeleton h-7 w-48" />
        <div className="skeleton h-64 w-full" />
      </div>
    );
  }
  if (me.isError) return <Notice tone="error">{(me.error as Error).message}</Notice>;
  return <ListingEntry token={token} me={me.data} initial={business.data} />;
}

/**
 * Decides once, from what was stored when the page opened, whether there is
 * anything to (re)submit. A successful submission updates the query cache,
 * and that must not swap the result screen for "already listed".
 */
function ListingEntry({ token, me, initial }: { token: string; me: Me; initial: Business | null | undefined }) {
  const [previous] = useState(initial);
  if (previous && previous.state !== "rejected") {
    return (
      <div className="grid gap-4">
        <PageHeader title="List your business" />
        <Notice>
          {previous.symbol} is already {previous.state}.{" "}
          <Link href="/business" className={linkClasses}>
            See your business
          </Link>
        </Notice>
      </div>
    );
  }
  return <ListingForm token={token} me={me} previous={previous} />;
}

const stateChip: Record<string, { tone: ChipTone; label: string }> = {
  listed: { tone: "success", label: "Listed" },
  rejected: { tone: "error", label: "Rejected" },
  submitted: { tone: "pending", label: "Submitted" },
};

function ListingForm({ token, me, previous }: { token: string; me: Me; previous: Business | null | undefined }) {
  const router = useRouter();
  const queryClient = useQueryClient();

  // The draft is read once, on mount. This component only renders on the
  // client after the session has hydrated, so localStorage is available.
  const [draft] = useState(() => readDraft());
  const [step, setStep] = useState(draft?.step ?? 0);
  const [failure, setFailure] = useState<string | null>(null);
  const [result, setResult] = useState<Business | null>(null);

  const form = useForm<ListingInput, unknown, ListingOutput>({
    resolver: zodResolver(listingSchema),
    mode: "onTouched",
    defaultValues: draft?.values ?? seedFrom(previous, me),
  });
  const { register, control, watch, setValue, setError, trigger, getValues, handleSubmit, formState } = form;
  const errors = formState.errors;

  const founders = useFieldArray({ control, name: "founders" });

  // Persist every change, debounced, so a reload lands where the merchant left off.
  useEffect(() => {
    let t: ReturnType<typeof setTimeout> | undefined;
    const sub = watch((values) => {
      clearTimeout(t);
      t = setTimeout(() => writeDraft({ values: values as ListingInput, step }), 250);
    });
    return () => {
      clearTimeout(t);
      sub.unsubscribe();
    };
  }, [watch, step]);
  useEffect(() => {
    writeDraft({ values: getValues(), step });
  }, [step, getValues]);

  // Suggest a symbol from the trading name until the merchant types their own.
  const tradingName = watch("trading_name");
  const symbol = watch("symbol");
  const lastSuggestion = useRef<string>(suggestSymbol(draft?.values.trading_name ?? previous?.trading_name ?? ""));
  useEffect(() => {
    const next = suggestSymbol(tradingName ?? "");
    const current = getValues("symbol");
    if (current === "" || current === lastSuggestion.current) {
      setValue("symbol", next, { shouldValidate: current !== "" });
      lastSuggestion.current = next;
    }
  }, [tradingName, getValues, setValue]);

  const mcc = watch("mcc");
  // Shares in issue × reference price, exact on the integer values, shown as the merchant types.
  const sharesInIssue = watch("shares_in_issue");
  const referencePrice = watch("reference_price");
  const valuation = valuationFromText(sharesInIssue ?? "", referencePrice ?? "");
  // A whole-list problem (the total exceeds treasury) lands on `founders`
  // itself, alongside the per-row errors.
  const foundersError = errors.founders?.root?.message ?? (errors.founders as { message?: string } | undefined)?.message;

  // preventDefault matters: `trigger` resolves in a microtask, which runs
  // before the browser's activation behaviour for the click. By then React
  // has re-rendered this button as the submit button, and without it the
  // click would submit the form from the step before the review.
  async function next(e: MouseEvent<HTMLButtonElement>) {
    e.preventDefault();
    setFailure(null);
    const ok = await trigger(STEP_FIELDS[step] as FieldPath<ListingInput>[], { shouldFocus: true });
    if (ok) setStep((s) => Math.min(s + 1, STEPS.length - 1));
  }

  const submit = handleSubmit(
    async (values) => {
      setFailure(null);
      try {
        const business = await createBusiness(token, toRequest(values));
        clearDraft();
        queryClient.setQueryData(businessKey(token), business);
        queryClient.invalidateQueries({ queryKey: ["holders", token] });
        setResult(business);
      } catch (e) {
        if (e instanceof ApiError && e.fieldProblems) {
          let earliest = STEPS.length - 1;
          for (const [field, problem] of Object.entries(e.fieldProblems)) {
            const formField = serverFieldToForm(field);
            if (!formField) continue;
            setError(formField as FieldPath<ListingInput>, { type: "server", message: problem });
            earliest = Math.min(earliest, stepOfField(formField));
          }
          setStep(earliest);
          setFailure("The API found problems with some fields. They are marked below.");
        } else {
          setFailure(e instanceof Error ? e.message : "Submission failed");
        }
      }
    },
    (errs) => {
      // Something on an earlier step is invalid: go there.
      const first = Object.keys(errs)[0];
      if (first) setStep(stepOfField(first));
    },
  );

  if (result) {
    const chip = stateChip[result.state] ?? { tone: "neutral" as ChipTone, label: result.state };
    return (
      <div className="grid gap-6">
        <PageHeader
          eyebrow={result.symbol}
          title={result.state === "listed" ? "Listed" : result.state === "rejected" ? "Not admitted" : "Submitted"}
          subtitle={
            result.state === "listed"
              ? "Your business is on Freedom Exchange. Taps at your till now buy your customers shares."
              : result.state === "rejected"
                ? "The market checked the submission and did not admit it. What it found is below."
                : "The market has the submission and will decide."
          }
          trailing={<StatusChip tone={chip.tone}>{chip.label}</StatusChip>}
        />
        <div className="panel">
          <Findings findings={result.findings} />
        </div>
        <div className="flex flex-wrap gap-3">
          <Button onClick={() => router.push("/business")}>Go to your business</Button>
          {result.state === "rejected" ? (
            <Button variant="secondary" onClick={() => { setResult(null); setStep(0); }}>
              Edit and resubmit
            </Button>
          ) : null}
        </div>
      </div>
    );
  }

  const v = getValues();

  return (
    <form onSubmit={submit} noValidate className="grid gap-6">
      <PageHeader
        title="List your business"
        subtitle={previous?.state === "rejected" ? `Resubmitting ${previous.symbol}. The previous findings are on your business page.` : "Four steps. Your answers are saved on this device as you go."}
      />
      <Stepper steps={[...STEPS]} current={step} onSelect={setStep} />

      {failure ? <Notice tone="error">{failure}</Notice> : null}

      {step === 0 ? (
        <section className="grid gap-4">
          <TextField id="legal_name" label="Legal name" hint="As registered with the CAC." error={errors.legal_name?.message} {...register("legal_name")} />
          <TextField id="trading_name" label="Trading name" hint="What customers call the shop." error={errors.trading_name?.message} {...register("trading_name")} />
          <div className="grid gap-4 sm:grid-cols-2">
            <TextField
              id="rc_number"
              label="RC or BN number"
              placeholder="RC1483920"
              hint="Your CAC registration number."
              error={errors.rc_number?.message}
              {...register("rc_number", { setValueAs: (s: string) => s.toUpperCase().trim() })}
            />
            <TextField
              id="symbol"
              label="Ticker symbol"
              placeholder="MAMAPUT"
              hint="3 to 12 characters, letters and digits, starting with a letter."
              error={errors.symbol?.message}
              trailing={SYMBOL_RE.test(symbol ?? "") ? <Check className="size-3.5 text-ok-dot" /> : undefined}
              {...register("symbol", { setValueAs: (s: string) => s.toUpperCase().trim() })}
            />
          </div>
          <div className="grid gap-4 sm:grid-cols-2">
            <SelectField id="mcc" label="Merchant category" error={errors.mcc?.message} {...register("mcc")}>
              <option value="">Choose one</option>
              {MCC_OPTIONS.map((o) => (
                <option key={o.code} value={o.code}>
                  {o.code} · {o.label}
                </option>
              ))}
              <option value="other">Other (enter a code)</option>
            </SelectField>
            {mcc === "other" ? (
              <TextField id="mcc_other" label="Merchant category code" placeholder="5812" inputMode="numeric" error={errors.mcc_other?.message} {...register("mcc_other")} />
            ) : null}
          </div>
        </section>
      ) : null}

      {step === 1 ? (
        <section className="grid gap-6">
          <div className="grid gap-4 sm:grid-cols-2">
            <TextField id="shares_in_issue" label="Shares in issue" inputMode="numeric" hint="Whole shares held today, all holders together." error={errors.shares_in_issue?.message} {...register("shares_in_issue")} />
            <TextField id="shares_authorised" label="Authorised shares" inputMode="numeric" hint="The most the company may ever issue. At least the shares in issue." error={errors.shares_authorised?.message} {...register("shares_authorised")} />
            <TextField id="treasury_shares" label="Treasury pool" inputMode="numeric" hint="Shares set aside for customers to earn by tapping." error={errors.treasury_shares?.message} {...register("treasury_shares")} />
            <TextField id="public_shares" label="Public shares" inputMode="numeric" hint="The free float: shares not held by founders or insiders." error={errors.public_shares?.message} {...register("public_shares")} />
            <TextField id="holders_count" label="Holders today" inputMode="numeric" hint="How many people hold shares now." error={errors.holders_count?.message} {...register("holders_count")} />
            <TextField id="reference_price" label="Reference price per share" inputMode="decimal" placeholder="40.00" trailing="NGN" hint="What one share is worth at listing, in naira." error={errors.reference_price?.message} {...register("reference_price")} />
            <TextField id="daily_release" label="Daily release cap" inputMode="numeric" hint="The most treasury shares that can go to customers in one day." error={errors.daily_release?.message} {...register("daily_release")} />
            <TextField id="cofund_bps" label="Co-funding" inputMode="numeric" trailing="bps" hint="0 to 300. Each 100 bps adds 1% of every tap to the customer's shares, from you, instead of a cash discount." error={errors.cofund_bps?.message} {...register("cofund_bps")} />
          </div>

          <p className="text-[13px] leading-relaxed text-fg-muted" aria-live="polite">
            {valuation ? (
              <>
                At <span className="tabular-nums text-fg">{valuation.price}</span> per share, <span className="tabular-nums text-fg">{valuation.shares}</span> shares value the company at{" "}
                <span className="tabular-nums font-medium text-fg">{valuation.value}</span>.
              </>
            ) : (
              "Enter the shares in issue and a reference price to see what they value the company at."
            )}
          </p>

          <div className="grid gap-2">
            <div className="flex items-baseline justify-between">
              <p className={labelClasses}>Founders&apos; allocations</p>
              <button
                type="button"
                className={linkClasses + " text-xs"}
                onClick={() => founders.append({ label: "", shares: "", cardholder_ref: "", owner: false })}
              >
                Add a row
              </button>
            </div>
            <p className={hintClasses}>Shares allotted from treasury at listing, each to a Tapp account. Your own account is prefilled.</p>
            {foundersError ? (
              <p className={errorClasses} role="alert">
                {foundersError}
              </p>
            ) : null}
            {founders.fields.length === 0 ? (
              <p className="text-[13px] text-fg-subtle">No allocations. The whole treasury stays available to customers.</p>
            ) : (
              <div className="panel divide-y divide-line">
                {founders.fields.map((f, i) => {
                  const rowErr = errors.founders?.[i];
                  return (
                    <div key={f.id} className="grid gap-3 p-3 sm:grid-cols-[1fr_140px_1fr_auto] sm:items-start">
                      <TextField id={`founders.${i}.label`} label="Label" placeholder="founder" error={rowErr?.label?.message} {...register(`founders.${i}.label`)} />
                      <TextField id={`founders.${i}.shares`} label="Shares" inputMode="numeric" error={rowErr?.shares?.message} {...register(`founders.${i}.shares`)} />
                      {f.owner ? (
                        <div className="grid gap-1.5">
                          <span className={labelClasses}>Tapp account</span>
                          <div className="flex h-9 items-center rounded-md border border-line bg-tile px-3 font-mono text-xs text-fg-muted" title={f.cardholder_ref}>
                            {maskRef(f.cardholder_ref)} <span className="ml-2 font-sans text-fg-subtle">(you)</span>
                          </div>
                          <input type="hidden" {...register(`founders.${i}.cardholder_ref`)} />
                        </div>
                      ) : (
                        <TextField id={`founders.${i}.cardholder_ref`} label="Tapp user id" placeholder="8f3c1a2e-…" hint="Found in the Tapp app under Settings." error={rowErr?.cardholder_ref?.message} {...register(`founders.${i}.cardholder_ref`)} />
                      )}
                      <input type="hidden" {...register(`founders.${i}.owner`)} />
                      <button
                        type="button"
                        aria-label="Remove this allocation"
                        onClick={() => founders.remove(i)}
                        className="focus-ring h-9 rounded-md px-2 text-xs text-fg-muted transition-colors hover:bg-tile hover:text-fg sm:mt-[22px]"
                      >
                        Remove
                      </button>
                    </div>
                  );
                })}
              </div>
            )}
          </div>
        </section>
      ) : null}

      {step === 2 ? (
        <section className="grid gap-4">
          <div className="max-w-[320px]">
            <TextField id="trading_months" label="Months trading" inputMode="numeric" hint="The standard is 24 months or more." error={errors.trading_months?.message} {...register("trading_months")} />
          </div>
          <div className="panel divide-y divide-line">
            <Declaration id="audited_accounts" statement="Our accounts are audited." rule="Rule: at least one set of audited financial statements." {...register("audited_accounts")} />
            <Declaration id="auditor_on_list" statement="Our auditor is on the SEC register." rule="Rule: the audit firm appears on the SEC's list of approved auditors." {...register("auditor_on_list")} />
            <Declaration id="board_resolution" statement="The board has resolved to list." rule="Rule: a board resolution approving the listing and the treasury pool." {...register("board_resolution")} />
            <Declaration id="directors_clear" statement="No director is disqualified or under sanction." rule="Rule: every director passes the fit-and-proper check." {...register("directors_clear")} />
          </div>
          <p className={hintClasses}>These are declarations. The market records them with the listing and may ask for the documents behind them.</p>
        </section>
      ) : null}

      {step === 3 ? (
        <section className="grid gap-4">
          <div className="panel">
            <div className="grid grid-cols-2 divide-x divide-line">
              <Stat label="Share price" value={valuation?.price ?? "—"} sub="reference price at listing" />
              <Stat label="Company value at listing" value={valuation?.value ?? "—"} sub={valuation ? `${valuation.shares} shares in issue × price` : "shares in issue × price"} />
            </div>
          </div>
          <div className="panel">
            <KeyValueList
              rows={[
                { k: "Legal name", v: v.legal_name },
                { k: "Trading name", v: v.trading_name },
                { k: "RC or BN number", v: v.rc_number },
                { k: "Merchant category", v: mccLabel(v.mcc === "other" ? v.mcc_other : v.mcc) },
                { k: "Symbol", v: <span className="font-medium">{v.symbol}</span> },
                { k: "Shares in issue", v: formatWholeShares(Number(v.shares_in_issue)) },
                { k: "Authorised", v: formatWholeShares(Number(v.shares_authorised)) },
                { k: "Treasury pool", v: formatWholeShares(Number(v.treasury_shares)) },
                { k: "Public shares", v: formatWholeShares(Number(v.public_shares)) },
                { k: "Holders today", v: v.holders_count },
                { k: "Reference price", v: formatNaira(Number(v.reference_price)) },
                { k: "Daily release cap", v: `${formatWholeShares(Number(v.daily_release))} shares` },
                { k: "Co-funding", v: `${v.cofund_bps} bps (${(Number(v.cofund_bps) / 100).toFixed(2)}%)` },
                {
                  k: "Founders' allocations",
                  v:
                    v.founders.length === 0 ? (
                      "None"
                    ) : (
                      <ul className="grid gap-0.5">
                        {v.founders.map((f, i) => (
                          <li key={i}>
                            {f.label}: {formatWholeShares(Number(f.shares))} shares to <span className="font-mono text-xs">{maskRef(f.cardholder_ref)}</span>
                          </li>
                        ))}
                      </ul>
                    ),
                },
                { k: "Months trading", v: v.trading_months },
                { k: "Audited accounts", v: yesNo(v.audited_accounts) },
                { k: "Auditor on SEC list", v: yesNo(v.auditor_on_list) },
                { k: "Board resolution", v: yesNo(v.board_resolution) },
                { k: "Directors clear", v: yesNo(v.directors_clear) },
              ]}
            />
          </div>
          <p className={hintClasses}>Submitting sends this to Freedom Exchange. The decision, with its findings, comes back straight away.</p>
        </section>
      ) : null}

      <div className="flex items-center justify-between gap-3 border-t border-line pt-4">
        <div>
          {step > 0 ? (
            <Button variant="secondary" onClick={() => setStep((s) => s - 1)}>
              Back
            </Button>
          ) : (
            <Link href="/business" className={secondaryBtnClasses}>
              Cancel
            </Link>
          )}
        </div>
        {step < STEPS.length - 1 ? (
          <Button key="continue" onClick={next}>
            Continue
          </Button>
        ) : (
          <Button key="submit" type="submit" loading={formState.isSubmitting}>
            Submit listing
          </Button>
        )}
      </div>
    </form>
  );
}

function yesNo(b: boolean) {
  return b ? "Yes" : "No";
}

/** Starting values: a rejected record's identity, plus the owner's founder row. */
function seedFrom(previous: Business | null | undefined, me: Me): ListingInput {
  return {
    ...emptyListing,
    legal_name: previous?.legal_name ?? "",
    trading_name: previous?.trading_name ?? "",
    rc_number: previous?.rc_number ?? "",
    mcc: previous ? (MCC_OPTIONS.some((o) => o.code === previous.mcc) ? previous.mcc : "other") : "",
    mcc_other: previous && !MCC_OPTIONS.some((o) => o.code === previous.mcc) ? previous.mcc : "",
    symbol: previous?.symbol ?? "",
    reference_price: previous ? (previous.reference_price.minor / 100).toFixed(2) : "",
    founders: [{ label: "You (owner)", shares: "", cardholder_ref: me.id, owner: true }],
  };
}
