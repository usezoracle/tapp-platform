package tap

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usezoracle/tapp/api/internal/ledger/movements"
)

// ErrTapUnknown means no such tap belongs to this merchant.
var ErrTapUnknown = errors.New("tap not found")

// Acknowledge completes the token rotation a debit started.
//
// written reports whether the merchant app got the new token onto the card.
//
//	true  -- promote it: the pending token becomes the only valid one.
//	false -- discard it: the card still holds the previous token, which is
//	         still current, so it keeps working and nobody has to re-sync.
//
// This is the half the predecessor did not have. It rotated at debit time and
// used the acknowledgement only to set a needs_resync flag, so a lost write
// left a legitimate card permanently unable to pay -- and the recovery flow
// required Web NFC, which iOS does not implement, so for half the market there
// was no recovery at all.
//
// Not acknowledging is safe too. An unpromoted pending token simply expires,
// after which the card is back to a single valid value.
func (s *Service) Acknowledge(ctx context.Context, merchantID, tapID uuid.UUID, written bool) error {
	return movements.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var cardID uuid.UUID
		err := tx.QueryRow(ctx,
			`SELECT card_id FROM card_taps WHERE id = $1 AND merchant_id = $2`,
			tapID, merchantID).Scan(&cardID)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTapUnknown
		}
		if err != nil {
			return fmt.Errorf("tap: locate tap: %w", err)
		}

		if written {
			_, err = tx.Exec(ctx, `
				UPDATE tapp_cards
				   SET current_token_ciphertext = COALESCE(pending_token_ciphertext, current_token_ciphertext),
				       pending_token_ciphertext = NULL,
				       pending_token_issued_at  = NULL,
				       needs_resync             = false,
				       updated_at               = now()
				 WHERE id = $1`, cardID)
		} else {
			_, err = tx.Exec(ctx, `
				UPDATE tapp_cards
				   SET pending_token_ciphertext = NULL,
				       pending_token_issued_at  = NULL,
				       updated_at               = now()
				 WHERE id = $1`, cardID)
		}
		if err != nil {
			return fmt.Errorf("tap: acknowledge token: %w", err)
		}
		return nil
	})
}

// Reverse refunds a tap in full.
//
// One reversal per tap, enforced by a unique constraint rather than by a
// check-then-insert that two concurrent requests could both pass.
func (s *Service) Reverse(ctx context.Context, merchantID, tapID uuid.UUID, reason string) error {
	if reason == "" {
		return fmt.Errorf("tap: a reversal must say why")
	}

	return movements.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		var (
			cardholder, merchant  uuid.UUID
			currency              string
			amountMinor, feeMinor int64
		)
		err := tx.QueryRow(ctx, `
			SELECT cardholder_id, merchant_id, currency, amount_minor, fee_minor
			  FROM card_taps WHERE id = $1 AND merchant_id = $2`,
			tapID, merchantID).Scan(&cardholder, &merchant, &currency, &amountMinor, &feeMinor)
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrTapUnknown
		}
		if err != nil {
			return fmt.Errorf("tap: locate tap: %w", err)
		}

		amount, fee := amountFrom(currency, amountMinor), amountFrom(currency, feeMinor)
		ledgerTx, err := movements.TapReversal(ctx, tx, cardholder, merchant, amount, fee, tapID, reason)
		if err != nil {
			return err
		}

		_, err = tx.Exec(ctx, `
			INSERT INTO card_tap_reversals (id, tap_id, reason, ledger_tx_id)
			VALUES (gen_random_uuid(), $1, $2, $3)`, tapID, reason, ledgerTx)
		if err != nil {
			return fmt.Errorf("tap: record reversal: %w", err)
		}

		// The market has to unwind what the tap bought, and it learns that
		// the same way it learned of the tap: a row in this transaction.
		if s.EquityReversal != nil {
			if err := s.EquityReversal(ctx, tx, tapID, reason); err != nil {
				return err
			}
		}
		return nil
	})
}
