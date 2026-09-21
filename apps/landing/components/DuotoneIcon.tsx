import type { SVGProps } from "react";

/**
 * Duotone icons, one hue per icon: a 1.5px stroke in `currentColor` over a
 * 22% fill of the same colour, on a 24px grid. The fill is the body of the
 * thing; the stroke is its edge and detail. Same approach as the app.
 */
export type DuotoneName = "tap" | "slice" | "chart" | "wallet" | "check" | "hourglass" | "receipt";

interface Glyph {
  fill: string;
  stroke: string;
  solid?: string;
}

const GLYPHS: Record<DuotoneName, Glyph> = {
  // A card with the contactless arcs.
  tap: {
    fill: "M2.5 8.5A2.5 2.5 0 0 1 5 6h10a2.5 2.5 0 0 1 2.5 2.5v8A2.5 2.5 0 0 1 15 19H5a2.5 2.5 0 0 1-2.5-2.5Z",
    stroke:
      "M2.5 8.5A2.5 2.5 0 0 1 5 6h10a2.5 2.5 0 0 1 2.5 2.5v8A2.5 2.5 0 0 1 15 19H5a2.5 2.5 0 0 1-2.5-2.5Z M2.5 10.5h15 M6 15h3.5 M19 9.5a4.5 4.5 0 0 1 0 6 M21.5 7.5a7.5 7.5 0 0 1 0 10",
  },
  // A disc with one slice lifted out: your share.
  slice: {
    fill: "M12 12V3a9 9 0 1 1-9 9h9Z",
    stroke: "M12 12V3a9 9 0 1 1-9 9h9Z",
    solid: "M10 10H1.1A9 9 0 0 1 10 1.1V10Z",
  },
  chart: {
    fill: "M3 16.5l5-5.5 4 3 6.5-7.5L21 8v11H3Z",
    stroke: "M3 16.5l5-5.5 4 3 6.5-7.5 M15.5 6.5H19v3.5 M3 20h18",
  },
  wallet: {
    fill: "M3 8a2.5 2.5 0 0 1 2.5-2.5h13A2.5 2.5 0 0 1 21 8v8.5a2.5 2.5 0 0 1-2.5 2.5h-13A2.5 2.5 0 0 1 3 16.5Z",
    stroke:
      "M3 8a2.5 2.5 0 0 1 2.5-2.5h13A2.5 2.5 0 0 1 21 8v8.5a2.5 2.5 0 0 1-2.5 2.5h-13A2.5 2.5 0 0 1 3 16.5Z M21 10.5h-4.5a2 2 0 0 0 0 4H21 M3 8V6.5A2 2 0 0 1 5 4.5h11",
    solid: "M16.5 11.5a1 1 0 1 0 0 2 1 1 0 0 0 0-2Z",
  },
  check: {
    fill: "M12 3a9 9 0 1 1 0 18 9 9 0 0 1 0-18Z",
    stroke: "M12 3a9 9 0 1 1 0 18 9 9 0 0 1 0-18Z M8 12.5l2.75 2.75L16.5 9.5",
  },
  hourglass: {
    fill: "M6.5 4h11v3l-5.5 5 5.5 5v3h-11v-3l5.5-5-5.5-5Z",
    stroke: "M6.5 4h11v3l-5.5 5 5.5 5v3h-11v-3l5.5-5-5.5-5Z M5 4h14 M5 20h14",
    solid: "M9 18.5h6l-3-3Z",
  },
  // A receipt with its torn edge: the record of a tap.
  receipt: {
    fill: "M5 3h14v18l-2.33-1.5L14.33 21 12 19.5 9.67 21 7.33 19.5 5 21Z",
    stroke: "M5 3h14v18l-2.33-1.5L14.33 21 12 19.5 9.67 21 7.33 19.5 5 21Z M8.5 8h7 M8.5 11.5h7 M8.5 15h4",
  },
};

export interface DuotoneIconProps extends Omit<SVGProps<SVGSVGElement>, "name"> {
  name: DuotoneName;
  size?: number;
}

export function DuotoneIcon({ name, size = 24, className, ...rest }: DuotoneIconProps) {
  const g = GLYPHS[name];
  return (
    <svg
      viewBox="0 0 24 24"
      width={size}
      height={size}
      fill="none"
      aria-hidden
      focusable="false"
      className={className}
      {...rest}
    >
      <path d={g.fill} fill="currentColor" fillOpacity={0.22} />
      <path d={g.stroke} stroke="currentColor" strokeWidth={1.5} strokeLinecap="round" strokeLinejoin="round" />
      {g.solid ? <path d={g.solid} fill="currentColor" /> : null}
    </svg>
  );
}
