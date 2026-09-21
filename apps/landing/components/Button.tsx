import type { AnchorHTMLAttributes } from "react";

type Variant = "primary" | "hairline" | "link";

const base =
  "inline-flex h-10 items-center justify-center gap-2 rounded-sm px-4 text-[14px] font-medium whitespace-nowrap transition-colors select-none";

const variants: Record<Variant, string> = {
  primary: "bg-primary text-primary-fg hover:opacity-90",
  hairline: "border border-line-strong text-fg hover:bg-tile",
  link: "h-auto px-0 text-fg-muted hover:text-fg",
};

export function Button({
  variant = "primary",
  className = "",
  children,
  ...rest
}: { variant?: Variant } & AnchorHTMLAttributes<HTMLAnchorElement>) {
  return (
    <a className={`${base} ${variants[variant]} ${className}`} {...rest}>
      {children}
    </a>
  );
}

/** Text link with the small arrow; the tertiary action in Attio's ladder. */
export function Arrow({ children, className = "", ...rest }: AnchorHTMLAttributes<HTMLAnchorElement>) {
  return (
    <a
      className={`inline-flex items-center gap-1.5 text-[15px] font-medium text-fg hover:text-accent transition-colors ${className}`}
      {...rest}
    >
      {children}
      <svg width="14" height="14" viewBox="0 0 14 14" fill="none" aria-hidden>
        <path d="M2.5 7h9M8 3.5 11.5 7 8 10.5" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
      </svg>
    </a>
  );
}
