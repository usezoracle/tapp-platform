package equity

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/usezoracle/tapp/api/internal/money"
)

// Outbox states.
//
// A tap row starts held and is queued only when the tap is settled to the
// merchant: shares are bought with the fee the merchant's payment earned,
// so nothing is sent to the market until that payment has landed. A
// reversal row starts queued.
const (
	StateHeld      = "held"
	StateQueued    = "queued"
	StateDelivered = "delivered"
	StateFailed    = "failed"
	StateCancelled = "cancelled"
)

// Outbox kinds.
const (
	KindTap     = "tap"
	KindReverse = "reverse"
)

// Delivery policy.
const (
	// BatchSize is how many rows one tick delivers.
	BatchSize = 50
	// MaxAttempts is how many refusals a row survives before it is failed.
	// Only a refusal counts (a 4xx other than 409/429); an unreachable
	// market is retried for as long as it takes.
	MaxAttempts = 5
	// backoffBase doubles with every failure, up to backoffCap.
	backoffBase = 5 * time.Second
	backoffCap  = 10 * time.Minute
)

// TapEvent is what the tap's transaction knows when it queues a delivery.
type TapEvent struct {
	TapID      uuid.UUID
	Cardholder uuid.UUID
	Merchant   uuid.UUID
	Amount     money.Amount
	At         time.Time
}

// Execer is what the outbox needs of a transaction or a pool: one statement
// at a time, each atomic on its own.
type Execer interface {
	Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error)
}

// EnqueueTap records a charged tap for delivery, in the tap's own
// transaction. The row is held, not queued: it is released by
// ReleaseIfSettled once the merchant has been paid.
//
// Only naira taps are recorded: the market is denominated in kobo, and a tap
// in any other currency has no meaning to it. Such a tap is skipped with a
// log line, never an error -- nothing about the charge itself is wrong.
func EnqueueTap(ctx context.Context, tx pgx.Tx, e TapEvent) error {
	if e.Amount.Currency() != money.NGN {
		slog.Info("equity: tap not queued: market is NGN only",
			"tap", e.TapID, "currency", e.Amount.Currency())
		return nil
	}

	// The display name is a courtesy for Freedom's ledger, not an identity:
	// the cardholder_ref is the user id. A user with no name is still a
	// holder.
	var first, last string
	err := tx.QueryRow(ctx,
		`SELECT first_name, last_name FROM users WHERE id = $1`, e.Cardholder).Scan(&first, &last)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return fmt.Errorf("equity: cardholder name: %w", err)
	}

	payload := TapRequest{
		TapRef:                e.TapID.String(),
		MerchantRef:           e.Merchant.String(),
		CardholderRef:         e.Cardholder.String(),
		CardholderDisplayName: strings.TrimSpace(first + " " + last),
		AmountKobo:            e.Amount.Minor(),
		ChargedAt:             e.At.UTC().Format(time.RFC3339),
	}
	return insert(ctx, tx, KindTap, e.TapID, StateHeld, payload)
}

// RecordReversal records a reversal, in the reversal's own transaction.
//
// If the market has not heard of the tap -- its row is still held or queued
// -- there is nothing to unwind: the row is cancelled and nothing is ever
// sent. Otherwise a reversal is queued. The cancel waits for a delivery in
// flight (the worker locks the row while it talks to Freedom), so the tap is
// either never sent or its reversal follows it; there is no third outcome.
//
// A reversal is queued even when the tap itself has no row (a tap charged
// before the feature was turned on): the worker delivers a reversal only
// after the tap it reverses is delivered, so such a row waits forever, which
// is visible, and is not an error.
func RecordReversal(ctx context.Context, tx pgx.Tx, tapID uuid.UUID, reason string) error {
	tag, err := tx.Exec(ctx, `
		UPDATE equity_outbox
		   SET state = $3, last_error = $4, updated_at = now()
		 WHERE tap_id = $1 AND kind = $2 AND state IN ($5, $6)`,
		tapID, KindTap, StateCancelled, "reversed before delivery: "+reason, StateHeld, StateQueued)
	if err != nil {
		return fmt.Errorf("equity: cancel tap: %w", err)
	}
	if tag.RowsAffected() > 0 {
		return nil
	}
	return insert(ctx, tx, KindReverse, tapID, StateQueued, ReverseRequest{Reason: reason})
}

