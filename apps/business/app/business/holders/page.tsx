"use client";

import { useInfiniteQuery } from "@tanstack/react-query";
import { RequireSession } from "@/components/RequireSession";
import { PageHeader } from "@/components/ui/PageHeader";
import { Button } from "@/components/ui/Button";
import { Notice } from "@/components/ui/Notice";
import { ApiError, getHolders, type HoldersPage } from "@/lib/api";
import { formatShares } from "@/lib/units";
import { formatDate, maskRef } from "@/lib/utils";

export default function HoldersRoute() {
  return <RequireSession>{(session) => <HoldersView token={session.jwt} />}</RequireSession>;
}

function HoldersView({ token }: { token: string }) {
  const q = useInfiniteQuery<HoldersPage, ApiError>({
    queryKey: ["holders", token],
    initialPageParam: null as string | null,
    queryFn: ({ pageParam }) => getHolders(token, pageParam as string | null),
    getNextPageParam: (last) => last.next_cursor ?? undefined,
  });

  const rows = q.data?.pages.flatMap((p) => p.holders) ?? [];

  return (
    <div className="grid gap-6">
      <PageHeader title="Holders" subtitle="Everyone who owns a share of your business, by Tapp account." />

      {q.isPending ? (
        <div className="grid gap-2" aria-busy>
          <div className="skeleton h-10 w-full" />
          <div className="skeleton h-10 w-full" />
          <div className="skeleton h-10 w-full" />
        </div>
      ) : q.isError ? (
        <Notice tone="error">
          {q.error.status === 404
            ? "There is no listed business on this account yet, so there is no register to show."
            : q.error.message}
        </Notice>
      ) : rows.length === 0 ? (
        <p className="text-[13px] text-fg-muted">No holders yet. The register fills as customers tap.</p>
      ) : (
        <div className="panel overflow-x-auto">
          <table className="w-full min-w-[640px] text-[13px]">
            <thead>
              <tr className="border-b border-line text-left">
                <Th>Cardholder</Th>
                <Th align="right">Shares</Th>
                <Th align="right">Locked</Th>
                <Th align="right">Cost</Th>
                <Th align="right">First acquired</Th>
              </tr>
            </thead>
            <tbody className="divide-y divide-line">
              {rows.map((h) => (
                <tr key={h.cardholder_ref} className="transition-colors hover:bg-tile-hover">
                  <td className="px-4 py-2.5 font-mono text-xs text-fg" title={h.cardholder_ref}>
                    {maskRef(h.cardholder_ref)}
                  </td>
                  <td className="px-4 py-2.5 text-right tabular-nums">{formatShares(h.holding)}</td>
                  <td className="px-4 py-2.5 text-right tabular-nums text-fg-muted">{formatShares(h.locked)}</td>
                  <td className="px-4 py-2.5 text-right tabular-nums">{h.cost.display}</td>
                  <td className="px-4 py-2.5 text-right tabular-nums text-fg-muted">{formatDate(h.first_acquired)}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}

      {q.hasNextPage ? (
        <div>
          <Button variant="secondary" loading={q.isFetchingNextPage} onClick={() => q.fetchNextPage()}>
            Load more
          </Button>
        </div>
      ) : null}
    </div>
  );
}

function Th({ children, align = "left" }: { children: string; align?: "left" | "right" }) {
  return (
    <th scope="col" className={`eyebrow px-4 py-2 font-medium ${align === "right" ? "text-right" : "text-left"}`}>
      {children}
    </th>
  );
}
