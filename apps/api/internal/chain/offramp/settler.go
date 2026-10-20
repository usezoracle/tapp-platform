package offramp

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"

	"github.com/usezoracle/tapp/api/internal/equity"
	"github.com/usezoracle/tapp/api/internal/ledger/movements"
	"github.com/usezoracle/tapp/api/internal/money"
)

// MaxAttempts bounds how often one tap's order is retried.
//
// A tap that cannot be sold after this many tries is not going to start
// working on the next tick, and retrying it forever buries the taps behind it
// under log noise. It is left failed and visible rather than retried in
// silence.
const MaxAttempts = 5

// MaxRounds bounds how many orders one tap may create.
//
// A refunded order is sold again, because the usual reason is the provider of
// the moment -- unavailable, or the amount outside their limits -- and the
// next one may fill it. It is not sold forever: every round spends sponsored
// gas, and a tap the market keeps declining is an operator's problem, left
// failed with the merchant's claim standing where the audit shows it.
const MaxRounds = 3

// RetryDelay is how long a refunded tap waits before its next round.
//
// Whatever declined the order is unlikely to have changed in the seconds it
// takes the tracker to notice; a fresh order straight away would most likely
// buy a second refund.
const RetryDelay = 10 * time.Minute

// Execer is the part of a transaction Record needs, so the tap can pass its
// own and the write commits with the charge.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// Settler turns taps into Switch payouts.
//
// Separate from the tap itself on purpose. Funding a payout is an on-chain
// operation, and the cardholder is standing at a till: making the tap wait for
// a chain confirmation would put a provider's latency between somebody and
// their coffee. The tap authorises against the ledger and finishes; this
// follows moments later.
type Settler struct {
	Pool   *pgxpool.Pool
	Orders Rail
	Now    func() time.Time
}

// Rail is the provider a tap's USDC is sold through. Satisfied by Client.
//
// Three steps, so the payout can be written down between opening it and
// funding it: the deposit address is recorded before any money moves, which
// is what lets the deposit watcher recognise a refund from it.
type Rail interface {
	Open(ctx context.Context, o Order) (Payment, error)
	Fund(ctx context.Context, from string, p Payment) (string, error)
	Outcome(ctx context.Context, reference, txHash string) (Outcome, string, error)
}

func (s *Settler) now() time.Time {
	if s.Now != nil {
		return s.Now()
	}
	return time.Now()
}

// Record notes that a tap needs settling on chain: deliver is what this
// leg pays the merchant, and sellMicro the USDC estimated to cover it.
//
// Written by the tap, in the tap's own transaction, so a charge cannot exist
// without a record that it has to be settled. The primary key on tap_id is
// what makes one payment open one order and no more.
//
// deliver is what the on-chain leg owes the merchant. It is the whole of the
// tap less the fee when the tap was paid entirely by converting USDC, and
// less again when part of the tap came from a naira balance, which the naira
// leg pays for (internal/settlement/naira).
func (s *Settler) Record(
	ctx context.Context, q Execer, tapID uuid.UUID, from string, sellMicro int64, deliver money.Amount,
) error {
	if !deliver.IsPositive() {
		return fmt.Errorf("offramp: an order must deliver a positive amount, got %s", deliver)
	}
	_, err := q.Exec(ctx, `
		INSERT INTO card_tap_settlements (tap_id, from_address, sell_micro, deliver_minor)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (tap_id) DO NOTHING`, tapID, from, sellMicro, deliver.Minor())
	if err != nil {
		return fmt.Errorf("offramp: record tap settlement: %w", err)
	}
	return nil
}

