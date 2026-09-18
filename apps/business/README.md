# Freedom Exchange — merchant portal

The merchant-facing web portal: a business registers on Tapp and lists on
Freedom Exchange. Next.js 16 (App Router), React 19, Tailwind v4, TanStack
Query, react-hook-form + zod. Desktop-first, usable at phone width.

## Run

From the repository root (pnpm workspace):

```sh
pnpm install --filter tapp-business
pnpm --filter tapp-business dev        # http://localhost:3000
pnpm --filter tapp-business build
pnpm --filter tapp-business typecheck
```

## Env

| variable | meaning |
|---|---|
| `NEXT_PUBLIC_API_BASE_URL` | The API origin, no trailing slash. Defaults to `https://api-production-ce83.up.railway.app`. |

Copy `.env.example` to `.env.local` to override.

## What it talks to

All under the API's envelope `{status, message, data}`; the shapes are in
`apps/api/docs/equity.md`.

| screen | endpoint |
|---|---|
| `/register` | `POST /v1/auth/register` with `scopes: ["sender"]`, `currency: "NGN"` |
| `/sign-in` | `POST /v1/auth/login` with `scope: "sender"`; `POST /v1/auth/refresh` once on a 401 |
| `/business` | `GET /v1/sender/me/business` (404 = nothing submitted yet) |
| `/business/list` | `GET /v1/me` for the owner's user id, `POST /v1/sender/me/business` |
| `/business/holders` | `GET /v1/sender/me/business/holders?limit=&cursor=` |

The session lives in `localStorage` (`tapp.business.session.v1`); the listing
draft too (`tapp.business.draft.v1`), cleared on a successful submission.

Shares are typed as whole shares and sent as units (x 1e8); prices are typed
in naira and sent in kobo. `lib/units.ts` is the only place that converts.

## Deploy

Vercel project `tapp-business`, Root Directory `apps/business`, framework
Next.js. Set `ENABLE_EXPERIMENTAL_COREPACK=1` so Vercel uses the pnpm version
pinned in the root `package.json`, and `NEXT_PUBLIC_API_BASE_URL` if the API
is anywhere other than the default.
