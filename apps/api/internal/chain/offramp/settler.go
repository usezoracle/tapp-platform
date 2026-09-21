package offramp

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/big"
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

// Settler turns taps into settlement orders.
//
// Separate from the tap itself on purpose. Creating an order is an on-chain
// operation, and the cardholder is standing at a till: making the tap wait for
// a chain confirmation would put a provider's latency between somebody and
// their coffee. The tap authorises against the ledger and finishes; this
// follows moments later.
type Settler struct {
	Pool   *pgxpool.Pool
	Orders *Client
	Now    func() time.Time

	// Price says how much USDC to sell for what the merchant is owed, at the
	// moment the order is created. Estimate is what the tap recorded.
	//
	// The order's rate is not ours to choose. The aggregator assigns an
	// order to a provider only when its rate is within a few kobo of that
	// provider's own, for that amount, at that moment; an order priced from
	// a rate fetched at the till, for a different amount, is an order nobody
	// is allowed to fill, and it comes back refunded. So the price is asked
	// for again here, where the amount and the moment are both known, and
	// asked for again on every round. Nil sells the estimate as recorded.
	Price func(ctx context.Context, deliver money.Amount, estimate int64) (int64, error)
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
			// ours, and pairing that with an on-chain sender fee would take
			// the same margin twice.
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
		if s.Price != nil {
			sell, err := s.Price(ctx, p.deliver, p.sell)
			if err != nil {
				// Nothing was sent, so nothing is counted; the next tick
				// asks again. A provider that cannot quote is not a reason
				// to sell at a price it will not fill.
				slog.Warn("offramp: could not price a tap settlement, will retry",
					"tap", p.tap, "err", err)
				continue
			}
			if sell != p.sell {
				// The row says what was actually sold: the tracker matches
				// the order's event on it, and an operator reading the row
				// should see the same number the chain does.
				if _, err := s.Pool.Exec(ctx, `
					UPDATE card_tap_settlements
					   SET sell_micro = $2, updated_at = now()
					 WHERE tap_id = $1 AND state = 'pending'`, p.tap, sell); err != nil {
					return created, err
				}
				p.sell = sell
			}
		}

		// The attempt is counted BEFORE the call, not after.
		//
		// createOrder can be accepted by the chain and still fail to answer
		// us. Counting afterwards would leave a submitted order looking
		// untried, and the next tick would sell the cardholder's money a
		// second time. The idempotency key on the tap is the other half of
		// that guarantee; this is the half that survives a crash.
		if _, err := s.Pool.Exec(ctx, `
			UPDATE card_tap_settlements
			   SET attempts = attempts + 1, updated_at = now()
			 WHERE tap_id = $1`, p.tap); err != nil {
			return created, err
		}

		txHash, err := s.Orders.Create(ctx, Order{
			From:      p.from,
			Sell:      big.NewInt(p.sell),
			Deliver:   p.deliver,
			Bank:      p.bank,
			Reference: reference(p.tap, p.round),
		})
		if err != nil {
			final := p.attempts+1 >= MaxAttempts
			state := "pending"
			if final {
				state = "failed"
			}
			if _, e := s.Pool.Exec(ctx, `
				UPDATE card_tap_settlements
				   SET state = $2, last_error = $3, updated_at = now()
				 WHERE tap_id = $1`, p.tap, state, err.Error()); e != nil {
				return created, e
			}
			if final {
				slog.Error("offramp: giving up on a tap settlement",
					"tap", p.tap, "attempts", p.attempts+1, "err", err)
			} else {
				slog.Warn("offramp: tap settlement failed, will retry",
					"tap", p.tap, "attempt", p.attempts+1, "err", err)
			}
			continue
		}

		// Marking it submitted and discharging what the merchant is owed
		// commit together.
		//
		// The order is already on chain either way, so the question is only
		// what the books say about it. A submitted order with the claim still
		// standing would have us owing money a provider has been paid to
		// deliver; a discharged claim with no record of the order would lose
		// the only thing tying it to a transaction.
		if err := movements.InTx(ctx, s.Pool, func(tx pgx.Tx) error {
			if _, err := tx.Exec(ctx, `
				UPDATE card_tap_settlements
				   SET state = 'submitted', tx_hash = $2, last_error = NULL, updated_at = now()
				 WHERE tap_id = $1`, p.tap, txHash); err != nil {
				return err
			}
			_, err := movements.MerchantSettledOnChain(ctx, tx, p.merchant, p.deliver, p.tap, p.round)
			return err
		}); err != nil {
			// The order is on chain. Failing here loses only our note of it,
			// and the attempt counter above stops it being sold again.
			return created, fmt.Errorf("offramp: record submitted order for tap %s (tx %s): %w",
				p.tap, txHash, err)
		}
		created++
	}
	return created, nil
}

