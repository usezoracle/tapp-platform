"use client";

import Link from "next/link";
import { usePathname } from "next/navigation";
import { useEffect, useState } from "react";
import { Logo } from "@/components/ui/Logo";
import { ThemeSwitch } from "@/components/ThemeSwitch";
import { useSession } from "@/lib/auth";
import { Button } from "@/components/ui/Button";
import { shouldShowBottomNav } from "./BottomNav";

export function Navbar() {
  const { hydrated, session, clear } = useSession();
  const pathname = usePathname() ?? "";
  const [mounted, setMounted] = useState(false);

  useEffect(() => setMounted(true), []);

  if (!mounted) {
    return (
      <header className="fixed left-0 top-0 z-20 w-full border-b border-line bg-surface transition-colors">
        <nav className="mx-auto flex h-14 w-full max-w-mobile items-center justify-between px-4">
          <Logo />
        </nav>
      </header>
    );
  }

  const isLoggedIn = hydrated && !!session;
  const navVisible = isLoggedIn && shouldShowBottomNav(pathname);

  return (
    <header className="fixed left-0 top-0 z-20 w-full border-b border-line bg-surface transition-colors">
      <nav
        aria-label="Navbar"
        className="mx-auto flex h-14 w-full max-w-mobile items-center justify-between px-4 text-fg"
      >
        <Link href={isLoggedIn ? "/" : "/sign-in"} className="focus-ring flex items-center rounded-sm">
          <Logo />
        </Link>
        <div className="flex items-center gap-2 text-sm">
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
