/**
 * Reusable class strings. Components stay thin; the visual language is edited
 * here. Tokens come from app/globals.css.
 */

const btnBase =
  "focus-ring inline-flex h-9 items-center justify-center gap-2 rounded-md px-3.5 text-[13px] font-medium transition-colors active:translate-y-px disabled:cursor-not-allowed disabled:opacity-50";

/** Near-black in light, white in dark. The one loud button on a screen. */
export const primaryBtnClasses = `${btnBase} bg-primary text-primary-fg hover:bg-primary-hover`;
export const secondaryBtnClasses = `${btnBase} border border-line bg-surface text-fg hover:bg-tile`;
export const ghostBtnClasses = `${btnBase} bg-transparent text-fg-muted hover:bg-tile hover:text-fg`;

export const inputClasses =
  "focus-ring h-9 w-full rounded-md border border-line bg-surface px-3 text-[13px] text-fg transition-colors placeholder:text-fg-subtle hover:border-line-strong focus:border-line-strong disabled:cursor-not-allowed disabled:bg-tile aria-[invalid=true]:border-err-dot";

export const labelClasses = "block text-xs font-medium text-fg-muted";
export const hintClasses = "text-xs text-fg-subtle";
export const errorClasses = "text-xs text-err-fg";

export const linkClasses = "focus-ring rounded-sm font-medium text-accent hover:underline";

/** Hairline panel. The only card in the system; never nested. */
export const panelClasses = "panel";
export const listClasses = "panel divide-y divide-line overflow-hidden";
