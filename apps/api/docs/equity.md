# Equity: taps that buy shares

The card side is this API; the market side is Freedom (`/Users/mac/freedom`,
contract in its `docs/INTEGRATION.md`). This document is what a frontend
needs to build against, and what an operator needs to debug it.

## Env

| var | meaning |
|---|---|
| `FREEDOM_BASE_URL` | Freedom's origin (no `/v1/rail`). Empty = feature off. |
| `FREEDOM_RAIL_TOKEN` | Bearer token for every rail call. Empty = feature off. |
| `EQUITY_OUTBOX_INTERVAL_SECONDS` | Worker tick, default 5. |
| `DISABLE_BACKGROUND_JOBS` | As for every worker: `true` on any second instance. |

"Off" means: taps charge exactly as before and queue nothing; every equity
endpoint answers `404` with the message
`Equity is not enabled on this deployment (FREEDOM_BASE_URL and FREEDOM_RAIL_TOKEN are not set)`.

## How a tap reaches the market

Shares are issued when the merchant is **paid**, not when the cardholder is
charged: the fee that funds the buyback is earned by the merchant's payment
landing, and a tap whose settlement fails has earned nothing.

1. `tap.Service.Debit` charges the card and, in the **same transaction**,
   inserts one `equity_outbox` row (`kind='tap'`, payload = the exact
   `POST /v1/rail/taps` body) in state **`held`**. Only NGN taps; any other
   currency is skipped with a log line. Nothing is ever lost -- the row
   exists from the moment the charge does -- but the worker never delivers a
   held row.
2. The row moves `held → queued` when the tap is **fully settled**: every
   leg it has is at rest in its paid state -- the USDC leg `fulfilled` in
   `card_tap_settlements`, the naira leg `settled` in
   `card_tap_ngn_settlements`, a mixed tap both. A tap with no leg at all
   stays held. The one function that decides this is
   `equity.ReleaseIfSettled(ctx, q, tapID)` (SQL mirrors the `settled`
   derivation in `internal/transactions`). It is called:
   - by the offramp settler, in the transaction that marks the USDC leg
     `fulfilled` (`internal/chain/offramp/settler.go`);
   - by the naira worker after it marks a leg `settled`, through
     `apiv1.OnLegSettled(ctx, q, tapID)` (`internal/api/v1/equity_wiring.go`);
   - as a safety net, by the worker itself: every tick starts with
     `equity.ReleaseSettled`, a sweep that queues any held row whose tap is
     now settled, so a missed hook cannot strand shares. The worst case for
     a missed hook is one tick of delay.
3. A reversal (`tap.Service.Reverse`, same transaction as the reversal)
   looks at the tap's row. If it is still `held` or `queued` -- the market
   has never heard of the tap -- the row is **`cancelled`** (`last_error`
   carries the reason) and nothing is sent, ever. If it is `delivered`, a
   `kind='reverse'` row is queued as before. The worker locks a row while it
   is being delivered, so a reversal arriving mid-delivery waits and then
   takes the `delivered` branch; a tap cannot be sent a moment after a
   reversal decided it never would be.
4. `equity.Worker` ticks every 5s: sweeps (step 2), then takes up to 50
   `queued` rows that are due, oldest-first, and delivers them. A reversal is
   held back until its tap row is `delivered`.
5. On success: `state='delivered'`, `response` = Freedom's JSON, `delivered_at`.
   On failure: `attempts+1`, `last_error`, `next_at = now + 5s·2^(attempts-1)`
   capped at 10 min. A 4xx other than 409/429 is a refusal and fails the row
   (`state='failed'`) on the 5th attempt; anything else (network, 5xx, 409,
   429) retries indefinitely.

States on the row: `held | queued | delivered | failed | cancelled`
(migration 0026; the old `pending` is now `queued`, and every undelivered
tap row was moved to `held` -- the sweep releases the settled ones on the
first tick).

Debugging: `SELECT id, kind, tap_id, state, attempts, last_error, next_at FROM
equity_outbox WHERE state <> 'delivered' ORDER BY id`. Every fact is on the
row; there is no other state. A row stuck in `held` means the tap is not
settled -- look at its legs in `card_tap_settlements` /
`card_tap_ngn_settlements`, not at the outbox.

