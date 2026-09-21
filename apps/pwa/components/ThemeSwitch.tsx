"use client";

import { useTheme } from "next-themes";
import { useState, useEffect, useRef, type ReactElement } from "react";
import { flushSync } from "react-dom";
import { PiSun, PiMoon } from "react-icons/pi";

type IconButtonProps = {
  icon: ReactElement;
  onClick: (e: React.MouseEvent) => void;
  isActive: boolean;
};

const IconButton = ({ icon, onClick, isActive }: IconButtonProps) => (
  <button
    type="button"
    className={`focus-ring flex size-7 cursor-pointer items-center justify-center rounded-sm transition-colors [&>svg]:size-4 ${
      isActive ? "bg-sunken text-fg" : "text-fg-subtle hover:text-fg"
    }`}
    onClick={onClick}
    title={`Switch to ${isActive ? "dark" : "light"} mode`}
  >
    {icon}
  </button>
);

export function ThemeSwitch() {
  const [mounted, setMounted] = useState(false);
  const { setTheme, resolvedTheme } = useTheme();
  const pathRef = useRef<SVGPathElement>(null);

  useEffect(() => {
    setMounted(true);
  }, []);

  if (!mounted) {
    return <div className="h-8 w-[62px]" aria-hidden />;
  }

  const getBlobPath = (cx: number, cy: number, r: number, nodes: number[]): string => {
    const N = nodes.length;
    const points: { x: number; y: number }[] = [];
    for (let i = 0; i < N; i++) {
      const angle = (i * 2 * Math.PI) / N;
      const currentR = Math.max(0, r + nodes[i]);
      const x = cx + currentR * Math.cos(angle);
      const y = cy + currentR * Math.sin(angle);
      points.push({ x, y });
    }

    let d = `M ${points[0].x} ${points[0].y}`;
    for (let i = 0; i < N; i++) {
      const p0 = points[i];
      const p1 = points[(i + 1) % N];
      const p2 = points[(i + 2) % N];
      const pPrevious = points[(i - 1 + N) % N];

      const cp1x = p0.x + (p1.x - pPrevious.x) * 0.15;
      const cp1y = p0.y + (p1.y - pPrevious.y) * 0.15;
      const cp2x = p1.x - (p2.x - p0.x) * 0.15;
      const cp2y = p1.y - (p2.y - p0.y) * 0.15;

      d += ` C ${cp1x} ${cp1y}, ${cp2x} ${cp2y}, ${p1.x} ${p1.y}`;
    }
    return d + " Z";
  };

  const handleThemeChange = (e: React.MouseEvent, targetTheme: "light" | "dark") => {
    if (resolvedTheme === targetTheme) return;

    // Accessibility: if user prefers reduced motion, change theme instantly
    if (typeof window !== "undefined" && window.matchMedia("(prefers-reduced-motion: reduce)").matches) {
      setTheme(targetTheme);
      return;
    }

    // Prevent duplicate concurrent transitions
    if (typeof document !== "undefined" && document.documentElement.classList.contains("theme-transitioning")) {
      return;
    }

    const x = e.clientX;
    const y = e.clientY;

    const w = window.innerWidth;
    const h = window.innerHeight;
    const dx1 = x;
    const dy1 = y;
    const dx2 = w - x;
    const dy2 = h - y;
    const maxDist = Math.sqrt(
      Math.max(
        dx1 * dx1 + dy1 * dy1,
        dx2 * dx2 + dy1 * dy1,
        dx1 * dx1 + dy2 * dy2,
        dx2 * dx2 + dy2 * dy2
      )
    );

    if (typeof document !== "undefined") {
      // Register custom click coordinates for the CSS clipPath
      document.documentElement.style.setProperty("--x", `${x}px`);
      document.documentElement.style.setProperty("--y", `${y}px`);
      document.documentElement.classList.add("theme-transitioning");

      // @ts-ignore
      if (typeof document.startViewTransition === "function") {
        const isMobile =
          window.matchMedia("(max-width: 768px)").matches ||
          "ontouchstart" in window ||
          (typeof navigator !== "undefined" && navigator.maxTouchPoints > 0);

        if (isMobile) {
          document.documentElement.classList.add("mobile-reveal", "no-transitions");
          document.documentElement.style.setProperty("--max-dist", `${maxDist * 1.15}px`);

          // @ts-ignore
          const transition = document.startViewTransition(() => {
            flushSync(() => {
              setTheme(targetTheme);
            });
          });

          transition.ready.then(() => {
            document.documentElement.classList.remove("no-transitions");
          });

          transition.finished.then(() => {
            document.documentElement.classList.remove("theme-transitioning", "mobile-reveal");
          });
          return;
        }

        // 1. Reset and draw initial state (r = 0) synchronously to prevent flashing the old expanded path
        const N = 16;
        const initialPath = getBlobPath(x, y, 0, Array(N).fill(0));
        if (pathRef.current) {
          pathRef.current.setAttribute("d", initialPath);
        }

        // 2. Disable standard CSS transitions temporarily so screenshots are captured in fully-themed state
        document.documentElement.classList.add("no-transitions");

        // 3. Start the view transition and update DOM synchronously using React flushSync
        // @ts-ignore
        const transition = document.startViewTransition(() => {
          flushSync(() => {
            setTheme(targetTheme);
          });
        });

        // 4. Resolve physics inside the transition ready promise
        transition.ready.then(() => {
          // Re-enable CSS transitions on live DOM since snapshot capture is done
          document.documentElement.classList.remove("no-transitions");

          let r = 0;
          let rVel = 0;
          
          // Easing parameters tuned for a slow, gentle, comforting wave
          const k_r = 18;   // lower stiffness = slow gentle rise
          const c_r = 8.5;  // critically damped to ensure zero overshoot and smooth landing

          // Initialize circular node string physics for wave propagation (ripples)
          const nodes = Array.from({ length: N }, () => ({
            val: 0,
            vel: 0,
          }));

          // Seed organic waves at random spots along the perimeter on initial impact
          for (let i = 0; i < 3; i++) {
            const idx = Math.floor(Math.random() * N);
            nodes[idx].vel = (Math.random() - 0.5) * 160;
          }

          // Node-to-node wave coupling constants (circular string tension solver)
          const k_node = 24;    // Restoring force stiffness
          const c_node = 2.0;   // Low damping to allow soft wave propagation
          const tension = 12;   // Surface tension coupling between adjacent nodes

          let lastTime = performance.now();

          const animate = (now: number) => {
            let dt = (now - lastTime) / 1000;
            if (dt > 0.05) dt = 0.05; // clamp delta time
            lastTime = now;

            // Update main radius spring
            const targetR = maxDist * 1.15;
            const rForce = -k_r * (r - targetR) - c_r * rVel;
            rVel += rForce * dt;
            r += rVel * dt;

            // Calculate accelerations first (1D circular string wave equation)
            const accelerations = new Array(N).fill(0);
            for (let i = 0; i < N; i++) {
              const val = nodes[i].val;
              const vel = nodes[i].vel;
              const val_prev = nodes[(i - 1 + N) % N].val;
              const val_next = nodes[(i + 1) % N].val;

              const f_restore = -k_node * val;
              const f_damping = -c_node * vel;
              const f_coupling = tension * (val_prev + val_next - 2 * val); // Tension force pulling towards neighbors

              accelerations[i] = f_restore + f_damping + f_coupling;
            }

            // Update node displacements and velocities
            for (let i = 0; i < N; i++) {
              nodes[i].vel += accelerations[i] * dt;
              nodes[i].val += nodes[i].vel * dt;
            }

            // Render path data
            const pathData = getBlobPath(x, y, r, nodes.map(n => n.val));
            if (pathRef.current) {
              pathRef.current.setAttribute("d", pathData);
            }

            // End transition when covers screen
            if (r >= maxDist) {
              // Skip the view transition to cleanly destroy the pseudo-elements and show the live DOM
              // @ts-ignore
              if (typeof transition.skipTransition === "function") {
                // @ts-ignore
                transition.skipTransition();
              }
              document.documentElement.classList.remove("theme-transitioning");
              return;
            }

            requestAnimationFrame(animate);
          };

          requestAnimationFrame(animate);
        });
      } else {
        // Fallback for unsupported browsers
        setTheme(targetTheme);
        document.documentElement.classList.remove("theme-transitioning");
      }
    }
  };

  return (
    <>
      <div className="flex items-center gap-0.5 rounded-md border border-line p-0.5 transition-colors">
        <IconButton
          icon={<PiSun />}
          onClick={(e) => handleThemeChange(e, "light")}
          isActive={resolvedTheme === "light"}
        />
        <IconButton
          icon={<PiMoon />}
          onClick={(e) => handleThemeChange(e, "dark")}
          isActive={resolvedTheme === "dark"}
        />
      </div>

      {/* SVG mask source and gooey filter always active in the DOM */}
      <svg style={{ position: "absolute", width: 0, height: 0, opacity: 0, pointerEvents: "none" }}>
        <defs>
          <filter id="liquid-goo">
            {/* Soft wide Gaussian blur */}
            <feGaussianBlur in="SourceGraphic" stdDeviation="18" result="blur" />
            {/* Alpha channel thresholding to snap blurred edges into a clean fluid boundary */}
            <feColorMatrix
              in="blur"
              mode="matrix"
              values="1 0 0 0 0  0 1 0 0 0  0 0 1 0 0  0 0 0 32 -13"
              result="goo"
            />
          </filter>
          <mask id="liquid-mask" maskContentUnits="userSpaceOnUse">
            {/* Apply gooey filter to the expanding path to shape the mask boundary */}
            <path ref={pathRef} fill="white" filter="url(#liquid-goo)" />
          </mask>
        </defs>
      </svg>
    </>
  );
}