// Tick creates orders for taps that have none yet.
func (s *Settler) Tick(ctx context.Context) (created int, err error) {
	if s.Orders == nil {
		return 0, ErrNotConfigured
	}

	// Everything one order needs, in one read: what to sell, from where, and
	// the bank the merchant verified.
	//
	// The bank must be VERIFIED -- account_name is what the bank returned for
	// the number, not what somebody typed, and a provider pays against that
	// blob without a second check. A merchant who has not verified one leaves
	// their taps outstanding rather than sending money to a mistyped digit.
	//
	// A later round waits RetryDelay from when the refund was noticed; the
	// first round is due at once.
	//
	// A reversed tap is never sold. The cardholder has their money back and
	// the merchant's claim is withdrawn, so there is nothing an order would
	// pay for -- and a settlement row put back to pending by an operator
	// after a reversal must not undo that from the chain side.
	rows, err := s.Pool.Query(ctx, `
		SELECT st.tap_id, st.from_address, st.sell_micro, st.attempts, st.round,
		       t.merchant_id, t.currency, coalesce(st.deliver_minor, t.amount_minor - t.fee_minor),
		       b.bank_code, b.account_number, b.account_name
		  FROM card_tap_settlements st
		  JOIN card_taps t ON t.id = st.tap_id
		  JOIN merchant_bank_accounts b
		    ON b.sender_profile_merchant_bank_account = t.merchant_id
		   AND b.currency = t.currency::text
		   AND b.verified_at IS NOT NULL
		 WHERE st.state = 'pending'
		   AND st.attempts < $1
		   AND (st.round = 0 OR st.updated_at <= $2)
		   AND NOT EXISTS (SELECT 1 FROM card_tap_reversals r WHERE r.tap_id = st.tap_id)
		 ORDER BY st.created_at
		 LIMIT 20`, MaxAttempts, s.now().Add(-RetryDelay))
	if err != nil {
		return 0, fmt.Errorf("offramp: find taps to settle: %w", err)
	}

	type pending struct {
		tap      uuid.UUID
		from     string
		sell     int64
		attempts int
		round    int
		merchant uuid.UUID
		deliver  money.Amount
		bank     Bank
	}
	var due []pending
	for rows.Next() {
		var (
			p pending
			// What the MERCHANT receives from this leg: the tap less the
			// platform's fee, less whatever the naira leg pays. Selling
			// against the gross would deliver them money the ledger says is
			// ours.
			cur   string
			minor int64
		)
		if err := rows.Scan(&p.tap, &p.from, &p.sell, &p.attempts, &p.round,
			&p.merchant, &cur, &minor,
			&p.bank.Institution, &p.bank.AccountNumber, &p.bank.AccountName); err != nil {
			rows.Close()
			return 0, err
		}
		p.deliver = money.New(minor, money.Currency(cur))
		due = append(due, p)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for _, p := range due {
		// The attempt is counted BEFORE the call, not after.
		//
		// The transfer can land on chain and still fail to answer us.
		// Counting afterwards would leave a funded payout looking untried,
		// and the next tick would sell the cardholder's money a second time.
		// The reference on the payout and the operation is the other half of
		// that guarantee; this is the half that survives a crash.
		if _, err := s.Pool.Exec(ctx, `
			UPDATE card_tap_settlements
			   SET attempts = attempts + 1, updated_at = now()
			 WHERE tap_id = $1`, p.tap); err != nil {
			return created, err
		}

		ref := reference(p.tap, p.round)
		pay, err := s.Orders.Open(ctx, Order{
			From:      p.from,
			Deliver:   p.deliver,
			Bank:      p.bank,
			Reference: ref,
		})
		if err != nil {
			if err := s.attemptFailed(ctx, p.tap, p.attempts, err); err != nil {
				return created, err
			}
			continue
		}

		// Written down before any money moves: the address the USDC goes to,
		// so a refund from it is never credited as a deposit, and the amount
		// Switch priced, so the row says what was actually sold.
		if err := movements.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `
				INSERT INTO offramp_payments (reference, tap_id, round, deposit_address, sell_micro)
				VALUES ($1, $2, $3, $4, $5)
				ON CONFLICT (reference) DO NOTHING`,
				ref, p.tap, p.round, strings.ToLower(pay.Address.Hex()), pay.Sell); err != nil {
				return err
			}
			_, err := tx.Exec(ctx, `
				UPDATE card_tap_settlements
				   SET sell_micro = $2, updated_at = now()
				 WHERE tap_id = $1`, p.tap, pay.Sell)
			return err
		}); err != nil {
			return created, fmt.Errorf("offramp: record payout %s for tap %s: %w", ref, p.tap, err)
		}

		var txHash *string
		if !pay.Funded() {
			hash, err := s.Orders.Fund(ctx, p.from, pay)
			if err != nil {
				if err := s.attemptFailed(ctx, p.tap, p.attempts, err); err != nil {
					return created, err
				}
				continue
			}
			txHash = &hash
		}

		// Marking it submitted and discharging what the merchant is owed
		// commit together.
		//
		// The USDC is with Switch either way, so the question is only what
		// the books say about it. A submitted payout with the claim still
		// standing would have us owing money a provider has been paid to
		// deliver; a discharged claim with no record of the payout would lose
		// the only thing tying it to Switch.
		if err := movements.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `
				UPDATE card_tap_settlements
				   SET state = 'submitted', tx_hash = $2, order_id = $3,
				       last_error = NULL, updated_at = now()
				 WHERE tap_id = $1`, p.tap, txHash, ref); err != nil {
				return err
			}
			_, err := movements.MerchantSettledOnChain(ctx, tx, p.merchant, p.deliver, p.tap, p.round)
			return err
		}); err != nil {
			// The payout is funded. Failing here loses only our note of it,
			// and the attempt counter above stops it being sold again.
			return created, fmt.Errorf("offramp: record submitted payout %s for tap %s: %w",
				ref, p.tap, err)
		}
		created++
	}
	return created, nil
}

