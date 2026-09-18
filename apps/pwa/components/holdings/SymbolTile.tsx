import { cn } from "@/lib/utils";
import { holdingInitials } from "@/lib/holdings";

/** Quiet 28px tile with the business's initials. */
export function SymbolTile({
  name,
  className,
  size = "sm",
}: {
  name: string;
  className?: string;
  size?: "sm" | "lg";
}) {
  return (
    <span
      aria-hidden
      className={cn(
        "grid shrink-0 place-items-center rounded-sm bg-sunken font-medium text-fg-muted",
        size === "sm" ? "size-7 text-[11px]" : "size-10 rounded-md text-sm",
        className,
      )}
    >
      {holdingInitials(name)}
    </span>
  );
}
