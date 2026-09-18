"use client";

import { useEffect, useMemo, useRef, useState } from "react";
import {
  createChart,
  createSeriesMarkers,
  AreaSeries,
  ColorType,
  CrosshairMode,
  LineStyle,
  type IChartApi,
  type ISeriesApi,
  type ISeriesMarkersPluginApi,
  type MouseEventParams,
  type Time,
} from "lightweight-charts";
import { cn } from "@/lib/utils";
import { formatDate, formatDayMonth } from "@/lib/holdings";

/** One session: a calendar date and a value in major units. */
export interface ChartPoint {
  /** YYYY-MM-DD. */
  time: string;
  value: number;
}

export interface ChartMarker {
  time: string;
  text?: string;
}

export interface LineChartProps {
  /** Oldest first. */
  points: ChartPoint[];
  /** For the axis. */
  format: (value: number) => string;
  /** For the tooltip, with the pence the axis leaves out. Defaults to `format`. */
  formatExact?: (value: number) => string;
  /** Marks on the line, drawn as small discs below it. */
  markers?: ChartMarker[];
  /** Emphasise the newest point with a dot and a once-a-second ring. */
  live?: boolean;
  ariaLabel: string;
  /** Sizes the chart; the chart follows it through a ResizeObserver. */
  className?: string;
}

/**
 * The app's one chart: an area under a 2px accent line, on the page's own
 * ground. Everything it draws is read from the theme's CSS variables at
 * mount and again whenever `<html>` gains or loses `dark`, so it holds in
 * both themes without being told which one it is in.
 *
 * lightweight-charts draws to a canvas, so this file is only ever loaded
 * on the client (the consumers import it through `next/dynamic`).
 *
 * The tooltip and the live dot are our own elements, positioned from the
 * chart's coordinate helpers, so they use the same type and tokens as the
 * rest of the screen rather than the library's label chrome.
 */
