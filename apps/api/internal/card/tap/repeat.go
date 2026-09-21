package tap

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usezoracle/tapp/api/internal/money"
)

// RepeatWindow is how soon after a tap the same card may be charged the same
// amount at the same till again.
//
// A card left resting on the phone is read again the moment the till is
// re-armed, and a till that still shows the last amount charges it in one
// press: two payments ten seconds apart, one of them unintended. A second
// genuine purchase of exactly the same amount, from the same card, at the
// same till, inside this window is rare enough that refusing it costs a
// merchant a moment's wait; not refusing it cost a cardholder ₦1,500.
const RepeatWindow = 30 * time.Second

// ErrRepeatTap means this card was charged this amount here moments ago.
var ErrRepeatTap = errors.New("tap: this card was just charged this amount here")

// refuseRepeat fails when the card has an unreversed tap of the same amount
// at this merchant inside RepeatWindow.
//
// Checked at the challenge, so the refusal is a message and nothing has
// moved, and again inside the debit's own transaction, so two challenges
// issued in the same second cannot both go on to charge.
func refuseRepeat(ctx context.Context, tx pgx.Tx, cardID, merchantID uuid.UUID, amount money.Amount, now time.Time) error {
	var last uuid.UUID
	err := tx.QueryRow(ctx, `
		SELECT t.id
		  FROM card_taps t
		 WHERE t.card_id = $1 AND t.merchant_id = $2
		   AND t.amount_minor = $3 AND t.currency::text = $4
		   AND t.created_at > $5
		   AND NOT EXISTS (SELECT 1 FROM card_tap_reversals r WHERE r.tap_id = t.id)
		 ORDER BY t.created_at DESC
		 LIMIT 1`,
		cardID, merchantID, amount.Minor(), string(amount.Currency()), now.Add(-RepeatWindow)).Scan(&last)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("tap: check for a repeat: %w", err)
	}
	return fmt.Errorf("%w: tap %s", ErrRepeatTap, last)
}
