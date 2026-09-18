"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import { type PricePoint, formatDayMonth, formatDate } from "@/lib/holdings";

interface PriceChartProps {
  /** Newest first, as the API returns it. */
  prices: PricePoint[];
  currency: string;
  height?: number;
}

const PAD = { top: 18, right: 6, bottom: 22, left: 6 };

function niceTicks(min: number, max: number, count = 3): number[] {
  if (max <= min) return [min];
  const span = max - min;
  const rough = span / count;
  const mag = Math.pow(10, Math.floor(Math.log10(rough)));
  const norm = rough / mag;
  const step = (norm >= 5 ? 5 : norm >= 2 ? 2 : 1) * mag;
  const start = Math.ceil(min / step) * step;
  const out: number[] = [];
  for (let v = start; v <= max + 1e-9; v += step) out.push(v);
  return out;
}

/**
 * 90-session adjusted price, one series. 2px accent line, hairline
 * grid, 8px endpoint marker, crosshair + tooltip on hover/touch. Every
 * colour is a theme token so it flips with dark mode.
 */
export function PriceChart({ prices, currency, height = 180 }: PriceChartProps) {
  const hostRef = useRef<HTMLDivElement>(null);
  const [width, setWidth] = useState(0);
  const [hover, setHover] = useState<number | null>(null);

  useEffect(() => {
    const el = hostRef.current;
    if (!el) return;
    const ro = new ResizeObserver(([entry]) => setWidth(entry.contentRect.width));
    ro.observe(el);
    setWidth(el.clientWidth);
    return () => ro.disconnect();
  }, []);

  // Oldest → newest, using the adjusted price (falls back to raw).
  const series = useMemo(
    () =>
      [...prices]
        .reverse()
        .map((p) => ({ date: p.date, kobo: p.adjusted_price_kobo ?? p.price_kobo })),
    [prices],
  );

  const fmt = useMemo(
    () =>
      new Intl.NumberFormat("en-NG", {
        style: "currency",
        currency: currency || "NGN",
        maximumFractionDigits: 0,
      }),
    [currency],
  );
  const fmtExact = useMemo(
    () =>
      new Intl.NumberFormat("en-NG", {
        style: "currency",
        currency: currency || "NGN",
        minimumFractionDigits: 2,
        maximumFractionDigits: 2,
      }),
    [currency],
  );

  if (series.length < 2) {
    return (
      <div ref={hostRef} className="panel px-4 py-8 text-center text-[13px] text-fg-muted">
        Not enough sessions to draw a chart yet.
      </div>
    );
  }

  const values = series.map((s) => s.kobo / 100);
  const min = Math.min(...values);
  const max = Math.max(...values);
  const padV = (max - min || Math.abs(max) * 0.05 || 1) * 0.12;
  const lo = min - padV;
  const hi = max + padV;

  const w = Math.max(width, 0);
  const plotW = Math.max(w - PAD.left - PAD.right, 1);
  const plotH = height - PAD.top - PAD.bottom;
  const x = (i: number) => PAD.left + (i / (series.length - 1)) * plotW;
  const y = (v: number) => PAD.top + (1 - (v - lo) / (hi - lo)) * plotH;

  const path = values.map((v, i) => `${i === 0 ? "M" : "L"}${x(i).toFixed(1)},${y(v).toFixed(1)}`).join(" ");
  const ticks = niceTicks(lo, hi, 3);
  const last = series.length - 1;
  const mid = Math.floor(last / 2);
  const active = hover ?? null;

  const onPointer = (e: React.PointerEvent<SVGRectElement>) => {
    const rect = e.currentTarget.getBoundingClientRect();
    const px = e.clientX - rect.left;
    const i = Math.round(((px - PAD.left) / plotW) * last);
    setHover(Math.max(0, Math.min(last, i)));
  };

  const tipLeft = active !== null ? x(active) : 0;
  const tipFlip = w > 0 && tipLeft > w * 0.6;

  return (
    <div ref={hostRef} className="relative w-full select-none" style={{ touchAction: "pan-y" }}>
      {w > 0 ? (
        <svg width={w} height={height} role="img" aria-label="Adjusted price over the last 90 sessions" className="block overflow-visible">
          {/* grid + y labels, drawn above the line */}
          {ticks.map((t) => (
            <g key={t}>
              <line x1={PAD.left} x2={w - PAD.right} y1={y(t)} y2={y(t)} stroke="var(--line)" strokeWidth={1} />
              <text
                x={PAD.left}
                y={y(t) - 4}
                fontSize={10}
                fill="var(--fg-subtle)"
                stroke="var(--surface)"
                strokeWidth={3}
                paintOrder="stroke"
                style={{ fontVariantNumeric: "tabular-nums" }}
              >
                {fmt.format(t)}
              </text>
            </g>
          ))}

          {/* x labels */}
          {[0, mid, last].map((i, k) => (
            <text
              key={i}
              x={x(i)}
              y={height - 6}
              fontSize={10}
              fill="var(--fg-subtle)"
              textAnchor={k === 0 ? "start" : k === 2 ? "end" : "middle"}
              style={{ fontVariantNumeric: "tabular-nums" }}
            >
              {formatDayMonth(series[i].date)}
            </text>
          ))}

          {/* series */}
          <path d={path} fill="none" stroke="var(--accent)" strokeWidth={2} strokeLinejoin="round" strokeLinecap="round" />

          {/* endpoint */}
          <circle cx={x(last)} cy={y(values[last])} r={5.5} fill="var(--surface)" />
          <circle cx={x(last)} cy={y(values[last])} r={4} fill="var(--accent)" />

          {/* crosshair */}
          {active !== null ? (
            <g>
              <line x1={x(active)} x2={x(active)} y1={PAD.top} y2={height - PAD.bottom} stroke="var(--line-strong)" strokeWidth={1} />
              <circle cx={x(active)} cy={y(values[active])} r={5.5} fill="var(--surface)" />
              <circle cx={x(active)} cy={y(values[active])} r={4} fill="var(--accent)" />
            </g>
          ) : null}

          {/* hit layer */}
          <rect
            x={0}
            y={0}
            width={w}
            height={height}
            fill="transparent"
            onPointerMove={onPointer}
            onPointerDown={onPointer}
            onPointerLeave={() => setHover(null)}
            onPointerCancel={() => setHover(null)}
          />
        </svg>
      ) : (
        <div style={{ height }} />
      )}

      {active !== null ? (
        <div
          role="status"
          className="pointer-events-none absolute top-0 z-10 rounded-md bg-fg px-2 py-1.5 text-xs leading-4 text-surface shadow-sheet"
          style={{
            left: tipLeft,
            transform: tipFlip ? "translate(calc(-100% - 8px), 0)" : "translate(8px, 0)",
          }}
        >
          <p className="tabular-nums font-medium">{fmtExact.format(values[active])}</p>
          <p className="tabular-nums opacity-70">{formatDate(series[active].date)}</p>
        </div>
      ) : null}
    </div>
  );
}