func insert(ctx context.Context, tx pgx.Tx, kind string, tapID uuid.UUID, state string, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("equity: encode %s payload: %w", kind, err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO equity_outbox (kind, tap_id, state, payload)
		VALUES ($1, $2, $3, $4)`, kind, tapID, state, body); err != nil {
		return fmt.Errorf("equity: queue %s: %w", kind, err)
	}
	return nil
}

// releaseSQL queues held tap rows whose taps are settled.
//
// Settled means what the transactions read model means by it: the tap has
// at least one leg, and every leg it has is at rest in its paid state --
// the USDC leg fulfilled in card_tap_settlements, the naira leg settled in
// card_tap_ngn_settlements. A tap with no leg at all was never handed to a
// settler and stays held. A reversed tap's row was cancelled by the
// reversal and is not held any more, so nothing here has to look at
// reversals. $1 narrows to one tap; NULL takes every held row (the sweep).
const releaseSQL = `
	UPDATE equity_outbox o
	   SET state = 'queued', updated_at = now()
	 WHERE o.kind = 'tap' AND o.state = 'held'
	   AND ($1::uuid IS NULL OR o.tap_id = $1)
	   AND EXISTS (
	         SELECT 1
	           FROM card_taps t
	           LEFT JOIN card_tap_settlements     st ON st.tap_id = t.id
	           LEFT JOIN card_tap_ngn_settlements ns ON ns.tap_id = t.id
	          WHERE t.id = o.tap_id
	            AND (st.tap_id IS NOT NULL OR ns.tap_id IS NOT NULL)
	            AND (st.tap_id IS NULL OR st.state = 'fulfilled')
	            AND (ns.tap_id IS NULL OR ns.state = 'settled'))`

// ReleaseIfSettled queues the tap's held row if the tap is now fully
// settled, and says whether it did. Safe to call from any leg's settlement
// path, in or out of that path's transaction, and as often as anyone likes:
// a tap that is not yet settled, or whose row is not held, is left alone.
//
// Every leg that reaches its paid state should call this; the worker's sweep
// (ReleaseSettled) catches any that did not.
func ReleaseIfSettled(ctx context.Context, q Execer, tapID uuid.UUID) (bool, error) {
	tag, err := q.Exec(ctx, releaseSQL, tapID)
	if err != nil {
		return false, fmt.Errorf("equity: release tap %s: %w", tapID, err)
	}
	return tag.RowsAffected() > 0, nil
}

// ReleaseSettled queues every held row whose tap is settled, and says how
// many. The worker runs it each tick, so a leg that settled without calling
// ReleaseIfSettled cannot strand a cardholder's shares.
func ReleaseSettled(ctx context.Context, q Execer) (int, error) {
	tag, err := q.Exec(ctx, releaseSQL, nil)
	if err != nil {
		return 0, fmt.Errorf("equity: release settled taps: %w", err)
	}
	return int(tag.RowsAffected()), nil
}

// Unsent is a cardholder's tap the market has not been told of yet: held
// until the merchant is paid, or queued and on its way.
type Unsent struct {
	TapID    uuid.UUID
	State    string // StateHeld or StateQueued
	Merchant uuid.UUID
	Amount   money.Amount
	At       time.Time
}

// Queryer is what a read needs of a transaction or a pool.
type Queryer interface {
	Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error)
}

// UnsentFor lists a cardholder's undelivered taps, newest first, so an
// activity feed can show what is awaiting settlement alongside what the
// market has already answered for. limit <= 0 means no limit.
func UnsentFor(ctx context.Context, q Queryer, cardholder uuid.UUID, limit int) ([]Unsent, error) {
	rows, err := q.Query(ctx, `
		SELECT o.tap_id, o.state, t.merchant_id, t.currency::text, t.amount_minor, t.created_at
		  FROM equity_outbox o
		  JOIN card_taps t ON t.id = o.tap_id
		 WHERE o.kind = $1 AND o.state IN ($2, $3) AND t.cardholder_id = $4
		 ORDER BY t.created_at DESC
		 LIMIT CASE WHEN $5 > 0 THEN $5 END`, KindTap, StateHeld, StateQueued, cardholder, limit)
	if err != nil {
		return nil, fmt.Errorf("equity: unsent taps: %w", err)
	}
	defer rows.Close()
	out := []Unsent{}
	for rows.Next() {
		var (
			u     Unsent
			cur   string
			minor int64
		)
		if err := rows.Scan(&u.TapID, &u.State, &u.Merchant, &cur, &minor, &u.At); err != nil {
			return nil, fmt.Errorf("equity: unsent taps: %w", err)
		}
		u.Amount = money.New(minor, money.Currency(cur))
		out = append(out, u)
	}
	return out, rows.Err()
}

// Worker delivers the outbox.
//
// One instance per database (DISABLE_BACKGROUND_JOBS guards this, as it does
// every other worker). Two would not corrupt anything -- Freedom is
// idempotent on tap_ref -- they would just make the same call twice.
type Worker struct {
	Pool   *pgxpool.Pool
	Client *Client
	// Now is injectable so backoff can be tested without waiting.
	Now func() time.Time
}

func (w *Worker) now() time.Time {
	if w.Now != nil {
		return w.Now()
	}
	return time.Now()
}

// row is one queued delivery.
type row struct {
	ID       int64
	Kind     string
	TapID    uuid.UUID
	Payload  []byte
	Attempts int
}

// Tick releases what has settled, delivers everything that is due, and says
// how many were delivered and how many were given up on.
//
// Rows are taken oldest first. A reversal is held back until the tap it
// reverses has been delivered: sending it first would be reversing a tap
// Freedom has never heard of.
func (w *Worker) Tick(ctx context.Context) (delivered, failed int, err error) {
	if !w.Client.Enabled() {
		return 0, 0, ErrDisabled
	}
	released, err := ReleaseSettled(ctx, w.Pool)
	if err != nil {
		return 0, 0, err
	}
	if released > 0 {
		slog.Info("equity: settled taps released for delivery", "count", released)
	}
	rows, err := w.Pool.Query(ctx, `
		SELECT o.id, o.kind, o.tap_id, o.payload, o.attempts
		  FROM equity_outbox o
		 WHERE o.state = 'queued'
		   AND o.next_at <= $1
		   AND (o.kind = 'tap' OR EXISTS (
		          SELECT 1 FROM equity_outbox t
		           WHERE t.tap_id = o.tap_id AND t.kind = 'tap' AND t.state = 'delivered'))
		 ORDER BY o.id
		 LIMIT $2`, w.now(), BatchSize)
	if err != nil {
		return 0, 0, fmt.Errorf("equity: read outbox: %w", err)
	}
	var due []row
	for rows.Next() {
		var r row
		if err := rows.Scan(&r.ID, &r.Kind, &r.TapID, &r.Payload, &r.Attempts); err != nil {
			rows.Close()
			return 0, 0, fmt.Errorf("equity: scan outbox: %w", err)
		}
		due = append(due, r)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, err
	}

	for _, r := range due {
		if ctx.Err() != nil {
			return delivered, failed, ctx.Err()
		}
		outcome, err := w.deliverOne(ctx, r)
		if err != nil {
			return delivered, failed, err
		}
		switch outcome {
		case outcomeDelivered:
			delivered++
		case outcomeGaveUp:
			failed++
		}
	}
	return delivered, failed, nil
}

type outcome int

const (
	outcomeSkipped outcome = iota // the row was cancelled or taken under us
	outcomeDelivered
	outcomeRetry
	outcomeGaveUp
)

// deliverOne makes one row's call and records the answer, holding the row
// locked for the duration.
//
// The lock is what makes a reversal and a delivery unable to cross: a
// reversal cancels a row only while it is held or queued, and if the row is
// being delivered at that moment the cancel waits, finds the row delivered,
// and queues a reversal instead. Without it a tap could be sent to Freedom a
// moment after the reversal decided nothing had been sent.
func (w *Worker) deliverOne(ctx context.Context, r row) (outcome, error) {
	tx, err := w.Pool.Begin(ctx)
	if err != nil {
		return outcomeSkipped, fmt.Errorf("equity: begin delivery: %w", err)
	}
	defer tx.Rollback(context.WithoutCancel(ctx))

	var state string
	err = tx.QueryRow(ctx, `SELECT state FROM equity_outbox WHERE id = $1 FOR UPDATE`, r.ID).Scan(&state)
	if errors.Is(err, pgx.ErrNoRows) {
		return outcomeSkipped, nil
	}
	if err != nil {
		return outcomeSkipped, fmt.Errorf("equity: lock outbox row: %w", err)
	}
	if state != StateQueued {
		return outcomeSkipped, nil
	}

	out := outcomeRetry
	response, err := w.deliver(ctx, r)
	if err == nil {
		if err := w.markDelivered(ctx, tx, r.ID, response); err != nil {
			return outcomeSkipped, err
		}
		out = outcomeDelivered
	} else {
		gaveUp, err := w.markFailed(ctx, tx, r, err)
		if err != nil {
			return outcomeSkipped, err
		}
		if gaveUp {
			out = outcomeGaveUp
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return outcomeSkipped, fmt.Errorf("equity: commit delivery: %w", err)
	}
	return out, nil
}

// deliver makes the one call a row describes and returns Freedom's answer.
func (w *Worker) deliver(ctx context.Context, r row) (json.RawMessage, error) {
	switch r.Kind {
	case KindTap:
		var req TapRequest
		if err := json.Unmarshal(r.Payload, &req); err != nil {
			return nil, &StatusError{Code: http.StatusBadRequest, Body: "stored payload is not a tap request: " + err.Error()}
		}
		resp, err := w.Client.DeliverTap(ctx, req)
		if err != nil {
			return nil, err
		}
		return json.Marshal(resp)
	case KindReverse:
		var req ReverseRequest
		if err := json.Unmarshal(r.Payload, &req); err != nil {
			return nil, &StatusError{Code: http.StatusBadRequest, Body: "stored payload is not a reverse request: " + err.Error()}
		}
		resp, err := w.Client.ReverseTap(ctx, r.TapID.String(), req)
		if err != nil {
			return nil, err
		}
		return json.Marshal(resp)
	default:
		return nil, &StatusError{Code: http.StatusBadRequest, Body: "unknown outbox kind " + r.Kind}
	}
}

func (w *Worker) markDelivered(ctx context.Context, q Execer, id int64, response json.RawMessage) error {
	_, err := q.Exec(ctx, `
		UPDATE equity_outbox
		   SET state = 'delivered', attempts = attempts + 1, last_error = NULL,
		       response = $2, delivered_at = $3, updated_at = $3
		 WHERE id = $1`, id, response, w.now())
	if err != nil {
		return fmt.Errorf("equity: mark delivered: %w", err)
	}
	return nil
}

// markFailed records a failed attempt and schedules the next, or gives up.
//
// Whether to give up depends on what failed. Freedom refusing the request
// (a 4xx) will refuse it again, so five of those and the row is failed for
// an operator. Freedom not answering, or answering 5xx, 409 or 429, says
// nothing about the request, so the row is retried for as long as it takes
// -- with the interval doubling up to ten minutes, so an outage does not
// become a flood when it ends.
func (w *Worker) markFailed(ctx context.Context, q Execer, r row, cause error) (gaveUp bool, err error) {
	attempts := r.Attempts + 1
	msg := cause.Error()
	if len(msg) > 1000 {
		msg = msg[:1000]
	}

	if refused(cause) && attempts >= MaxAttempts {
		slog.Error("equity: giving up", "outbox", r.ID, "kind", r.Kind, "tap", r.TapID,
			"attempts", attempts, "err", cause)
		_, err := q.Exec(ctx, `
			UPDATE equity_outbox
			   SET state = 'failed', attempts = $2, last_error = $3, updated_at = $4
			 WHERE id = $1`, r.ID, attempts, msg, w.now())
		if err != nil {
			return false, fmt.Errorf("equity: mark failed: %w", err)
		}
		return true, nil
	}

	next := w.now().Add(Backoff(attempts))
	slog.Warn("equity: delivery failed, will retry", "outbox", r.ID, "kind", r.Kind,
		"tap", r.TapID, "attempts", attempts, "next", next, "err", cause)
	_, err = q.Exec(ctx, `
		UPDATE equity_outbox
		   SET attempts = $2, last_error = $3, next_at = $4, updated_at = $5
		 WHERE id = $1`, r.ID, attempts, msg, next, w.now())
	if err != nil {
		return false, fmt.Errorf("equity: schedule retry: %w", err)
	}
	return false, nil
}

// refused reports whether Freedom rejected the request on its merits.
func refused(err error) bool {
	code := StatusOf(err)
	if code < 400 || code >= 500 {
		return false
	}
	return code != http.StatusConflict && code != http.StatusTooManyRequests
}

// Backoff is how long to wait after the nth failure: 5s, 10s, 20s, ... up to
// ten minutes.
func Backoff(attempts int) time.Duration {
	if attempts < 1 {
		attempts = 1
	}
	d := backoffBase
	for i := 1; i < attempts && d < backoffCap; i++ {
		d *= 2
	}
	if d > backoffCap {
		d = backoffCap
	}
	return d
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
			delivered, failed, err := w.Tick(ctx)
			if err != nil && !errors.Is(err, ErrDisabled) {
				slog.Error("equity: tick failed", "err", err)
				continue
			}
			if delivered > 0 || failed > 0 {
				slog.Info("equity", "delivered", delivered, "failed", failed)
			}
		}
	}
}

// Shares renders share units (1e-8 of a share) as the human figure Freedom
// shows: "0.203125", "12", never a trailing zero.
func Shares(units int64) string {
	return decimal.New(units, -8).String()
}
