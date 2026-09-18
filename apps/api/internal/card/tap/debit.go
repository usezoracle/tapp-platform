package tap

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usezoracle/tapp/api/internal/card/auth"
	"github.com/usezoracle/tapp/api/internal/card/token"
	"github.com/usezoracle/tapp/api/internal/ledger/movements"
)

// Debit performs a card payment.
//
// Every step runs in one transaction, in this order:
//
//  1. claim the challenge          -- single-use, bound to card and merchant
//  2. load the card                -- must be live
//  3. verify the token             -- current or an unacknowledged pending one
//  4. satisfy the tier             -- none, PIN, or a cardholder approval
//  5. check the amount             -- must equal what was challenged
//  6. check the daily limit        -- summed from taps, not a counter
//  7. move the money               -- locks the account, refuses an overdraft
//  8. record the tap
//  9. issue the next token         -- pending, not yet promoted
//
// Nothing here contacts a bank or a chain, and nothing waits on one. The
// predecessor submitted a chain transaction at step 7, so a customer stood at
// the counter waiting on a block; when it could not reach the chain it
// fabricated a hash and returned "settled" for a payment that never moved.
// Settlement reads this ledger afterwards, on its own clock.
//
// # What commits when a tap is refused
//
// Not every refusal may roll back, and getting this wrong is worse than any
// bug the transaction was protecting against.
//
// A wrong PIN has to COMMIT: it burns the challenge and decrements the
// attempts left. Rolling it back -- which is what returning an error from the
// closure does -- would return the nonce to the pool and restore the counter,
// making a four-digit PIN guessable in an afternoon at no cost. The same goes
// for an unrecognised token, whose mismatch count is the only thing that ever
// locks a cloned card.
//
// So a refusal on the merits is carried out of the closure in `refusal` and
// returned after the commit. Only an infrastructure fault -- a failed query, a
// broken connection -- rolls back, because in that case nothing is known and
// nothing should be recorded.
//
// The exception is a step-up awaiting the cardholder's approval. Nothing has
// been decided and nothing needs recording, so the challenge is returned to
// the pool and the merchant can present it again once the approval lands.
func (s *Service) Debit(ctx context.Context, req Request) (*Receipt, error) {
	if !req.Amount.IsPositive() {
		return nil, fmt.Errorf("tap: an amount must be positive, got %s", req.Amount)
	}

	var (
		receipt *Receipt
		refusal error
	)
	err := movements.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		now := s.now()

		// 1. The challenge is claimed first, so a replay is stopped before any
		//    work is done and before anything can be learned from timing.
		challenge, err := consumeNonce(ctx, tx, req.Nonce, req.MerchantID)
		if err != nil {
			return err
		}

		// 2.
		k, err := loadCard(ctx, tx, req.CardUIDHash, req.Amount.Currency())
		if err != nil {
			return err
		}
		if err := k.usable(now); err != nil {
			return err
		}
		if err := refuseRepeat(ctx, tx, k.ID, req.MerchantID, req.Amount, now); err != nil {
			return err
		}

		// 3. A mismatch is recorded and COMMITTED: the count is what
		//    eventually locks a cloned card, and rolling it back would mean it
		//    never rose.
		match, err := k.Token.Verify(req.PresentedToken, now)
		if err != nil {
			refusal, err = s.recordTokenMismatch(ctx, tx, k)
			return err
		}

		// 4. Likewise a wrong PIN: the decremented attempt must survive.
		//    A step-up still awaiting approval is the one case that rolls
		//    back, so the merchant can present the same challenge again.
		authErr, err := s.satisfyTier(ctx, tx, k, challenge, req)
		if err != nil {
			return err
		}
		if authErr != nil {
			if errors.Is(authErr, ErrStepUpRequired) {
				return authErr // roll back: the challenge stays usable
			}
			refusal = authErr
			return nil
		}

		// 5. The debit must be for exactly what was challenged. Without this a
		//    merchant could take a no-PIN challenge for a small amount and
		//    present a large debit against it. The challenge is burned either
		//    way -- a merchant that has just tried this does not get to reuse it.
		if err := challenge.matches(req.Amount); err != nil {
			refusal = err
			return nil
		}

		// 6.
		spent, err := spentToday(ctx, tx, k.ID, req.Amount.Currency(), s.dayStart(now))
		if err != nil {
			return err
		}
		remaining, err := k.Limits.Daily.Sub(spent)
		if err != nil {
			return err
		}
		if cmp, err := req.Amount.Cmp(remaining); err != nil {
			return err
		} else if cmp > 0 {
			refusal = fmt.Errorf("%w: %s already spent today of %s, %s remaining",
				ErrDailyLimitReached, spent, k.Limits.Daily, remaining)
			return nil
		}

		// 6b. And what their identity supports. The card's own limits bound
		//     one piece of plastic; this bounds the person, across every card
		//     and every other way they can move money.
		if s.Limits != nil {
			allowed, reason, err := s.Limits.Check(ctx, *k.Cardholder, req.Amount)
			if err != nil {
				return err
			}
			if !allowed {
				refusal = fmt.Errorf("%w: %s", ErrIdentityLimitReached, reason)
				return nil
			}
		}

		// 7.
		fee := s.Fee.FeeFor(req.Amount)

		// 7a. Buy the spend, if the balance is held in another currency.
		//
		// Exactly the tap amount, and not a unit more: the platform's fee
		// comes OUT of it -- movements.Tap debits the cardholder the full
		// amount and pays the merchant amount-minus-fee -- so buying
		// amount+fee would leave the fee's worth of naira stranded in the
		// cardholder's account after every single tap.
		//
		// In the same transaction as the debit: a conversion that commits
		// without its tap would leave somebody's dollars exchanged for naira
		// they never agreed to spend.
		if funded, err := s.fundTap(ctx, tx, *k.Cardholder, req.Amount); err != nil {
			if errors.Is(err, movements.ErrInsufficientFunds) {
				refusal = err
				return nil
			}
			return err
		} else if !funded {
			refusal = fmt.Errorf("%w: no rate to price %s from %s",
				ErrCannotPrice, req.Amount, s.Funding)
			return nil
		}

		tapID := uuid.New()
		ledgerTx, err := movements.Tap(ctx, tx, *k.Cardholder, req.MerchantID, req.Amount, fee, tapID)
		if err != nil {
			// A declined card is a refusal, not a fault, and the challenge is
			// spent. Anything else is genuinely wrong and rolls back.
			if errors.Is(err, movements.ErrInsufficientFunds) {
				refusal = err
				return nil
			}
			return err
		}

		// 8.
		if err := recordTap(ctx, tx, tapRecord{
			ID: tapID, CardID: k.ID, Cardholder: *k.Cardholder, Merchant: req.MerchantID,
			Amount: req.Amount, Fee: fee, Tier: challenge.Tier,
			LedgerTx: ledgerTx, Nonce: req.Nonce, At: now,
		}); err != nil {
			return err
		}

		// 8a. Note that this tap has to be settled on chain.
		//
		// Written in the tap's own transaction, so a charge cannot exist
		// without a record that the money still has to be sold. A settlement
		// row with no tap would sell somebody's USDC for a payment that never
		// happened; a tap with no settlement row is a merchant who is never
		// paid, and neither is recoverable by looking at the other.
		if s.Settle != nil {
			if err := s.Settle(ctx, tx, tapID, *k.Cardholder, req.Amount); err != nil {
				return err
			}
		}

		// 9. The new token is PENDING. It becomes current only when the
		//    merchant app confirms it reached the card.
		next, err := token.New()
		if err != nil {
			return err
		}
		if err := s.issuePendingToken(ctx, tx, k, next, match, now); err != nil {
			return err
		}

		afterThisTap, err := remaining.Sub(req.Amount)
		if err != nil {
			return err
		}
		receipt = &Receipt{
			TapID: tapID, LedgerTxID: ledgerTx,
			Amount: req.Amount, Fee: fee, Tier: challenge.Tier,
			NewToken: next, RemainingDaily: afterThisTap,
		}
		return nil
	})
	if err != nil {
		return nil, err
	}
	if refusal != nil {
		return nil, refusal
	}
	return receipt, nil
}

