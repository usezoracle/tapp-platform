"use client";

import { forwardRef, type InputHTMLAttributes } from "react";
import { cn } from "@/lib/utils";

/**
 * A yes/no declaration: a statement, the rule it satisfies, and a switch.
 * Rendered as a checkbox underneath so react-hook-form's `register()` works
 * unchanged; the switch is the visible part.
 */
export const Declaration = forwardRef<
  HTMLInputElement,
  {
    id: string;
    statement: string;
    rule: string;
  } & Omit<InputHTMLAttributes<HTMLInputElement>, "id" | "type">
>(function Declaration({ id, statement, rule, className, ...rest }, ref) {
  return (
    <label
      htmlFor={id}
      className={cn(
        "flex cursor-pointer items-start justify-between gap-4 px-4 py-3 transition-colors hover:bg-tile-hover",
        className,
      )}
    >
      <span className="min-w-0">
        <span className="block text-[13px] text-fg">{statement}</span>
        <span className="mt-0.5 block text-xs text-fg-subtle">{rule}</span>
      </span>
      <span className="relative mt-0.5 shrink-0">
        <input ref={ref} id={id} type="checkbox" className="peer sr-only" {...rest} />
        <span
          aria-hidden
          className="block h-5 w-9 rounded-full bg-line-strong transition-colors peer-checked:bg-accent peer-focus-visible:outline-2 peer-focus-visible:outline-offset-2 peer-focus-visible:outline-accent"
        />
        <span
          aria-hidden
          className="absolute top-0.5 left-0.5 size-4 rounded-full bg-white shadow-[0_1px_2px_rgb(0_0_0/0.2)] transition-transform peer-checked:translate-x-4"
        />
      </span>
    </label>
  );
});
