package settlement

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"

	"github.com/usezoracle/tapp/api/internal/ledger"
	"github.com/usezoracle/tapp/api/internal/ledger/movements"
	"github.com/usezoracle/tapp/api/internal/money"
	"github.com/usezoracle/tapp/api/services/baas"
)

// confirm discharges the payout: the money has reached the account.
//
// The ONLY path that moves value out of `payable`, and it is driven by the
// provider confirming the credit. Its idempotency key is the provider's own
// reference, so a redelivered webhook and a poll that arrives at the same
// conclusion cannot discharge it twice.
func (w *Worker) confirm(ctx context.Context, p *Payout, providerRef string) error {
	reference := providerRef
	if reference == "" {
		// Some providers confirm synchronously without returning a reference.
		// The payout id still uniquely identifies the movement.
		reference = p.ID.String()
	}

	return movements.InTx(ctx, w.Pool, func(tx pgx.Tx) error {
		settleTx, err := movements.Settled(ctx, tx, p.Amount, reference)
		if err != nil && !errors.Is(err, ledger.ErrDuplicate) {
			return err
		}

		_, err = tx.Exec(ctx, `
			UPDATE payouts
			   SET state = 'confirmed', provider = $2, provider_ref = COALESCE(provider_ref, $3),
			       settle_tx_id = COALESCE(settle_tx_id, $4), settled_at = now(), updated_at = now()
			 WHERE id = $1 AND state <> 'confirmed'`,
			p.ID, w.Rail.Name(), nullIfEmpty(providerRef), nullUUID(settleTx))
		return err
	})
}

// fail is terminal: the provider refused on the merits and no money moved.
//
// The reservation is returned, because the beneficiary is owed it and the
// delivery did not happen. Leaving it in `payable` would be the platform
// quietly keeping money it neither earned nor delivered.
func (w *Worker) fail(ctx context.Context, p *Payout, reason string) error {
	if reason == "" {
		reason = "the provider refused the transfer"
	}

	return movements.InTx(ctx, w.Pool, func(tx pgx.Tx) error {
		var err error
		switch p.Beneficiary.Kind {
		case Merchant:
			// Back to what the merchant is owed. They earned it; we could not
			// deliver it.
			_, err = movements.MerchantPayoutReturned(ctx, tx, p.Beneficiary.ID, p.Amount, p.ID, reason)
		case User:
			_, err = movements.Returned(ctx, tx, p.Beneficiary.ID, p.Amount, p.ID, reason)
		}
		if err != nil && !errors.Is(err, ledger.ErrDuplicate) {
			return err
		}

		_, err = tx.Exec(ctx, `
			UPDATE payouts SET state = 'failed', last_error = $2, updated_at = now()
			 WHERE id = $1 AND state NOT IN ('confirmed', 'failed')`, p.ID, reason)
		return err
	})
}

// recordFailure decides whether a provider error is worth retrying.
//
// This is the distinction the whole worker turns on. A payout that failed
// because the float is empty must go back in the queue; one that failed
// because the account number is wrong must not, and must return the money.
// Getting it backwards either kills a transfer a top-up would have fixed, or
// retries a wrong account number until somebody notices.
func (w *Worker) recordFailure(ctx context.Context, p *Payout, err error) error {
	switch {
	case errors.Is(err, ErrUnknown), isTimeout(err):
		// It may have moved money. Chased, never retried: retrying an unknown
		// is how somebody gets paid twice.
		_, e := w.Pool.Exec(ctx, `
			UPDATE payouts SET state = 'unknown', last_error = $2, updated_at = now()
			 WHERE id = $1`, p.ID, err.Error())
		return e

	case isTemporary(err):
		if p.Attempts >= MaxAttempts {
			// Not going to succeed on the next one either, and leaving it in
			// the queue means nobody finds out.
			return w.fail(ctx, p, fmt.Sprintf(
				"gave up after %d attempts: %s", p.Attempts, err))
		}
		_, e := w.Pool.Exec(ctx, `
			UPDATE payouts SET state = 'pending', last_error = $2, updated_at = now()
			 WHERE id = $1`, p.ID, err.Error())
		return e

	default:
		return w.fail(ctx, p, err.Error())
	}
}