// satisfyTier enforces whatever the challenge said this amount needs.
//
// It returns two errors deliberately. The first is a refusal to report to the
// cardholder, which the caller commits before returning; the second is an
// infrastructure fault, which rolls the transaction back.
//
// The tier comes from the challenge, never from recomputing it here. Limits
// can change between challenge and debit, and a tap should be held to what was
// agreed when the cardholder was asked.
func (s *Service) satisfyTier(
	ctx context.Context, tx pgx.Tx, k *card, ch *issued, req Request,
) (refusal, fault error) {
	switch ch.Tier {
	case auth.TierNone:
		return nil, nil

	case auth.TierPIN:
		err := auth.VerifyPIN(k.Anchor, req.Nonce, req.PINResponse)
		switch {
		case err == nil:
			// A correct PIN restores the allowance. Otherwise four mistakes
			// spread over a year would lock a card on the fifth.
			_, fault = tx.Exec(ctx,
				`UPDATE tapp_cards SET pin_attempts_remaining = $2, updated_at = now() WHERE id = $1`,
				k.ID, PINAttempts)
			return nil, fault
		case errors.Is(err, auth.ErrWrongPIN), errors.Is(err, auth.ErrNoPIN):
			return s.recordPINFailure(ctx, tx, k)
		default:
			return nil, err
		}

	case auth.TierStepUp:
		if req.StepUpRef == "" || req.StepUpRef != ch.ID.String() || ch.StepUpAt == nil {
			return ErrStepUpRequired, nil
		}
		return nil, nil

	default:
		return nil, fmt.Errorf("tap: unknown authentication tier %q", ch.Tier)
	}
}

