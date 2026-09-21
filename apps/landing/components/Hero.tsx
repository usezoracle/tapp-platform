"use client";

import { useLayoutEffect, useRef, useState } from "react";
import {
  motion,
  useMotionValue,
  useReducedMotion,
  useScroll,
  useSpring,
  useTransform,
  type MotionStyle,
  type MotionValue,
} from "framer-motion";
import { Button } from "./Button";
import { LINKS } from "@/lib/links";
import { EDITIONS, cardBox, type Edition } from "./cards";

/*
 * The hero is pinned for 170vh on desktop. Scrolling the first 70vh drives
 * one progress value 0 -> 1: the three cards start stacked and fanned, then
 * spread into a row and tip toward the viewer. Every animated property is a
 * transform, so nothing re-lays-out while the user scrolls. The pointer adds
 * a small tilt and slides a specular sheen across the faces; both are spring
 * motion values, never React state. Reduced motion: no pin, a static row.
 * On phones the cards stack vertically and simply fade in as they appear.
 */

/** Stacked-state offsets per card: checker leads, classic behind, premium peeks right. */
const STACK_X = [-52, 6, 78];
const STACK_Y = [0, 4, 14];
const STACK_Z = [-9, 2, 11];

const SHADOW = "0 28px 56px -28px rgba(0,0,0,0.5), 0 8px 16px -10px rgba(0,0,0,0.25)";

interface Pointer {
  px: MotionValue<number>;
  py: MotionValue<number>;
}

function usePointer(): Pointer & { onMove: (e: React.PointerEvent) => void; onLeave: () => void } {
  const rawX = useMotionValue(-0.6);
  const rawY = useMotionValue(0);
  const px = useSpring(rawX, { stiffness: 120, damping: 20, mass: 0.4 });
  const py = useSpring(rawY, { stiffness: 120, damping: 20, mass: 0.4 });
  return {
    px,
    py,
    onMove: (e) => {
      const r = e.currentTarget.getBoundingClientRect();
      rawX.set(((e.clientX - r.left) / r.width) * 2 - 1);
      rawY.set(((e.clientY - r.top) / r.height) * 2 - 1);
    },
    onLeave: () => {
      rawX.set(-0.6);
      rawY.set(0);
    },
  };
}

/** One card in the desktop cluster; scroll progress and pointer drive it. */
function ScrollCard({
  edition,
  index,
  short,
  progress,
  pointer,
  offsetX,
  still,
}: {
  edition: Edition;
  index: number;
  short: number;
  progress: MotionValue<number>;
  pointer: Pointer;
  /** Distance from the cluster centre to this card's centre, px. */
  offsetX: number;
  still: boolean;
}) {
  const box = cardBox(edition.orientation, short);
  const side = index - 1; // -1, 0, 1

  // Stacked: pulled to the centre, fanned so every edge shows, tipped back.
  const x = useTransform(progress, [0, 1], [-offsetX + STACK_X[index], 0]);
  const y = useTransform(progress, [0, 1], [STACK_Y[index], 0]);
  const rotZ = useTransform(progress, [0, 1], [STACK_Z[index], 0]);
  const scrollRotX = useTransform(progress, [0, 1], [16, 0]);
  const scrollRotY = useTransform(progress, [0, 1], [side * 22, 0]);
  const scale = useTransform(progress, [0, 1], [0.94, 1]);

  const tiltY = useTransform(pointer.px, [-1, 1], [-7, 7]);
  const tiltX = useTransform(pointer.py, [-1, 1], [5, -5]);
  const rotateX = useTransform(() => scrollRotX.get() + tiltX.get());
  const rotateY = useTransform(() => scrollRotY.get() + tiltY.get());
  const sheenX = useTransform(pointer.px, [-1, 1], ["-40%", "40%"]);

  // The checker (first) sits on top of the stack: it is the one people ask about.
  const base = { ...box, "--short": `${short}px`, boxShadow: SHADOW, zIndex: EDITIONS.length - index } as MotionStyle;
  const style: MotionStyle = still ? base : { ...base, x, y, rotateZ: rotZ, rotateX, rotateY, scale };

  return (
    <motion.div className="card-3d shrink-0" style={style}>
      <img src={edition.src} alt={`The ${edition.name} card`} width={box.width} height={box.height} draggable={false} />
      <motion.div
        aria-hidden
        className="absolute pointer-events-none"
        style={{
          inset: "-50%",
          x: still ? "0%" : sheenX,
          background:
            "linear-gradient(105deg, transparent 34%, rgba(255,255,255,0.14) 46%, rgba(255,255,255,0.2) 50%, rgba(255,255,255,0.14) 54%, transparent 66%)",
        }}
      />
    </motion.div>
  );
}

