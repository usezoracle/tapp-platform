"use client";

import { Arrow } from "./Button";
import { Container, Heading } from "./Section";
import { LINKS } from "@/lib/links";
import { formatChange, formatCompactNaira, formatPrice, useMarket } from "@/lib/market";

const tone = { up: "text-accent", down: "text-fg", flat: "text-fg-subtle" } as const;

/** The top five listed businesses by company value, as a hairline table. */
export function Market() {
  const market = useMarket();
  const rows =
    market.status === "ok"
      ? market.feed.instruments
          .filter((i) => i.status === "listed")
          .sort((a, b) => b.market_cap_kobo - a.market_cap_kobo)
          .slice(0, 5)
      : [];

  return (
    <section id="market" className="py-24 md:py-32">
      <Container>
        <div className="flex flex-col gap-8 md:flex-row md:items-end md:justify-between">
          <Heading
            eyebrow="The market"
            title="A real market behind every card."
            lede="Every shop on Freedom has a price, and it moves with what people are willing to pay. Your shares are worth what the market says they are worth, not what we say."
          />
          <Arrow href={LINKS.market} className="shrink-0">
            See the market
          </Arrow>
        </div>

        {rows.length > 0 ? (
          <div className="mt-12 overflow-x-auto">
            <table className="w-full min-w-[560px] border-collapse text-[15px]">
              <thead>
                <tr className="border-b border-line-strong text-left">
                  <th className="eyebrow py-3 pr-4 font-normal">Business</th>
                  <th className="py-3 pr-4 text-right font-mono text-[12px] tracking-[0.12em] uppercase text-fg-muted">Price</th>
                  <th className="py-3 pr-4 text-right font-mono text-[12px] tracking-[0.12em] uppercase text-fg-muted">Today</th>
                  <th className="py-3 text-right font-mono text-[12px] tracking-[0.12em] uppercase text-fg-muted">Company value</th>
                </tr>
              </thead>
              <tbody>
                {rows.map((r) => {
                  const ch = formatChange(r.change_bps);
                  return (
                    <tr key={r.symbol} className="border-b border-line">
                      <td className="py-4 pr-4">
                        <div className="font-medium">{r.trading_name}</div>
                        <div className="font-mono text-[12px] text-fg-subtle">{r.symbol}</div>
                      </td>
                      <td className="num py-4 pr-4 text-right text-[17px]">{formatPrice(r.price_kobo)}</td>
                      <td className={`num py-4 pr-4 text-right text-[17px] ${tone[ch.tone]}`}>{ch.text}</td>
                      <td className="num py-4 text-right text-[17px]">{formatCompactNaira(r.market_cap_kobo)}</td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
            {market.status === "ok" ? (
              <p className="mt-3 font-mono text-[12px] text-fg-subtle">
                As of {new Date(market.feed.as_of).toLocaleString("en-NG", { dateStyle: "medium", timeStyle: "short" })}
              </p>
            ) : null}
          </div>
        ) : null}
      </Container>
    </section>
  );
}
