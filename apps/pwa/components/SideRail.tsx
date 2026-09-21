"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { cn } from "@/lib/utils";
import { NAV_ITEMS, hueStyle } from "@/lib/nav";
import { DuotoneIcon } from "@/components/ui/DuotoneIcon";
import { useSession } from "@/lib/auth";
import { Logo } from "@/components/ui/Logo";
import { ThemeSwitch } from "@/components/ThemeSwitch";
import { Web3Avatar } from "@/components/ui/Web3Avatar";

/**
 * The left rail, from 768px up. It replaces both the bottom tabs and the
 * top navbar on those widths, so everything the two of them carried lives
 * here: the wordmark, who is signed in, the places to go, the theme, and the
 * way out. The places come from lib/nav.ts, shared with the tabs.
 */
const itemClasses =
  "focus-ring flex h-8 items-center gap-2.5 rounded-md px-2 text-[13px] font-medium transition-colors [&>svg]:shrink-0";

export function SideRail() {
  const pathname = usePathname() ?? "";
  const { session, clear } = useSession();

  return (
    <aside
      aria-label="Primary"
      className="fixed inset-y-0 left-0 z-20 hidden w-rail flex-col border-r border-line bg-surface transition-colors md:flex"
    >
      {/* The brand, alone at the top; who is signed in lives at the foot.
          Everything here sits on the 8px grid: 8px from the rail's edge to
          the 64px row, 8px from the row to the nav. The row's px-2 puts the
          mark's box on the same 16px line as the items' glyphs, so the mark
          and the icons below it share a left edge. */}
      <Link
        href="/"
        className="focus-ring m-2 flex h-16 items-center rounded-md px-2 hover:bg-hover"
        aria-label="Freedom home"
      >
        <Logo className="h-11" />
      </Link>

      <nav className="grid gap-px px-2">
        {NAV_ITEMS.map((item) => {
          const active = item.match(pathname);
          return (
            <Link
              key={item.href}
              href={item.href}
              aria-current={active ? "page" : undefined}
              style={hueStyle(item.hue)}
              className={cn(
                itemClasses,
                active ? "hue-tint hue-text" : "text-fg-muted hover:bg-hover hover:text-fg",
              )}
            >
              {/* The glyph keeps its hue when the item is at rest, at 70%,
                  and comes to full strength with the tint behind it. */}
              <DuotoneIcon name={item.icon} size={18} className={cn("hue-text", !active && "opacity-70")} />
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
        {session ? (
          <div className="flex min-w-0 items-center gap-2.5 px-1 py-1">
            <Web3Avatar address={session.email} size={24} />
            <span className="truncate text-xs leading-4 text-fg-muted">{session.email}</span>
          </div>
        ) : null}
        <button
          type="button"
          onClick={clear}
          className={cn(itemClasses, "-mx-1 text-fg-muted hover:bg-hover hover:text-fg")}
        >
          <DuotoneIcon name="sign-out" size={18} className="text-fg-subtle" />
          Sign out
        </button>
      </div>
    </aside>
  );
}