export default function LineChart({
  points,
  format,
  formatExact = format,
  markers,
  live = false,
  ariaLabel,
  className,
}: LineChartProps) {
  const hostRef = useRef<HTMLDivElement>(null);
  const chartRef = useRef<IChartApi | null>(null);
  const seriesRef = useRef<ISeriesApi<"Area"> | null>(null);
  const markersRef = useRef<ISeriesMarkersPluginApi<Time> | null>(null);
  const shownRef = useRef<ChartPoint[]>([]);
  const formatRef = useRef(format);
  formatRef.current = format;

  const [hover, setHover] = useState<{ x: number; index: number } | null>(null);
  const [dot, setDot] = useState<{ x: number; y: number } | null>(null);
  const [width, setWidth] = useState(0);

  const byTime = useMemo(() => {
    const m = new Map<string, number>();
    points.forEach((p, i) => m.set(p.time, i));
    return m;
  }, [points]);
  const byTimeRef = useRef(byTime);
  byTimeRef.current = byTime;

  // Create once.
  useEffect(() => {
    const el = hostRef.current;
    if (!el) return;

    const theme = readTheme(el);
    const chart = createChart(el, {
      width: el.clientWidth,
      height: el.clientHeight,
      layout: {
        background: { type: ColorType.Solid, color: "transparent" },
        textColor: theme.text,
        fontFamily: theme.font,
        fontSize: 10,
        attributionLogo: false,
      },
      grid: {
        vertLines: { visible: false },
        horzLines: { color: theme.line, style: LineStyle.Solid },
      },
      rightPriceScale: {
        borderVisible: false,
        scaleMargins: { top: 0.14, bottom: 0.08 },
        entireTextOnly: true,
      },
      timeScale: {
        borderVisible: false,
        fixLeftEdge: true,
        fixRightEdge: true,
        lockVisibleTimeRangeOnResize: true,
        tickMarkFormatter: (time: Time) => formatDayMonth(timeKey(time)),
      },
      crosshair: {
        mode: CrosshairMode.Normal,
        vertLine: {
          color: theme.lineStrong,
          width: 1,
          style: LineStyle.Solid,
          labelVisible: false,
        },
        horzLine: { visible: false, labelVisible: false },
      },
      localization: {
        locale: "en-NG",
        priceFormatter: (v: number) => formatRef.current(v),
      },
      handleScroll: false,
      handleScale: false,
      kineticScroll: { mouse: false, touch: false },
    });

    const series = chart.addSeries(AreaSeries, {
      lineColor: theme.accent,
      lineWidth: 2,
      topColor: withAlpha(theme.accent, 0.18),
      bottomColor: withAlpha(theme.accent, 0),
      priceLineVisible: false,
      lastValueVisible: false,
      crosshairMarkerVisible: true,
      crosshairMarkerRadius: 4,
      crosshairMarkerBorderWidth: 2,
      crosshairMarkerBorderColor: theme.surface,
      crosshairMarkerBackgroundColor: theme.accent,
    });
    const seriesMarkers = createSeriesMarkers(series, []);

    chartRef.current = chart;
    seriesRef.current = series;
    markersRef.current = seriesMarkers;
    shownRef.current = [];

    const onMove = (param: MouseEventParams<Time>) => {
      if (!param.point || param.time === undefined) {
        setHover(null);
        return;
      }
      const index = byTimeRef.current.get(timeKey(param.time));
      if (index === undefined) {
        setHover(null);
        return;
      }
      setHover({ x: param.point.x, index });
    };
    chart.subscribeCrosshairMove(onMove);

    // Follow the container, and re-place the dot once the chart has laid
    // the new size out.
    const ro = new ResizeObserver(([entry]) => {
      const { width: w, height: h } = entry.contentRect;
      if (w === 0 || h === 0) return;
      chart.applyOptions({ width: w, height: h });
      chart.timeScale().fitContent();
      setWidth(w);
      placeDot(chart, series, shownRef.current, setDot);
    });
    ro.observe(el);

    // Theme: `.dark` on <html> is the whole of it.
    const mo = new MutationObserver(() => {
      const t = readTheme(el);
      chart.applyOptions({
        layout: { textColor: t.text },
        grid: { horzLines: { color: t.line } },
        crosshair: { vertLine: { color: t.lineStrong } },
      });
      series.applyOptions({
        lineColor: t.accent,
        topColor: withAlpha(t.accent, 0.18),
        bottomColor: withAlpha(t.accent, 0),
        crosshairMarkerBorderColor: t.surface,
        crosshairMarkerBackgroundColor: t.accent,
      });
      seriesMarkers.setMarkers(
        seriesMarkers.markers().map((m) => ({ ...m, color: t.accent })),
      );
    });
    mo.observe(document.documentElement, { attributes: true, attributeFilter: ["class"] });

    return () => {
      mo.disconnect();
      ro.disconnect();
      chart.unsubscribeCrosshairMove(onMove);
      chart.remove();
      chartRef.current = null;
      seriesRef.current = null;
      markersRef.current = null;
    };
  }, []);

  // Data. A newer last point is pushed with `update()`, so a live tick
  // moves the line's end without redrawing the whole series; anything
  // else (first load, a longer history, an edited past) is `setData()`.
  useEffect(() => {
    const chart = chartRef.current;
    const series = seriesRef.current;
    if (!chart || !series) return;
    const shown = shownRef.current;

    const same = (a: ChartPoint, b: ChartPoint) => a.time === b.time && a.value === b.value;
    const n = shown.length;
    const appendedOne = points.length === n + 1 && n > 0 && same(points[n - 1], shown[n - 1]);
    const movedLast = points.length === n && n > 0;
    const prefixHolds = (upTo: number) => {
      for (let i = 0; i < upTo; i++) if (!same(points[i], shown[i])) return false;
      return true;
    };

    if (points.length === 0) {
      series.setData([]);
    } else if ((appendedOne || movedLast) && prefixHolds(n - 1)) {
      if (!movedLast || !same(points[n - 1], shown[n - 1])) {
        series.update(points[points.length - 1]);
      }
    } else {
      series.setData(points);
    }
    shownRef.current = points;
    chart.timeScale().fitContent();
    placeDot(chart, series, points, setDot);
  }, [points]);

  // Markers, in the accent.
  useEffect(() => {
    const el = hostRef.current;
    const m = markersRef.current;
    if (!el || !m) return;
    const accent = readTheme(el).accent;
    m.setMarkers(
      (markers ?? []).map((mk) => ({
        time: mk.time,
        position: "belowBar",
        shape: "circle",
        color: accent,
        size: 0.6,
        text: mk.text,
      })),
    );
  }, [markers]);

  const tip = hover ? points[hover.index] : null;
  const prev = hover && hover.index > 0 ? points[hover.index - 1] : null;
  const flip = width > 0 && hover ? hover.x > width * 0.6 : false;
  const delta = tip && prev ? tip.value - prev.value : null;

  return (
    <div
      ref={hostRef}
      role="img"
      aria-label={ariaLabel}
      className={cn("relative w-full select-none", className)}
      style={{ touchAction: "pan-y" }}
    >
      {live && dot && !hover ? (
        <span
          aria-hidden
          className="pointer-events-none absolute z-10 grid size-1.5 place-items-center"
          style={{ left: dot.x - 3, top: dot.y - 3 }}
        >
          <span className="chart-pulse absolute inset-0 rounded-full bg-accent" />
          <span className="absolute -inset-0.5 rounded-full bg-surface" />
          <span className="absolute inset-0 rounded-full bg-accent" />
        </span>
      ) : null}

      {tip ? (
        <div
          role="status"
          className="pointer-events-none absolute top-1 z-20 grid gap-0.5 rounded-md bg-fg px-2 py-1.5 text-xs leading-4 text-surface shadow-sheet"
          style={{
            left: hover!.x,
            transform: flip ? "translate(calc(-100% - 10px), 0)" : "translate(10px, 0)",
          }}
        >
          <p className="tabular-nums opacity-70">{formatDate(tip.time)}</p>
          <p className="font-medium tabular-nums">{formatExact(tip.value)}</p>
          {/* The sign carries the direction: a green or red on the ink
              ground would be the theme's light-mode tones on a dark one. */}
          {delta !== null ? (
            <p className="tabular-nums opacity-80">
              {delta > 0 ? "+" : delta < 0 ? "−" : ""}
              {formatExact(Math.abs(delta))}
              {prev && prev.value !== 0 ? ` · ${pct(delta / prev.value)}` : ""}
            </p>
          ) : null}
        </div>
      ) : null}
    </div>
  );
}

