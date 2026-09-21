import type { ReactNode } from "react";

/** Page-width container with the 16px phone gutter. */
export function Container({ children, className = "" }: { children: ReactNode; className?: string }) {
  return <div className={`mx-auto w-full max-w-[1200px] px-4 sm:px-6 md:px-8 ${className}`}>{children}</div>;
}

/** Eyebrow, display heading, optional lede. The opening of every section. */
export function Heading({
  eyebrow,
  title,
  lede,
  align = "left",
  className = "",
}: {
  eyebrow: string;
  title: ReactNode;
  lede?: ReactNode;
  align?: "left" | "center";
  className?: string;
}) {
  const centred = align === "center";
  return (
    <div className={`${centred ? "text-center mx-auto flex flex-col items-center" : ""} max-w-[680px] ${className}`}>
      <span className="eyebrow">{eyebrow}</span>
      <h2 className="display mt-5 text-[32px] sm:text-[40px] lg:text-[48px]">{title}</h2>
      {lede ? <p className="mt-5 text-[17px] leading-[1.5] text-fg-muted max-w-[520px]">{lede}</p> : null}
    </div>
  );
}
