/**
 * A crosshair mark for the corners of a hairline grid (the goat device).
 * 17px, centred on the corner, in the strong hairline grey.
 */
type Position = "top-left" | "top-right" | "bottom-left" | "bottom-right" | "top-center" | "bottom-center";

const pos: Record<Position, string> = {
  "top-left": "-top-[8.5px] -left-[8.5px]",
  "top-right": "-top-[8.5px] -right-[8.5px]",
  "bottom-left": "-bottom-[8.5px] -left-[8.5px]",
  "bottom-right": "-bottom-[8.5px] -right-[8.5px]",
  "top-center": "-top-[8.5px] left-1/2 -translate-x-1/2",
  "bottom-center": "-bottom-[8.5px] left-1/2 -translate-x-1/2",
};

export function Cross({ position, className = "" }: { position: Position; className?: string }) {
  return (
    <svg
      width="17"
      height="17"
      viewBox="0 0 17 17"
      aria-hidden
      className={`absolute z-10 pointer-events-none text-line-strong ${pos[position]} ${className}`}
    >
      <path d="M8.9 7.84h8v.8h-8v8.22H8V8.64H0v-.8h8V0h.9v7.84Z" fill="currentColor" />
    </svg>
  );
}

/** All four corners at once. */
export function Corners({ center = false }: { center?: boolean }) {
  return (
    <>
      <Cross position="top-left" />
      <Cross position="top-right" />
      <Cross position="bottom-left" />
      <Cross position="bottom-right" />
      {center ? (
        <>
          <Cross position="top-center" className="hidden md:block" />
          <Cross position="bottom-center" className="hidden md:block" />
        </>
      ) : null}
    </>
  );
}
