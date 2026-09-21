# NGN deposits by bank transfer

## The gap

The README names three ways money comes in:

> They fund it with physical cash handed to an agent, **an NGN bank transfer**,
> or USDC on Base.

Two are built. The deposit chooser (`apps/pwa/app/deposit/page.tsx`) offers Cash
and USDC on Base, and its own subheading says *"Two ways in."* A cardholder has
no way to be given an account number to transfer naira into.

This specifies the third.

## What already exists — do not rebuild it

Most of the hard part is done, which is why this is a smaller job than it looks.

| Piece | Where | State |
|---|---|---|
| The ledger movement | `internal/ledger/movements/deposit.go` | **Done.** `Deposit` is currency-generic and its doc already names "an NGN bank transfer that landed" as a case it serves |
| Idempotency | same | **Done.** Key is `deposit:<source>:<reference>`; the doc already cites Fintava's 72-hour webhook retries as the reason |
| Virtual account creation | `services/baas/{korapay,fintava,mfb}/adapter.go` | **Done**, behind `baas.Rail.CreateSubAccount` — but only ever called for LPs and the platform float |
| Webhook signature verification | `controllers/baas_webhook.go` | **Done** |
| Balance + activity reads | `/v1/me/balances`, `/v1/me/activity` | **Done.** NGN already renders as the home currency |

What is missing is the middle: nothing provisions a **cardholder** an account
number, and nothing turns an inbound credit to one into a call to
`movements.Deposit`.

## The constraint that shapes everything: a VBA is an identifier, not a container

Korapay's adapter says it plainly — *"Korapay has no balance-holding
sub-accounts (VBAs pool …)"*, and it sets `Balance: decimal.Zero` with the
comment *"pooled rail: no per-VBA balance"*.

So on that rail the naira does not sit in the cardholder's account. It lands in
the platform's pooled account, and the virtual account number is only the label
that says whose it was. Three consequences, and they are not optional:

1. **The webhook is the only attribution.** There is no per-user balance on the
   rail to read back or reconcile against. If the callback is lost, the money is
   in the pooled account with nothing pointing at an owner.
2. **Reconciliation is one-sided.** It compares the pooled account's total
   against the sum of credits we posted, not per-user figures. A drift tells you
   *that* something is unattributed, not *whose*.
3. **The rails differ and must not leak.** Fintava's is a `STATIC_FUND` wallet;
   Safe Haven MFB uses sub-accounts. Express the feature against
   `baas.Rail`, never against a provider's shape, exactly as the payout path
   already does.

## Design

### Provisioning

```
POST /v1/deposits/ngn/account     -> { account_number, bank_name, account_name }
GET  /v1/deposits/ngn/account     -> the same, or 404 when not provisioned
```

Allocated on first request and stable thereafter, mirroring
`GET /v1/deposits/address`: an account number somebody saved as a payee must
keep working.

**BVN is required.** Korapay's `CreateSubAccount` refuses without one
(`adapter.go:168`), and the code notes the BVN itself is needed for the VBA's
`kyc` field. So this endpoint is gated behind the existing KYC ladder in
`internal/identity/kyc`, and must return a distinguishable error when the caller
has not cleared it — not a generic 400. The BVN is passed to the rail and, as
the admin float provisioning already records, **never stored by us**.

**Not registered when no rail is configured.** The same rule the Base rail
follows: an account number nobody is watching is money sent into a void. With
`BaaSConfig` empty the routes do not exist, and the PWA hides the option rather
than offering a button that 404s.

### Table

```sql
CREATE TABLE ngn_deposit_accounts (
    user_id        uuid PRIMARY KEY,
    rail           text        NOT NULL,   -- korapay | fintava | mfb
    account_number text        NOT NULL,
    bank_name      text        NOT NULL,
    account_name   text        NOT NULL,
    rail_ref       text        NOT NULL,   -- the rail's own id for this account
    created_at     timestamptz NOT NULL DEFAULT now(),
    UNIQUE (rail, account_number)
);
```

`rail` is stored per row, not read from config, so that accounts issued under a
previous rail still resolve after a switch. `services.CurrentFloatRail()`
already exists and can be switched at runtime; an account provisioned before the
switch must still credit after it.

### Crediting

One new branch in the BaaS webhook. Today it routes by `paymentReference` prefix
into the settlement (outbound) flows; an inbound credit naming a known cardholder
account number is a new case:

```
verify signature  ─►  is this an inbound credit?  ─►  look up account_number
                                                            │
                                                     known cardholder?
                                                       │           │
                                                      yes          no ──► existing
                                                       │                  settlement routing
                                    movements.Deposit(ctx, tx, user,
                                        money.New(kobo, money.NGN),
                                        rail, providerReference)
```

Posting:

```
external (system) NGN   -amount     deposit.from_korapay
user available    NGN   +amount     deposit.credited
```

Identical in shape to the Base path at `internal/chain/base/deposits.go:142`,
which posts `deposit.from_base`. `source` is the rail name so the reason line
says which rail the money came in on, and so the idempotency key cannot collide
across rails.

**Idempotency comes free.** `deposit:<rail>:<providerReference>` is the key, and
a redelivered webhook hits `ledger.ErrDuplicate`, which the caller treats as
"already done" and ACKs 200. This is the mechanism working, not a failure.

