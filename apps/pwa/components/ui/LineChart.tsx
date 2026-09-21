"use client";

import { useEffect, useRef, useState } from "react";
import {
  createChart,
  createSeriesMarkers,
  AreaSeries,
  ColorType,
  CrosshairMode,
  LineStyle,
  TickMarkType,
  type AreaData,
  type AutoscaleInfo,
  type IChartApi,
  type ISeriesApi,
  type ISeriesMarkersPluginApi,
  type MouseEventParams,
  type Time,
  type UTCTimestamp,
  type WhitespaceData,
} from "lightweight-charts";
import { cn } from "@/lib/utils";
import { formatPointDate } from "@/lib/holdings";

/** One point: a UTC timestamp in seconds and a value in major units. */
export interface ChartPoint {
  time: number;
  value: number;
}

export interface ChartMarker {
  /** Must be a point's time. */
  time: number;
  text?: string;
}

/** The span of time the x-axis covers, in UTC seconds. */
export interface ChartFrame {
  from: number;
  to: number;
}

export interface LineChartProps {
  /** Oldest first, strictly increasing times, all inside `frame`. */
  points: ChartPoint[];
  /** The x-axis covers this whole span, however few points there are. */
  frame: ChartFrame;
  /** Whole units, for gridlines that fall on whole units. */
  format: (value: number) => string;
  /** With the pence: the tooltip, and gridlines that fall between whole units. Defaults to `format`. */
  formatExact?: (value: number) => string;
  /** Marks on the line, drawn as small discs below it. */
  markers?: ChartMarker[];
  /** Emphasise the newest point with a dot and a once-a-second ring. */
  live?: boolean;
  ariaLabel: string;
  /** Sizes the chart; the chart follows it through a ResizeObserver. */
  className?: string;
}

const HOUR = 3600;
const DAY = 86_400;

/**
 * The app's one chart: an area under a 2px accent line, on the page's own
 * ground. Everything it draws is read from the theme's CSS variables at
 * mount and again whenever `<html>` gains or loses `dark`, so it holds in
 * both themes without being told which one it is in.
 *
 * The frame is the chart, not the data: the x-axis is `frame` at an hour's
 * resolution (whitespace bars, one an hour, that the points are placed
 * among), so a single point sits at its own date on a full axis of dates,
 * and an intraday point lands at its hour. The y-axis starts at 0 and ends
 * at a round figure above the highest point, so the gridlines are whole
 * amounts and the fill reaches the ground.
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
  frame,
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
  const pointsRef = useRef<ChartPoint[]>(points);
  pointsRef.current = points;
  const formatRef = useRef({ format, formatExact });
  formatRef.current = { format, formatExact };

  const [hover, setHover] = useState<{ x: number; index: number } | null>(null);
  const [dot, setDot] = useState<{ x: number; y: number } | null>(null);
  const [width, setWidth] = useState(0);

  // Create once.
  useEffect(() => {
    const el = hostRef.current;
    if (!el) return;

    const theme = readTheme(el);
    // Whole units when every gridline is on one; the pence otherwise.
    const labels = (values: number[]) => {
      const f = formatRef.current;
      return values.every(isWhole) ? values.map(f.format) : values.map(f.formatExact);
    };
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
        // No bottom margin: the range starts at 0 and 0 is the ground
        // (the autoscale below leaves the "₦0" label its half-line).
        scaleMargins: { top: 0.08, bottom: 0 },
        entireTextOnly: true,
      },
      timeScale: {
        borderVisible: false,
        fixLeftEdge: true,
        fixRightEdge: true,
        lockVisibleTimeRangeOnResize: true,
        // A month of hours on a phone is well under a pixel a bar.
        minBarSpacing: 0.001,
        tickMarkFormatter: (time: Time, type: TickMarkType) => {
          const d = new Date(seconds(time) * 1000);
          if (type === TickMarkType.Year) return String(d.getUTCFullYear());
          if (type === TickMarkType.Month) return d.toLocaleDateString("en-GB", { month: "short", timeZone: "UTC" });
          return d.toLocaleDateString("en-GB", { day: "numeric", month: "short", timeZone: "UTC" });
        },
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
        priceFormatter: (v: number) => labels([v])[0],
        tickmarksPriceFormatter: labels,
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
      // Kobo is the smallest step, so no gridline is finer than 0.01 and
      // two-decimal labels are always distinct.
      priceFormat: { type: "price", precision: 2, minMove: 0.01 },
      autoscaleInfoProvider: (original: () => AutoscaleInfo | null) => {
        const info = original();
        if (!info?.priceRange) return info;
        return {
          priceRange: { minValue: 0, maxValue: roundTop(info.priceRange.maxValue) },
          // Half a line of text under 0, so its label is drawn whole; far
          // too little for a gridline below 0 to fit.
          margins: { above: 0, below: 6 },
        };
      },
    });
    const seriesMarkers = createSeriesMarkers(series, []);

    chartRef.current = chart;
    seriesRef.current = series;
    markersRef.current = seriesMarkers;

    // The point in force under the cursor: the last one at or before it.
    const onMove = (param: MouseEventParams<Time>) => {
      if (!param.point || param.time === undefined) {
        setHover(null);
        return;
      }
      const index = indexAtOrBefore(pointsRef.current, seconds(param.time));
      if (index < 0) {
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
      placeDot(chart, series, pointsRef.current, setDot);
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

  // Data: the frame's hours as whitespace, with the points among them.
  // A few hundred bars is nothing to the library, so every change is a
  // plain `setData()` rather than a diff.
  useEffect(() => {
    const chart = chartRef.current;
    const series = seriesRef.current;
    if (!chart || !series) return;
    series.setData(withFrame(points, frame));
    chart.timeScale().fitContent();
    placeDot(chart, series, points, setDot);
  }, [points, frame]);

  // Markers, in the accent.
  useEffect(() => {
    const el = hostRef.current;
    const m = markersRef.current;
    if (!el || !m) return;
    const accent = readTheme(el).accent;
    m.setMarkers(
      (markers ?? []).map((mk) => ({
        time: mk.time as UTCTimestamp,
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
          <p className="tabular-nums opacity-70">{formatWhen(tip.time)}</p>
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

/**
 * The frame's hours as whitespace bars, with the points placed among them
 * by time. A point on an hour replaces that hour's whitespace; the result
 * is sorted and strictly increasing, which is what `setData` requires.
 */
