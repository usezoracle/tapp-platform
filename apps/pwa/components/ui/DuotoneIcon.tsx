import type { SVGProps } from "react";

/**
 * The app's own icon set: duotone, one hue per icon.
 *
 * Each glyph is a 1.5px stroke in `currentColor` over a 22% fill of the same
 * colour, on a 24px grid. The hue comes from wherever the icon sits -- a nav
 * item, a tile, a status line -- through `color`, so the same glyph reads as
 * royal in the wallet and violet on the card without a prop for it. Two
 * tones from one colour is what keeps six coloured items reading as a
 * system rather than a rainbow: nothing here has a second hue.
 *
 * The fill is the "body" of the thing (the wallet, the card, the area under
 * the line); the stroke is its edge and its detail. A glyph with no obvious
 * body (an arrow) takes a disc behind it.
 */
export type DuotoneName =
  | "wallet"
  | "activity"
  | "qr"
  | "card"
  | "chart"
  | "gear"
  | "cash"
  | "arrow-in"
  | "hourglass"
  | "sign-out"
  | "warning"
  | "link";

interface Glyph {
  /** The 22% body. */
  fill: string;
  /** The 1.5px edge and detail. */
  stroke: string;
  /** Detail drawn solid in the hue (dots, sand, the QR modules). */
  solid?: string;
}

const GEAR =
  "M19.81 10.29L21.86 10.36L21.86 13.64L19.81 13.71L18.74 16.31L20.14 17.81L17.81 20.14L16.31 18.74L13.71 19.81L13.64 21.86L10.36 21.86L10.29 19.81L7.69 18.74L6.19 20.14L3.86 17.81L5.26 16.31L4.19 13.71L2.14 13.64L2.14 10.36L4.19 10.29L5.26 7.69L3.86 6.19L6.19 3.86L7.69 5.26L10.29 4.19L10.36 2.14L13.64 2.14L13.71 4.19L16.31 5.26L17.81 3.86L20.14 6.19L18.74 7.69Z";

