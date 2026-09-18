import type { ReactNode } from "react";

/** The narrow column both auth screens share. */
export function AuthCard({ title, subtitle, children, footer }: { title: string; subtitle?: ReactNode; children: ReactNode; footer: ReactNode }) {
  return (
    <div className="mx-auto w-full max-w-[400px] pt-6 sm:pt-16">
      <h1 className="display text-[22px] leading-7">{title}</h1>
      {subtitle ? <p className="mt-1 text-[13px] text-fg-muted">{subtitle}</p> : null}
      <div className="mt-6">{children}</div>
      <p className="mt-6 text-[13px] text-fg-muted">{footer}</p>
    </div>
  );
}
