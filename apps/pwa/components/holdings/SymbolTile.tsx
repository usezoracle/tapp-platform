import type { CSSProperties } from "react";
import { cn } from "@/lib/utils";
import { holdingInitials } from "@/lib/holdings";

/**
 * Which of the six symbol hues a symbol gets. A small string hash, so the
 * same business is the same colour on every screen and every visit
 * without anybody keeping a table of assignments.
 */
export function symbolHue(symbol: string): string {
  let h = 0;
  for (let i = 0; i < symbol.length; i++) h = (h * 31 + symbol.charCodeAt(i)) | 0;
  return `--sym-${(Math.abs(h) % 6) + 1}`;
}

/** 28px tile with the business's initials, in the symbol's hue on a 12% wash of it. */
export function SymbolTile({
  name,
  symbol,
  className,
  size = "sm",
}: {
  name: string;
  /** Hashed for the hue; falls back to the name. */
  symbol?: string;
  className?: string;
  size?: "sm" | "lg";
}) {
  return (
    <span
      aria-hidden
      style={{ "--hue": `var(${symbolHue(symbol ?? name)})` } as CSSProperties}
      className={cn(
        "hue-wash hue-text grid shrink-0 place-items-center rounded-sm font-medium",
        size === "sm" ? "size-7 text-[11px]" : "size-10 rounded-md text-sm",
        className,
      )}
    >
      {holdingInitials(name)}
    </span>
  );
}