## Wire shapes

Every response is the usual envelope `{status, message, data}`. Money is
`money.Amount` = `{minor, currency, display}`; share quantities are
`{units, shares}` where `units` is 1e-8 of a share and `shares` the human
figure (`"0.203125"`).

### Merchant (sender JWT/API key)

`POST /v1/sender/me/business` — body:

```json
{ "legal_name": "Mama Put Kitchens Ltd", "trading_name": "Mama Put",
  "rc_number": "RC1483920", "mcc": "5812", "symbol": "MAMAPUT",
  "evidence": { "trading_months": 30, "audited_accounts": true, "auditor_on_list": true,
                "shares_in_issue": 800000000000000, "public_shares": 120000000000000,
                "holders": 31, "treasury_units": 180000000000000,
                "board_resolution": true, "directors_clear": true,
                "net_assets": { "minor": 20000000000, "currency": "NGN" },
                "revenue": { "minor": 12000000000, "currency": "NGN" } },
  "shares_authorised_units": 1000000000000000, "daily_release_units": 50000000000000,
  "cofund_bps": 0,
  "holders": [ { "cardholder_ref": "<user uuid>", "units": 50000000000000, "label": "founder" } ] }
```

The applicant does not propose a price. Freedom values the company from the
audited accounts -- fair value = `net_assets` + 1.0× `revenue` (trailing
twelve months) -- and the listing price is fair value divided by the shares
in issue; the exchange names it and it comes back as `reference_price`. In
the example, ₦200m + ₦120m = ₦320m over 8,000,000 shares is ₦40.00 a share.
A `reference_price` in the request is accepted and forwarded for callers that
have not caught up, but ignored by the exchange.

Validation (400 with `data: {field: problem}`): `legal_name` required;
`rc_number` `^(RC|BN)\d+$` (upper-cased); `symbol` `^[A-Z][A-Z0-9]{2,11}$`
(upper-cased); evidence counts ≥ 0; `evidence.net_assets` and
`evidence.revenue` required, NGN and > 0 (as `{minor, currency}`);
`reference_price`, if sent, NGN and > 0; `cofund_bps` 0..10000; holder refs
must be user ids.

`200` for both a listing and a rejection (the merchant needs the findings).
`GET /v1/sender/me/business` answers the same shape (404 if never submitted):

```json
{ "sender_id": "…", "legal_name": "…", "trading_name": "…", "rc_number": "RC1483920",
  "mcc": "5812", "symbol": "MAMAPUT", "state": "listed | rejected | submitted",
  "findings": [ { "criterion": "free_float", "met": true, "detail": "15.0% ≥ 10%" } ],
  "instrument_id": "… | null",
  "evidence": { "net_assets": Amount | null, "revenue": Amount | null },
  "fair_value": {"minor":32000000000,"currency":"NGN","display":"₦320,000,000.00"} | null,
  "reference_price": {"minor":4000,"currency":"NGN","display":"₦40.00"} | null,
  "submitted_at": "2026-09-18T11:24:03Z", "decided_at": "… | null",
  "live": { "shares_authorised": {"units":…,"shares":"…"}, "in_issue": {…}, "treasury_remaining": {…},
            "released_today": {…}, "daily_release": {…}, "holders": 32,
            "top_holders": [ { "cardholder_ref": "<user uuid>", "units": …, "shares": "…" } ],
            "pending_funding": Amount, "escrowed_funding": Amount, "reference_price": Amount,
            "last_session": { "date": "2026-09-17", "state": "published", "price": Amount, "volume": {units,shares} } | null,
            "halted": false, "halt_reason": "…" } | null,
  "live_error": "… (only when live is null because Freedom could not be reached)" }
```

`evidence` is the two audited figures as submitted; `fair_value` is the
valuation the exchange set from them and `reference_price` the listing price
it set. Each is `null` when not known: a rejected business has neither a fair
value nor a price, and a business listed before the figures were collected
has no `evidence`.