// chaseStale asks the provider what happened to payouts we are unsure about.
func (w *Worker) chaseStale(ctx context.Context) (int, error) {
	rows, err := w.Pool.Query(ctx, `
		SELECT id, currency, amount_minor, provider_ref, beneficiary_kind, beneficiary_id, state
		  FROM payouts
		 WHERE state IN ('sent', 'unknown', 'submitting')
		   AND updated_at < now() - $1::interval
		 LIMIT 100`, StaleAfter.String())
	if err != nil {
		return 0, fmt.Errorf("settlement: find stale payouts: %w", err)
	}

	type stale struct {
		p     Payout
		state string
	}
	var due []stale
	for rows.Next() {
		var s stale
		var currency string
		var minor int64
		var ref *string
		if err := rows.Scan(&s.p.ID, &currency, &minor, &ref,
			&s.p.Beneficiary.Kind, &s.p.Beneficiary.ID, &s.state); err != nil {
			rows.Close()
			return 0, err
		}
		s.p.Amount = money.New(minor, money.Currency(currency))
		if ref != nil {
			s.p.ProviderRef = *ref
		}
		due = append(due, s)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	chased := 0
	for _, s := range due {
		if s.p.ProviderRef == "" {
			// Never reached the provider. Safe to send again: no reference
			// means no request they could have acted on.
			if _, err := w.Pool.Exec(ctx,
				`UPDATE payouts SET state = 'pending', updated_at = now() WHERE id = $1`,
				s.p.ID); err != nil {
				return chased, err
			}
			chased++
			continue
		}

		status, err := w.Rail.TransferStatus(ctx, s.p.ProviderRef)
		if err != nil {
			slog.Warn("settlement: could not chase payout", "payout", s.p.ID, "err", err)
			continue
		}
		switch status.Status {
		case baas.TransferSuccess:
			if err := w.confirm(ctx, &s.p, s.p.ProviderRef); err != nil {
				return chased, err
			}
		case baas.TransferFailed:
			if err := w.fail(ctx, &s.p, status.Message); err != nil {
				return chased, err
			}
		default:
			// Still in flight. Touch it so it is not chased every tick.
			if _, err := w.Pool.Exec(ctx,
				`UPDATE payouts SET updated_at = now() WHERE id = $1`, s.p.ID); err != nil {
				return chased, err
			}
		}
		chased++
	}
	return chased, nil
}

// isTemporary reports whether a provider error will stop being true.
func isTemporary(err error) bool {
	if errors.Is(err, ErrTemporary) {
		return true
	}
	lower := strings.ToLower(err.Error())
	for _, s := range []string{
		"insufficient", "balance is low", "try again", "temporarily",
		"timeout", "timed out", "unavailable", "too many requests",
		"rate limit", "503", "502", "500",
	} {
		if strings.Contains(lower, s) {
			return true
		}
	}
	return false
}

func isTimeout(err error) bool {
	lower := strings.ToLower(err.Error())
	return strings.Contains(lower, "timeout") ||
		strings.Contains(lower, "deadline exceeded") ||
		strings.Contains(lower, "context canceled")
}

// Run ticks until the context ends.
func (w *Worker) Run(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			// PayMerchants is deliberately NOT called here.
			//
			// Card taps are settled on chain now: the cardholder's own USDC
			// is sold to the settlement gateway and a liquidity provider pays
			// the merchant's bank, so the platform never holds their money.
			// Draining merchant_payable through a bank rail as well would pay
			// the same tap twice -- once by the provider and once by us.
			//
			// It remains for a deployment that settles merchants from a float
			// instead, which is a different arrangement with different
			// custody, not a fallback for this one.
			submitted, chased, err := w.Tick(ctx)
			if err != nil {
				if !errors.Is(err, ErrNoRail) {
					slog.Error("settlement: tick failed", "err", err)
				}
				continue
			}
			if submitted > 0 || chased > 0 {
				slog.Info("settlement", "submitted", submitted, "chased", chased)
			}
		}
	}
}

func decimalFrom(a money.Amount) decimal.Decimal {
	return decimal.New(a.Minor(), -int32(a.Currency().Exponent()))
}

func nullIfEmpty(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}

func nullUUID(id [16]byte) *[16]byte {
	var zero [16]byte
	if id == zero {
		return nil
	}
	return &id
}
