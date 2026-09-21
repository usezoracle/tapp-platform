"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useEffect, useState } from "react";
import { Logo } from "@/components/ui/Logo";
import { ThemeSwitch } from "@/components/ThemeSwitch";
import { useSession } from "@/lib/auth";
import { Button } from "@/components/ui/Button";
import { shouldShowBottomNav } from "./BottomNav";
import { isAuthRoute } from "@/lib/nav";
import { cn } from "@/lib/utils";

export function Navbar({ className }: { className?: string }) {
  const { hydrated, session, clear } = useSession();
  const pathname = usePathname() ?? "";
  const [mounted, setMounted] = useState(false);

  useEffect(() => setMounted(true), []);

  // The sign-in screen draws its own, larger wordmark above the form; the
  // navbar's would be a second one 40px above it.
  const brand = !isAuthRoute(pathname);

  if (!mounted) {
    return (
      <header className={cn("fixed left-0 top-0 z-20 w-full border-b border-line bg-surface transition-colors", className)}>
        <nav className="mx-auto flex h-14 w-full max-w-mobile items-center justify-between px-4 md:max-w-flow md:px-8">
          {brand ? <Logo /> : null}
        </nav>
      </header>
    );
  }

  const isLoggedIn = hydrated && !!session;
  const navVisible = isLoggedIn && shouldShowBottomNav(pathname);

  return (
    <header className={cn("fixed left-0 top-0 z-20 w-full border-b border-line bg-surface transition-colors", className)}>
      <nav
        aria-label="Navbar"
        className="mx-auto flex h-14 w-full max-w-mobile items-center justify-between px-4 text-fg md:max-w-flow md:px-8"
      >
        {brand ? (
          <Link href={isLoggedIn ? "/" : "/sign-in"} className="focus-ring flex items-center rounded-sm">
            <Logo />
          </Link>
        ) : null}
        <div className="ml-auto flex items-center gap-2 text-sm">
          {/* When the bottom tab nav is visible it owns wallet-nav + sign-out
              (via the Settings tab → Security). Keep the navbar minimal so the
              two pieces of chrome don't duplicate. */}
          {isLoggedIn && !navVisible && (
            <Button
              variant="secondary"
              size="sm"
              fullWidth={false}
              onClick={clear}
            >
              Sign out
            </Button>
          )}
          <ThemeSwitch />
        </div>
      </nav>
    </header>
  );
}