// attemptFailed records an attempt that moved no money, leaving the tap for
// the next tick or, after MaxAttempts, failed and visible.
func (s *Settler) attemptFailed(ctx context.Context, tap uuid.UUID, attempts int, cause error) error {
	final := attempts+1 >= MaxAttempts
	state := "pending"
	if final {
		state = "failed"
	}
	if _, err := s.Pool.Exec(ctx, `
		UPDATE card_tap_settlements
		   SET state = $2, last_error = $3, updated_at = now()
		 WHERE tap_id = $1`, tap, state, cause.Error()); err != nil {
		return err
	}
	if final {
		slog.Error("offramp: giving up on a tap settlement",
			"tap", tap, "attempts", attempts+1, "err", cause)
	} else {
		slog.Warn("offramp: tap settlement failed, will retry",
			"tap", tap, "attempt", attempts+1, "err", cause)
	}
	return nil
}

// reference is Switch's key for one round's payout, and the idempotency key
// of the transfer that funds it.
//
// Switch keys payouts on a UUID. The first round is the tap id itself; a
// later round, sold again after a refund, needs a key of its own or Switch
// would hand back the payout that was refunded. It is derived from the tap
// and the round, so it is the same on every retry of that round.
func reference(tap uuid.UUID, round int) string {
	if round == 0 {
		return tap.String()
	}
	return uuid.NewSHA1(tap, []byte(fmt.Sprintf("round:%d", round))).String()
}

// Track follows submitted payouts to their outcome.
//
// A funded payout is a question, not an answer: Switch pays the bank or
// refunds the cardholder, and until one of those has happened the merchant
// has not been paid. Tick discharged the merchant's claim on submission
// because that is what a completed payout means; this is what puts the claim
// back when the payout fails instead, and sells again while there are rounds
// left.
func (s *Settler) Track(ctx context.Context) (resolved int, err error) {
	if s.Orders == nil {
		return 0, ErrNotConfigured
	}

	rows, err := s.Pool.Query(ctx, `
		SELECT st.tap_id, st.tx_hash, st.order_id, st.round,
		       t.merchant_id, t.currency, coalesce(st.deliver_minor, t.amount_minor - t.fee_minor)
		  FROM card_tap_settlements st
		  JOIN card_taps t ON t.id = st.tap_id
		 WHERE st.state = 'submitted'
		 ORDER BY st.created_at
		 LIMIT 50`)
	if err != nil {
		return 0, fmt.Errorf("offramp: find submitted payouts: %w", err)
	}

	type submitted struct {
		tap      uuid.UUID
		txHash   *string
		ref      *string
		round    int
		merchant uuid.UUID
		deliver  money.Amount
	}
	var open []submitted
	for rows.Next() {
		var (
			o     submitted
			cur   string
			minor int64
		)
		if err := rows.Scan(&o.tap, &o.txHash, &o.ref, &o.round, &o.merchant, &cur, &minor); err != nil {
			rows.Close()
			return 0, err
		}
		o.deliver = money.New(minor, money.Currency(cur))
		open = append(open, o)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, err
	}

	for _, o := range open {
		// Every payout Tick submits carries its Switch reference, a UUID.
		// Anything else was submitted to the settlement gateway this
		// replaced, which Switch cannot answer for; it is said, not polled.
		if o.ref == nil || uuid.Validate(*o.ref) != nil {
			slog.Error("offramp: submitted settlement has no Switch reference; follow it up by hand",
				"tap", o.tap, "order", hashOf(o.ref), "tx", hashOf(o.txHash))
			continue
		}

		tx := ""
		if o.txHash != nil {
			tx = *o.txHash
		}
		outcome, status, err := s.Orders.Outcome(ctx, *o.ref, tx)
		if err != nil {
			slog.Warn("offramp: could not read payout", "tap", o.tap, "reference", *o.ref, "err", err)
			continue
		}
		switch outcome {
		case Fulfilled:
			if err := s.fulfilled(ctx, o.tap); err != nil {
				return resolved, err
			}
			resolved++
		case Refunded:
			why := "Switch reports " + status
			if err := s.refunded(ctx, o.tap, o.merchant, o.deliver, o.round, *o.ref, why); err != nil {
				return resolved, err
			}
			resolved++
		}
	}
	return resolved, nil
}

