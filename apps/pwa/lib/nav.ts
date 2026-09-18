import {
  PiWalletBold,
  PiClockCounterClockwiseBold,
  PiQrCodeBold,
  PiCreditCardBold,
  PiChartLineUpBold,
  PiGearSixBold,
} from "react-icons/pi";
import type { IconType } from "react-icons";

export interface NavItem {
  href: string;
  label: string;
  icon: IconType;
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
 * app for; Pay and Card are the two ways money leaves; Holdings is what a
 * card earns; Settings last. The tabs drop Holdings -- five is the most
 * a thumb can tell apart -- and the shares module on the wallet links to it.
 */
export const NAV_ITEMS: NavItem[] = [
  { href: "/",              label: "Wallet",   icon: PiWalletBold,                match: (p) => p === "/" || p === "/wallet" },
  { href: "/history",       label: "Activity", icon: PiClockCounterClockwiseBold, match: (p) => p === "/history" },
  { href: "/pay",           label: "Pay",      icon: PiQrCodeBold,                match: (p) => p === "/pay" },
  { href: "/settings/card", label: "Card",     icon: PiCreditCardBold,            match: isCard },
  { href: "/holdings",      label: "Holdings", icon: PiChartLineUpBold,           match: (p) => p.startsWith("/holdings") },
  { href: "/settings",      label: "Settings", icon: PiGearSixBold,               match: (p) => (p === "/settings" || p.startsWith("/settings/")) && !isCard(p) },
];

/** The five that fit under a thumb. */
export const TAB_ITEMS: NavItem[] = NAV_ITEMS.filter((i) => i.href !== "/holdings");
