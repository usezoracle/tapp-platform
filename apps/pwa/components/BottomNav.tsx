"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { motion } from "framer-motion";
import { cn } from "@/lib/utils";
import { TAB_ITEMS, hueStyle, isAuthRoute, type NavItem } from "@/lib/nav";
import { DuotoneIcon } from "@/components/ui/DuotoneIcon";
import { useSession } from "@/lib/auth";
import { SPRINGS, useHaptic, useMotionPrefs } from "@/lib/motion";

type Tab = NavItem;

/**
 * Pay sits in the middle and is the prominent one: it is the tab that
 * gets used standing at a counter, with one hand. The labels and icons
 * are the rail's own (lib/nav.ts), so a phone and a laptop name the same
 * places the same way.
 */
const PROMINENT = "/cash";

export function shouldShowBottomNav(pathname: string): boolean {
  if (pathname.startsWith("/demo-deck")) return false;
  if (isAuthRoute(pathname)) return false;
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
        {TAB_ITEMS.map((tab) =>
          tab.href === PROMINENT ? (
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

  return (
    <li className="flex-1">
      <Link
        href={tab.href}
        aria-current={active ? "page" : undefined}
        onClick={() => haptic.light()}
        style={hueStyle(tab.hue)}
        className="focus-ring flex flex-col items-center gap-1 rounded-md py-1 touch-manipulation"
      >
        <span className="relative grid h-8 w-12 place-items-center">
          {/* The tile behind the active glyph takes the item's own hue; the
              sliding pill is one element carried between tabs, and the
              colour on it changes with the tab it lands on. */}
          {active && !reduced ? (
            <motion.span
              layoutId="bn-active-pill"
              transition={SPRINGS.default}
              className="hue-tint absolute inset-0 rounded-md"
              aria-hidden
            />
          ) : null}
          {active && reduced ? (
            <span aria-hidden className="hue-tint absolute inset-0 rounded-md" />
          ) : null}
          <DuotoneIcon
            name={tab.icon}
            size={22}
            className={cn("relative z-10 transition-colors", active ? "hue-text" : "text-fg-muted")}
          />
        </span>
        <span
          className={cn(
            "text-[11px] font-medium leading-none transition-colors",
            active ? "hue-text" : "text-fg-subtle",
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
  return (
    <li className="flex-1">
      <Link
        href={tab.href}
        aria-current={active ? "page" : undefined}
        onClick={() => haptic.medium()}
        style={hueStyle(tab.hue)}
        className="focus-ring flex flex-col items-center gap-1 rounded-md py-1 touch-manipulation"
      >
        <motion.span
          whileTap={{ scale: 0.9 }}
          transition={SPRINGS.tight}
          className="relative grid h-8 w-12 place-items-center"
        >
          {/* Always tinted: this is the tab pressed at a counter, and it
              stays findable from any screen. */}
          <span aria-hidden className="hue-tint absolute inset-0 rounded-md" />
          <DuotoneIcon name={tab.icon} size={22} className="hue-text relative z-10" />
        </motion.span>
        <span
          className={cn(
            "text-[11px] font-medium leading-none transition-colors",
            active ? "hue-text" : "text-fg-muted",
          )}
        >
          {tab.label}
        </span>
      </Link>
    </li>
  );
}
