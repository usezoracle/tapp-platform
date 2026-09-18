"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import type { ReactNode } from "react";
import { useSession } from "@/lib/auth";
import { cn } from "@/lib/utils";

const nav = [
  { href: "/business", label: "Your business" },
  { href: "/business/holders", label: "Holders" },
];

/**
 * Top bar and the centred 880px column. Desktop-first; at phone width the bar
 * keeps its links and the column takes the full width with a 16px gutter.
 */
export function AppShell({ children }: { children: ReactNode }) {
  const { session, hydrated, clear } = useSession();
  const pathname = usePathname();

  return (
    <div className="flex min-h-dvh flex-col">
      <div className="border-b border-line">
        <div className="mx-auto flex h-12 w-full max-w-[880px] items-center gap-6 px-4">
          <Link href="/" className="focus-ring flex items-center gap-2 rounded-sm">
            <span aria-hidden className="grid size-5 place-items-center rounded-sm bg-primary text-[10px] font-semibold text-primary-fg">
              T
            </span>
            <span className="text-[13px] font-medium">Tapp Business</span>
          </Link>
          {session ? (
            <nav className="flex items-center gap-1" aria-label="Main">
              {nav.map((n) => {
                const active = n.href === "/business" ? pathname === "/business" || pathname === "/business/list" : pathname.startsWith(n.href);
                return (
                  <Link
                    key={n.href}
                    href={n.href}
                    className={cn(
                      "focus-ring rounded-md px-2.5 py-1.5 text-[13px] transition-colors",
                      active ? "bg-tile text-fg" : "text-fg-muted hover:bg-tile hover:text-fg",
                    )}
                  >
                    {n.label}
                  </Link>
                );
              })}
            </nav>
          ) : null}
          <div className="ml-auto flex items-center gap-3">
            {hydrated && session ? (
              <>
                <span className="hidden max-w-[220px] truncate text-xs text-fg-subtle sm:inline">{session.email}</span>
                <button
                  type="button"
                  onClick={clear}
                  className="focus-ring rounded-md px-2 py-1 text-[13px] text-fg-muted transition-colors hover:bg-tile hover:text-fg"
                >
                  Sign out
                </button>
              </>
            ) : null}
          </div>
        </div>
      </div>
      <main className="mx-auto w-full max-w-[880px] flex-1 px-4 py-8">{children}</main>
    </div>
  );
}
