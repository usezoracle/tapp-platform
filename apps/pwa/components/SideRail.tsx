"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import {
  PiWalletBold,
  PiClockCounterClockwiseBold,
  PiQrCodeBold,
  PiCreditCardBold,
  PiChartLineUpBold,
  PiGearSixBold,
  PiSignOutBold,
} from "react-icons/pi";
import type { IconType } from "react-icons";
import { cn } from "@/lib/utils";
import { useSession } from "@/lib/auth";
import { TappMark } from "@/components/ui/Logo";
import { ThemeSwitch } from "@/components/ThemeSwitch";
import { Web3Avatar } from "@/components/ui/Web3Avatar";

interface Item {
  href: string;
  label: string;
  icon: IconType;
  match: (pathname: string) => boolean;
}

/**
 * The left rail, from 768px up. It replaces both the bottom tabs and the
 * top navbar on those widths, so everything the two of them carried lives
 * here: the mark, who is signed in, the places to go, the theme, and the
 * way out.
 *
 * Wallet and Activity come first because they are what people open the
 * app for; Pay and Card are the two ways money leaves; Holdings is what a
 * card earns; Settings last.
 */
const ITEMS: Item[] = [
  { href: "/",              label: "Wallet",   icon: PiWalletBold,                match: (p) => p === "/" || p === "/wallet" },
  { href: "/history",       label: "Activity", icon: PiClockCounterClockwiseBold, match: (p) => p === "/history" },
  { href: "/pay",           label: "Pay",      icon: PiQrCodeBold,                match: (p) => p === "/pay" },
  { href: "/settings/card", label: "Card",     icon: PiCreditCardBold,            match: (p) => p === "/settings/card" || p.startsWith("/settings/limits") || p.startsWith("/cards/") || p.startsWith("/link") },
  { href: "/holdings",      label: "Holdings", icon: PiChartLineUpBold,           match: (p) => p.startsWith("/holdings") },
  { href: "/settings",      label: "Settings", icon: PiGearSixBold,               match: (p) => (p === "/settings" || p.startsWith("/settings/")) && p !== "/settings/card" && !p.startsWith("/settings/limits") },
];

const itemClasses =
  "focus-ring flex h-8 items-center gap-2.5 rounded-md px-2 text-[13px] font-medium transition-colors [&>svg]:size-4 [&>svg]:shrink-0";

export function SideRail() {
  const pathname = usePathname() ?? "";
  const { session, clear } = useSession();

  return (
    <aside
      aria-label="Primary"
      className="fixed inset-y-0 left-0 z-20 hidden w-rail flex-col border-r border-line bg-surface transition-colors md:flex"
    >
      {/* Workspace row: the mark and who is signed in. */}
      <Link
        href="/"
        className="focus-ring m-2 flex h-11 items-center gap-2.5 rounded-md px-2 hover:bg-hover"
      >
        <TappMark className="size-6" />
        <span className="grid min-w-0 flex-1">
          <span
            className="truncate text-sm font-bold italic leading-5 tracking-tight text-fg"
            style={{ fontFamily: "var(--font-dm-sans), var(--font-sans)" }}
          >
            tapp
          </span>
          <span className="truncate text-xs leading-4 text-fg-muted">{session?.email ?? ""}</span>
        </span>
        {session ? <Web3Avatar address={session.email} size={20} /> : null}
      </Link>

      <nav className="mt-2 grid gap-px px-2">
        {ITEMS.map((item) => {
          const active = item.match(pathname);
          const Icon = item.icon;
          return (
            <Link
              key={item.href}
              href={item.href}
              aria-current={active ? "page" : undefined}
              className={cn(
                itemClasses,
                active ? "bg-sunken text-fg" : "text-fg-muted hover:bg-hover hover:text-fg",
              )}
            >
              <Icon className={active ? "text-fg" : "text-fg-subtle"} />
              {item.label}
            </Link>
          );
        })}
      </nav>

      <div className="mt-auto grid gap-2 border-t border-line p-3">
        <div className="flex items-center justify-between gap-2">
          <span className="text-xs text-fg-muted">Theme</span>
          <ThemeSwitch />
        </div>
        <button
          type="button"
          onClick={clear}
          className={cn(itemClasses, "-mx-1 text-fg-muted hover:bg-hover hover:text-fg")}
        >
          <PiSignOutBold className="text-fg-subtle" />
          Sign out
        </button>
      </div>
    </aside>
  );
}