### The one thing genuinely new: reversals

A confirmed chain deposit cannot be undone, which is what `BASE_CONFIRMATIONS`
buys. A bank credit can — a mistaken or fraudulent inbound transfer may be
clawed back by the sending bank days later, and by then the cardholder may have
spent it.

This has no equivalent in the existing deposit paths and must be decided rather
than discovered:

- **Option A — credit immediately.** Best experience, and the platform absorbs
  any reversal as a loss (`loss_reserve` exists in the ledger for exactly this).
- **Option B — hold new payers.** First transfer from an unseen source sits in
  `escrow` for a defined window, then releases. Safer, worse first impression.
- **Option C — cap it.** Credit immediately below a threshold, hold above it.

**Recommendation: A, with a cap.** Reversal risk on small NGN transfers into a
KYC'd, BVN-verified account is low, and `obligation` already exists in the ledger
to represent a person owing the platform after a clawback — currently declared
but unused, which makes this its first honest use. The cap is a parameter, not a
constant.

Whatever is chosen, the reversal path itself must be a named movement in
`movements`, never assembled ad hoc in a handler.

### Reconciliation

A periodic job comparing the pooled account balance against the sum of NGN
credits posted, in the shape of the existing `tasks/reconcile.go`. A drift is an
alert, never an automatic correction: on a pooled rail the platform cannot tell
whose money is unattributed, and guessing would credit the wrong person.

## Operations

Two things the first accounts taught, and the knobs that fix them.

### The bank name

Fintava's create-customer response names the bank as `wallet.serviceProvider`
(and/or `wallet.bank`) -- e.g. `"loma"` -- never as `bankName`. The first
decoder read `bankName`, got nothing, and stored the literal
**"Fintava partner bank"**, which real people were then shown as the bank to
pay into. Now:

- The adapter decodes `serviceProvider`/`bank` (string or object), resolves it
  against the rail's bank list to a display name and NIP code ("loma" →
  "Loma Microfinance Bank" / 090620), and stores both (`bank_name`,
  `bank_code`; migration `0023_ngn_deposit_bank_code.sql`).
- `FINTAVA_DEPOSIT_BANK_NAME` / `FINTAVA_DEPOSIT_BANK_CODE` (default empty)
  are used at provisioning when the response has no bank, and at read time
  (`GET /v1/deposits/ngn/account`) in place of the placeholder on rows that
  still carry it. The placeholder is never shown to a person again; with no
  fallback configured the name comes back blank.
- `POST /v1/admin/deposits/ngn/accounts/{account_number}/bank` with
  `{"bank_name": "...", "bank_code": "..."}` corrects a row in place (audited
  as `ngn_deposit.bank.set`). `GET /v1/admin/deposits/ngn/accounts?email=`
  finds the row and says whether it `needs_bank_fix`.

### Reconciliation (credits the webhook never delivered)

Fintava sends its webhook to ONE url, and that url was the Zerocard backbone
until forwarding to this API was deployed. Credits that landed before then
were never posted here. STATIC_FUND wallets hold what they receive until
transferred out, and nothing in this codebase transfers out of them, so the
wallet's balance is the total ever deposited.

`POST /v1/admin/deposits/ngn/accounts/{account_number}/reconcile` (audited as
`ngn_deposit.reconcile`) reads the wallet balance, sums what the ledger has
already credited to the owner from source `fintava`, and posts the shortfall
as one deposit with reference `reconcile:<account>:<balance-kobo>:<date>`.
Idempotent: a second run finds no shortfall and posts nothing; a balance
below what was credited is reported in `note` and nothing is posted -- money
leaving the wallet by a path this system did not record is a question for a
person, not a debit. Response:
`{account_number, wallet_balance, credited_before, posted, reference, note, wallet_id}`.

The balance endpoint takes Fintava's *wallet* id, and `rail_ref` holds the
*customer* id (a different string, and on the live response shape it was not
even decoded). The wallet id is now stored in `wallet_id`; for rows opened
before it existed, reconciliation looks the wallet up by the owner's email
and account number (`GET /customers/list`) and records it.

## What cannot be tested here

No BaaS rail is configured on this instance — boot logs `BaaS rail (mfb) not
configured; fiat payout routes disabled`. So provisioning and the live webhook
cannot be exercised end to end without credentials.

What can be tested without a rail, and should be:

- the ledger movement, against a fake rail, as `settlement/worker_test.go`
  already does with `fakeRail`
- idempotency: the same reference twice credits once
- an unknown account number falls through to the existing settlement routing
  rather than erroring
- the routes are absent when no rail is configured

## Open decisions

1. **Reversal policy** — A, B or C above. Recommendation is A with a cap.
2. **Which rail is primary for cardholders.** Korapay is the only one whose
   VBA path is exercised today (`ProvisionFloatAccount`), so it is the shortest
   route to something working.
3. **Whether the cap, once chosen, is per-transfer or per-day.**
4. **Naming.** `/v1/deposits/ngn/account` mirrors `/v1/deposits/address`; if
   the intent is that a cardholder never thinks in currencies,
   `/v1/deposits/bank` may read better.

## Not in scope

Payouts (`Withdraw` and the settlement worker already cover naira going out),
merchant settlement, and LP funding. This is one direction for one party:
naira arriving into a cardholder's balance.