function withFrame(points: ChartPoint[], frame: ChartFrame): (AreaData<Time> | WhitespaceData<Time>)[] {
  const out: (AreaData<Time> | WhitespaceData<Time>)[] = [];
  let i = 0;
  const pushPointsBefore = (t: number) => {
    for (; i < points.length && points[i].time < t; i++) {
      out.push({ time: points[i].time as UTCTimestamp, value: points[i].value });
    }
  };
  for (let t = Math.ceil(frame.from / HOUR) * HOUR; t <= frame.to; t += HOUR) {
    pushPointsBefore(t);
    if (i < points.length && points[i].time === t) {
      out.push({ time: t as UTCTimestamp, value: points[i].value });
      i++;
    } else {
      out.push({ time: t as UTCTimestamp });
    }
  }
  pushPointsBefore(Infinity);
  return out;
}

/**
 * A round figure above the highest value, so the top gridline is a whole
 * amount and the line has headroom: ₦15.75 → ₦20, ₦70,000 → ₦80,000.
 * Nothing (or nothing above 0) gets a ₦1 axis.
 */
export function roundTop(max: number): number {
  if (!(max > 0)) return 1;
  const raw = max * 1.1;
  const step = niceStep(raw / 4);
  return Math.ceil(raw / step) * step;
}

/** The smallest of 1, 2, 5 × 10ⁿ that is at least `x`. */
function niceStep(x: number): number {
  const exp = Math.pow(10, Math.floor(Math.log10(x)));
  const f = x / exp;
  return (f <= 1 ? 1 : f <= 2 ? 2 : f <= 5 ? 5 : 10) * exp;
}

const isWhole = (v: number) => Math.abs(v - Math.round(v)) < 1e-6;

/** The last index whose time is at or before `t`; -1 before the first. */
function indexAtOrBefore(points: ChartPoint[], t: number): number {
  let lo = 0;
  let hi = points.length - 1;
  let found = -1;
  while (lo <= hi) {
    const mid = (lo + hi) >> 1;
    if (points[mid].time <= t) {
      found = mid;
      lo = mid + 1;
    } else {
      hi = mid - 1;
    }
  }
  return found;
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
    const x = chart.timeScale().timeToCoordinate(last.time as UTCTimestamp);
    const y = series.priceToCoordinate(last.value);
    set(x === null || y === null ? null : { x, y });
  });
}

/** A lightweight-charts time back to UTC seconds. */
function seconds(time: Time): number {
  if (typeof time === "number") return time;
  if (typeof time === "string") return Date.parse(time) / 1000;
  return Date.UTC(time.year, time.month - 1, time.day) / 1000;
}

/**
 * A point's moment for the tooltip. A point on a UTC midnight is a
 * session, so its date alone; anything else happened at a time of day,
 * shown in the reader's own clock.
 */
function formatWhen(sec: number): string {
  const date = formatPointDate(sec);
  if (sec % DAY === 0) return date;
  const time = new Date(sec * 1000).toLocaleTimeString("en-GB", { hour: "2-digit", minute: "2-digit" });
  return `${date}, ${time}`;
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
