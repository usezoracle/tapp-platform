"use client";

import { useEffect, useState } from "react";
import { useRouter } from "next/navigation";
import { PiKeyboardBold } from "react-icons/pi";
import { Screen } from "@/components/ui/Screen";
import { Button } from "@/components/ui/Button";
import { QRScanner } from "@/components/ui/QRScanner";
import { InputError } from "@/components/ui/InputError";
import { useSession } from "@/lib/auth";

/**
 * Pay flow entry — full-screen scanner.
 *
 * Accepts either a checkout URL as the merchant app broadcasts it
 * (https://…/pay/<uuid>) or a bare id typed in, and routes to /pay/[id].
 */
export default function PayPage() {
  const router = useRouter();
  const { hydrated, session } = useSession();
  const [scanning, setScanning] = useState(true);
  const [manual, setManual] = useState("");
  const [error, setError] = useState<string | null>(null);

  useEffect(() => {
    if (hydrated && !session) router.replace("/sign-in?next=/pay");
  }, [hydrated, session, router]);

  function handleResult(text: string) {
    setScanning(false);
    const id = extractCheckoutId(text);
    if (!id) {
      setError("That QR isn't a Freedom payment request. Try again.");
      return;
    }
    router.replace(`/pay/${encodeURIComponent(id)}`);
  }

  function submitManual() {
    if (!manual.trim()) return;
    const id = extractCheckoutId(manual.trim());
    if (!id) {
      setError("Couldn't read that link. Paste a Freedom payment link.");
      return;
    }
    router.replace(`/pay/${encodeURIComponent(id)}`);
  }

  if (!hydrated || !session) return <Screen />;

  if (scanning) {
    return (
      <QRScanner
        onResult={handleResult}
        onClose={() => setScanning(false)}
      />
    );
  }

  return (
    <Screen>
      <div className="grid gap-6 py-10 text-sm text-neutral-900 dark:text-white">
        <div className="space-y-2">
          <h1 className="text-xl font-medium">Paste a payment link</h1>
          <p className="text-sm text-gray-500 dark:text-white/50">
            Or restart the scanner.
          </p>
        </div>

        <div className="grid gap-2 rounded-3xl border border-gray-200 p-4 dark:border-white/10">
          <label
            htmlFor="manual"
            className="text-xs font-medium uppercase tracking-wider text-gray-400 dark:text-white/30"
          >
            Order URL or ID
          </label>
          <input
            id="manual"
            type="text"
            inputMode="url"
            autoCapitalize="off"
            spellCheck={false}
            value={manual}
            onChange={(e) => setManual(e.target.value)}
            placeholder="https://app.usetapp.xyz/order/…"
            className="w-full rounded-xl border border-gray-300 bg-white px-4 py-2 text-sm transition-all placeholder:text-gray-400 focus:outline-none focus:ring-2 focus:ring-blue-500 focus:ring-opacity-50 dark:border-white/20 dark:bg-neutral-900 dark:text-white/80 dark:placeholder:text-white/30"
          />
        </div>

        {error ? <InputError message={error} /> : null}

        <div className="flex gap-3">
          <Button
            variant="secondary"
            onClick={() => {
              setError(null);
              setScanning(true);
            }}
            leadingIcon={<PiKeyboardBold className="text-base" />}
          >
            Scan again
          </Button>
          <Button onClick={submitManual} disabled={!manual.trim()}>
            Continue
          </Button>
        </div>
      </div>
    </Screen>
  );
}

/**
 * A checkout id, from a scanned URL or something typed in.
 *
 * A checkout id is a UUID, so that is what is accepted. The predecessor
 * matched any 4-64 characters of [A-Za-z0-9_-], which let a QR code from
 * anywhere at all route the app to a detail page that then 404'd -- the error
 * arrived one screen too late to say anything useful.
 */
const UUID = /^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$/i;

function extractCheckoutId(text: string): string | null {
  const trimmed = text.trim();
  if (!trimmed) return null;
  try {
    const url = new URL(trimmed);
    const match = url.pathname.match(/\/pay\/([^/]+)\/?$/);
    const id = match?.[1] ? decodeURIComponent(match[1]) : null;
    if (id && UUID.test(id)) return id;
    return null;
  } catch {
    // Not a URL -- treat it as a bare id.
  }
  return UUID.test(trimmed) ? trimmed : null;
}
