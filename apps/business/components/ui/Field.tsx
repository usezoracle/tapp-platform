"use client";

import { forwardRef, type InputHTMLAttributes, type ReactNode, type SelectHTMLAttributes } from "react";
import { cn } from "@/lib/utils";
import { errorClasses, hintClasses, inputClasses, labelClasses } from "./Styles";

interface FieldFrameProps {
  id: string;
  label: string;
  hint?: ReactNode;
  error?: string;
  /** Something to the right of the input: a unit, a check. */
  trailing?: ReactNode;
  children: ReactNode;
}

function FieldFrame({ id, label, hint, error, trailing, children }: FieldFrameProps) {
  return (
    <div className="grid gap-1.5">
      <label htmlFor={id} className={labelClasses}>
        {label}
      </label>
      <div className="relative">
        {children}
        {trailing ? (
          <span className="pointer-events-none absolute inset-y-0 right-3 flex items-center text-xs text-fg-subtle">
            {trailing}
          </span>
        ) : null}
      </div>
      {error ? (
        <p className={errorClasses} role="alert">
          {error}
        </p>
      ) : hint ? (
        <p className={hintClasses}>{hint}</p>
      ) : null}
    </div>
  );
}

export interface TextFieldProps extends Omit<InputHTMLAttributes<HTMLInputElement>, "id"> {
  id: string;
  label: string;
  hint?: ReactNode;
  error?: string;
  trailing?: ReactNode;
}

/** A labelled input that takes a react-hook-form `register()` spread. */
export const TextField = forwardRef<HTMLInputElement, TextFieldProps>(function TextField(
  { id, label, hint, error, trailing, className, ...rest },
  ref,
) {
  return (
    <FieldFrame id={id} label={label} hint={hint} error={error} trailing={trailing}>
      <input
        ref={ref}
        id={id}
        aria-invalid={error ? true : undefined}
        className={cn(inputClasses, trailing && "pr-10", className)}
        {...rest}
      />
    </FieldFrame>
  );
});

export interface SelectFieldProps extends Omit<SelectHTMLAttributes<HTMLSelectElement>, "id"> {
  id: string;
  label: string;
  hint?: ReactNode;
  error?: string;
}

export const SelectField = forwardRef<HTMLSelectElement, SelectFieldProps>(function SelectField(
  { id, label, hint, error, className, children, ...rest },
  ref,
) {
  return (
    <FieldFrame id={id} label={label} hint={hint} error={error} trailing={<Caret />}>
      <select
        ref={ref}
        id={id}
        aria-invalid={error ? true : undefined}
        className={cn(inputClasses, "pr-9", className)}
        {...rest}
      >
        {children}
      </select>
    </FieldFrame>
  );
});

function Caret() {
  return (
    <svg className="size-3.5" viewBox="0 0 16 16" fill="none" aria-hidden>
      <path d="M4 6l4 4 4-4" stroke="currentColor" strokeWidth="1.5" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}