// reference is the idempotency key of one round's order.
//
// The first round keeps the bare tap id, which is what every order created
// before rounds existed was sent under; a retry of one of those must still
// find its own operation and not make a second.
func reference(tap uuid.UUID, round int) string {
	if round == 0 {
		return tap.String()
	}
	return fmt.Sprintf("%s:r%d", tap, round)
}

// Track follows submitted orders to their outcome.
//
// An order on chain is a question, not an answer: a provider fills it or the
// Gateway refunds it, and until one of those has happened the merchant has
// not been paid. Tick discharged the merchant's claim on submission because
// that is what a filled order means; this is what puts the claim back when
// the order is refunded instead, and sells again while there are rounds left.
//
// Outcomes are read from the Gateway itself rather than from the provider's
// API. The contract is what actually holds or returns the money, and it
// answers for every order without a credential.
func (s *Settler) Track(ctx context.Context) (resolved int, err error) {
	if s.Orders == nil {
		return 0, ErrNotConfigured
	}

	rows, err := s.Pool.Query(ctx, `
		SELECT st.tap_id, st.from_address, st.sell_micro, st.tx_hash, st.order_id, st.round,
		       t.merchant_id, t.currency, coalesce(st.deliver_minor, t.amount_minor - t.fee_minor)
		  FROM card_tap_settlements st
		  JOIN card_taps t ON t.id = st.tap_id
		 WHERE st.state = 'submitted'
		 ORDER BY st.created_at
		 LIMIT 50`)
	if err != nil {
		return 0, fmt.Errorf("offramp: find submitted orders: %w", err)
	}

	type submitted struct {
		tap      uuid.UUID
		from     string
		sell     int64
		txHash   *string
		orderID  *string
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
		if err := rows.Scan(&o.tap, &o.from, &o.sell, &o.txHash, &o.orderID, &o.round,
			&o.merchant, &cur, &minor); err != nil {
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
		var id [32]byte
		switch {
		case o.orderID != nil:
			b, err := hex.DecodeString(strings.TrimPrefix(*o.orderID, "0x"))
			if err != nil || len(b) != 32 {
				return resolved, fmt.Errorf("offramp: tap %s has a malformed order id %q", o.tap, *o.orderID)
			}
			copy(id[:], b)

		case o.txHash != nil:
			// First sight of this order: learn its id from the transaction.
			id, err = s.Orders.OrderID(ctx, *o.txHash, o.from, big.NewInt(o.sell))
			if errors.Is(err, ErrNotMined) {
				continue
			}
			if errors.Is(err, ErrOrderNotCreated) {
				// Nothing was sold. The claim was discharged on the strength
				// of a transaction that did not do what it was for, so this
				// is a refund in everything but the event.
				if err := s.refunded(ctx, o.tap, o.merchant, o.deliver, o.round, *o.txHash, err.Error()); err != nil {
					return resolved, err
				}
				resolved++
				continue
			}
			if err != nil {
				slog.Warn("offramp: could not read order id", "tap", o.tap, "tx", *o.txHash, "err", err)
				continue
			}
			if _, err := s.Pool.Exec(ctx, `
				UPDATE card_tap_settlements
				   SET order_id = $2, updated_at = now()
				 WHERE tap_id = $1 AND state = 'submitted'`,
				o.tap, "0x"+hex.EncodeToString(id[:])); err != nil {
				return resolved, err
			}

		default:
			// Submitted with neither is a row the old code could not have
			// written; say so rather than poll it forever.
			slog.Error("offramp: submitted order has no transaction hash", "tap", o.tap)
			continue
		}

		info, err := s.Orders.Info(ctx, id)
		if err != nil {
			slog.Warn("offramp: could not read order", "tap", o.tap, "err", err)
			continue
		}
		switch {
		case info.Fulfilled:
			if err := s.fulfilled(ctx, o.tap); err != nil {
				return resolved, err
			}
			resolved++
		case info.Refunded:
			if err := s.refunded(ctx, o.tap, o.merchant, o.deliver, o.round, hashOf(o.txHash), "refunded by the gateway"); err != nil {
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
	ctx context.Context, tap, merchant uuid.UUID, deliver money.Amount, round int, txHash, why string,
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
			// The refunded order's transaction stays on the row, in words:
			// tx_hash is cleared for the next round, and the ledger names
			// the tap, so this is the only place the old hash survives.
			tap, state, next, fmt.Sprintf("round %d (%s): %s", round, txHash, why))
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
			slog.Warn("offramp: order refunded, will sell again",
				"tap", tap, "round", round, "why", why)
		}
		return nil
	})
}

func hashOf(h *string) string {
	if h == nil {
		return "no tx"
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
				// Not configured is a deployment without a gateway, which is
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
