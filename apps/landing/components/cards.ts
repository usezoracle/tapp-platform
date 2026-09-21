/** The three editions and their artwork. ISO/IEC 7810 ID-1: 85.6 x 54 mm. */
export const CARD_RATIO = 85.6 / 54;

export type Orientation = "portrait" | "landscape";

export interface Edition {
  id: "checker" | "classic" | "premium";
  name: string;
  line: string;
  src: string;
  orientation: Orientation;
}

export const EDITIONS: Edition[] = [
  {
    id: "checker",
    name: "Checker Edition",
    line: "Black, white, and one red square. The one people ask about.",
    src: "/cards/checker.png",
    orientation: "portrait",
  },
  {
    id: "classic",
    name: "Classic",
    line: "Red through and through. The everyday card.",
    src: "/cards/classic.png",
    orientation: "landscape",
  },
  {
    id: "premium",
    name: "Premium",
    line: "Black on black. Just the mark.",
    src: "/cards/premium.png",
    orientation: "portrait",
  },
];

/** Pixel box for a card given the short side. */
export function cardBox(orientation: Orientation, short: number) {
  const long = Math.round(short * CARD_RATIO);
  return orientation === "portrait" ? { width: short, height: long } : { width: long, height: short };
}
