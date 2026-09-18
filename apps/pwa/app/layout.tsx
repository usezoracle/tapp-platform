import type { Metadata, Viewport } from "next";
import { Bricolage_Grotesque, DM_Sans, Inter } from "next/font/google";
import "./globals.css";
import { QueryProvider } from "@/lib/query-provider";
import { SessionProvider } from "@/lib/auth";
import { ServiceWorkerRegistrar } from "./register-sw";
import { ThemeProvider } from "@/components/ThemeProvider";
import { AppShell } from "@/components/AppShell";
import { BottomNav } from "@/components/BottomNav";

// Zap-style components
import { LogoOutlineBg } from "@/components/ui/LogoOutlineBg";
import { CookieConsent } from "@/components/ui/CookieConsent";

const inter = Inter({
  variable: "--font-inter",
  subsets: ["latin"],
});

// Display face: balance, big numerals, page titles only.
const bricolage = Bricolage_Grotesque({
  variable: "--font-bricolage",
  subsets: ["latin"],
  weight: ["500", "600", "700"],
});

// Landing page + brand wordmark (the app body keeps Inter).
const dmSans = DM_Sans({
  variable: "--font-dm-sans",
  subsets: ["latin"],
  style: ["normal", "italic"],
});

export const metadata: Metadata = {
  title: "Tapp — Tap. Pay. Done.",
  description:
    "Customers tap to pay in USDC. Merchants get money in their bank — without ever touching crypto.",
  manifest: "/manifest.json",
  appleWebApp: {
    capable: true,
    title: "Tapp by Zoracle Labs",
    statusBarStyle: "default",
  },
  icons: {
    icon: "/favicon.ico",
    apple: "/icon-192.png",
  },
};

export const viewport: Viewport = {
  themeColor: "#FFFFFF",
  width: "device-width",
  initialScale: 1,
  // Transactional surface — accidental pinch-zooms on a PIN pad =
  // bad UX. Use the OS-level text-size setting for accessibility.
  maximumScale: 1,
  userScalable: false,
};

/**
 * Root layout — mirrors paycrest/zap's shape: fixed navbar at top,
 * mobile-first centered column for content, footer at the bottom,
 * dark mode driven by `next-themes`. Each page renders into the
 * `<main>` slot inside `max-w-mobile`.
 */
export default function RootLayout({
  children,
}: Readonly<{
  children: React.ReactNode;
}>) {
  return (
    <html
      lang="en"
      className={`${inter.variable} ${bricolage.variable} ${dmSans.variable} h-full antialiased`}
      suppressHydrationWarning
    >
      <body className="min-h-full bg-surface text-fg transition-colors">
        <ThemeProvider>
          <SessionProvider>
            <QueryProvider>
              <AppShell>{children}</AppShell>
              <BottomNav />
              <LogoOutlineBg />
              <CookieConsent />
            </QueryProvider>
          </SessionProvider>
        </ThemeProvider>
        <ServiceWorkerRegistrar />
      </body>
    </html>
  );
}
