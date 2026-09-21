"use client";

import { useId, useState, type ReactNode } from "react";

interface TooltipProps {
  message: string;
  children: ReactNode;
}

/** Inverted 12px tooltip above the trigger. The only shadow besides sheets. */
export function Tooltip({ message, children }: TooltipProps) {
  const [open, setOpen] = useState(false);
  const id = useId();
  return (
    <span
      className="relative inline-flex"
      aria-describedby={open ? id : undefined}
      onMouseEnter={() => setOpen(true)}
      onMouseLeave={() => setOpen(false)}
      onFocus={() => setOpen(true)}
      onBlur={() => setOpen(false)}
    >
      {children}
      {open ? (
        <span
          role="tooltip"
          id={id}
          className="pointer-events-none absolute bottom-full left-1/2 z-10 mb-1.5 w-max max-w-52 -translate-x-1/2 rounded-md bg-fg px-2 py-1.5 text-xs leading-4 text-surface shadow-sheet"
        >
          {message}
        </span>
      ) : null}
    </span>
  );
}
