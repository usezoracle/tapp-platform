# Yield on cardholders' USDC

Status: proposed 2026-10-20. Nothing is built. Lands after the demo, behind
`YIELD_ENABLED` (off by default).

## The goal

USDC a cardholder holds and has not spent earns the vault's rate, and their
balance grows by it. No app change: the yield shows up in the USDC balance
`GET /v1/me/balances` already returns.

## The constraints

1. **The money stays the cardholder's.** Today their USDC sits in their own
   CDP smart account (their deposit address) and nothing is pooled
   (`custody-and-gas-spec.md`). The vault shares must be owned by that same
   account, never by the platform.
2. **A tap must still settle.** The settler funds a Switch payout by
   transferring USDC *from that account* (`internal/chain/offramp`). USDC
   inside the vault cannot be transferred; it has to be released first.
3. **The ledger is what the app shows.** Balances and activity are read from
   the ledger, not the chain. Yield the ledger does not record is invisible;
   yield it records that the vault did not pay is money the platform owes.
4. **The cardholder gets the full vault rate.** Unlike corridor-api, there is
   no platform cut, so there is no entitlement to replay and no fee to
   collect.

## The design

### The vault

The same ERC-4626 Morpho USDC vault on Base that corridor-api uses,
`0xeE8F4eC5672F09119b96Ab6fB59C27E1b7e44b61` (`YIELD_VAULT_ADDRESS`). One
vault, no vault-to-vault moves. A vault's value per account is
`convertToAssets(balanceOf(account))`, read on chain.

### Putting money in: the sweeper

A worker, every few minutes, per cardholder account:

- **Skip** the account if anything of theirs is in flight on chain: a tap
  whose payout is not yet funded, a withdrawal not yet sent, or an earlier
  sweep or release not yet confirmed. One on-chain operation per account at
  a time. That is what keeps a sweep from moving USDC a tap is about to
  send.
- **Sweep** what the account holds in USDC, if it is at least
  `YIELD_SWEEP_MIN` (proposed 1 USDC), in one sponsored operation:
  `approve(vault, x)` then `deposit(x, account)`.
- **Record** a `vault_movements` row (`deposit`, amount, tx hash) only once
  the operation confirms.

The ledger does not move. The USDC was the cardholder's before the sweep and
is still theirs after; only where it sits changed.

### Taking money out: release before spend

The settler's `Fund` (tap payouts) and the withdrawal sender gain one step in
front of the transfer they already make:

1. Read the account's USDC balance on chain.
2. If it is short of what must be sent, **release** the shortfall:
   `withdraw(shortfall, account, account)`, as its **own** sponsored
   operation, with idempotency key `release:<reference>`. Wait for it to
   confirm and record a `vault_movements` row (`release`).
3. Send the transfer exactly as today.

The release and the transfer are two operations, not one batch. corridor-api
learned this on chain: CDP sets a UserOperation's gas limit itself, with
about 20-28k gas of margin and no override, and a vault withdrawal batched
with a payout ran out of gas (corridor transfer `01a0d97e…`). Each one on
its own fits.

A release that fails is an ordinary failed attempt. The settler already
retries an attempt up to `MaxAttempts`, and the tap waits as it does today.

### Showing the yield: the accrual job

Once a day, at 00:00 UTC, per account with a vault position:

```
principal = sum(deposits) - sum(releases)          -- confirmed vault_movements
earned    = convertToAssets(balanceOf(account)) - principal
new       = earned - already_credited               -- yield_credits
```

If `new` is at least one cent, it is credited to the cardholder's available
USD in the ledger in whole cents. The double entry is against a platform
`vault_yield` account. The fraction under a cent carries to the next run.
This is what makes the balance the app shows grow, with no app change.

Ledger USD is in cents, so a small balance shows its yield slowly. $3 at 5%
earns about $0.0004 a day and gains its first cent after about 25 days. That
is accurate, not a bug.

### Reconciliation

Per cardholder, the following must hold, with only the uncredited fraction
of a cent as difference:

```
ledger available USD  ≈  wallet USDC + vault value − in-flight spends
```

The existing reconcile path gains the vault term. A mismatch alerts and is
never auto-corrected.

## What changes, by file

| Where | Change |
|---|---|
| `internal/chain/yield` (new) | vault calls (deposit, withdraw, value), sweeper, accrual job |
| migration | `vault_movements`, `yield_credits` |
| `internal/chain/offramp/offramp.go` `Fund` | release the shortfall before the transfer |
| `internal/chain/base/withdrawals.go` | the same release before sending |
| `internal/ledger/movements` | `YieldCredit` movement |
| `main.go` | start the sweeper and accrual job when `YIELD_ENABLED` |
| config | `YIELD_ENABLED`, `YIELD_VAULT_ADDRESS`, `YIELD_SWEEP_MIN`, `YIELD_SWEEP_INTERVAL`, `YIELD_ACCRUAL_INTERVAL` |

No route, payload or response changes.

## Decisions needed

| # | Question | Proposed |
|---|---|---|
| Y1 | Leave a liquid buffer in each account so small taps need no release? | **No buffer.** Settlement runs after the till, so a release only adds seconds to the merchant's payout, never to the tap. A buffer is money that does not earn. |
| Y2 | Minimum sweep | **1 USDC.** Every sweep is a sponsored operation the platform pays gas for. |
| Y3 | The vault loses value (bad debt, share price falls), so `earned` goes negative | **Stop crediting and alert.** Never debit a cardholder automatically. The operator decides; until then the platform carries the gap. |
| Y4 | Do yield credits appear in the activity feed? | The feed is ledger history, so a credit appears as a line. **One credit per user per day at most**, so the feed gets at most one "Yield" line a day, and only after checking the PWA renders an unknown kind sensibly. If it does not, credits are left out of the feed and show in the balance only. |
| Y5 | Rollout | **Enable for one test account first** (allow-list), run a tap and a withdrawal through a release on mainnet, then everyone. |

## Risks

- **Vault liquidity.** A Morpho vault can briefly hold less idle USDC than is
  asked of it (`maxWithdraw`). A release that cannot be met delays that
  tap's payout until it can. Taps are not declined for it.
- **Curator risk.** The cardholder's USDC is exposed to the vault's lending
  markets. With Y3, a loss shows up as yield stopping, not as balances
  falling, until an operator acts.
- **More on-chain operations.** Every sweep and release is sponsored gas.
  Y1 and Y2 set the trade-off.
