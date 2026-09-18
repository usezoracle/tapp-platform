"use client";

import { Navbar } from "@/components/Navbar";
import { SideRail } from "@/components/SideRail";
import { Preloader } from "@/components/ui/Preloader";
import { Disclaimer } from "@/components/ui/Disclaimer";
import { shouldShowBottomNav } from "@/components/BottomNav";
import { useSession } from "@/lib/auth";
import { usePathname } from "next/navigation";
import { cn } from "@/lib/utils";

/**
 * The screens that have something to spread out on a wide window: the
 * wallet, activity, holdings and settings. Everything else is a flow --
 * one thing after another -- and reads best as a single 560px column no
 * matter how wide the window is.
 */
function isWideRoute(pathname: string): boolean {
  if (pathname === "/" || pathname === "/wallet" || pathname === "/history") return true;
  if (pathname === "/settings" || pathname === "/holdings") return true;
  if (pathname.startsWith("/holdings/")) return true;
  return false;
}

/**
 * Route-aware chrome shell.
 *
 * Below 768px: the fixed navbar on top, the bottom tabs, and a single
 * 428px column with 16px gutters. That is the phone experience and it
 * does not change.
 *
 * From 768px: the rail on the left takes over navigation (and the theme
 * switch and sign-out the navbar carried), the navbar and the tabs go
 * away, and the content sits beside the rail -- 1080px wide with 32px
 * gutters for the screens that use the room, a centred 560px column for
 * the flows that do not.
 *
 * Screens with no navigation (sign-in, a payment request, card linking)
 * keep the navbar on every width and centre the same way.
 */
export function AppShell({ children }: { children: React.ReactNode }) {
  const pathname = usePathname() ?? "";
  const { hydrated, session } = useSession();

  if (pathname.startsWith("/demo-deck")) return <>{children}</>;

  const rail = hydrated && !!session && shouldShowBottomNav(pathname);
  const wide = rail && isWideRoute(pathname);

  return (
    <>
      <Preloader />
      <Navbar className={rail ? "md:hidden" : undefined} />
      {rail ? <SideRail /> : null}
      <div
        className={cn(
          "relative z-10 flex min-h-screen w-full flex-col",
          rail && "md:pl-rail",
        )}
      >
        <div
          className={cn(
            "mx-auto flex w-full max-w-mobile flex-1 flex-col px-4 pt-20 pb-24",
            rail ? "md:pt-10 md:pb-16" : "md:pt-24 md:pb-16",
            wide ? "md:max-w-content md:px-8" : "md:max-w-flow md:px-8",
          )}
        >
          <main className="w-full flex-grow">{children}</main>
        </div>
      </div>
      <Disclaimer />
    </>
  );
}