function DesktopCluster({ progress, still }: { progress: MotionValue<number>; still: boolean }) {
  const ref = useRef<HTMLDivElement>(null);
  const [offsets, setOffsets] = useState<number[]>([0, 0, 0]);
  const pointer = usePointer();
  const short = useShortSide();

  // Measure each card's centre against the cluster centre once laid out, so
  // the stacked state is exact at every breakpoint without hard-coded math.
  // offsetLeft/offsetWidth are layout geometry: they ignore the transforms
  // this very measurement drives (getBoundingClientRect would not).
  useLayoutEffect(() => {
    const el = ref.current;
    if (!el) return;
    const measure = () => {
      const mid = el.clientWidth / 2;
      const next = Array.from(el.children).map((child) => {
        const c = child as HTMLElement;
        return c.offsetLeft + c.offsetWidth / 2 - mid;
      });
      setOffsets(next);
    };
    measure();
    const ro = new ResizeObserver(measure);
    ro.observe(el);
    return () => ro.disconnect();
  }, [short]);

  return (
    <div
      ref={ref}
      onPointerMove={pointer.onMove}
      onPointerLeave={pointer.onLeave}
      className="relative hidden md:flex items-center justify-center gap-6"
      style={{ perspective: 1400 }}
    >
      {EDITIONS.map((e, i) => (
        <ScrollCard
          key={e.id}
          edition={e}
          index={i}
          short={short}
          progress={progress}
          pointer={pointer}
          offsetX={offsets[i] ?? 0}
          still={still}
        />
      ))}
    </div>
  );
}

/** Short side of a card by breakpoint: md 150, lg 140 (two columns), xl 168. */
function useShortSide(): number {
  const [short, setShort] = useState(150);
  useLayoutEffect(() => {
    const lg = window.matchMedia("(min-width: 1024px)");
    const xl = window.matchMedia("(min-width: 1280px)");
    const read = () => setShort(xl.matches ? 168 : lg.matches ? 140 : 150);
    read();
    lg.addEventListener("change", read);
    xl.addEventListener("change", read);
    return () => {
      lg.removeEventListener("change", read);
      xl.removeEventListener("change", read);
    };
  }, []);
  return short;
}

function MobileCluster() {
  const reduce = useReducedMotion();
  return (
    <div className="flex md:hidden flex-col items-center gap-5">
      {EDITIONS.map((e, i) => {
        const box = cardBox(e.orientation, 136);
        return (
          <motion.div
            key={e.id}
            className="card-3d"
            style={{ ...box, "--short": "136px", boxShadow: SHADOW } as React.CSSProperties}
            initial={reduce ? false : { opacity: 0, y: 28 }}
            whileInView={{ opacity: 1, y: 0 }}
            viewport={{ once: true, margin: "-40px" }}
            transition={{ duration: 0.5, delay: i * 0.08, ease: [0.32, 0.72, 0, 1] }}
          >
            <img src={e.src} alt={`The ${e.name} card`} width={box.width} height={box.height} draggable={false} />
          </motion.div>
        );
      })}
    </div>
  );
}

export function Hero() {
  const outer = useRef<HTMLElement>(null);
  const reduce = useReducedMotion() ?? false;
  const { scrollYProgress } = useScroll({ target: outer, offset: ["start start", "end end"] });
  const progress = useSpring(scrollYProgress, { stiffness: 140, damping: 26, mass: 0.5 });

  return (
    <section ref={outer} className={`relative ${reduce ? "" : "md:h-[170vh]"}`} aria-label="The Freedom card">
      <div className={`${reduce ? "" : "md:sticky md:top-14"} flex items-center md:min-h-[calc(100vh-56px)]`}>
        <div className="mx-auto w-full max-w-[1200px] px-5 md:px-8 py-14 md:py-10 grid gap-12 lg:grid-cols-12 lg:gap-8 items-center">
          <div className="lg:col-span-5 max-w-[560px]">
            <span className="eyebrow">The Freedom card</span>
            <h1 className="display mt-6 text-[44px] sm:text-[56px] xl:text-[64px]">A card that makes money for you.</h1>
            <p className="mt-6 text-[18px] leading-[1.45] text-fg-muted max-w-[420px]">
              Tap to pay at any Freedom shop and you own a slice of it. Every time.
            </p>
            <div className="mt-8 flex flex-wrap gap-3">
              <Button href={LINKS.getCard}>Get the card</Button>
              <Button href={LINKS.business} variant="hairline">
                List your business
              </Button>
            </div>
          </div>

          <div className="lg:col-span-7 relative">
            <div className="checker" aria-hidden />
            <div className="relative py-6 md:py-10">
              <DesktopCluster progress={progress} still={reduce} />
              <MobileCluster />
            </div>
          </div>
        </div>
      </div>
    </section>
  );
}
