# Naira settlement of card taps

## The gap

A tap is financed by whatever the cardholder holds. Until now every tap was
settled the same way regardless: the cardholder's USDC was sold on chain to the
settlement gateway and a liquidity provider paid the merchant's bank
(`internal/chain/offramp`). A cardholder who funded their balance by naira
transfer (`docs/ngn-deposits-spec.md`) has no USDC to sell -- the tap charged
them and the merchant was never paid. The tap sat as "merchant owed" forever.

## The rule

**The merchant is paid from the cardholder's own assets, per leg.** The
platform is not the payer, holds no float, and sweeps nothing.

| What the tap took            | Who pays the merchant                                    | Where                          |
|------------------------------|----------------------------------------------------------|--------------------------------|
| naira already in the balance | the cardholder's own wallet at the bank rail (Fintava)   | `internal/settlement/naira`    |
| naira bought by converting USDC | the cardholder's own USDC, sold on chain to the gateway | `internal/chain/offramp` (unchanged) |

One tap can have both legs. Each leg has its own row, its own idempotent
reference at its rail, and its own state. The tap is **settled only when every
leg is settled**, **failed if any leg failed** (the merchant is still owed that
leg), and processing while any is in flight.

## The split

`tap.Service.fundTap` already knew whether it converted; it now reports the
split it made (`tap.Funding{NGN, USDC}`), and `recordTap` writes it on the tap
row: `funding_source` (`ngn` | `usdc` | `mixed`), `funded_ngn_minor`,
`funded_usdc_minor` (summing to `amount_minor`; a CHECK enforces it).

`tap.Charged.Legs()` turns the split into what each rail delivers. The fee
stays on the platform side as today -- booked to revenue in the ledger at the
till and never moved on either rail -- so the legs together deliver exactly
`amount - fee`. **The fee comes out of the naira first**: a naira share no
bigger than the fee (the few naira of dust an earlier conversion left behind)
never becomes a bank transfer of its own, and the on-chain sale that produced
it already covered it.

```
owed     = amount - fee
ngn leg  = max(funded_ngn - fee, 0)   (capped at owed)
usdc leg = owed - ngn leg
```

## Recording, in the tap's transaction

`internal/api/v1/offramp_wiring.go:RecordTapSettlement` is the one place that
knows both the card and the rails. For each positive leg:

- **naira** -> `naira.Record`: looks up the cardholder's `ngn_deposit_accounts`
  row on the configured rail (`rail_ref` is the Fintava customer id the wallet
  is debited by; `account_number` is what reconciliation is keyed on) and the
  merchant's **verified** `merchant_bank_accounts` row, and inserts
  `card_tap_ngn_settlements` (state `queued`, reference `tap-<tap_id>-ngn`).
- **usdc** -> `offramp.Settler.Record` as before, now with `deliver_minor` =
  this leg's share (NULL on older rows means "the whole of `amount - fee`").

Refusals fail the tap, so the charge rolls back with them -- the same class as
`errNoDepositAddress`:

| Error                     | Code the till sees                | When                                                  |
|---------------------------|-----------------------------------|-------------------------------------------------------|
| `naira.ErrNoWallet`       | `cardholder_wallet_required` 409  | a naira leg is needed and the cardholder has no wallet on the rail (or its customer id was never recorded -- run reconcile) |
| `naira.ErrNoBankAccount`  | `merchant_bank_account_required` 409 | a naira leg is needed and the merchant has no verified bank account |

A USDC-only tap does not need a wallet or a verified bank at charge time (the
provider pays whatever bank the merchant verifies later, as before).

## The naira worker

`naira.Worker` (started in `main.go`, guarded by `DISABLE_BACKGROUND_JOBS`,
every `NGN_SETTLEMENT_INTERVAL_SECONDS`, default 10 s):

1. **claim** a queued row (`queued -> submitted`, `attempts + 1`) and, in the
   same transaction, post `movements.MerchantSettledFromWallet`
   (`merchant_payable -> external`, keyed on tap + attempt). This is the same
   posture as the on-chain leg: the claim leaves the books the moment the rail
   is asked, because the cardholder's own asset is paying it.
2. name-enquire the merchant's account and refuse (terminal) if the name has
   changed since it was verified.
3. `baas.WalletTransferer.TransferFromWallet` -- for Fintava,
   `POST /bank/credit` with `sourceId` = the cardholder's customer id,
   `CustomerReference` = `tap-<id>-ngn`, narration `Tapp: <merchant name>`.
4. Outcome:
   - sync **success** -> `settled`, `rail_ref` stored;
   - sync **failed** or a rail **refusal** (`baas.IsRefusal`: 4xx) -> `failed`
     with the rail's message, and `movements.MerchantWalletSettlementReturned`
     puts the claim back in `merchant_payable`;
   - **pending** -> stays `submitted` with `rail_ref`; the Fintava transfer
     webhook (`customer_bank_transfer`, `debit_transfer_reversal`, routed by
     the `tap-` prefix in `controllers/lp/lp.go`) or the chase settles/fails it;
   - **indeterminate** (timeout, 5xx) -> stays `submitted` with the error and
     no `rail_ref`; the chase (`StaleAfter` = 2 min) asks `TransferStatus`; a
     transfer the rail has no record of is **failed, not resent**.
5. **No automatic retry.** `POST /v1/admin/settlements/ngn/{tap_id}/retry`
   (audited) puts a failed row back to `queued`; the next tick re-asks under the
   same reference (rail-side idempotency) as a new attempt (new ledger key).

## Reconciliation

`ReconcileNGNDeposit` (`internal/api/v1/ngn_deposits_ops.go`) now compares
`wallet balance + naira.PaidFromWallet(account)` (legs in `submitted` or
`settled`) against what the ledger has credited from the rail, and posts only the
shortfall, under a reference that names that gross figure and the day. It also
fills in both rail handles (`wallet_id`, and `rail_ref` = customer id) for rows
opened before they were recorded, via `baas.CustomerLocator`.

## Status and views

`internal/transactions` derives a tap's status from its legs and exposes
`SettlementRail` (`paycrest` | `fintava` | `mixed`) and `Legs` (rail, amount,
status, reference, error, settled_at). Admin `GET /v1/admin/transactions[/:id]`
adds `settlement_rail` and `legs`; the merchant list adds `settlementRail` and
`legs` (camelCase like the rest of that legacy shape). A fintava-only tap shows
its naira amount at par with the rail reference where the tx hash would be.

Admin: `GET /v1/admin/settlements/ngn?state=queued|submitted|settled|failed`,
`POST /v1/admin/settlements/ngn/{tap_id}/retry`.

## Env

| Var | Default | Meaning |
|---|---|---|
| `NGN_SETTLEMENT_INTERVAL_SECONDS` | 10 | how often queued naira legs are paid |

The rail is `baas.Default()` (Fintava, chosen from the admin Payment Rails
card); a rail that cannot pay out of a customer wallet leaves legs queued and
logs nothing per tick.

## Known limits

- A cardholder whose naira came in any way other than the wallet (cash, an
  operator's credit) has no wallet to pay from: naira legs are refused at the
  till. The ledger balance and the wallet balance are assumed to track.
- A reversed tap's queued naira leg is skipped by the worker but stays
  `queued` in the table (the read model already shows the tap as reversed).
- Which Fintava id `/bank/credit`'s `sourceId` wants (customer id vs wallet id)
  is taken from the Zerocard backbone's types (`sourceId: string // customerId`)
  and should be confirmed against a live call before the first real payout.
