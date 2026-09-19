// Package naira settles the naira leg of a card tap.
//
// A tap is financed by whatever the cardholder held. What it took from a
// naira balance -- deposited by bank transfer into the cardholder's own
// wallet at the rail -- is paid to the merchant's bank straight out of that
// wallet. What it had to buy by converting USDC is paid, as before, by
// selling that USDC on chain (internal/chain/offramp). One tap can have both
// legs; this package is the naira one.
//
// The platform is not the payer. The money goes from the cardholder's own
// asset at the rail to the merchant's bank, the same way the on-chain leg
// goes from the cardholder's own USDC to a liquidity provider who pays the
// merchant. There is no float, and nothing here holds anybody's money.
//
// Two halves:
//
//   - Record, in the tap's own transaction: one row per tap saying what this
//     leg delivers, which wallet it comes from, and which verified bank
//     account it goes to.
//   - Worker: pays queued rows, one transfer each, under a reference derived
//     from the tap so the rail cannot be asked twice for the same money;
//     follows each to the rail's confirmation.
//
// # What must never happen
//
// Money must not loop. A payout that failed is left failed, with the rail's
// own words, until an operator retries it. Nothing here retries on its own:
// the ways a transfer fails are mostly the ways a second attempt fails too.
package naira

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usezoracle/tapp/api/internal/money"
	"github.com/usezoracle/tapp/api/services/baas"
)

// States of a settlement.
const (
	// Queued: owed, nothing sent.
	Queued = "queued"
	// Submitted: the rail has been asked; the outcome is not yet known.
	Submitted = "submitted"
	// Settled: the rail confirmed the credit.
	Settled = "settled"
	// Failed: the rail refused. The merchant is still owed, and only an
	// operator can ask again.
	Failed = "failed"
)

// ErrNoBankAccount means the merchant has no verified bank account in the
// tap's currency, so there is nowhere to pay. The tap is refused: charging
// somebody for a payment that can never reach the merchant is worse than
// declining it.
var ErrNoBankAccount = errors.New("naira: the merchant has no verified bank account to settle to")

// ErrNoWallet means the cardholder has no wallet at the rail to pay the
// naira leg from: their naira balance came in some other way (cash, an
// operator's credit), or the wallet was opened before its customer id was
// recorded and nobody has reconciled it since. Refused for the same reason
// as ErrNoBankAccount -- the merchant could never be paid.
var ErrNoWallet = errors.New("naira: the cardholder has no naira wallet to pay from")

// ErrNotFailed means a retry was asked of a settlement that is not failed.
var ErrNotFailed = errors.New("naira: only a failed settlement can be retried")

// ErrNotFound means no settlement exists for that tap.
var ErrNotFound = errors.New("naira: no such settlement")

// Reference is the idempotency key presented to the rail for a tap's naira
// leg. The same on every attempt, so a retry finds the rail's own record of
// the first one rather than making a second transfer. Suffixed with the leg,
// so it can never collide with the on-chain leg's reference for the same tap.
func Reference(tapID uuid.UUID) string {
	return baas.PaymentReference("tap", tapID.String()) + referenceSuffix
}

// ReferencePrefix and referenceSuffix are what Reference produces, for
// routing a webhook back to the tap.
const (
	ReferencePrefix = "tap-"
	referenceSuffix = "-ngn"
)

// tapOf reads the tap id back out of a reference.
func tapOf(reference string) (uuid.UUID, bool) {
	if !strings.HasPrefix(reference, ReferencePrefix) || !strings.HasSuffix(reference, referenceSuffix) {
		return uuid.Nil, false
	}
	id, err := uuid.Parse(strings.TrimSuffix(strings.TrimPrefix(reference, ReferencePrefix), referenceSuffix))
	return id, err == nil
}

