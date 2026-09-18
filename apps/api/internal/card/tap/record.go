package tap

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usezoracle/tapp/api/internal/card/auth"
	"github.com/usezoracle/tapp/api/internal/money"
)

// tapRecord is one card payment, written alongside its ledger entries.
type tapRecord struct {
	ID         uuid.UUID
	CardID     uuid.UUID
	Cardholder uuid.UUID
	Merchant   uuid.UUID
	Amount     money.Amount
	Fee        money.Amount
	Tier       auth.Tier
	LedgerTx   uuid.UUID
	Nonce      []byte
	// At is the service's clock, not the database's: the same clock the
	// daily window and the repeat window are judged by.
	At time.Time
}

// recordTap writes the tap. It shares the caller's transaction with the ledger
// entries, so a tap row without its movement -- or a movement without its tap
// row -- cannot exist.
func recordTap(ctx context.Context, tx pgx.Tx, r tapRecord) error {
	_, err := tx.Exec(ctx, `
		INSERT INTO card_taps
			(id, card_id, cardholder_id, merchant_id, currency,
			 amount_minor, fee_minor, tier, ledger_tx_id, nonce, created_at)
		VALUES ($1, $2, $3, $4, $5::currency, $6, $7, $8, $9, $10, $11)`,
		r.ID, r.CardID, r.Cardholder, r.Merchant, string(r.Amount.Currency()),
		r.Amount.Minor(), r.Fee.Minor(), string(r.Tier), r.LedgerTx, r.Nonce, r.At)
	if err != nil {
		return fmt.Errorf("tap: record tap: %w", err)
	}
	return nil
}

// spentToday sums what a card has spent since the start of the day, net of
// anything reversed.
//
// Derived rather than counted. The predecessor kept a running total on the
// card row, incremented outside any transaction, so concurrent taps read the
// same figure and each concluded there was room; and its day counter was
// written but never compared, so the total never reset. A sum computed inside
// the debit transaction cannot drift and cannot be raced.
//
// Reversed taps are excluded. A refunded payment should not go on consuming
// somebody's daily allowance.
func spentToday(
	ctx context.Context, tx pgx.Tx, cardID uuid.UUID, c money.Currency, since time.Time,
) (money.Amount, error) {
	var minor int64
	err := tx.QueryRow(ctx, `
		SELECT COALESCE(SUM(t.amount_minor), 0)
		  FROM card_taps t
		  LEFT JOIN card_tap_reversals r ON r.tap_id = t.id
		 WHERE t.card_id = $1
		   AND t.currency = $2::currency
		   AND t.created_at >= $3
		   AND r.id IS NULL`,
		cardID, string(c), since).Scan(&minor)
	if err != nil {
		return money.Zero(c), fmt.Errorf("tap: today's spend: %w", err)
	}
	return money.New(minor, c), nil
}

// formatMinor renders an amount as the plain decimal the nonce table stores.
func formatMinor(a money.Amount) string {
	scale := a.Currency().Scale()
	minor := a.Minor()
	sign := ""
	if minor < 0 {
		sign, minor = "-", -minor
	}
	if a.Currency().Exponent() == 0 {
		return fmt.Sprintf("%s%d", sign, minor)
	}
	return fmt.Sprintf("%s%d.%0*d", sign, minor/scale, a.Currency().Exponent(), minor%scale)
}

// amountFrom rebuilds an amount from its stored parts.
func amountFrom(currency string, minor int64) money.Amount {
	return money.New(minor, money.Currency(currency))
}

// SpentToday reports what a card has spent since the start of the local day,
// net of reversals.
//
// Exported for the read paths -- the cardholder's app and the operator console
// -- which previously reported the stale `spent_today_subunit` column. That
// column is no longer written, so those screens would have shown zero forever:
// a wrong number presented as a fact, which is worse than no number.
func SpentToday(
	ctx context.Context, q Querier, cardID uuid.UUID, c money.Currency, now time.Time,
) (money.Amount, error) {
	y, m, d := now.Local().Date()
	since := time.Date(y, m, d, 0, 0, 0, 0, now.Local().Location())

	var minor int64
	err := q.QueryRow(ctx, `
		SELECT COALESCE(SUM(t.amount_minor), 0)
		  FROM card_taps t
		  LEFT JOIN card_tap_reversals r ON r.tap_id = t.id
		 WHERE t.card_id = $1 AND t.currency = $2::currency
		   AND t.created_at >= $3 AND r.id IS NULL`,
		cardID, string(c), since).Scan(&minor)
	if err != nil {
		return money.Zero(c), fmt.Errorf("tap: today's spend: %w", err)
	}
	return money.New(minor, c), nil
}

// Querier is the read surface SpentToday needs, satisfied by a pool or a tx.
type Querier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}