`live` is only fetched for a `listed` business; it is `null` (and the stored
record still answers) when Freedom is down.

`GET /v1/sender/me/business/holders?limit=&cursor=` →
`{ "holders": [ { "cardholder_ref", "holding": {units,shares}, "locked": {units,shares}, "cost": Amount, "first_acquired": "…" } ], "next_cursor": "… | null" }`

`GET /v1/sender/orders…` (and admin `/v1/admin/transactions`) gain one
additive field on every row, `equity`, `null` for a tap never sent to the
market and for offramps:

```json
"equity": { "state": "held | queued | failed | cancelled | escrowed | pending | allocated | reversed",
            "symbol": "MAMAPUT | null", "units": 12500000, "shares": "0.125",
            "price": Amount | null }
```

`held` is "awaiting settlement": the market is told of the tap only once the
merchant has been paid. `queued` is settled and on its way. `cancelled` is a
tap reversed before the market heard of it -- nothing was or will be sent
(distinct from `reversed`, where the market has unwound shares it issued).
The wire value is the bare state; the client maps `held` to its own copy.

### Cardholder (JWT)

`GET /v1/me/holdings` →
```json
{ "as_of": "2026-09-18", "total_value": Amount, "total_cost": Amount,
  "holdings": [ { "symbol": "MAMAPUT", "legal_name": "…", "trading_name": "…",
     "holding": {units,shares}, "sellable": {units,shares}, "locked": {units,shares},
     "next_unlock": "2027-01-16 | null", "cost": Amount, "reference_price": Amount | null,
     "value": Amount, "change_bps": 156, "lot_count": 3,
     "last_session": { "date": "2026-09-17", "price": Amount, "source": "auction" } | null } ] }
```
`GET /v1/me/holdings/:symbol` → one holding as above plus
`"lots": [ { "units", "shares", "cost": Amount, "acquired_at", "transferable_from", "tap_id": "… | null" } ]`
and `"prices"` (Freedom's market-data array, passed through).
`GET /v1/me/equity-activity?limit=` → one item per tap, the merchant named:
```json
{ "activity": [ { "tap_id": "…",
     "merchant": { "ref": "<sender profile id>", "name": "Mama Put", "symbol": "MAMAPUT | null" },
     "symbol": "… | null", "tap_amount": Amount, "funding": Amount | null,
     "state": "held | queued | allocated | pending | escrowed", "bought": {units,shares},
     "price": Amount | null, "at": "…" } ] }
```
`tap_amount` is the ticket; `funding` is the slice of it that bought shares.
Taps the market has not been told of yet -- `held` (awaiting the merchant's
settlement) and `queued` (settled, delivery pending) -- are read from the
outbox on this side and listed **first**, ahead of Freedom's rows in the
order Freedom gave them. They carry `tap_amount`, the merchant and `at` (the
charge time), but no `funding`, `price` (both `null`) or `bought` (zero):
those are the market's to decide. Once delivered the tap is Freedom's to
answer for and is no longer listed from here; a `cancelled` tap is not listed
at all.
`merchant.name` is Freedom's trading name for the merchant. A merchant that
took taps before it listed is a placeholder on Freedom, named by its bare
ref, so any item whose name is empty or equals the ref is named from this
side instead — `merchant_businesses.trading_name` (legal name if blank), else
the sender user's first + last name — in one query for the whole page. A ref
nobody knows keeps an empty name rather than a made-up one.

`GET /v1/me/activity` (the ledger feed) carries the same shape on every
movement whose `refType` is `"tap"`: `"merchant": { "ref", "name", "symbol" }`,
resolved from `card_taps.merchant_id` in one extra query per page (collect the
page's tap ids → sender ids → names), not one per row. Every other movement
has `"merchant": null`. Here `symbol` is the business's ticker only while its
listing is `listed`.

Errors: `404` + message when the feature is off (`data` is an empty list for
the list endpoints); `503` "The equity market is unreachable right now; try
again shortly" when Freedom cannot be reached; a cardholder Freedom has never
seen gets an empty `200`.
