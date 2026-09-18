/**
 * Reusable Tailwind class strings. Keep components thin: import these
 * directly rather than re-declaring per-button styling. Edit here to
 * adjust the whole app's visual language. Tokens come from
 * app/globals.css (bg-surface, text-fg, border-line, …).
 */

// Size (height, padding) is kept out of the base so a size can be chosen
// without fighting it: with no class merger, "h-8" beside "h-10" resolves
// by stylesheet order, not by which one was meant.
const btnBase =
  "focus-ring inline-flex items-center justify-center gap-2 rounded-md font-medium transition-colors active:translate-y-px disabled:cursor-not-allowed disabled:opacity-50";

/** md is the default; sm for inline actions; lg for the thumb-pressed row on the wallet. */
export const btnSizeClasses = {
  md: "h-10 px-4 text-sm",
  sm: "h-8 px-3 text-xs",
  lg: "h-11 px-3 text-sm",
} as const;

export const btnVariantClasses = {
  /** Near-black in light, white in dark. The one loud button on a screen. */
  primary: `${btnBase} bg-primary text-primary-fg hover:bg-primary-hover`,
  secondary: `${btnBase} border border-line bg-raised text-fg hover:bg-sunken`,
  ghost: `${btnBase} bg-transparent text-fg hover:bg-sunken`,
  danger: `${btnBase} bg-negative text-white hover:opacity-90`,
} as const;

/** Complete md buttons, for the places that render a button without <Button>. */
export const primaryBtnClasses = `${btnVariantClasses.primary} ${btnSizeClasses.md}`;

export const secondaryBtnClasses = `${btnVariantClasses.secondary} ${btnSizeClasses.md}`;

export const ghostBtnClasses = `${btnVariantClasses.ghost} ${btnSizeClasses.md}`;

export const dangerBtnClasses = `${btnVariantClasses.danger} ${btnSizeClasses.md}`;

export const inputClasses =
  "focus-ring h-10 w-full rounded-md border border-line bg-raised px-3 text-sm text-fg transition-colors placeholder:text-fg-subtle hover:border-line-strong focus:border-line-strong disabled:cursor-not-allowed disabled:bg-sunken";

export const labelClasses = "mb-1.5 block text-xs font-medium text-fg-muted";

export const linkClasses =
  "focus-ring rounded-sm text-sm font-medium text-accent hover:underline";

/** Hairline panel — the only "card" in the system. Never nest one in another. */
export const cardClasses = "panel p-4";

export const subtleCardClasses = "rounded-lg bg-sunken p-4";

/** List container: hairline border + solid hairline dividers. */
export const listClasses = "panel divide-y divide-line overflow-hidden";

/** 48px dense row. */
export const rowClasses =
  "flex min-h-12 items-center gap-3 px-3 py-2 transition-colors hover:bg-hover";

/** 28px quiet tile holding a 16px icon, before its colour: pair with a text-* class. */
export const tileBaseClasses =
  "grid size-7 shrink-0 place-items-center rounded-sm bg-sunken [&>svg]:size-4";

/** The same tile in the default muted grey. */
export const tileClasses = `${tileBaseClasses} text-fg-muted`;

/** A section's action: "View all", "Manage". Right-aligned, accent, text only. */
export const sectionLinkClasses = `${linkClasses} text-xs`;

/** The vertical rhythm between sections: 24px on the phone, 32px from 768px. */
export const stackClasses = "grid gap-6 md:gap-8";

/** Table-like list row (holdings on a wide screen): aligned columns. */
export const cellLabelClasses = "text-xs text-fg-muted";
