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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"

	"github.com/usezoracle/tapp/api/internal/money"
)

// Outbox states.
const (
	StatePending   = "pending"
	StateDelivered = "delivered"
	StateFailed    = "failed"
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

// EnqueueTap queues a charged tap for delivery, in the tap's own transaction.
//
// Only naira taps are queued: the market is denominated in kobo, and a tap
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
	return insert(ctx, tx, KindTap, e.TapID, payload)
}

// EnqueueReverse queues a reversal, in the reversal's own transaction.
//
// It is queued even when the tap itself was never queued (a tap charged
// before the feature was turned on): the worker delivers a reversal only
// after the tap it reverses is delivered, so such a row waits forever, which
// is visible, and is not an error.
func EnqueueReverse(ctx context.Context, tx pgx.Tx, tapID uuid.UUID, reason string) error {
	return insert(ctx, tx, KindReverse, tapID, ReverseRequest{Reason: reason})
}

func insert(ctx context.Context, tx pgx.Tx, kind string, tapID uuid.UUID, payload any) error {
	body, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("equity: encode %s payload: %w", kind, err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO equity_outbox (kind, tap_id, payload)
		VALUES ($1, $2, $3)`, kind, tapID, body); err != nil {
		return fmt.Errorf("equity: queue %s: %w", kind, err)
	}
	return nil
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

// Tick delivers everything that is due, and says how many were delivered
// and how many were given up on.
//
// Rows are taken oldest first. A reversal is held back until the tap it
// reverses has been delivered: sending it first would be reversing a tap
// Freedom has never heard of.
func (w *Worker) Tick(ctx context.Context) (delivered, failed int, err error) {
	if !w.Client.Enabled() {
		return 0, 0, ErrDisabled
	}
	rows, err := w.Pool.Query(ctx, `
		SELECT o.id, o.kind, o.tap_id, o.payload, o.attempts
		  FROM equity_outbox o
		 WHERE o.state = 'pending'
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
		response, err := w.deliver(ctx, r)
		if err == nil {
			if err := w.markDelivered(ctx, r.ID, response); err != nil {
				return delivered, failed, err
			}
			delivered++
			continue
		}
		gaveUp, err := w.markFailed(ctx, r, err)
		if err != nil {
			return delivered, failed, err
		}
		if gaveUp {
			failed++
		}
	}
	return delivered, failed, nil
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

func (w *Worker) markDelivered(ctx context.Context, id int64, response json.RawMessage) error {
	_, err := w.Pool.Exec(ctx, `
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
func (w *Worker) markFailed(ctx context.Context, r row, cause error) (gaveUp bool, err error) {
	attempts := r.Attempts + 1
	msg := cause.Error()
	if len(msg) > 1000 {
		msg = msg[:1000]
	}

	if refused(cause) && attempts >= MaxAttempts {
		slog.Error("equity: giving up", "outbox", r.ID, "kind", r.Kind, "tap", r.TapID,
			"attempts", attempts, "err", cause)
		_, err := w.Pool.Exec(ctx, `
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
	_, err = w.Pool.Exec(ctx, `
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
