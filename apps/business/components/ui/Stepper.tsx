import { cn } from "@/lib/utils";

/** Four numbered steps in a row; done steps are quiet, the current one is dark. */
export function Stepper({
  steps,
  current,
  onSelect,
}: {
  steps: string[];
  current: number;
  /** Lets a merchant go back to a completed step; forward is via the form. */
  onSelect?: (i: number) => void;
}) {
  return (
    <ol className="flex items-center gap-2 overflow-x-auto" aria-label="Steps">
      {steps.map((s, i) => {
        const state = i < current ? "done" : i === current ? "current" : "todo";
        const clickable = onSelect && i < current;
        return (
          <li key={s} className="flex items-center gap-2">
            <button
              type="button"
              disabled={!clickable}
              onClick={() => onSelect?.(i)}
              aria-current={state === "current" ? "step" : undefined}
              className={cn(
                "focus-ring flex h-7 items-center gap-2 rounded-md pr-2 pl-1 text-[13px] transition-colors disabled:cursor-default",
                clickable && "hover:bg-tile",
              )}
            >
              <span
                className={cn(
                  "grid size-5 place-items-center rounded-full text-[11px] font-medium tabular-nums",
                  state === "current" && "bg-primary text-primary-fg",
                  state === "done" && "bg-ok-bg text-ok-fg",
                  state === "todo" && "bg-tile text-fg-subtle",
                )}
              >
                {state === "done" ? <Check /> : i + 1}
              </span>
              <span className={cn("whitespace-nowrap", state === "current" ? "font-medium text-fg" : "text-fg-muted")}>
                {s}
              </span>
            </button>
            {i < steps.length - 1 ? <span aria-hidden className="h-px w-4 bg-line" /> : null}
          </li>
        );
      })}
    </ol>
  );
}

export function Check({ className }: { className?: string }) {
  return (
    <svg className={cn("size-3", className)} viewBox="0 0 16 16" fill="none" aria-hidden>
      <path d="M3.5 8.5l3 3 6-7" stroke="currentColor" strokeWidth="1.8" strokeLinecap="round" strokeLinejoin="round" />
    </svg>
  );
}
