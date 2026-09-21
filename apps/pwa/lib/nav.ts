import type { CSSProperties } from "react";
import type { DuotoneName } from "@/components/ui/DuotoneIcon";

/**
 * The screens that sign somebody in. They carry no navigation, and the
 * page draws the wordmark itself, so the navbar keeps its brand off them:
 * one logo on a screen, not two.
 */
export function isAuthRoute(pathname: string): boolean {
  return pathname.startsWith("/sign-in");
}

export interface NavItem {
  href: string;
  label: string;
  /** The duotone glyph, drawn in `hue` at 18px in the rail and 22px in the tabs. */
  icon: DuotoneName;
  /** The item's colour token in app/globals.css (`--nav-wallet`, …). */
  hue: string;
  match: (pathname: string) => boolean;
}

const isCard = (p: string) =>
  p === "/settings/card" ||
  p.startsWith("/settings/limits") ||
  p.startsWith("/cards/") ||
  p.startsWith("/link");

/**
 * The places to go, in one list, so the rail and the bottom tabs say the
 * same words over the same icons. They used to be two lists: the tab
 * said "Cash" where the rail said "Pay", and drew the wallet with a
 * different glyph.
 *
 * Wallet and Activity come first because they are what people open the
 * app for; Cash is the pledge -- handing cash to an agent for wallet money
 * and back -- and Card is the other way money moves; Holdings is what a
 * card earns; Settings last. The tabs drop Holdings -- five is the most
 * a thumb can tell apart -- and the Stocks tile on the wallet links to it.
 *
 * The navigation is one colour: every glyph is drawn duotone in `--nav`
 * (the accent), so the rail reads as one instrument rather than six. The
 * semantic hues (`--nav-card` violet, `--nav-holdings` green, …) still
 * colour the tiles and rows on the screens themselves, where meaning is
 * what the colour carries.
 */
export const NAV_ITEMS: NavItem[] = [
  { href: "/",              label: "Wallet",   icon: "wallet",   hue: "--nav", match: (p) => p === "/" || p === "/wallet" },
  { href: "/history",       label: "Activity", icon: "activity", hue: "--nav", match: (p) => p === "/history" },
  { href: "/cash",          label: "Cash",     icon: "cash",     hue: "--nav", match: (p) => p === "/cash" || p.startsWith("/cash/") },
  { href: "/settings/card", label: "Card",     icon: "card",     hue: "--nav", match: isCard },
  { href: "/holdings",      label: "Holdings", icon: "chart",    hue: "--nav", match: (p) => p.startsWith("/holdings") },
  { href: "/settings",      label: "Settings", icon: "gear",     hue: "--nav", match: (p) => (p === "/settings" || p.startsWith("/settings/")) && !isCard(p) },
];

/** The five that fit under a thumb. */
export const TAB_ITEMS: NavItem[] = NAV_ITEMS.filter((i) => i.href !== "/holdings");

/** The inline style that hands an item's hue to the hue-* utilities. */
export const hueStyle = (token: string): CSSProperties =>
  ({ "--hue": `var(${token})` }) as CSSProperties;
