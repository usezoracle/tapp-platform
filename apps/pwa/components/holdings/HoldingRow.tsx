"use client";

import { PressableScale } from "@/components/ui/PressableScale";
import { cellLabelClasses } from "@/components/ui/Styles";
import { cn } from "@/lib/utils";
import { type Holding, sharesLabel, formatBps, changeClass } from "@/lib/holdings";
import { SymbolTile } from "./SymbolTile";

/**
 * Column template shared by the table rows and their header so the two
 * cannot drift: business | shares | value | change. Below 768px the row is
 * the compact two-line kind and the header is not shown.
 */
const tableCols =
  "md:grid md:grid-cols-[minmax(0,2fr)_minmax(0,1fr)_minmax(0,1fr)_minmax(0,1fr)] md:gap-4";

/**
 * 48px row: initials tile, trading name + shares, value + change.
 *
 * `layout="table"` lays the same facts out in aligned columns from 768px,
 * for the holdings list; the compact form stays for the wallet module,
 * whose column is narrow on every width.
 */
export function HoldingRow({
  holding,
  showChange = true,
  layout = "compact",
}: {
  holding: Holding;
  showChange?: boolean;
  layout?: "compact" | "table";
}) {
  const table = layout === "table";
  return (
    <PressableScale as="link" href={`/holdings/${encodeURIComponent(holding.symbol)}`}>
      <div
        className={cn(
          "flex min-h-12 items-center gap-3 px-3 py-2 transition-colors hover:bg-hover",
          table && tableCols,
        )}
      >
        <div className={cn("flex min-w-0 flex-1 items-center gap-3", table && "md:flex-none")}>
          <SymbolTile name={holding.trading_name} />
          <div className="grid min-w-0 flex-1 gap-0.5 text-left">
            <p className="truncate text-sm font-medium text-fg">{holding.trading_name}</p>
            <p className="truncate text-xs tabular-nums text-fg-muted">
              {table ? (
                <>
                  <span className="md:hidden">{sharesLabel(holding.holding.shares)} · </span>
                  {holding.symbol}
                </>
              ) : (
                <>
                  {sharesLabel(holding.holding.shares)} · {holding.symbol}
                </>
              )}
            </p>
          </div>
        </div>

        {table ? (
          <p className="hidden text-right text-sm tabular-nums text-fg md:block">
            {sharesLabel(holding.holding.shares)}
          </p>
        ) : null}

        <div className={cn("grid shrink-0 gap-0.5 text-right", table && "md:contents")}>
          <p className="text-sm font-medium tabular-nums text-fg md:text-right">{holding.value.display}</p>
          {showChange ? (
            <p className={cn("text-xs tabular-nums md:text-right", table && "md:text-sm", changeClass(holding.change_bps))}>
              {formatBps(holding.change_bps)}
            </p>
          ) : null}
        </div>
      </div>
    </PressableScale>
  );
}

/** Column labels for the table layout. Hidden below 768px. */
export function HoldingTableHeader() {
  return (
    <div className={cn("hidden border-b border-line px-3 py-2", tableCols)} aria-hidden>
      <p className={cellLabelClasses}>Business</p>
      <p className={cn(cellLabelClasses, "text-right")}>Shares</p>
      <p className={cn(cellLabelClasses, "text-right")}>Value</p>
      <p className={cn(cellLabelClasses, "text-right")}>Change</p>
    </div>
  );
}
