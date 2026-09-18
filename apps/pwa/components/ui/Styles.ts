/**
 * Reusable Tailwind class strings. Keep components thin: import these
 * directly rather than re-declaring per-button styling. Edit here to
 * adjust the whole app's visual language. Tokens come from
 * app/globals.css (bg-surface, text-fg, border-line, …).
 */

const btnBase =
  "focus-ring inline-flex h-10 items-center justify-center gap-2 rounded-md px-4 text-sm font-medium transition-colors active:translate-y-px disabled:cursor-not-allowed disabled:opacity-50";

/** Near-black in light, white in dark. The one loud button on a screen. */
export const primaryBtnClasses = `${btnBase} bg-primary text-primary-fg hover:bg-primary-hover`;

export const secondaryBtnClasses = `${btnBase} border border-line bg-raised text-fg hover:bg-sunken`;

export const ghostBtnClasses = `${btnBase} bg-transparent text-fg hover:bg-sunken`;

export const dangerBtnClasses = `${btnBase} bg-negative text-white hover:opacity-90`;

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

/** 28px quiet tile holding a 16px icon. */
export const tileClasses =
  "grid size-7 shrink-0 place-items-center rounded-sm bg-sunken text-fg-muted [&>svg]:size-4";