const GLYPHS: Record<DuotoneName, Glyph> = {
  wallet: {
    fill: "M3 8a2.5 2.5 0 0 1 2.5-2.5h13A2.5 2.5 0 0 1 21 8v8.5a2.5 2.5 0 0 1-2.5 2.5h-13A2.5 2.5 0 0 1 3 16.5Z",
    stroke:
      "M3 8a2.5 2.5 0 0 1 2.5-2.5h13A2.5 2.5 0 0 1 21 8v8.5a2.5 2.5 0 0 1-2.5 2.5h-13A2.5 2.5 0 0 1 3 16.5Z M21 10.5h-4.5a2 2 0 0 0 0 4H21 M3 8V6.5A2 2 0 0 1 5 4.5h11",
    solid: "M16.5 11.5a1 1 0 1 0 0 2 1 1 0 0 0 0-2Z",
  },
  activity: {
    fill: "M12 4a8 8 0 1 1 0 16 8 8 0 0 1 0-16Z",
    stroke: "M5.6 8.5A8 8 0 1 1 4 12 M4 8v4h4 M12 8.5V12l2.6 1.8",
  },
  qr: {
    fill: "M3.5 3.5h7v7h-7Z M13.5 3.5h7v7h-7Z M3.5 13.5h7v7h-7Z",
    stroke:
      "M3.5 3.5h7v7h-7Z M13.5 3.5h7v7h-7Z M3.5 13.5h7v7h-7Z M6 6h2v2H6Z M16 6h2v2h-2Z M6 16h2v2H6Z",
    solid: "M13.5 13.5h2v2h-2Z M18.5 13.5h2v2h-2Z M16 16h2v2h-2Z M13.5 18.5h2v2h-2Z M18.5 18.5h2v2h-2Z",
  },
  card: {
    fill: "M2.5 7.5A2.5 2.5 0 0 1 5 5h14a2.5 2.5 0 0 1 2.5 2.5v9A2.5 2.5 0 0 1 19 19H5a2.5 2.5 0 0 1-2.5-2.5Z",
    stroke:
      "M2.5 7.5A2.5 2.5 0 0 1 5 5h14a2.5 2.5 0 0 1 2.5 2.5v9A2.5 2.5 0 0 1 19 19H5a2.5 2.5 0 0 1-2.5-2.5Z M2.5 9.5h19 M6 15h4",
  },
  chart: {
    fill: "M3 16.5l5-5.5 4 3 6.5-7.5L21 8v11H3Z",
    stroke: "M3 16.5l5-5.5 4 3 6.5-7.5 M15.5 6.5H19v3.5 M3 20h18",
  },
  gear: {
    fill: GEAR,
    stroke: `${GEAR} M12 9a3 3 0 1 1 0 6 3 3 0 0 1 0-6Z`,
  },
  cash: {
    fill: "M2.5 7.5A1.5 1.5 0 0 1 4 6h16a1.5 1.5 0 0 1 1.5 1.5v9A1.5 1.5 0 0 1 20 18H4a1.5 1.5 0 0 1-1.5-1.5Z",
    stroke:
      "M2.5 7.5A1.5 1.5 0 0 1 4 6h16a1.5 1.5 0 0 1 1.5 1.5v9A1.5 1.5 0 0 1 20 18H4a1.5 1.5 0 0 1-1.5-1.5Z M12 9.25a2.75 2.75 0 1 1 0 5.5 2.75 2.75 0 0 1 0-5.5Z",
    solid: "M5.25 11.25a.75.75 0 1 0 0 1.5.75.75 0 0 0 0-1.5Z M18.75 11.25a.75.75 0 1 0 0 1.5.75.75 0 0 0 0-1.5Z",
  },
  "arrow-in": {
    fill: "M12 3a9 9 0 1 1 0 18 9 9 0 0 1 0-18Z",
    stroke: "M15.5 8.5l-7 7 M8.5 10.25v5.25h5.25",
  },
  hourglass: {
    fill: "M6.5 4h11v3l-5.5 5 5.5 5v3h-11v-3l5.5-5-5.5-5Z",
    stroke: "M6.5 4h11v3l-5.5 5 5.5 5v3h-11v-3l5.5-5-5.5-5Z M5 4h14 M5 20h14",
    solid: "M9 18.5h6l-3-3Z",
  },
  "sign-out": {
    fill: "M3.5 5.5A1.5 1.5 0 0 1 5 4h7v16H5a1.5 1.5 0 0 1-1.5-1.5Z",
    stroke: "M12 4H5a1.5 1.5 0 0 0-1.5 1.5v13A1.5 1.5 0 0 0 5 20h7 M16.5 8.5 20 12l-3.5 3.5 M20 12H9",
  },
  warning: {
    fill: "M10.7 4.2a1.5 1.5 0 0 1 2.6 0l7.8 13.5a1.5 1.5 0 0 1-1.3 2.3H4.2a1.5 1.5 0 0 1-1.3-2.3Z",
    stroke: "M10.7 4.2a1.5 1.5 0 0 1 2.6 0l7.8 13.5a1.5 1.5 0 0 1-1.3 2.3H4.2a1.5 1.5 0 0 1-1.3-2.3Z M12 9v4.5",
    solid: "M12 15.75a1 1 0 1 0 0 2 1 1 0 0 0 0-2Z",
  },
  link: {
    fill: "M2.5 8.5A2.5 2.5 0 0 1 5 6h9a2.5 2.5 0 0 1 2.5 2.5v7A2.5 2.5 0 0 1 14 18H5a2.5 2.5 0 0 1-2.5-2.5Z",
    stroke:
      "M2.5 8.5A2.5 2.5 0 0 1 5 6h9a2.5 2.5 0 0 1 2.5 2.5v7A2.5 2.5 0 0 1 14 18H5a2.5 2.5 0 0 1-2.5-2.5Z M2.5 10.5h14 M19 9v6 M16 12h6",
  },
};

export interface DuotoneIconProps extends Omit<SVGProps<SVGSVGElement>, "name"> {
  name: DuotoneName;
  /** Pixel size; the rail uses 18, the tabs 22, tiles and buttons 16. */
  size?: number;
}

export function DuotoneIcon({ name, size = 16, className, ...rest }: DuotoneIconProps) {
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
      <path
        d={g.stroke}
        stroke="currentColor"
        strokeWidth={1.5}
        strokeLinecap="round"
        strokeLinejoin="round"
      />
      {g.solid ? <path d={g.solid} fill="currentColor" /> : null}
    </svg>
  );
}
