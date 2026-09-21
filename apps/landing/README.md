# Freedom — landing

The public face of the Freedom ecosystem: the card, the shops, the market.
One page. Next.js 16 (App Router), React 19, Tailwind v4, framer-motion for
the hero's scroll-driven card cluster. No other runtime dependencies.

## Run

From the repository root (pnpm workspace):

```sh
pnpm install --filter freedom-landing
pnpm --filter freedom-landing dev        # http://localhost:3000
pnpm --filter freedom-landing build
pnpm --filter freedom-landing typecheck
```

## What is where

| path | what |
|---|---|
| `app/layout.tsx` | fonts (Inter, Bricolage Grotesque, JetBrains Mono), SEO + Open Graph metadata |
| `app/globals.css` | design tokens (light / dark / `.scope-dark`), display type, eyebrow, checker backdrop, card mockup base |
| `app/page.tsx` | section order |
| `app/api/market/route.ts` | server-side proxy for the public market feed (60s revalidate); the upstream sends no CORS headers |
| `components/Hero.tsx` | the three 3D card mockups: scroll-scrubbed spread on desktop, stacked reveal on phones, static row under `prefers-reduced-motion` |
| `components/cards.ts` | the editions and the ID-1 card geometry |
| `components/DuotoneIcon.tsx` | duotone icon set (1.5px stroke + 22% fill) |
| `lib/market.ts` | one shared fetch of `/api/market`, formatting |
| `lib/links.ts` | every outbound URL |
| `public/cards/*.png` | card artwork; `public/og.png` the share image |

The live strip and the market table read the same feed and hide themselves
if it is down; nothing on the page is a placeholder.

## Deploy

Vercel project `freedom-landing`, Root Directory `apps/landing`, framework
Next.js. Set `ENABLE_EXPERIMENTAL_COREPACK=1` so Vercel uses the pnpm version
pinned in the root `package.json`. No environment variables are required.
