"use client";

import { useCallback, useEffect, useRef, useState } from "react";
import { createPortal } from "react-dom";
import Link from "next/link";
import { AnimatePresence, motion } from "framer-motion";
import { PiXBold } from "react-icons/pi";
import { useSession } from "@/lib/auth";
import { useKycStatus } from "@/lib/ledger";
import { CURVES, useMotionPrefs } from "@/lib/motion";
import { hueStyle } from "@/lib/nav";
import { cn } from "@/lib/utils";
import { Button } from "./Button";
import { DuotoneIcon, type DuotoneName } from "./DuotoneIcon";
import { IconMark } from "./Logo";
import { tileBaseClasses } from "./Styles";

/**
 * The first-run ask: verify, so the account can do what it is for.
 *
 * Opens over the wallet once, for somebody whose identity is not on file
 * (tier 0 from /v1/kyc), and never while that status is still loading -- a
 * prompt that flashes up before the answer arrives is a prompt that shows
 * to the wrong people. Any way out (the button, "Not now", Esc, the scrim,
 * the X) is remembered for seven days against the email it was shown to,
 * so a second account on the same phone still gets asked. If storage is
 * unavailable the module-level flag below still keeps it to once a session.
 */
const DISMISSED_KEY = "freedom.verify-prompt.dismissed";
const DISMISS_FOR_MS = 7 * 24 * 60 * 60 * 1000;
export const KYC_ROUTE = "/settings/kyc";

let shownThisSession = false;

/**
 * The early-access notice (components/ui/Disclaimer.tsx) owns the first
 * moments of a first run and sits above everything. Two dialogs at once is
 * one too many, and a stray Esc meant for that one would silently dismiss
 * this one underneath it for a week -- so this waits its turn.
 */
function disclaimerAccepted(): boolean {
  try {
    return !!window.localStorage.getItem("hasAcceptedDisclaimer");
  } catch {
    return true;
  }
}

function dismissedRecently(email: string): boolean {
  try {
    const raw = window.localStorage.getItem(DISMISSED_KEY);
    if (!raw) return false;
    const rec = JSON.parse(raw) as { email?: string; at?: number };
    return rec.email === email && typeof rec.at === "number" && Date.now() - rec.at < DISMISS_FOR_MS;
  } catch {
    return false;
  }
}

function rememberDismissal(email: string): void {
  try {
    window.localStorage.setItem(DISMISSED_KEY, JSON.stringify({ email, at: Date.now() }));
  } catch {
    // Private mode or a full quota: the session flag still holds.
  }
}

const STEPS: Array<{ icon: DuotoneName; hue: string; label: string }> = [
  { icon: "id-card", hue: "--nav-wallet", label: "Your BVN" },
  { icon: "selfie", hue: "--nav-wallet", label: "A quick selfie" },
  { icon: "check", hue: "--nav-holdings", label: "Done" },
];

export function VerifyPrompt() {
  const { session } = useSession();
  const kyc = useKycStatus();
  const [open, setOpen] = useState(false);
  const email = session?.email;

  useEffect(() => {
    if (!email || !kyc.data) return;
    if (kyc.data.tier !== 0) return;
    if (shownThisSession || dismissedRecently(email)) return;

    function show() {
      shownThisSession = true;
      setOpen(true);
    }
    if (disclaimerAccepted()) {
      show();
      return;
    }
    // The notice does not announce its acceptance; look for it now and then.
    const timer = window.setInterval(() => {
      if (!disclaimerAccepted()) return;
      window.clearInterval(timer);
      show();
    }, 500);
    return () => window.clearInterval(timer);
  }, [email, kyc.data]);

  const close = useCallback(() => {
    if (email) rememberDismissal(email);
    setOpen(false);
  }, [email]);

  if (typeof document === "undefined") return null;
  return createPortal(<Dialog open={open} onClose={close} />, document.body);
}

const FOCUSABLE = 'a[href], button:not([disabled]), [tabindex]:not([tabindex="-1"])';

function Dialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const panel = useRef<HTMLDivElement>(null);
  const { reduced } = useMotionPrefs();

  // Focus lives inside the card while it is open, Esc closes it, and the page
  // behind it does not scroll. Whatever had focus before gets it back.
  useEffect(() => {
    if (!open) return;
    const el = panel.current;
    if (!el) return;
    const before = document.activeElement as HTMLElement | null;
    el.focus();

    function onKey(e: KeyboardEvent) {
      if (e.key === "Escape") {
        e.preventDefault();
        onClose();
        return;
      }
      if (e.key !== "Tab" || !el) return;
      const items = Array.from(el.querySelectorAll<HTMLElement>(FOCUSABLE));
      if (items.length === 0) return;
      const first = items[0];
      const last = items[items.length - 1];
      const active = document.activeElement;
      if (e.shiftKey && (active === first || active === el)) {
        e.preventDefault();
        last.focus();
      } else if (!e.shiftKey && active === last) {
        e.preventDefault();
        first.focus();
      }
    }

    document.addEventListener("keydown", onKey);
    const overflow = document.body.style.overflow;
    document.body.style.overflow = "hidden";
    return () => {
      document.removeEventListener("keydown", onKey);
      document.body.style.overflow = overflow;
      before?.focus?.();
    };
  }, [open, onClose]);

  const duration = reduced ? 0 : 0.15;

  return (
    <AnimatePresence>
      {open ? (
        <motion.div
          key="verify-prompt"
          className="fixed inset-0 z-40 grid items-end bg-black/40 md:place-items-center"
          initial={{ opacity: 0 }}
          animate={{ opacity: 1 }}
          exit={{ opacity: 0 }}
          transition={{ duration, ease: CURVES.easeOut }}
          onClick={onClose}
        >
          <motion.div
            ref={panel}
            role="dialog"
            aria-modal="true"
            aria-labelledby="verify-prompt-title"
            aria-describedby="verify-prompt-body"
            tabIndex={-1}
            onClick={(e) => e.stopPropagation()}
            initial={reduced ? false : { opacity: 0, scale: 0.97, y: 8 }}
            animate={{ opacity: 1, scale: 1, y: 0 }}
            exit={reduced ? { opacity: 0 } : { opacity: 0, scale: 0.97, y: 8 }}
            transition={{ duration, ease: CURVES.easeOut }}
            className={cn(
              "relative grid w-full gap-5 bg-raised p-5 shadow-sheet outline-none",
              "rounded-t-xl pb-[max(env(safe-area-inset-bottom),1.25rem)]",
              "md:w-[420px] md:rounded-xl md:p-6 md:pb-6",
            )}
          >
            <button
              type="button"
              onClick={onClose}
              aria-label="Close"
              className="focus-ring absolute top-3 right-3 grid size-8 place-items-center rounded-md text-fg-muted transition-colors hover:bg-hover hover:text-fg [&>svg]:size-4"
            >
              <PiXBold />
            </button>

            <span className="grid size-12 place-items-center rounded-lg bg-primary" aria-hidden>
              <IconMark className="h-6" />
            </span>

            <div className="grid gap-2">
              <h2 id="verify-prompt-title" className="display text-[22px] leading-7">
                Verify your identity
              </h2>
              <p id="verify-prompt-body" className="text-sm leading-5 text-fg-muted">
                Verification gives you a naira account to deposit into, higher card
                limits, and cash-out.
              </p>
              <p className="text-[13px] leading-5 text-fg-muted">
                Takes about two minutes; you need your BVN.
              </p>
            </div>

            <ul className="grid gap-2">
              {STEPS.map((s) => (
                <li key={s.icon} className="flex items-center gap-3">
                  <span className={tileBaseClasses} style={hueStyle(s.hue)}>
                    <DuotoneIcon name={s.icon} className="hue-text" />
                  </span>
                  <span className="text-sm text-fg">{s.label}</span>
                </li>
              ))}
            </ul>

            <div className="grid gap-2 md:flex md:flex-row-reverse">
              <Link href={KYC_ROUTE} onClick={onClose} className="block md:w-auto">
                <Button className="md:w-auto" fullWidth>
                  Verify now
                </Button>
              </Link>
              <Button variant="ghost" onClick={onClose} className="md:w-auto">
                Not now
              </Button>
            </div>
          </motion.div>
        </motion.div>
      ) : null}
    </AnimatePresence>
  );
}
