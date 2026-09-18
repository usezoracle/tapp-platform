"use client";

import Link from "next/link";
import { RequireSession } from "@/components/RequireSession";
import { PageHeader } from "@/components/ui/PageHeader";
import { StatusChip, type ChipTone } from "@/components/ui/StatusChip";
import { KeyValueList } from "@/components/ui/KeyValue";
import { Notice } from "@/components/ui/Notice";
import { primaryBtnClasses, secondaryBtnClasses } from "@/components/ui/Styles";
import { Findings } from "@/components/business/Findings";
import { CapTable } from "@/components/business/CapTable";
import { useBusiness } from "@/lib/queries";
import { mccLabel } from "@/lib/mcc";
import { formatDate } from "@/lib/utils";
import type { Business, BusinessState } from "@/lib/api";

const stateChip: Record<BusinessState, { tone: ChipTone; label: string }> = {
  listed: { tone: "success", label: "Listed" },
  rejected: { tone: "error", label: "Rejected" },
  submitted: { tone: "pending", label: "Submitted" },
};

export default function BusinessPage() {
  return <RequireSession>{(session) => <BusinessView token={session.jwt} />}</RequireSession>;
}

function BusinessView({ token }: { token: string }) {
  const q = useBusiness(token);

  if (q.isPending) {
    return (
      <div className="grid gap-4" aria-busy>
        <div className="skeleton h-7 w-48" />
        <div className="skeleton h-24 w-full" />
        <div className="skeleton h-40 w-full" />
      </div>
    );
  }

  if (q.isError) {
    return (
      <div className="grid gap-4">
        <PageHeader title="Your business" />
        <Notice tone="error">{q.error.message}</Notice>
        <div>
          <button type="button" className={secondaryBtnClasses} onClick={() => q.refetch()}>
            Try again
          </button>
        </div>
      </div>
    );
  }

  if (!q.data) return <EmptyState />;
  return <BusinessRecord business={q.data} />;
}

function EmptyState() {
  return (
    <div className="grid max-w-[640px] gap-6">
      <PageHeader title="Your business" subtitle="Not yet listed on Freedom Exchange." />
      <div className="grid gap-3 text-[14px] leading-relaxed text-fg">
        <p>
          Listing turns your shop into something your customers own a little of. Every tap at your till buys the
          customer a slice of the business, paid for out of the fee on that tap, not out of the price. Shares come
          from a treasury pool you set aside, released at a daily cap you choose, and each customer's holding is
          kept on Freedom Exchange under their Tapp account.
        </p>
        <p>
          You can also co-fund the slice. Instead of a cash discount, a rebate of up to 3% of each tap goes toward the
          customer's shares. The register of holders, the shares still in treasury and what is waiting to be
          released are all visible here once you are listed.
        </p>
      </div>
      <div>
        <Link href="/business/list" className={primaryBtnClasses}>
          List your business
        </Link>
      </div>
    </div>
  );
}

function BusinessRecord({ business: b }: { business: Business }) {
  const chip = stateChip[b.state] ?? { tone: "neutral" as ChipTone, label: b.state };
  return (
    <div className="grid gap-6">
      <PageHeader
        eyebrow={b.symbol}
        title={b.trading_name || b.legal_name}
        subtitle={
          <>
            {b.legal_name} · {b.rc_number} · submitted {formatDate(b.submitted_at)}
            {b.decided_at ? `, decided ${formatDate(b.decided_at)}` : ""}
          </>
        }
        trailing={<StatusChip tone={chip.tone}>{chip.label}</StatusChip>}
      />

      {b.state === "rejected" ? (
        <div className="flex flex-wrap items-center gap-3">
          <p className="text-[13px] text-fg-muted">The market did not admit this listing. Fix what is marked below and submit again.</p>
          <Link href="/business/list" className={secondaryBtnClasses}>
            Edit and resubmit
          </Link>
        </div>
      ) : null}

      {b.state === "submitted" ? (
        <Notice>The market has the submission and has not decided yet.</Notice>
      ) : null}

      <section className="panel">
        <KeyValueList
          rows={[
            { k: "Symbol", v: <span className="font-medium">{b.symbol}</span> },
            { k: "Reference price", v: b.reference_price.display },
            { k: "Merchant category", v: mccLabel(b.mcc) },
            { k: "Instrument", v: b.instrument_id ?? "—" },
          ]}
        />
      </section>

      <section className="grid gap-2">
        <h2 className="eyebrow">Findings</h2>
        <div className="panel">
          <Findings findings={b.findings} />
        </div>
      </section>

      {b.state === "listed" ? (
        <section className="grid gap-2">
          <h2 className="eyebrow">On the market</h2>
          {b.live ? (
            <CapTable live={b.live} />
          ) : (
            <p className="text-[13px] text-fg-muted">
              The market could not be reached, so the live figures are not shown. The record above is what was stored at listing.
              {b.live_error ? <span className="block text-xs text-fg-subtle">{b.live_error}</span> : null}
            </p>
          )}
        </section>
      ) : null}
    </div>
  );
}