/** Where the newest point sits, once the chart has laid out. */
function placeDot(
  chart: IChartApi,
  series: ISeriesApi<"Area">,
  points: ChartPoint[],
  set: (d: { x: number; y: number } | null) => void,
) {
  const last = points[points.length - 1];
  if (!last) {
    set(null);
    return;
  }
  requestAnimationFrame(() => {
    const x = chart.timeScale().timeToCoordinate(last.time);
    const y = series.priceToCoordinate(last.value);
    set(x === null || y === null ? null : { x, y });
  });
}

/** A lightweight-charts time back to the YYYY-MM-DD it was given as. */
function timeKey(time: Time): string {
  if (typeof time === "string") return time.slice(0, 10);
  if (typeof time === "number") return new Date(time * 1000).toISOString().slice(0, 10);
  const mm = String(time.month).padStart(2, "0");
  const dd = String(time.day).padStart(2, "0");
  return `${time.year}-${mm}-${dd}`;
}

function pct(ratio: number): string {
  const s = (Math.abs(ratio) * 100).toFixed(2) + "%";
  return ratio > 0 ? "+" + s : ratio < 0 ? "−" + s : s;
}

interface ChartTheme {
  accent: string;
  line: string;
  lineStrong: string;
  text: string;
  surface: string;
  font: string;
}

function readTheme(el: HTMLElement): ChartTheme {
  const cs = getComputedStyle(el);
  const v = (name: string) => cs.getPropertyValue(name).trim();
  return {
    accent: v("--accent") || "#0065f5",
    line: v("--line") || "#ebebec",
    lineStrong: v("--line-strong") || "#d9d9dd",
    text: v("--fg-subtle") || "#9a9aa1",
    surface: v("--surface") || "#ffffff",
    font: cs.fontFamily || "system-ui, sans-serif",
  };
}

/** `#rrggbb` (or `#rgb`) at an alpha; anything else is returned as given. */
function withAlpha(hex: string, alpha: number): string {
  const m = /^#([0-9a-f]{3}|[0-9a-f]{6})$/i.exec(hex);
  if (!m) return hex;
  let h = m[1];
  if (h.length === 3) h = h.split("").map((c) => c + c).join("");
  const n = parseInt(h, 16);
  return `rgba(${(n >> 16) & 255}, ${(n >> 8) & 255}, ${n & 255}, ${alpha})`;
}
