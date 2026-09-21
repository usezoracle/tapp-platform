package settlement

import (
	"context"
	"errors"
	"fmt"
	"log/slog"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usezoracle/tapp/api/internal/ledger/movements"
	"github.com/usezoracle/tapp/api/internal/money"
)

// MinMerchantPayoutMinor is the least a merchant payout may be.
//
// A bank transfer costs the same whether it carries fifty naira or fifty
// thousand, so paying out every few naira as it accrues spends more on fees
// than it delivers. Below this the balance stays owed -- visible to the
// merchant, and paid as soon as the next tap takes it over the line.
//
// It is not a fee and nothing is deducted: this only decides WHEN money moves,
// never how much.
const MinMerchantPayoutMinor = 10_000 // ₦100.00

// PayMerchants opens a payout for every merchant owed more than the minimum.
//
// Driven from the accrued ledger balance rather than from each tap.
//
// A tap must not depend on a bank provider being reachable: the cardholder is
// standing at a till and the money has already left their balance, so failing
// the tap because a payout could not be opened would decline a payment that
// already happened. Reading the balance afterwards also settles many taps in
// one transfer, which is what makes the fee bearable.
//
// It opens payouts and nothing more. Tick submits them, chases the ones that
// time out, and returns the money on a terminal refusal -- all of which
// already handle merchant beneficiaries.
func (w *Worker) PayMerchants(ctx context.Context) (opened int, err error) {
	// No rail, nothing opened.
	//
	// Opening reserves the claim out of merchant_payable and into the
	// system's payable, which is the honest place for "owed and not yet
	// landed" -- but only once something can actually land it. With no
	// provider configured that reservation would empty the account the
	// merchant is shown while no transfer is even attempted, making a gap in
	// our configuration look like money that has left. It stays where they
	// can see it until there is a rail to deliver it.
	//
	// Nil Rail means UNCONFIGURED, which is exactly the case this guards. A
	// provider that is merely down fails at submission instead, where the
	// retry and chase logic belongs.
	if w.Rail == nil {
		return 0, ErrNoRail
	}

	// One query for the whole decision: who is owed, and where it goes.
	//
	// The bank account must be VERIFIED. account_name is what the bank
	// returned for the number, not what somebody typed, and paying an
	// unverified account is how money reaches a mistyped digit and does not
	// come back.
	//
	// A merchant with no verified account is skipped, not failed: they are
	// owed the money and can add one. Their balance keeps accruing.
	rows, err := w.Pool.Query(ctx, `
		SELECT a.owner_id,
		       a.currency,
		       COALESCE(SUM(e.amount_minor), 0) AS owed_minor,
		       b.bank_code,
		       b.account_number,
		       b.account_name
		  FROM ledger_accounts a
		  JOIN ledger_entries e ON e.account_id = a.id
		  JOIN merchant_bank_accounts b
		    ON b.sender_profile_merchant_bank_account = a.owner_id
		   -- merchant_bank_accounts.currency is plain text; ledger_accounts
		   -- uses the currency enum. Cast rather than relying on Postgres
		   -- to compare them, which it will not.
		   AND b.currency = a.currency::text
		   AND b.verified_at IS NOT NULL
		 WHERE a.owner_kind = 'merchant'
		   AND a.kind = 'merchant_payable'
		 GROUP BY a.owner_id, a.currency, b.bank_code, b.account_number, b.account_name
		HAVING COALESCE(SUM(e.amount_minor), 0) >= $1
		 ORDER BY a.owner_id`, MinMerchantPayoutMinor)
	if err != nil {
		return 0, fmt.Errorf("settlement: find merchants owed: %w", err)
	}

	type due struct {
		merchant uuid.UUID
		amount   money.Amount
		bank     string
		number   string
		name     string
	}
	var owed []due
	for rows.Next() {
		var (
			d     due
			cur   string
			minor int64
		)
		if err := rows.Scan(&d.merchant, &cur, &minor, &d.bank, &d.number, &d.name); err != nil {
			rows.Close()
			return 0, err
		}
		d.amount = money.New(minor, money.Currency(cur))
		owed = append(owed, d)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for _, d := range owed {
		// One transaction per merchant. A provider or database failure on one
		// must not hold up the others, and each payout's reservation has to
		// commit with its own row -- see Open.
		err := movements.InTx(ctx, w.Pool, func(tx pgx.Tx) error {
			// Re-read under the lock MerchantSettled takes.
			//
			// The figure above is a snapshot: a tap between the query and here
			// would make it stale, and paying out more than is owed is what
			// ensureFunds refuses. Opening for the amount that is actually
			// there means a tap arriving mid-pass is simply included in the
			// next one.
			_, e := w.OpenIn(ctx, tx, Request{
				Beneficiary:   Beneficiary{Kind: Merchant, ID: d.merchant},
				Amount:        d.amount,
				BankCode:      d.bank,
				AccountNumber: d.number,
				AccountName:   d.name,
				Narration:     "Tapp card settlement",
			})
			return e
		})
		if err != nil {
			// Insufficient funds here means the balance moved under us, which
			// is ordinary: the next pass reads the new figure. Anything else
			// is worth saying out loud.
			if errors.Is(err, movements.ErrInsufficientFunds) {
				continue
			}
			slog.Error("settlement: could not open merchant payout",
				"merchant", d.merchant, "amount", d.amount, "err", err)
			continue
		}
		opened++
	}
	return opened, nil
}
