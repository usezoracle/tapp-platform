"use client";

import { useState, useEffect } from "react";
import { motion, AnimatePresence } from "framer-motion";
import { PiWarningOctagon } from "react-icons/pi";
import { primaryBtnClasses, secondaryBtnClasses } from "./Styles";

export function Disclaimer() {
  const [showDisclaimer, setShowDisclaimer] = useState(false);

  useEffect(() => {
    const hasAcceptedDisclaimer = localStorage.getItem("hasAcceptedDisclaimer");
    if (!hasAcceptedDisclaimer) {
      setShowDisclaimer(true);
    }
  }, []);

  const handleAccept = () => {
    localStorage.setItem("hasAcceptedDisclaimer", "true");
    setShowDisclaimer(false);
  };

  const handleClose = () => {
    // Navigate back in history
    if (typeof window !== "undefined") {
      window.history.back();
    }
  };

  return (
    <AnimatePresence>
      {showDisclaimer && (
        <motion.div
          initial={{ opacity: 0 }}
          animate={{ opacity: 1 }}
          exit={{ opacity: 0 }}
          transition={{ duration: 0.3 }}
          className="fixed inset-0 z-[150] grid min-h-screen place-items-center gap-4 bg-black/50 backdrop-blur-sm p-4"
        >
          <motion.div
            initial={{ scale: 0.9, opacity: 0 }}
            animate={{ scale: 1, opacity: 1 }}
            exit={{ scale: 0.9, opacity: 0 }}
            transition={{ duration: 0.3 }}
            className="grid w-full max-w-[400px] gap-5 rounded-3xl bg-white p-6 shadow-xl dark:bg-neutral-800"
          >
            <div className="flex items-center gap-3">
              <PiWarningOctagon className="text-3xl text-amber-500" />
              <h2 className="text-xl font-semibold text-neutral-900 dark:text-white">
                Heads up — early access
              </h2>
            </div>
            <div className="space-y-3 text-sm leading-relaxed text-neutral-600 dark:text-white/80">
              <p>
                Freedom is in early access. Card linking, top-ups, and merchant
                taps run against the Sui network — operations are real and
                balances move on-chain.
              </p>
              <p>
                Use small amounts while we iterate. Keep your Google account
                secure: it&apos;s the only key to your Freedom spending cap.
                We&apos;ll never ask for your PIN.
              </p>
            </div>

            <div className="flex items-center justify-between gap-4 pt-2">
              <button
                type="button"
                onClick={handleClose}
                className={secondaryBtnClasses}
              >
                Go Back
              </button>
              <button
                type="button"
                onClick={handleAccept}
                className={primaryBtnClasses}
              >
                I understand
              </button>
            </div>
          </motion.div>
        </motion.div>
      )}
    </AnimatePresence>
  );
}