// Record notes, in the tap's own transaction, that a merchant must be paid
// this leg out of the cardholder's wallet.
//
// The wallet is the cardholder's deposit account at the rail, by the customer
// id the rail debits it under. A cardholder without one gets ErrNoWallet.
//
// The bank is the merchant's VERIFIED account in the tap's currency, copied
// onto the row: account_name is what the bank returned for the number, not
// what somebody typed, and a payout to anything less is money reaching a
// mistyped digit. A merchant without one gets ErrNoBankAccount.
//
// Either refusal fails the tap, which rolls the charge back.
//
// Idempotent on the tap: a second call for the same tap changes nothing.
func Record(ctx context.Context, tx pgx.Tx, tapID, cardholder, merchant uuid.UUID, rail string, owed money.Amount) error {
	if !owed.IsPositive() {
		return fmt.Errorf("naira: a settlement must be positive, got %s", owed)
	}
	if rail == "" {
		return ErrNoWallet
	}

	// The wallet's own id is what the rail takes as the source of a
	// transfer -- not the customer's. A deposit account opened before the
	// wallet id was recorded has no source to pay from.
	var walletID, sourceAccount string
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(wallet_id, ''), account_number FROM ngn_deposit_accounts
		 WHERE user_id = $1 AND rail = $2`, cardholder, rail).Scan(&walletID, &sourceAccount)
	if errors.Is(err, pgx.ErrNoRows) || (err == nil && walletID == "") {
		return ErrNoWallet
	}
	if err != nil {
		return fmt.Errorf("naira: cardholder wallet: %w", err)
	}

	var bankCode, accountNumber, accountName string
	err = tx.QueryRow(ctx, `
		SELECT bank_code, account_number, account_name
		  FROM merchant_bank_accounts
		 WHERE sender_profile_merchant_bank_account = $1
		   AND currency = $2
		   AND verified_at IS NOT NULL
		 ORDER BY verified_at DESC
		 LIMIT 1`, merchant, string(owed.Currency())).
		Scan(&bankCode, &accountNumber, &accountName)
	if errors.Is(err, pgx.ErrNoRows) {
		return ErrNoBankAccount
	}
	if err != nil {
		return fmt.Errorf("naira: merchant bank account: %w", err)
	}

	_, err = tx.Exec(ctx, `
		INSERT INTO card_tap_ngn_settlements
			(tap_id, cardholder_id, merchant_id, source_wallet_id, source_account_number,
			 currency, amount_minor, bank_code, account_number, account_name, reference)
		VALUES ($1, $2, $3, $4, $5, $6::currency, $7, $8, $9, $10, $11)
		ON CONFLICT (tap_id) DO NOTHING`,
		tapID, cardholder, merchant, walletID, sourceAccount,
		string(owed.Currency()), owed.Minor(), bankCode, accountNumber, accountName, Reference(tapID))
	if err != nil {
		return fmt.Errorf("naira: record settlement: %w", err)
	}
	return nil
}

// Settlement is one row, as the operator reads it.
type Settlement struct {
	TapID        uuid.UUID
	CardholderID uuid.UUID
	MerchantID   uuid.UUID
	// SourceWalletID and SourceAccountNumber name the cardholder's wallet
	// the leg is paid from; the rail's transfer takes the wallet id as
	// its source.
	SourceWalletID      string
	SourceAccountNumber string
	Amount              money.Amount

	// BankCode is the catalogue's institution code the merchant's account
	// was saved with. FintavaBankCode is the rail's own code for the same
	// bank, resolved on each attempt and recorded so an operator can see
	// what was sent; empty until an attempt resolved one.
	BankCode        string
	FintavaBankCode string
	AccountNumber   string
	AccountName     string

	Reference string
	State     string
	Attempts  int
	RailRef   string
	Error     string

	CreatedAt   time.Time
	UpdatedAt   time.Time
	SubmittedAt *time.Time
	SettledAt   *time.Time
}

const settlementSelect = `
	SELECT tap_id, cardholder_id, merchant_id, source_wallet_id, source_account_number,
	       currency::text, amount_minor,
	       bank_code, coalesce(fintava_bank_code, ''), account_number, account_name,
	       reference, state, attempts, coalesce(rail_ref, ''), coalesce(error, ''),
	       created_at, updated_at, submitted_at, settled_at
	  FROM card_tap_ngn_settlements`

func scanSettlement(row pgx.Row) (*Settlement, error) {
	var (
		s     Settlement
		cur   string
		minor int64
	)
	if err := row.Scan(&s.TapID, &s.CardholderID, &s.MerchantID, &s.SourceWalletID, &s.SourceAccountNumber,
		&cur, &minor,
		&s.BankCode, &s.FintavaBankCode, &s.AccountNumber, &s.AccountName,
		&s.Reference, &s.State, &s.Attempts, &s.RailRef, &s.Error,
		&s.CreatedAt, &s.UpdatedAt, &s.SubmittedAt, &s.SettledAt); err != nil {
		return nil, err
	}
	s.Amount = money.New(minor, money.Currency(cur))
	return &s, nil
}

// Querier is the read surface the listings need: a pool or a transaction.
type Querier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

// Get reads one settlement.
func Get(ctx context.Context, q Querier, tapID uuid.UUID) (*Settlement, error) {
	s, err := scanSettlement(q.QueryRow(ctx, settlementSelect+` WHERE tap_id = $1`, tapID))
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, ErrNotFound
	}
	if err != nil {
		return nil, fmt.Errorf("naira: read settlement: %w", err)
	}
	return s, nil
}

// List reads settlements newest first, all of them or those in one state.
func List(ctx context.Context, q Querier, state string, limit int) ([]*Settlement, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := q.Query(ctx, settlementSelect+`
		 WHERE ($1::text = '' OR state = $1)
		 ORDER BY created_at DESC
		 LIMIT $2`, state, limit)
	if err != nil {
		return nil, fmt.Errorf("naira: list settlements: %w", err)
	}
	defer rows.Close()
	out := []*Settlement{}
	for rows.Next() {
		s, err := scanSettlement(rows)
		if err != nil {
			return nil, fmt.Errorf("naira: list settlements: %w", err)
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// PaidFromWallet is what has left a cardholder's wallet to pay merchants:
// every leg sourced from it that the rail did not refuse. Reconciliation
// adds it back to the wallet's balance to recover what was ever deposited.
func PaidFromWallet(ctx context.Context, q Querier, accountNumber string) (money.Amount, error) {
	var minor int64
	err := q.QueryRow(ctx, `
		SELECT coalesce(sum(amount_minor), 0)
		  FROM card_tap_ngn_settlements
		 WHERE source_account_number = $1 AND state IN ('submitted', 'settled')`, accountNumber).Scan(&minor)
	if err != nil {
		return money.Amount{}, fmt.Errorf("naira: paid from %s: %w", accountNumber, err)
	}
	return money.New(minor, money.NGN), nil
}
