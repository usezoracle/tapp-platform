"use client";

import { forwardRef, type ButtonHTMLAttributes } from "react";
import { cn } from "@/lib/utils";
import { ghostBtnClasses, primaryBtnClasses, secondaryBtnClasses } from "./Styles";
import { Spinner } from "./Spinner";

export type ButtonVariant = "primary" | "secondary" | "ghost";

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  loading?: boolean;
}

const variantClassMap: Record<ButtonVariant, string> = {
  primary: primaryBtnClasses,
  secondary: secondaryBtnClasses,
  ghost: ghostBtnClasses,
};

export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  { variant = "primary", loading, children, className, disabled, type = "button", ...rest },
  ref,
) {
  return (
    <button
      ref={ref}
      type={type}
      disabled={disabled || loading}
      aria-busy={loading || undefined}
      className={cn(variantClassMap[variant], className)}
      {...rest}
    >
      {loading ? <Spinner /> : null}
      <span>{children}</span>
    </button>
  );
});
