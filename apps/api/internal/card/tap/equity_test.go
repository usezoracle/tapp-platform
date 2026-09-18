package tap

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/usezoracle/tapp/api/internal/equity"
	"github.com/usezoracle/tapp/api/internal/money"
)

// wireEquity attaches the outbox the way routers/index.go does.
func (f *fixture) wireEquity() {
	f.Svc.Equity = func(ctx context.Context, tx pgx.Tx, e Charged) error {
		return equity.EnqueueTap(ctx, tx, equity.TapEvent{
			TapID: e.TapID, Cardholder: e.Cardholder, Merchant: e.Merchant, Amount: e.Amount, At: e.At,
		})
	}
	f.Svc.EquityReversal = func(ctx context.Context, tx pgx.Tx, tapID uuid.UUID, reason string) error {
		return equity.EnqueueReverse(ctx, tx, tapID, reason)
	}
}

func (f *fixture) outboxRows(t *testing.T, tapID uuid.UUID) map[string]json.RawMessage {
	t.Helper()
	rows, err := f.Pool.Query(context.Background(),
		`SELECT kind, payload FROM equity_outbox WHERE tap_id = $1 AND state = 'pending'`, tapID)
	if err != nil {
		t.Fatalf("read outbox: %v", err)
	}
	defer rows.Close()
	out := map[string]json.RawMessage{}
	for rows.Next() {
		var kind string
		var payload json.RawMessage
		if err := rows.Scan(&kind, &payload); err != nil {
			t.Fatal(err)
		}
		out[kind] = payload
	}
	return out
}

// The market learns of a tap through a row written in the tap's own
// transaction. This is the invariant everything downstream rests on: a
// charge and its delivery commit together or not at all.
func TestATapQueuesItsDeliveryToTheMarketInTheSameTransaction(t *testing.T) {
	f := newFixture(t, money.Naira(10_000))
	f.wireEquity()

	receipt, err := f.pay(t, money.Naira(1_500))
	if err != nil {
		t.Fatalf("Debit: %v", err)
	}

	rows := f.outboxRows(t, receipt.TapID)
	if _, ok := rows["tap"]; !ok {
		t.Fatalf("no outbox row for tap %s; rows = %v", receipt.TapID, rows)
	}
	var req equity.TapRequest
	if err := json.Unmarshal(rows["tap"], &req); err != nil {
		t.Fatal(err)
	}
	if req.TapRef != receipt.TapID.String() || req.MerchantRef != f.Merchant.String() ||
		req.CardholderRef != f.Cardholder.String() || req.AmountKobo != 150_000 ||
		req.CardholderDisplayName != "Test User" || req.ChargedAt == "" {
		t.Errorf("payload = %+v", req)
	}

	// A refused tap writes nothing: the charge did not happen, so there is
	// nothing to tell the market.
	if _, err := f.pay(t, money.Naira(9_000)); !errors.Is(err, ErrDailyLimitReached) &&
		err == nil {
		t.Fatalf("a ₦9,000 tap on ₦8,500 was charged")
	}
	var queued, charged int
	_ = f.Pool.QueryRow(context.Background(), `
		SELECT (SELECT count(*) FROM equity_outbox WHERE kind = 'tap' AND payload->>'merchant_ref' = $1),
		       (SELECT count(*) FROM card_taps WHERE merchant_id = $2)`,
		f.Merchant.String(), f.Merchant).Scan(&queued, &charged)
	if queued != 1 || charged != 1 {
		t.Errorf("%d outbox rows for %d charged taps; want 1 and 1", queued, charged)
	}

	// The reversal is queued the same way.
	if err := f.Svc.Reverse(context.Background(), f.Merchant, receipt.TapID, "customer_returned"); err != nil {
		t.Fatalf("Reverse: %v", err)
	}
	rows = f.outboxRows(t, receipt.TapID)
	var rev equity.ReverseRequest
	if err := json.Unmarshal(rows["reverse"], &rev); err != nil || rev.Reason != "customer_returned" {
		t.Errorf("reverse row = %s (%v)", rows["reverse"], err)
	}
}

// Without a market nothing is queued and nothing about the tap changes.
func TestATapWithoutAMarketQueuesNothing(t *testing.T) {
	f := newFixture(t, money.Naira(10_000))
	receipt, err := f.pay(t, money.Naira(1_500))
	if err != nil {
		t.Fatalf("Debit: %v", err)
	}
	if rows := f.outboxRows(t, receipt.TapID); len(rows) != 0 {
		t.Errorf("outbox rows = %v, want none", rows)
	}
}
