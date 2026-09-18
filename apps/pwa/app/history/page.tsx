"use client";

import { useEffect, useMemo, useState } from "react";
import { useRouter } from "next/navigation";
import { useQuery } from "@tanstack/react-query";
import { PiWarningBold } from "react-icons/pi";
import { Screen } from "@/components/ui/Screen";
import { PageHeader } from "@/components/ui/PageHeader";
import { Button } from "@/components/ui/Button";
import { InfoBanner } from "@/components/ui/InfoBanner";
import { EmptyState } from "@/components/ui/Surface";
import { MovementList } from "@/components/ui/MovementList";
import { SkeletonRows } from "@/components/ui/Skeleton";
import { AnimatedComponent, slideInOut } from "@/components/ui/AnimatedComponents";
import { stackClasses } from "@/components/ui/Styles";
import { cn } from "@/lib/utils";
import { useSession } from "@/lib/auth";
import { request, type Movement } from "@/lib/api";
import { useEquityActivity, indexEquityByTapId } from "@/lib/holdings";

interface Page {
  movements: Movement[];
  nextCursor?: string;
}

const PAGE = 30;

export default function HistoryPage() {
  const router = useRouter();
  const { hydrated, session } = useSession();

  // Cursors seen so far. Pages accumulate rather than replacing each other,
  // because "load more" that swaps the list is a back button people did not
  // ask for.
  const [cursors, setCursors] = useState<(string | undefined)[]>([undefined]);

  useEffect(() => {
    if (hydrated && !session) router.replace("/sign-in?next=/history");
  }, [hydrated, session, router]);

  const pages = useQuery({
    queryKey: ["ledger", "history", session?.email ?? "", cursors],
    enabled: hydrated && !!session,
    queryFn: async () => {
      const out: Page[] = [];
      for (const cursor of cursors) {
        const params = new URLSearchParams({ limit: String(PAGE) });
        if (cursor) params.set("cursor", cursor);
        out.push(
          await request<Page>("GET", `/v1/me/activity?${params}`, {
            token: session!.jwt,
          }),
        );
      }
      return out;
    },
  });

  const equity = useEquityActivity(200);
  const equityByRef = useMemo(() => indexEquityByTapId(equity.data?.activity), [equity.data]);

  const movements = (pages.data ?? []).flatMap((p) => p.movements);
  const next = pages.data?.[pages.data.length - 1]?.nextCursor;

  if (!hydrated || !session) return <Screen />;

  const count = movements.length;

  return (
    <Screen>
      <AnimatedComponent variant={slideInOut} className={cn(stackClasses, "py-4")}>
        <PageHeader
          title="Activity"
          hideBack
          subtitle={
            pages.data
              ? `${count}${next ? "+" : ""} ${count === 1 ? "movement" : "movements"} · in and out`
              : "Every movement of your money, in and out"
          }
        />

        {pages.isLoading ? (
          <SkeletonRows rows={6} />
        ) : pages.isError ? (
          <InfoBanner tone="error" icon={<PiWarningBold />}>
            <p className="font-medium">Could not load your activity</p>
            <p className="mt-0.5 text-xs">
              {pages.error instanceof Error ? pages.error.message : "Try again in a moment."}
            </p>
          </InfoBanner>
        ) : (
          <>
            <MovementList
              movements={movements}
              equityByRef={equityByRef}
              grouped
              emptyState={
                <EmptyState title="Nothing here yet">
                  Add cash through an agent, or receive USDC on Base, and every movement will
                  be listed here.
                </EmptyState>
              }
            />

            {next ? (
              <div className="md:flex md:justify-center">
                <Button
                  variant="secondary"
                  loading={pages.isFetching}
                  onClick={() => setCursors((c) => [...c, next])}
                  className="md:w-auto md:px-6"
                >
                  Load more
                </Button>
              </div>
            ) : movements.length ? (
              <p className="text-xs text-fg-subtle">That is everything.</p>
            ) : null}
          </>
        )}
      </AnimatedComponent>
    </Screen>
  );
}
