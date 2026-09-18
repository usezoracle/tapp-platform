"use client";

import Link from "next/link";
import { useRouter } from "next/navigation";
import type { ReactNode } from "react";
import { PiArrowLeftBold, PiXBold } from "react-icons/pi";
import { cn } from "@/lib/utils";

interface PageHeaderProps {
  title: ReactNode;
  /** One line under the title, muted. */
  subtitle?: ReactNode;
  /** Back target. A string renders a Link; a function runs on press;
   *  omitted falls back to router.back(). */
  back?: string | (() => void);
  /** Render the leading affordance as a close (X) instead of back. */
  close?: boolean;
  hideBack?: boolean;
  /** Trailing slot — an icon button, a chip. */
  trailing?: ReactNode;
  className?: string;
}

const leadingBtn =
  "focus-ring grid size-8 shrink-0 place-items-center rounded-md border border-line bg-raised text-fg transition-colors hover:bg-sunken [&>svg]:size-4";

/**
 * Page title with a back affordance (users-app PageHeader, left-aligned
 * the Linear way). Title uses the display face.
 */
export function PageHeader({
  title,
  subtitle,
  back,
  close,
  hideBack,
  trailing,
  className,
}: PageHeaderProps) {
  const router = useRouter();
  const Icon = close ? PiXBold : PiArrowLeftBold;
  const label = close ? "Close" : "Back";

  let leading: ReactNode = null;
  if (!hideBack) {
    leading =
      typeof back === "string" ? (
        <Link href={back} aria-label={label} className={leadingBtn}>
          <Icon />
        </Link>
      ) : (
        <button
          type="button"
          aria-label={label}
          onClick={() => (back ? back() : router.back())}
          className={leadingBtn}
        >
          <Icon />
        </button>
      );
  }

  return (
    <header className={cn("flex items-start gap-3", className)}>
      {leading}
      <div className="min-w-0 flex-1 pt-0.5">
        <h1 className="display truncate text-xl leading-7">{title}</h1>
        {subtitle ? (
          <p className="mt-0.5 text-[13px] text-fg-muted">{subtitle}</p>
        ) : null}
      </div>
      {trailing ? <div className="shrink-0">{trailing}</div> : null}
    </header>
  );
}
