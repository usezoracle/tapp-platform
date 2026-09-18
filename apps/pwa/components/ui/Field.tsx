"use client";

import type { InputHTMLAttributes, ReactNode } from "react";
import { cn } from "@/lib/utils";
import { inputClasses, labelClasses } from "./Styles";

/**
 * A labelled text input with an optional hint beneath it.
 *
 * The one form control the verification and account screens share, so a
 * BVN field looks the same whether it is asked for on the deposit screen or
 * in settings. `action` is a small trailing link beside the label ("Forgot
 * password?", "Resend code"); `children` replaces the default input for the
 * rare control that needs its own chrome (a password toggle).
 */
export function Field({
  id,
  label,
  value,
  onChange,
  hint,
  action,
  children,
  className,
  ...rest
}: {
  id: string;
  label: string;
  value: string;
  onChange: (v: string) => void;
  hint?: ReactNode;
  action?: ReactNode;
  children?: ReactNode;
} & Omit<InputHTMLAttributes<HTMLInputElement>, "id" | "value" | "onChange">) {
  return (
    <div>
      <div className="mb-1.5 flex items-center justify-between">
        <label htmlFor={id} className={cn(labelClasses, "mb-0")}>
          {label}
        </label>
        {action}
      </div>
      {children ?? (
        <input
          id={id}
          value={value}
          onChange={(e) => onChange(e.target.value)}
          className={cn(inputClasses, className)}
          {...rest}
        />
      )}
      {hint ? <p className="mt-1.5 text-xs text-fg-muted">{hint}</p> : null}
    </div>
  );
}