// fulfilled records that the provider paid the merchant. This leg is done,
// and the tap's shares -- held back until the merchant was paid -- are
// released to the market if no other leg is still outstanding. One
// transaction, so a leg cannot be fulfilled without its release being
// considered.
func (s *Settler) fulfilled(ctx context.Context, tap uuid.UUID) error {
	return movements.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE card_tap_settlements
			   SET state = 'fulfilled', updated_at = now()
			 WHERE tap_id = $1 AND state = 'submitted'`, tap)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		_, err = equity.ReleaseIfSettled(ctx, tx, tap)
		return err
	})
}

// refunded records that a round paid nobody: the merchant's claim comes back,
// and the tap is either queued for another round or left failed.
//
// One transaction, so the books and the row cannot disagree about whether the
// refund has been taken into account. The state predicate makes a second
// tracker, or a second tick of this one, find nothing to do.
func (s *Settler) refunded(
	ctx context.Context, tap, merchant uuid.UUID, deliver money.Amount, round int, ref, why string,
) error {
	next := round + 1
	state := "pending"
	if next >= MaxRounds {
		state = "failed"
	}
	return movements.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
		tag, err := tx.Exec(ctx, `
			UPDATE card_tap_settlements
			   SET state = $2, round = $3, order_id = NULL, tx_hash = NULL,
			       last_error = $4, updated_at = now()
			 WHERE tap_id = $1 AND state = 'submitted'`,
			// The refunded payout's reference stays on the row, in words:
			// order_id is cleared for the next round, and offramp_payments
			// keeps every round's reference and deposit address.
			tap, state, next, fmt.Sprintf("round %d (%s): %s", round, ref, why))
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return nil
		}
		_, err = movements.MerchantSettlementRefunded(ctx, tx, merchant, deliver, tap, round, why)
		if err != nil {
			return err
		}
		if state == "failed" {
			slog.Error("offramp: giving up on a tap settlement, the merchant is still owed",
				"tap", tap, "rounds", next, "owed", deliver.String())
		} else {
			slog.Warn("offramp: payout refunded, will sell again",
				"tap", tap, "round", round, "why", why)
		}
		return nil
	})
}

func hashOf(h *string) string {
	if h == nil {
		return "none"
	}
	return *h
}

// Run settles and tracks until the context ends.
func (s *Settler) Run(ctx context.Context, every time.Duration) {
	ticker := time.NewTicker(every)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			created, err := s.Tick(ctx)
			if err != nil {
				// Not configured is a deployment without Switch, which is
				// said once at boot; repeating it every tick adds nothing.
				if !errors.Is(err, ErrNotConfigured) {
					slog.Error("offramp: settle failed", "err", err)
				}
				continue
			}
			if created > 0 {
				slog.Info("offramp: settlement orders created", "count", created)
			}

			resolved, err := s.Track(ctx)
			if err != nil {
				if !errors.Is(err, ErrNotConfigured) {
					slog.Error("offramp: track failed", "err", err)
				}
				continue
			}
			if resolved > 0 {
				slog.Info("offramp: settlement orders resolved", "count", resolved)
			}
		}
	}
}
