"use client";

import type { ReactNode } from "react";
import { cn } from "@/lib/utils";

interface TabButtonProps {
  active: boolean;
  onClick: () => void;
  children: ReactNode;
  disabled?: boolean;
}

export function TabButton({ active, onClick, children, disabled }: TabButtonProps) {
  return (
    <button
      type="button"
      disabled={disabled}
      onClick={onClick}
      className={cn(
        "focus-ring h-8 flex-1 rounded-sm px-3 text-sm font-medium transition-colors",
        active ? "bg-raised text-fg shadow-[0_0_0_1px_var(--line)]" : "text-fg-muted hover:text-fg",
        disabled && "cursor-not-allowed opacity-70",
      )}
    >
      {children}
    </button>
  );
}

export function TabRow({ children }: { children: ReactNode }) {
  return (
    <div className="flex items-center gap-1 rounded-md bg-sunken p-1">
      {children}
    </div>
  );
}