// recordPINFailure decrements the allowance and locks the card at zero.
func (s *Service) recordPINFailure(ctx context.Context, tx pgx.Tx, k *card) (refusal, fault error) {
	left := k.AttemptsLeft - 1
	if left < 0 {
		left = 0
	}

	if left == 0 {
		until := s.now().Add(PINLockWindow)
		if _, err := tx.Exec(ctx, `
			UPDATE tapp_cards
			   SET pin_attempts_remaining = 0, status = 'locked', locked_until = $2, updated_at = now()
			 WHERE id = $1`, k.ID, until); err != nil {
			return nil, err
		}
		return fmt.Errorf("%w: card locked until %s", auth.ErrWrongPIN, until.Format(time.RFC3339)), nil
	}

	if _, err := tx.Exec(ctx,
		`UPDATE tapp_cards SET pin_attempts_remaining = $2, updated_at = now() WHERE id = $1`,
		k.ID, left); err != nil {
		return nil, err
	}
	return fmt.Errorf("%w: %d attempts remaining", auth.ErrWrongPIN, left), nil
}

// recordTokenMismatch counts an unrecognised token and locks the card once
// they accumulate. A card presenting a token we never issued is either cloned
// or so far out of sync that a human should look at it.
func (s *Service) recordTokenMismatch(ctx context.Context, tx pgx.Tx, k *card) (refusal, fault error) {
	count := k.MismatchCount + 1
	if count >= MismatchesBeforeLock {
		until := s.now().Add(PINLockWindow)
		if _, err := tx.Exec(ctx, `
			UPDATE tapp_cards
			   SET token_mismatch_count = $2, status = 'locked', locked_until = $3,
			       needs_resync = true, updated_at = now()
			 WHERE id = $1`, k.ID, count, until); err != nil {
			return nil, err
		}
		return fmt.Errorf("%w: card locked after %d unrecognised taps", ErrTokenStale, count), nil
	}

	if _, err := tx.Exec(ctx, `
		UPDATE tapp_cards SET token_mismatch_count = $2, needs_resync = true, updated_at = now()
		 WHERE id = $1`, k.ID, count); err != nil {
		return nil, err
	}
	return ErrTokenStale, nil
}

// issuePendingToken stores the token the merchant app is about to write.
//
// When the card presented the PENDING token, that write did land and only its
// acknowledgement was lost -- so the pending token is promoted to current
// before the new one takes its place. Otherwise current stays where it is, and
// a failed write leaves the card working on the token it already has.
func (s *Service) issuePendingToken(
	ctx context.Context, tx pgx.Tx, k *card, next []byte, match token.Match, now time.Time,
) error {
	current := k.Token.Current
	if match == token.MatchesPending {
		current = k.Token.Pending
	}

	_, err := tx.Exec(ctx, `
		UPDATE tapp_cards
		   SET current_token_ciphertext = $2,
		       pending_token_ciphertext = $3,
		       pending_token_issued_at  = $4,
		       token_mismatch_count     = 0,
		       updated_at               = now()
		 WHERE id = $1`, k.ID, current, next, now)
	return err
}

// dayStart is midnight in the operating timezone. The limit is a daily one as
// a person experiences a day, not as UTC does -- in Lagos those differ by an
// hour, which is an hour every night in which the limit resets early.
func (s *Service) dayStart(now time.Time) time.Time {
	y, m, d := now.Local().Date()
	return time.Date(y, m, d, 0, 0, 0, 0, now.Local().Location())
}
