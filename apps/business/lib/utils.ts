import clsx, { type ClassValue } from "clsx";

export function cn(...inputs: ClassValue[]): string {
  return clsx(...inputs);
}

/** "2026-09-18T11:24:03Z" -> "18 Sep 2026". Dates only; the server owns money formatting. */
export function formatDate(iso: string | null | undefined): string {
  if (!iso) return "—";
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleDateString("en-GB", { day: "numeric", month: "short", year: "numeric" });
}

/** A cardholder ref is a user uuid. Show enough to recognise, not enough to use. */
export function maskRef(ref: string): string {
  if (!ref) return "—";
  if (ref.length <= 12) return ref;
  return `${ref.slice(0, 8)}…${ref.slice(-4)}`;
}
