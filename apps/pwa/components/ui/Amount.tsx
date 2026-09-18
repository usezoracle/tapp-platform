import { cn } from "@/lib/utils";
import type { Money } from "@/lib/api";

type Size = "headline" | "hero" | "lg" | "md" | "sm";

// The large sizes use the display face; the two small ones stay in the
// body face so a figure inside a row lines up with the text beside it.
// `headline` is the wallet's one big number: 36px on the phone, 40 from 768.
const SIZES: Record<Size, string> = {
  headline: "display text-4xl leading-10 md:text-[40px] md:leading-[44px]",
  hero: "display text-[40px] leading-[44px]",
  lg: "display text-2xl leading-7",
  md: "text-sm font-medium",
  sm: "text-sm font-medium",
};

interface AmountProps {
  value: Money | null | undefined;
  size?: Size;
  /**
   * Colour the number by direction: green arriving, plain leaving.
   *
   * Off by default. A balance is not a direction, and painting every figure on
   * a screen green or red teaches people to stop reading the colour.
   */
  signed?: boolean;
  /** Show a leading + on positive values. Only meaningful with `signed`. */
  showPlus?: boolean;
  className?: string;
}

/**
 * An amount, rendered the way the server rendered it.
 *
 * `display` comes from the API and is shown as-is. Formatting money is decided
 * in exactly one place -- the server -- so a receipt the API prints and a
 * figure this app draws cannot disagree about what the same number looks like.
 * Re-implementing grouping and decimal places on the client is how a naira
 * figure ends up with two decimal places in one screen and none in the next.
 *
 * A missing value renders an em dash rather than a zero. "We don't know" and
 * "nothing" are different, and only one of them should look like ₦0.00.
 */
export function Amount({
  value,
  size = "md",
  signed = false,
  showPlus = false,
  className,
}: AmountProps) {
  if (!value) {
    return (
      <span className={cn("tabular-nums text-fg-subtle", SIZES[size], className)}>
        —
      </span>
    );
  }

  const positive = value.minor > 0;
  const tone = signed ? (positive ? "text-positive" : "text-fg") : "text-fg";

  return (
    <span className={cn("tabular-nums", SIZES[size], tone, className)}>
      {signed && showPlus && positive ? "+" : ""}
      {value.display}
    </span>
  );
}

/**
 * The three-letter code, for places where two currencies sit side by side and
 * the symbol alone is ambiguous.
 */
export function CurrencyTag({ code, className }: { code: string; className?: string }) {
  return (
    <span
      className={cn(
        "inline-flex h-5 items-center rounded-sm bg-sunken px-1.5 text-[11px] font-medium uppercase tracking-wide text-fg-muted",
        className,
      )}
    >
      {code}
    </span>
  );
}
