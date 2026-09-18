"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { motion } from "framer-motion";
import {
  PiWalletFill,
  PiClockCounterClockwiseBold,
  PiMoneyWavyBold,
  PiCreditCardBold,
  PiGearSixBold,
} from "react-icons/pi";
import type { IconType } from "react-icons";
import { cn } from "@/lib/utils";
import { useSession } from "@/lib/auth";
import { SPRINGS, useHaptic, useMotionPrefs } from "@/lib/motion";

interface Tab {
  href: string;
  label: string;
  icon: IconType;
  match: (pathname: string) => boolean;
  prominent?: boolean;
}

// Cash sits in the middle, and it is the prominent one.
//
// This is a naira product before it is a crypto one: the largest group of
// people it is for hold physical notes and want them in a balance. Putting
// that behind a settings menu, with a QR scanner in the prominent slot, had
// the priorities exactly backwards.
const TABS: Tab[] = [
  { href: "/",              label: "Wallet",   icon: PiWalletFill,                match: (p) => p === "/" },
  { href: "/history",       label: "Activity", icon: PiClockCounterClockwiseBold, match: (p) => p === "/history" },
  { href: "/cash",          label: "Cash",     icon: PiMoneyWavyBold,             match: (p) => p.startsWith("/cash") || p.startsWith("/agents"), prominent: true },
  { href: "/settings/card", label: "Card",     icon: PiCreditCardBold,            match: (p) => p === "/settings/card" || p.startsWith("/settings/limits") },
  { href: "/settings",      label: "Settings", icon: PiGearSixBold,               match: (p) => (p === "/settings" || p.startsWith("/settings/")) && !p.startsWith("/settings/card") && !p.startsWith("/settings/limits") },
];

export function shouldShowBottomNav(pathname: string): boolean {
  if (pathname.startsWith("/demo-deck")) return false;
  if (pathname.startsWith("/sign-in")) return false;
  if (pathname.startsWith("/link")) return false;
  // A payment request opened from somebody else's phone is a single-purpose
  // screen. Offering navigation away from it mid-payment is an invitation to
  // lose the thread.
  if (pathname.startsWith("/pay/")) return false;
  if (pathname.startsWith("/cards/")) return false;
  return true;
}

export function BottomNav() {
  const pathname = usePathname() ?? "";
  const { hydrated, session } = useSession();

  if (!hydrated || !session) return null;
  if (!shouldShowBottomNav(pathname)) return null;

  return (
    <nav
      aria-label="Primary"
      className="fixed inset-x-0 bottom-0 z-30 border-t border-line bg-surface transition-colors md:hidden"
    >
      <ul className="mx-auto flex w-full max-w-mobile items-end justify-between px-3 pt-2 pb-[max(env(safe-area-inset-bottom),0.5rem)]">
        {TABS.map((tab) =>
          tab.prominent ? (
            <ProminentTab key={tab.href} tab={tab} pathname={pathname} />
          ) : (
            <RegularTab key={tab.href} tab={tab} pathname={pathname} />
          ),
        )}
      </ul>
    </nav>
  );
}

function RegularTab({ tab, pathname }: { tab: Tab; pathname: string }) {
  const active = tab.match(pathname);
  const haptic = useHaptic();
  const { reduced } = useMotionPrefs();
  const Icon = tab.icon;

  return (
    <li className="flex-1">
      <Link
        href={tab.href}
        aria-current={active ? "page" : undefined}
        onClick={() => haptic.light()}
        className="focus-ring flex flex-col items-center gap-1 rounded-md py-1 touch-manipulation"
      >
        <span className="relative grid h-8 w-12 place-items-center">
          {active && !reduced ? (
            <motion.span
              layoutId="bn-active-pill"
              transition={SPRINGS.default}
              className="absolute inset-0 rounded-md bg-accent-wash"
              aria-hidden
            />
          ) : null}
          {active && reduced ? (
            <span
              aria-hidden
              className="absolute inset-0 rounded-md bg-accent-wash"
            />
          ) : null}
          <Icon
            className={cn(
              "relative z-10 size-5 transition-colors",
              active ? "text-accent" : "text-fg-subtle",
            )}
          />
        </span>
        <span
          className={cn(
            "text-[11px] font-medium leading-none transition-colors",
            active ? "text-fg" : "text-fg-subtle",
          )}
        >
          {tab.label}
        </span>
      </Link>
    </li>
  );
}

function ProminentTab({ tab, pathname }: { tab: Tab; pathname: string }) {
  const active = tab.match(pathname);
  const haptic = useHaptic();
  const Icon = tab.icon;
  return (
    <li className="flex-1">
      <Link
        href={tab.href}
        aria-current={active ? "page" : undefined}
        onClick={() => haptic.medium()}
        className="focus-ring flex flex-col items-center gap-1 rounded-md py-1 touch-manipulation"
      >
        <motion.span
          whileTap={{ scale: 0.9 }}
          transition={SPRINGS.tight}
          className="relative grid h-8 w-12 place-items-center"
        >
          <span
            aria-hidden
            className="absolute inset-0 rounded-md bg-accent-wash"
          />
          <Icon className="relative z-10 size-5 text-accent" />
        </motion.span>
        <span
          className={cn(
            "text-[11px] font-medium leading-none transition-colors",
            active ? "text-fg" : "text-fg-muted",
          )}
        >
          {tab.label}
        </span>
      </Link>
    </li>
  );
}
