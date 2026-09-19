"use client";

import { Container } from "./Section";
import { formatCompactNaira, sessionLabel, useMarket } from "@/lib/market";

/**
 * Four live figures from the market feed. Renders nothing at all until the
 * feed answers, and stays empty if it does not: no skeletons, no dashes.
 */
export function LiveStrip() {
  const market = useMarket();
  if (market.status !== "ok") return null;
  const { feed } = market;
  const listed = feed.instruments.filter((i) => i.status === "listed");
  const holders = listed.reduce((n, i) => n + i.holders, 0);
  const value = listed.reduce((n, i) => n + i.market_cap_kobo, 0);
  const session = sessionLabel(feed);

  const cells: { label: string; value: string; dot?: boolean }[] = [
    { label: "Businesses listed", value: listed.length.toLocaleString("en-NG") },
    { label: "Shareholders", value: holders.toLocaleString("en-NG") },
    { label: "Today's market value", value: formatCompactNaira(value) },
    { label: "Market", value: session, dot: true },
  ];

  return (
    <section aria-label="Live market figures" className="border-y border-line">
      <Container>
        <dl className="grid grid-cols-2 md:grid-cols-4">
          {cells.map((c, i) => (
            <div
              key={c.label}
              className={`py-6 md:py-7 ${i % 2 === 1 ? "pl-6 border-l border-line" : ""} ${i >= 2 ? "border-t border-line md:border-t-0" : ""} ${i === 2 ? "md:border-l md:pl-6" : ""} ${i === 3 ? "md:pl-6" : ""}`}
            >
              <dt className="eyebrow">{c.label}</dt>
              <dd className="num mt-3 text-[28px] md:text-[32px] leading-none flex items-center gap-2.5">
                {c.dot ? (
                  <span
                    aria-hidden
                    className={`inline-block h-2.5 w-2.5 rounded-full ${feed.is_trading ? "bg-accent" : "bg-line-strong"}`}
                  />
                ) : null}
                {c.value}
              </dd>
            </div>
          ))}
        </dl>
      </Container>
    </section>
  );
}
