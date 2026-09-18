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

1. `tap.Service.Debit` charges the card and, in the **same transaction**,
   inserts one `equity_outbox` row (`kind='tap'`, payload = the exact
   `POST /v1/rail/taps` body). Only NGN taps; any other currency is skipped
   with a log line. A reversal inserts a `kind='reverse'` row the same way.
2. `equity.Worker` ticks every 5s, takes up to 50 due rows oldest-first and
   delivers them. A reversal is held until its tap row is `delivered`.
3. On success: `state='delivered'`, `response` = Freedom's JSON, `delivered_at`.
   On failure: `attempts+1`, `last_error`, `next_at = now + 5s·2^(attempts-1)`
   capped at 10 min. A 4xx other than 409/429 is a refusal and fails the row
   (`state='failed'`) on the 5th attempt; anything else (network, 5xx, 409,
   429) retries indefinitely.

Debugging: `SELECT id, kind, tap_id, state, attempts, last_error, next_at FROM
equity_outbox WHERE state <> 'delivered' ORDER BY id`. Every fact is on the
row; there is no other state.

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
                "board_resolution": true, "directors_clear": true },
  "reference_price": { "minor": 4000, "currency": "NGN" },
  "shares_authorised_units": 1000000000000000, "daily_release_units": 50000000000000,
  "cofund_bps": 0,
  "holders": [ { "cardholder_ref": "<user uuid>", "units": 50000000000000, "label": "founder" } ] }
```

Validation (400 with `data: {field: problem}`): `legal_name` required;
`rc_number` `^(RC|BN)\d+$` (upper-cased); `symbol` `^[A-Z][A-Z0-9]{2,11}$`
(upper-cased); evidence counts ≥ 0; `reference_price` NGN and > 0;
`cofund_bps` 0..10000; holder refs must be user ids.

`200` for both a listing and a rejection (the merchant needs the findings).
`GET /v1/sender/me/business` answers the same shape (404 if never submitted):

```json
{ "sender_id": "…", "legal_name": "…", "trading_name": "…", "rc_number": "RC1483920",
  "mcc": "5812", "symbol": "MAMAPUT", "state": "listed | rejected | submitted",
  "findings": [ { "criterion": "free_float", "met": true, "detail": "15.0% ≥ 10%" } ],
  "instrument_id": "… | null", "reference_price": {"minor":4000,"currency":"NGN","display":"₦40.00"},
  "submitted_at": "2026-09-18T11:24:03Z", "decided_at": "… | null",
  "live": { "shares_authorised": {"units":…,"shares":"…"}, "in_issue": {…}, "treasury_remaining": {…},
            "released_today": {…}, "daily_release": {…}, "holders": 32,
            "top_holders": [ { "cardholder_ref": "<user uuid>", "units": …, "shares": "…" } ],
            "pending_funding": Amount, "escrowed_funding": Amount, "reference_price": Amount,
            "last_session": { "date": "2026-09-17", "state": "published", "price": Amount, "volume": {units,shares} } | null,
            "halted": false, "halt_reason": "…" } | null,
  "live_error": "… (only when live is null because Freedom could not be reached)" }
```

`live` is only fetched for a `listed` business; it is `null` (and the stored
record still answers) when Freedom is down.

`GET /v1/sender/me/business/holders?limit=&cursor=` →
`{ "holders": [ { "cardholder_ref", "holding": {units,shares}, "locked": {units,shares}, "cost": Amount, "first_acquired": "…" } ], "next_cursor": "… | null" }`

`GET /v1/sender/orders…` (and admin `/v1/admin/transactions`) gain one
additive field on every row, `equity`, `null` for a tap never sent to the
market and for offramps:

```json
"equity": { "state": "queued | failed | escrowed | pending | allocated | reversed",
            "symbol": "MAMAPUT | null", "units": 12500000, "shares": "0.125",
            "price": Amount | null }
```

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
`GET /v1/me/equity-activity?limit=` →
`{ "activity": [ { "tap_id", "symbol": "… | null", "funding": Amount, "state": "allocated | pending | escrowed", "bought": {units,shares}, "price": Amount | null, "at": "…" } ] }`

Errors: `404` + message when the feature is off (`data` is an empty list for
the list endpoints); `503` "The equity market is unreachable right now; try
again shortly" when Freedom cannot be reached; a cardholder Freedom has never
seen gets an empty `200`.
