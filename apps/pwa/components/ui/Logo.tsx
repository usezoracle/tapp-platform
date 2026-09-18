import { cn } from "@/lib/utils";

/**
 * Tapp brand mark — two stacked tap-cards, the front in royal blue
 * with a darker sliver of the card beneath showing bottom-right.
 * Sized in em so it scales with the wordmark's font size.
 */
export function TappMark({ className }: { className?: string }) {
  return (
    <svg
      viewBox="0 0 48 48"
      fill="none"
      className={cn("h-[1.35em] w-[1.35em]", className)}
      aria-hidden
    >
      <g transform="rotate(-8 24 24)">
        <rect x="18.5" y="15.5" width="21" height="27" rx="7" fill="#0047B3" />
        <rect x="8.5" y="5.5" width="21" height="27" rx="7" fill="#0065F5" />
      </g>
    </svg>
  );
}

/**
 * Full logo — card-stack mark + lowercase bold-italic "tapp"
 * wordmark in DM Sans. Override color/size via className
 * (e.g. `text-white` on dark surfaces, `text-sm` in footers).
 */
export function Logo({ className }: { className?: string }) {
  return (
    <span
      className={cn(
        "inline-flex items-center gap-1.5 text-xl font-bold italic tracking-tight text-fg",
        className,
      )}
      style={{ fontFamily: "var(--font-dm-sans), var(--font-sans)" }}
    >
      <TappMark />
      tapp
    </span>
  );
}
