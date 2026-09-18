"use client";

import { PressableScale } from "@/components/ui/PressableScale";
import { rowClasses } from "@/components/ui/Styles";
import { cn } from "@/lib/utils";
import { type Holding, sharesLabel, formatBps, changeClass } from "@/lib/holdings";
import { SymbolTile } from "./SymbolTile";

/** 48px row: initials tile, trading name + shares, value + change. */
export function HoldingRow({ holding, showChange = true }: { holding: Holding; showChange?: boolean }) {
  return (
    <PressableScale as="link" href={`/holdings/${encodeURIComponent(holding.symbol)}`}>
      <div className={rowClasses}>
        <SymbolTile name={holding.trading_name} />
        <div className="grid min-w-0 flex-1 gap-0.5 text-left">
          <p className="truncate text-sm font-medium text-fg">{holding.trading_name}</p>
          <p className="truncate text-xs tabular-nums text-fg-muted">
            {sharesLabel(holding.holding.shares)} · {holding.symbol}
          </p>
        </div>
        <div className="grid shrink-0 gap-0.5 text-right">
          <p className="text-sm font-medium tabular-nums text-fg">{holding.value.display}</p>
          {showChange ? (
            <p className={cn("text-xs tabular-nums", changeClass(holding.change_bps))}>
              {formatBps(holding.change_bps)}
            </p>
          ) : null}
        </div>
      </div>
    </PressableScale>
  );
}
