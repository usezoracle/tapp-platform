import type { Finding } from "@/lib/api";
import { Check } from "@/components/ui/Stepper";
import { cn } from "@/lib/utils";

/** What the market checked, one row per criterion. */
export function Findings({ findings }: { findings: Finding[] }) {
  if (findings.length === 0) {
    return <p className="px-4 py-3 text-[13px] text-fg-muted">The market has not recorded any findings yet.</p>;
  }
  return (
    <ul className="divide-y divide-line">
      {findings.map((f, i) => (
        <li key={`${f.criterion}-${i}`} className="flex items-start gap-3 px-4 py-2.5">
          <span
            aria-label={f.met ? "Met" : "Not met"}
            className={cn(
              "mt-0.5 grid size-4 shrink-0 place-items-center rounded-full",
              f.met ? "bg-ok-bg text-ok-fg" : "bg-err-bg text-err-fg",
            )}
          >
            {f.met ? <Check className="size-2.5" /> : <Cross />}
          </span>
          <span className="min-w-0 flex-1">
            <span className="block text-[13px] text-fg">{humanise(f.criterion)}</span>
            {f.detail ? <span className="block text-xs text-fg-muted tabular-nums">{f.detail}</span> : null}
          </span>
        </li>
      ))}
    </ul>
  );
}

function Cross() {
  return (
    <svg className="size-2.5" viewBox="0 0 16 16" fill="none" aria-hidden>
      <path d="M4 4l8 8M12 4l-8 8" stroke="currentColor" strokeWidth="2" strokeLinecap="round" />
    </svg>
  );
}

/** "free_float" -> "Free float". Criteria are the market's names; this only spaces them. */
export function humanise(s: string): string {
  const spaced = s.replace(/[_-]+/g, " ").trim();
  return spaced.charAt(0).toUpperCase() + spaced.slice(1);
}
