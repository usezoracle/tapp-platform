import { NextResponse } from "next/server";

/**
 * Server-side proxy for the public market feed. The upstream is open but sends
 * no CORS headers, so the browser cannot read it directly. Next's fetch cache
 * holds the upstream body for 60s, so a burst of visitors is one upstream hit.
 */
const UPSTREAM = "https://freedom-production-f31c.up.railway.app/v1/market";

export async function GET() {
  try {
    const res = await fetch(UPSTREAM, { next: { revalidate: 60 } });
    if (!res.ok) return NextResponse.json({ error: "upstream" }, { status: 502 });
    const body = await res.json();
    return NextResponse.json(body, {
      headers: { "cache-control": "public, max-age=30, s-maxage=60" },
    });
  } catch {
    return NextResponse.json({ error: "upstream" }, { status: 502 });
  }
}
