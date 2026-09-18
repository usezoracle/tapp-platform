"use client";

import { forwardRef, type ButtonHTMLAttributes, type ReactNode } from "react";
import { PiSpinnerBold } from "react-icons/pi";
import { cn } from "@/lib/utils";
import {
  dangerBtnClasses,
  ghostBtnClasses,
  primaryBtnClasses,
  secondaryBtnClasses,
} from "./Styles";

export type ButtonVariant = "primary" | "secondary" | "ghost" | "danger";
export type ButtonSize = "md" | "sm";

export interface ButtonProps extends ButtonHTMLAttributes<HTMLButtonElement> {
  variant?: ButtonVariant;
  size?: ButtonSize;
  loading?: boolean;
  leadingIcon?: ReactNode;
  trailingIcon?: ReactNode;
  /** Full-width by default — mobile-first button blocks. */
  fullWidth?: boolean;
}

const variantClassMap: Record<ButtonVariant, string> = {
  primary: primaryBtnClasses,
  secondary: secondaryBtnClasses,
  ghost: ghostBtnClasses,
  danger: dangerBtnClasses,
};

const sizeClassMap: Record<ButtonSize, string> = {
  md: "",
  sm: "h-8 px-3 text-xs",
};

/**
 * CTA primitive. Class strings live in `Styles.ts` so the visual
 * language stays in one place.
 */
export const Button = forwardRef<HTMLButtonElement, ButtonProps>(function Button(
  {
    variant = "primary",
    size = "md",
    loading,
    leadingIcon,
    trailingIcon,
    fullWidth = true,
    children,
    className,
    disabled,
    ...rest
  },
  ref,
) {
  const isDisabled = disabled || loading;
  return (
    <button
      ref={ref}
      disabled={isDisabled}
      aria-busy={loading || undefined}
      className={cn(
        variantClassMap[variant],
        sizeClassMap[size],
        fullWidth && "w-full",
        className,
      )}
      {...rest}
    >
      {loading ? (
        <PiSpinnerBold className="animate-spin" size={16} />
      ) : (
        <>
          {leadingIcon}
          <span>{children}</span>
          {trailingIcon}
        </>
      )}
    </button>
  );
});
